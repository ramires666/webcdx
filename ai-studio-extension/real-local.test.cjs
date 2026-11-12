const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { createClient } = require('./local-client.js');

test('compiled local agent discovers, executes once and reconnects without Gate or host registration', { skip: process.env.WEBCODEX_LOCAL_REAL_TEST !== '1' || process.platform !== 'win32', timeout: 60000 }, async t => {
  const tempParent = path.resolve(__dirname, '..', 'tmp');
  fs.mkdirSync(tempParent, { recursive: true });
  const temp = fs.mkdtempSync(path.join(tempParent, 'real-local-'));
  const appdata = path.join(temp, 'appdata'), root = path.join(temp, 'files');
  fs.mkdirSync(root);
  const executable = process.env.WEBCODEX_TEST_AGENT_PATH || path.resolve(__dirname, '..', 'bin', 'ai-studio-bridge', 'webcodex-agent.exe');
  const env = { ...process.env, APPDATA: appdata, LOCALAPPDATA: path.join(temp, 'localappdata'), WEBCODEX_ALLOWED_ROOTS: root, WEBCODEX_GATE_URL: 'http://127.0.0.1:1', WEBCODEX_AGENT_TOKEN: 'unused-local-test' };
  const children = [];
  let logs = '';
  function start() {
    const child = spawn(executable, ['--local'], { cwd: root, env, windowsHide: true, stdio: ['ignore', 'ignore', 'pipe'] });
    child.logs = ''; child.stderr.on('data', data => { logs += data; child.logs += data; }); children.push(child); return child;
  }
  async function stop(child) { if (child.exitCode !== null || child.signalCode !== null) return; await new Promise(resolve => { child.once('exit', resolve); child.kill(); }); }
  t.after(async () => { for (const child of children) await stop(child); assert.equal(path.dirname(temp), tempParent); assert.ok(path.basename(temp).startsWith('real-local-')); fs.rmSync(temp, { recursive: true, force: true }); });
  async function discover(options = {}) {
    const deadline = Date.now() + 10000;
    while (Date.now() < deadline) {
      const match = /Studio local ready: (http:\/\/127\.0\.0\.1:\d+)/.exec(child.logs);
      if (!match) { await new Promise(resolve => setTimeout(resolve,100)); continue; }
      const client = createClient({ extensionId: 'lgiihodklblifecheadnfjkaokmmfikk', endpoint: match[1], ...options });
      try { const init = await client.initialize(); return { client, init }; } catch { client.close(); await new Promise(resolve => setTimeout(resolve, 100)); }
    }
    throw Error('agent did not start: ' + logs);
  }
  let child = start();
  let { client, init } = await discover();
  assert.equal(init.serverInfo.version, '1.5.0-ai-studio-alpha.5');
  const endpoint = client.endpoint, agentIdentity = client.agentIdentity;
  const tools = await client.listTools();
  assert.ok(tools.some(tool => tool.name === 'exec_command'));
  assert.equal((await client.listAgents())[0].id, 'local');
  const roots = await client.rpc('tools/call', { name: 'list_allowed_roots', arguments: {} });
  assert.ok(JSON.stringify(roots).includes(root.replaceAll('\\', '\\\\')));
  const file = path.join(root, 'once.txt');
  const args = { key: 'local-write-once', name: 'write_file', arguments: { path: file, content: 'local works\n', if_absent: true }, agentId: 'local' };
  const op = await client.createOperation(args);
  assert.equal(op.status, 'completed', JSON.stringify(op));
  assert.equal(fs.readFileSync(file, 'utf8'), 'local works\n');
  const read = await client.createOperation({ key: 'local-read', name: 'read_file', arguments: { path: file }, agentId: 'local' });
  assert.match(JSON.stringify(read), /local works/);
  assert.equal((await fetch(endpoint + '/studio/rpc', { method: 'POST', headers: { Origin: 'https://aistudio.google.com', 'Content-Type': 'application/json', 'X-WebCodex-Extension': 'lgiihodklblifecheadnfjkaokmmfikk' }, body: '{}' })).status, 403);
  const content='<!doctype html>\n'+('const x = `code "Courier New" \\path \u044f \ud83d\ude00`;\n'.repeat(600000))+'<!-- end -->';
  assert.ok(Buffer.byteLength(content)>10*1024*1024);
  const largePath=path.join(root,'large.html');
  const {parseCall}=require('./protocol.js');
  const largeCall=parseCall(JSON.stringify({v:1,name:'write_file',arguments:{path:largePath,if_absent:true},content_source:'large_file'})+'\n<<<HELPER_FILE_BEGIN:large_file>>>\n'+content+'\n<<<HELPER_FILE_END:large_file>>>',null,tools);
  const largeWrite=await client.createOperation({key:'large-write-once',...largeCall,agentId:'local'});
  assert.equal(largeWrite.status,'completed');assert.ok(fs.readFileSync(largePath,'utf8')===content,'large write must preserve exact UTF-8');
  const largeRead=await client.createOperation({key:'large-read',name:'read_file',arguments:{path:largePath},agentId:'local'});
  assert.ok(largeRead.result.structuredContent.content===content,'large HTTP result must preserve the entire file');
  client.close(); await stop(child); child = start();
  ({ client } = await discover({ endpoint, agentIdentity }));
  assert.deepEqual(await client.getOperation(op.operation_id), op);
  assert.deepEqual(await client.createOperation(args), op);
  client.close();
  assert.match(logs, /Studio local connected: agent=1\.5\.0-ai-studio-alpha\.5 extension=0\.3\.10/);
  assert.match(logs, /Gate не используется/);
  assert.doesNotMatch(logs, /connected to Gate|Не удалось получить настройки/);
  assert.equal(fs.existsSync(path.join(appdata, 'webcodex', 'studio', 'native-host.json')), false);
  assert.equal(fs.existsSync(path.join(appdata, 'webcodex', 'studio', 'settings.json')), false);
});
