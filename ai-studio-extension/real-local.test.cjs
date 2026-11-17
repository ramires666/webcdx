const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { createClient } = require('./local-client.js');
const release = require('./package.json');

test('compiled local agent discovers, executes once and reconnects without Gate or host registration', { skip: process.env.WEBCODEX_LOCAL_REAL_TEST !== '1' || process.platform !== 'win32', timeout: 60000 }, async t => {
  const tempParent = path.resolve(__dirname, '..', 'tmp');
  fs.mkdirSync(tempParent, { recursive: true });
  const temp = fs.mkdtempSync(path.join(tempParent, 'real-local-'));
  const appdata = path.join(temp, 'appdata'), root = path.join(temp, 'files');
  fs.mkdirSync(root);
  const executable = process.env.WEBCODEX_TEST_AGENT_PATH || path.resolve(__dirname, '..', 'bin', 'ai-studio-bridge', 'webcodex-agent.exe');
  const env = { ...process.env, APPDATA: appdata, LOCALAPPDATA: path.join(temp, 'localappdata'), WEBCODEX_ALLOWED_ROOTS: root, WEBCODEX_GATE_URL: process.env.WEBCODEX_AIX_LOCAL_ONLY === '1' ? 'http://127.0.0.1:1' : '', WEBCODEX_AGENT_TOKEN: process.env.WEBCODEX_AIX_LOCAL_ONLY === '1' ? 'unused-test-token' : '' };
  const children = [];
  let logs = '';
  function start() {
    const child = spawn(executable, process.env.WEBCODEX_AIX_LOCAL_ONLY === '1' ? [] : ['--local'], { cwd: root, env, windowsHide: true, stdio: ['ignore', 'ignore', 'pipe'] });
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
  assert.equal(init.serverInfo.version, release.webcodexRelease.agent);
  const endpoint = client.endpoint, agentIdentity = client.agentIdentity;
  assert.equal(init.studioOperations.lookupByKey,true);
  assert.equal(init.studioSession.marker,true);
  const marker='a'.repeat(32);
  await client.setSessionMarker(marker);
  await client.initialize();await client.setSessionMarker(marker);
  assert.equal(logs.split('ID активного сеанса: '+marker).length-1,2);
  await client.setSessionMarker('');
  assert.equal((await client.getOperationByKey('never-sent')).status,'not_found');
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
  const racePath=path.join(root,'race.txt');fs.writeFileSync(racePath,'original');
  const originalHash=require('node:crypto').createHash('sha256').update('original').digest('hex');
  const raced=await Promise.all(Array.from({length:12},(_,i)=>client.createOperation({key:'race-'+i,name:'write_file',arguments:{path:racePath,content:'winner '+i,expected_sha256:originalHash},agentId:'local'})));
  assert.equal(raced.filter(result=>result.status==='completed').length,1);
  for(const result of raced.filter(result=>result.status!=='completed'))assert.equal(result.result.structuredContent.error.code,'stale_hash');
  let lostCreates=0;
  const lossy=createClient({endpoint,agentIdentity,extensionId:'lgiihodklblifecheadnfjkaokmmfikk',fetchImpl:async(url,request)=>{
    const response=await fetch(url,request);
    if(JSON.parse(request.body).method==='operations/create'){
      lostCreates++;
      await response.text();
      throw new TypeError('lost response before receiving operation ID');
    }
    return response;
  }});
  await lossy.initialize();
  const lostPath=path.join(root,'lost-response.txt');
  await assert.rejects(lossy.createOperation({key:'lost-before-id',name:'write_file',arguments:{path:lostPath,content:'once',if_absent:true},agentId:'local'}),error=>error.code==='NATIVE_DISCONNECTED');
  fs.writeFileSync(lostPath,'after');
  const lost=await lossy.getOperationByKey('lost-before-id');
  assert.equal(lost.status,'completed');assert.equal(lostCreates,1);assert.equal(fs.readFileSync(lostPath,'utf8'),'after');
  lossy.close();
  const read = await client.createOperation({ key: 'local-read', name: 'read_file', arguments: { path: file }, agentId: 'local' });
  assert.match(JSON.stringify(read), /local works/);
  await new Promise(resolve=>setTimeout(resolve,50));
  assert.ok(logs.includes('read_file — Читаю файл'));
  assert.ok(logs.includes('read_file — Прочитано строк: 1'));
  assert.equal(logs.includes('[source=local]'),false);
  assert.equal((await fetch(endpoint + '/studio/rpc', { method: 'POST', headers: { Origin: 'https://aistudio.google.com', 'Content-Type': 'application/json', 'X-WebCodex-Extension': 'lgiihodklblifecheadnfjkaokmmfikk' }, body: '{}' })).status, 403);
  const content='<!doctype html>\n'+('const x = `code "Courier New" \\path \u044f \ud83d\ude00`;\n'.repeat(600000))+'<!-- end -->';
  assert.ok(Buffer.byteLength(content)>10*1024*1024);
  const largePath=path.join(root,'large.html');
  const {parseCall}=require('./protocol.js');
  const rawWindowsRead=parseCall('{"v":1,"name":"read_file","arguments":{"path":"'+file+'"}}',null,tools);
  const recoveredRead=await client.createOperation({key:'raw-windows-path-read',...rawWindowsRead,agentId:'local'});
  assert.equal(recoveredRead.status,'completed');assert.equal(recoveredRead.result.structuredContent.content,'local works\n');
  const stdin='line\n'.repeat(10000),stdinKey='large-command-stdin';
  assert.ok(Buffer.byteLength(stdin)>32768);
  const stdinArgv=['find.exe','/c','/v',''];
  const stdinCall=parseCall('HELPER_large_stdin exec_command\ncwd: '+root.replaceAll('\\','/')+'\nyield_time_ms: 3000\nargv: '+JSON.stringify(stdinArgv)+'\n---\n'+stdin+'\nEND_large_stdin',null,tools);
  const stdinResult=await client.createOperation({key:stdinKey,name:stdinCall.name,arguments:{...stdinCall.arguments,request_key:stdinKey},agentId:'local'});
  assert.equal(stdinResult.status,'completed',JSON.stringify(stdinResult));
  assert.equal(stdinResult.result.isError,false,JSON.stringify(stdinResult));
  assert.equal(stdinResult.result.structuredContent.exit_code,0,JSON.stringify(stdinResult));
  assert.match(stdinResult.result.structuredContent.stdout,/10000/,JSON.stringify(stdinResult));
  const largeCall=parseCall(JSON.stringify({v:1,name:'write_file',arguments:{path:largePath,if_absent:true},content_source:'large_file'})+'\n<<<HELPER_FILE_BEGIN:large_file>>>\n'+content+'\n<<<HELPER_FILE_END:large_file>>>',null,tools);
  const largeWrite=await client.createOperation({key:'large-write-once',...largeCall,agentId:'local'});
  assert.equal(largeWrite.status,'completed');assert.ok(fs.readFileSync(largePath,'utf8')===content,'large write must preserve exact UTF-8');
  const largeRead=await client.createOperation({key:'large-read',name:'read_file',arguments:{path:largePath},agentId:'local'});
  assert.ok(largeRead.result.structuredContent.content===content,'large HTTP result must preserve the entire file');
  const attachmentRead=await client.createOperation({key:'large-file-attachment',name:'read_file',arguments:{path:largePath,delivery:'file'},agentId:'local'});
  assert.equal(attachmentRead.status,'completed');
  const resource=attachmentRead.result.content.find(item=>item.type==='resource').resource;
  assert.equal(Buffer.from(resource.blob,'base64').toString('utf8'),content);
  assert.equal(attachmentRead.result.structuredContent.content,undefined,'attachment is not duplicated in text JSON');
  const pdfPath=path.join(root,'document.pdf'),pdf=Buffer.from('%PDF-1.7\n\x00\xff\xfePDF','latin1');fs.writeFileSync(pdfPath,pdf);
  const pdfRead=await client.createOperation({key:'binary-file-attachment',name:'read_file',arguments:{path:pdfPath,delivery:'file'},agentId:'local'});
  assert.equal(pdfRead.status,'completed');assert.equal(pdfRead.result.structuredContent.attachment.mimeType,'application/pdf');
  assert.deepEqual(Buffer.from(pdfRead.result.content.find(item=>item.type==='resource').resource.blob,'base64'),pdf);
  client.close(); await stop(child); child = start();
  // Concurrent real-agent tests may occupy the previous port during the restart.
  ({ client } = await discover({ agentIdentity }));
  assert.equal(client.agentIdentity,agentIdentity);
  assert.deepEqual(await client.getOperation(op.operation_id), op);
  assert.deepEqual(await client.getOperationByKey(args.key), op);
  assert.deepEqual(await client.getOperationByKey('lost-before-id'), lost);
  assert.deepEqual(await client.createOperation(args), op);
  client.close();
  assert.ok(logs.includes('Studio local connected: agent='+release.webcodexRelease.agent+' extension='+release.version));
  assert.match(logs, /Gate не используется|Google AI Studio local agent ready/);
  assert.doesNotMatch(logs, /connected to Gate|Не удалось получить настройки/);
  assert.equal(fs.existsSync(path.join(appdata, 'webcodex', 'studio', 'native-host.json')), false);
  assert.equal(fs.existsSync(path.join(appdata, 'webcodex', 'studio', 'settings.json')), false);
});


test('Windows local launcher starts the repository EXE from a path containing spaces', { skip: process.env.WEBCODEX_LOCAL_REAL_TEST !== '1' || process.platform !== 'win32', timeout: 20000 }, async t => {
  const tempParent = path.resolve(__dirname, '..', 'tmp');
  fs.mkdirSync(tempParent, { recursive: true });
  const temp = fs.mkdtempSync(path.join(tempParent, 'launcher smoke '));
  let child;
  t.after(async () => {
    if (child?.pid && child.exitCode === null && child.signalCode === null) {
      await new Promise((resolve, reject) => {
        const killer = spawn('taskkill.exe', ['/PID', String(child.pid), '/T', '/F'], { windowsHide: true, stdio: 'ignore' });
        killer.once('error', reject);
        killer.once('exit', resolve);
      });
    }
    fs.rmSync(temp, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
  });
  const executable = process.env.WEBCODEX_TEST_AGENT_PATH || path.resolve(__dirname, '..', 'bin', 'ai-studio-bridge', 'webcodex-agent.exe');
  const agentDirectory = path.join(temp, 'bin', 'aix');
  const launchDirectory = path.join(temp, 'working directory');
  fs.mkdirSync(agentDirectory, { recursive: true });
  fs.mkdirSync(launchDirectory);
  fs.copyFileSync(executable, path.join(agentDirectory, 'webcodex-agent.exe'));
  fs.copyFileSync(path.resolve(__dirname, '..', 'start-local-agent.cmd'), path.join(temp, 'start-local-agent.cmd'));
  const env = { ...process.env, APPDATA: path.join(temp, 'appdata'), LOCALAPPDATA: path.join(temp, 'localappdata'), WEBCODEX_ALLOWED_ROOTS: temp, WEBCODEX_GATE_URL: 'http://127.0.0.1:1', WEBCODEX_AGENT_TOKEN: 'unused-test-token' };
  child = spawn('cmd.exe', ['/d', '/c', '..\\start-local-agent.cmd'], { cwd: launchDirectory, windowsHide: true, env, stdio: ['ignore', 'pipe', 'pipe'] });
  let output = '';
  let launchError;
  child.once('error', error => { launchError = error; });
  child.stdout.on('data', chunk => { output += chunk; });
  child.stderr.on('data', chunk => { output += chunk; });
  const deadline = Date.now() + 10000;
  while (!output.includes('Studio local ready:') && !launchError && child.exitCode === null && Date.now() < deadline) {
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  if (launchError) throw launchError;
  assert.match(output, /Studio local ready:/);
  assert.doesNotMatch(output, /connected to Gate|Не удалось получить настройки/);
});
