const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const { spawn } = require('node:child_process');
const { createClient } = require('./local-client.js');

test('one compiled agent handles local and Gate requests separately and survives Gate reconnect', { skip: process.env.WEBCODEX_UNIVERSAL_REAL_TEST !== '1' || process.platform !== 'win32', timeout: 45000 }, async t => {
  const tempParent = path.resolve(__dirname, '..', 'tmp'); fs.mkdirSync(tempParent, { recursive: true });
  const temp = fs.mkdtempSync(path.join(tempParent, 'real-universal-'));
  const localRoot = path.join(temp, 'local'), gateRoot = path.join(temp, 'gate'), appdata = path.join(temp, 'appdata');
  for (const dir of [localRoot, gateRoot, path.join(appdata, 'webcodex', 'studio')]) fs.mkdirSync(dir, { recursive: true });
  for (const [root, text] of [[localRoot, 'LOCAL'], [gateRoot, 'GATE']]) fs.writeFileSync(path.join(root, 'fixture.txt'), text);
  fs.writeFileSync(path.join(appdata, 'webcodex', 'studio', 'settings.json'), JSON.stringify({ extension_id: 'lgiihodklblifecheadnfjkaokmmfikk', allowed_roots: [localRoot] }));
  let stream = null, connections = 0, nextId = 0, logs = '', child, localClient;
  const pending = new Map();
  const server = http.createServer(async (request, response) => {
    try {
      assert.equal(request.headers.authorization, 'Bearer fixture-agent-token');
      if (request.url === '/agent/config') { response.setHeader('Content-Type', 'application/json'); response.end(JSON.stringify({ allowed_roots: gateRoot })); return; }
      if (request.url.startsWith('/agent/stream?')) {
        assert.match(request.url, /version=1\.5\.0-ai-studio-alpha\.4/);
        response.setHeader('X-WebCodex-Version', 'fixture-gate-version'); response.setHeader('Content-Type', 'application/x-ndjson'); response.write('\n'); stream = response; connections++; return;
      }
      if (request.url === '/agent/result') {
        let body = ''; for await (const data of request) body += data;
        const result = JSON.parse(body), waiter = pending.get(result.id);
        response.writeHead(204); response.end();
        if (waiter) { pending.delete(result.id); clearTimeout(waiter.timer); if (result.error) waiter.reject(Error(result.error)); else waiter.resolve(result.response); }
        return;
      }
      response.writeHead(404); response.end();
    } catch (error) { response.writeHead(500); response.end(); for (const waiter of pending.values()) waiter.reject(error); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(async () => {
    localClient?.close();
    if (child && child.exitCode === null && child.signalCode === null) await new Promise(resolve => { child.once('exit', resolve); child.kill(); });
    for (const waiter of pending.values()) clearTimeout(waiter.timer);
    server.closeAllConnections(); await new Promise(resolve => server.close(resolve));
    assert.equal(path.dirname(temp), tempParent); assert.ok(path.basename(temp).startsWith('real-universal-')); fs.rmSync(temp, { recursive: true, force: true });
  });
  const env = { ...process.env, APPDATA: appdata, LOCALAPPDATA: path.join(temp, 'localappdata'), WEBCODEX_GATE_URL: `http://127.0.0.1:${server.address().port}`, WEBCODEX_AGENT_TOKEN: 'fixture-agent-token', WEBCODEX_LOG_DIR: path.join(temp, 'gate-logs'), WEBCODEX_BROWSER_BACKEND: 'disabled' };
  delete env.WEBCODEX_ALLOWED_ROOTS;
  const executable = process.env.WEBCODEX_TEST_AGENT_PATH || path.resolve(__dirname, '..', 'bin', 'ai-studio-bridge', 'webcodex-agent.exe');
  child = spawn(executable, [], { cwd: temp, env, windowsHide: true, stdio: ['ignore', 'ignore', 'pipe'] }); child.stderr.on('data', data => { logs += data; });
  async function waitFor(predicate) { const deadline = Date.now() + 10000; while (!predicate()) { if (Date.now() > deadline) throw Error('agent did not become ready: ' + logs); await new Promise(resolve => setTimeout(resolve, 25)); } }
  await waitFor(() => connections === 1 && /Studio local ready: (http:\/\/127\.0\.0\.1:\d+)/.test(logs));
  const endpoint = /Studio local ready: (http:\/\/127\.0\.0\.1:\d+)/.exec(logs)[1];
  localClient = createClient({ endpoint, extensionId: 'lgiihodklblifecheadnfjkaokmmfikk' });
  assert.equal((await localClient.initialize()).serverInfo.version, '1.5.0-ai-studio-alpha.4');
  function gateCall(name, args = {}) {
    const id = 'gate-' + ++nextId;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { pending.delete(id); reject(Error('Gate response timed out')); }, 5000); pending.set(id, { resolve, reject, timer });
      stream.write(JSON.stringify({ id, deadline: new Date(Date.now() + 10000).toISOString(), request: { jsonrpc: '2.0', id, method: 'tools/call', params: { name, arguments: args, _webcodex_contract: 'v5' } } }) + '\n');
    }).then(response => response.result);
  }
  const localCall = (name, args = {}) => localClient.rpc('tools/call', { name, arguments: args });
  const [localRead, gateRead] = await Promise.all([localCall('read_file', { path: path.join(localRoot, 'fixture.txt') }), gateCall('read_file', { path: path.join(gateRoot, 'fixture.txt') })]);
  assert.match(JSON.stringify(localRead), /LOCAL/); assert.match(JSON.stringify(gateRead), /GATE/);
  assert.equal((await localCall('read_file', { path: path.join(gateRoot, 'fixture.txt') })).isError, true);
  assert.equal((await gateCall('read_file', { path: path.join(localRoot, 'fixture.txt') })).isError, true);
  const localArgs = { argv: ['cmd.exe', '/d', '/c', 'echo LOCAL'], cwd: localRoot, request_key: 'same-key', yield_time_ms: 1000 };
  const gateArgs = { argv: ['cmd.exe', '/d', '/c', 'echo GATE'], cwd: gateRoot, request_key: 'same-key', yield_time_ms: 1000 };
  const [localCommand, gateCommand] = await Promise.all([localCall('exec_command', localArgs), gateCall('exec_command', gateArgs)]);
  assert.equal(localCommand.isError, false); assert.equal(gateCommand.isError, false);
  assert.notEqual(localCommand.structuredContent.session_id, gateCommand.structuredContent.session_id);
  assert.equal((await localCall('list_commands')).structuredContent.sessions.length, 1);
  assert.equal((await gateCall('list_commands')).structuredContent.sessions.length, 1);
  assert.equal((await localCall('poll_command', { session_id: gateCommand.structuredContent.session_id })).isError, true);
  assert.equal((await gateCall('cancel_command', { session_id: localCommand.structuredContent.session_id })).isError, true);
  stream.destroy();
  assert.equal((await localCall('read_file', { path: path.join(localRoot, 'fixture.txt') })).isError, false);
  await waitFor(() => connections >= 2);
  assert.equal((await gateCall('exec_command', gateArgs)).structuredContent.session_id, gateCommand.structuredContent.session_id);
  assert.match(logs, /\[source=local\].*tool="read_file"/); assert.match(logs, /\[source=gate\].*tool="read_file"/);
  assert.match(logs, /agent version=1\.5\.0-ai-studio-alpha\.4 gate version=fixture-gate-version/);
});
