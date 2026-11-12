const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { createClient } = require('./native-client.js');

const enabled = process.env.WEBCODEX_NATIVE_REAL_TEST === '1';
const agentPath = process.env.WEBCODEX_TEST_AGENT_PATH || path.resolve(__dirname, '..', 'bin', 'ai-studio-bridge', 'webcodex-agent.exe');
const extensionId = 'lgiihodklblifecheadnfjkaokmmfikk';
const origin = `chrome-extension://${extensionId}/`;
const expectedVersion = '1.5.0-ai-studio-alpha.5';

test('compiled agent serves real Chrome Native Messaging requests and persists operations', { skip: !enabled || process.platform !== 'win32', timeout: 60_000 }, async t => {
  assert.ok(fs.existsSync(agentPath), 'compiled agent binary is missing');
  const tempParent = path.resolve(__dirname, '..', 'tmp');
  fs.mkdirSync(tempParent, { recursive: true });
  const temp = fs.mkdtempSync(path.join(tempParent, 'real-native-'));
  const appData = path.join(temp, 'appdata');
  const root = path.join(temp, 'files');
  fs.mkdirSync(path.join(appData, 'webcodex', 'studio'), { recursive: true });
  fs.mkdirSync(root);
  fs.writeFileSync(path.join(appData, 'webcodex', 'studio', 'native-host.json'), JSON.stringify({
    name: 'org.webcodex.studio', description: 'isolated smoke test', path: agentPath,
    type: 'stdio', allowed_origins: [origin],
  }));
  fs.writeFileSync(path.join(appData, 'webcodex', 'studio', 'settings.json'), JSON.stringify({
    extension_id: extensionId, allowed_roots: [root],
  }));
  const children = [];

  function startHost() {
    const env = { ...process.env, APPDATA: appData, WEBCODEX_MAX_RESPONSE_BYTES: String(2 * 1024 * 1024), WEBCODEX_BROWSER_BACKEND: 'disabled' };
    for (const key of Object.keys(env)) if (/WEBCODEX_(ALLOWED_ROOTS|GATE_URL|AGENT_TOKEN|TOKEN|PASSWORD)/i.test(key)) delete env[key];
    const child = spawn(agentPath, [origin], { cwd: temp, env, windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
    child.stderrText = '';
    child.stderr.setEncoding('utf8').on('data', text => { child.stderrText += text; });
    children.push(child);
    return child;
  }
  function chromeRuntime(child) {
    let buffered = Buffer.alloc(0);
    const incoming = event(), disconnected = event();
    child.stdout.on('data', bytes => {
      buffered = Buffer.concat([buffered, bytes]);
      while (buffered.length >= 4) {
        const length = buffered.readUInt32LE(0);
        if (length > 8 * 1024 * 1024) { disconnected.emit(); return; }
        if (buffered.length < length + 4) return;
        const frame = JSON.parse(buffered.subarray(4, length + 4).toString('utf8'));
        buffered = buffered.subarray(length + 4);
        incoming.emit(frame);
      }
    });
    child.once('exit', () => disconnected.emit());
    return {
      runtime: { connectNative(host) { assert.equal(host, 'org.webcodex.studio'); return {
        onMessage: incoming, onDisconnect: disconnected,
        postMessage(message) { const data = Buffer.from(JSON.stringify(message)); const header = Buffer.alloc(4); header.writeUInt32LE(data.length); child.stdin.write(Buffer.concat([header, data])); },
        disconnect() { child.stdin.end(); },
      }; }, get lastError() { return undefined; } },
    };
  }
  function clientFor(child) { return createClient({ ...chromeRuntime(child), version: '0.3.0' }); }
  async function waitForCompleted(client, id) {
    const deadline = Date.now() + 10_000;
    while (Date.now() < deadline) {
      const op = await client.getOperation(id);
      if (op.status !== 'running') return op;
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    throw new Error('operation did not finish');
  }

  t.after(async () => {
    for (const child of children) if (child.exitCode === null) child.kill();
    await Promise.all(children.map(child => child.exitCode !== null ? Promise.resolve() : new Promise(resolve => {
      const timer = setTimeout(resolve, 1500); child.once('exit', () => { clearTimeout(timer); resolve(); });
    })));
    assert.equal(path.dirname(path.resolve(temp)), tempParent);
    assert.ok(path.basename(temp).startsWith('real-native-'));
    fs.rmSync(temp, { recursive: true, force: true });
  });

  const child = startHost();
  let client = clientFor(child);
  const init = await client.initialize();
  assert.equal(init.serverInfo.version, expectedVersion);
  await waitFor(() => child.stderrText.includes(`Studio connected: agent=${expectedVersion} client=0.3.0`));
  const tools = (await client.listTools()).map(tool => tool.name);
  for (const name of ['read_file', 'exec_command', 'poll_command', 'list_commands']) assert.ok(tools.includes(name), `missing native tool ${name}`);
  assert.equal((await client.listAgents())[0].id, 'local');
  const roots = await client.rpc('tools/call', { name: 'list_allowed_roots', arguments: {} });
  assert.ok(structuredToolResult(roots).roots.some(item => item.path === root && item.available), 'persisted allowed roots were not applied');

  const target = path.join(root, 'written.txt');
  const op = await client.createOperation({ key: 'smoke-write-once', name: 'write_file', arguments: { path: target, content: 'native smoke\n', if_absent: true }, agentId: 'local' });
  assert.match(op.operation_id, /^op_/);
  assert.equal(op.status, 'completed', JSON.stringify(op));
  assert.equal((await waitForCompleted(client, op.operation_id)).status, 'completed');
  assert.equal(fs.readFileSync(target, 'utf8'), 'native smoke\n');
  assert.equal((await client.createOperation({ key: 'smoke-write-once', name: 'write_file', arguments: { path: target, content: 'native smoke\n', if_absent: true }, agentId: 'local' })).operation_id, op.operation_id);
  await assert.rejects(client.createOperation({ key: 'smoke-write-once', name: 'write_file', arguments: { path: target, content: 'changed\n' }, agentId: 'local' }), /idempotency key reused with different payload/);
  const readOp = await client.createOperation({ key: 'smoke-read-once', name: 'read_file', arguments: { path: target }, agentId: 'local' });
  assert.equal(readOp.status, 'completed');
  assert.match(JSON.stringify(readOp.result), /native smoke/);

  const commandOp = await client.createOperation({ key: 'smoke-command-operation', name: 'exec_command', arguments: {
    argv: ['cmd.exe', '/c', 'hostname'], cwd: root, request_key: 'smoke-command-request', yield_time_ms: 1000, timeout_seconds: 10,
  }, agentId: 'local' });
  assert.equal(commandOp.status, 'completed');
  let command = structuredToolResult(commandOp.result);
  const deadline = Date.now() + 10_000;
  while (['starting', 'running'].includes(command.status) && Date.now() < deadline) {
    command = structuredToolResult(await client.rpc('tools/call', { name: 'poll_command', arguments: {
      session_id: command.session_id, stdout_offset: command.stdout_next_offset || 0, stderr_offset: command.stderr_next_offset || 0,
    } }));
    if (['starting', 'running'].includes(command.status)) await new Promise(resolve => setTimeout(resolve, 50));
  }
  assert.equal(command.exit_code, 0, 'native exec_command did not finish successfully');
  assert.ok(String(command.stdout || command.output || '').trim(), 'native exec_command returned no hostname output');

  const large = 'large-response-🧪'.repeat(90_000);
  const largePath = path.join(root, 'large.txt');
  fs.writeFileSync(largePath, large);
  const read = await client.rpc('tools/call', { name: 'read_file', arguments: { path: largePath } });
  assert.ok(JSON.stringify(read).length > 1024 * 1024, 'large native response was not returned');
  client.close();
  child.kill();
  await new Promise(resolve => child.once('exit', resolve));

  const restarted = startHost();
  client = clientFor(restarted);
  await client.initialize();
  assert.equal((await client.getOperation(op.operation_id)).status, 'completed');
  client.close();
});

function event() { const listeners = []; return { addListener(fn) { listeners.push(fn); }, emit(...args) { for (const listener of listeners) listener(...args); } }; }
async function waitFor(predicate) { const deadline = Date.now() + 2_000; while (Date.now() < deadline) { if (predicate()) return; await new Promise(resolve => setTimeout(resolve, 10)); } throw new Error('timed out waiting for native host log'); }
function structuredToolResult(result) {
  if (result?.structuredContent) return result.structuredContent;
  for (const item of result?.content || []) if (item.type === 'text') { try { return JSON.parse(item.text); } catch (_) {} }
  return result;
}
