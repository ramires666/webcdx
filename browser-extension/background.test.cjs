const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const crypto = require('node:crypto');
const manifest = JSON.parse(fs.readFileSync(`${__dirname}/manifest.json`));
const event = () => ({addListener(fn){this.listener=fn}});
async function environment(options = {}) {
  const sent = [], commands = [], ports = [], debuggerAttaches = [], timers = new Map(), alarms = new Map();
  const storage = options.storage || {};
  const tabs = new Map((options.tabs || [{id:7,url:'https://example.com/',active:true}]).map(tab => [tab.id,tab]));
  let online = options.online !== false, timerID = 0;
  const flush = () => new Promise(setImmediate);
  const chrome = {runtime:{id:'extension',onMessage:event(),onStartup:event(),onInstalled:event(),connectNative(){
    const nativePort = {onDisconnect:event(),onMessage:event(),postMessage:message=>sent.push(message),disconnect(){this.onDisconnect.listener()}};
    ports.push(nativePort);
    queueMicrotask(() => { if (online) nativePort.onMessage.listener({kind:'hello',allow_local:false,command_protocol:2,connection_id:'connection-test'}); else nativePort.disconnect(); });
    return nativePort;
  }},action:{setBadgeText:async()=>{},setBadgeBackgroundColor:async()=>{}},
    storage:{local:{get:async key=>({[key]:storage[key]}),set:async values=>Object.assign(storage,structuredClone(values))}},
    alarms:{onAlarm:event(),get:async key=>alarms.get(key),create:async(key,value)=>alarms.set(key,value),clear:async key=>alarms.delete(key)},
    tabs:{get:async id=>{if(!tabs.has(id))throw Error('Tab closed');return tabs.get(id)},query:async query=>[...tabs.values()].filter(tab=>!query.active||tab.active),
      onCreated:event(),onUpdated:event(),onRemoved:event(),onActivated:event(),remove:async id=>tabs.delete(id),create:async({url})=>{const tab=options.pendingCreate?{id:100+tabs.size,url:'',pendingUrl:url}:{id:100+tabs.size,url};tabs.set(tab.id,tab);return tab}},
    debugger:{onDetach:event(),onEvent:event(),attach:async source=>{debuggerAttaches.push(source.tabId);if(options.attach)await options.attach()},detach:async()=>{},sendCommand:async(source,method,params)=>{commands.push({source,method,params});return {ok:true}}},
    downloads:{onDeterminingFilename:event(),onChanged:event(),cancel:async()=>{}}};
  const context = vm.createContext({chrome,URL,Map,Set,Promise,TextEncoder,performance,crypto:crypto.webcrypto,navigator:{userAgent:options.userAgent||"Chrome/Test"},
    setTimeout:(fn,ms)=>{timers.set(++timerID,{fn,ms});return timerID},clearTimeout:id=>timers.delete(id)});
  context.importScripts=name=>vm.runInContext(fs.readFileSync(`${__dirname}/${name}`,'utf8'),context);
  vm.runInContext(fs.readFileSync(`${__dirname}/background.js`,'utf8'),context);
  context.sent=sent;
  await flush();
  if (options.manual !== false) vm.runInContext(`port={postMessage:message=>sent.push(message)};connected=true;attached.set(7,{origin:'https://example.com',publicWeb:false,created:false})`,context);
  const message = (value,sender={id:'extension'})=>new Promise(resolve=>{
    if(!chrome.runtime.onMessage.listener(value,sender,resolve))resolve({rejected:true});
  });
  return {context,chrome,sent,commands,ports,debuggerAttaches,timers,alarms,storage,tabs,flush,message,
    setOnline:value=>{online=value},run:code=>vm.runInContext(code,context),async tick(ms){
      const selected=[...timers].filter(([,timer])=>timer.ms===ms);
      for(const [id,timer] of selected){timers.delete(id);timer.fn()}
      await flush();
    }};
}
async function connectedPersonal() {
  const e=await environment({manual:false,storage:{webcodexPersonalAccess:{continuous:true}}});
  await e.flush();
  assert.equal(e.ports.length,1);
  const tab=e.ports[0];
  return {e,tab,send(value){tab.onMessage.listener(value)},command(id,method='Page.getFrameTree',extra={}){
    return {kind:'command',id,tab_id:7,method,params:{},command_protocol:2,connection_id:e.run('commandConnection'),scope_epoch:e.run('accessEpoch'),deadline_ms:Date.now()+5000,...extra};
  }};
}
const responseFor=(e,id)=>e.sent.findLast(message=>message.kind==='response'&&message.id===id);
test('manifest public key matches the native host extension ID',()=>{
  const hex=crypto.createHash('sha256').update(Buffer.from(manifest.key,'base64')).digest('hex').slice(0,32);
  const id=[...hex].map(x=>String.fromCharCode(97+parseInt(x,16))).join('');
  assert.equal(id,'pmobmihkpbblajdanleplliihfmcdmpp');
  assert.equal(manifest.manifest_version,3);
});
test('native messages enforce the 900000-byte UTF-8 JSON limit',async()=>{
  const e=await environment();
  assert.doesNotThrow(()=>e.run(`post({data:'a'.repeat(899989)})`)); // JSON overhead is 11 bytes.
  assert.throws(()=>e.run(`post({data:'a'.repeat(899990)})`),/exceeds limit/);
  assert.throws(()=>e.run(`post({data:String.fromCodePoint(1046).repeat(450000)})`),/exceeds limit/);
  assert.throws(()=>e.run(`post({data:String.fromCodePoint(0x1f600).repeat(225000)})`),/exceeds limit/);
});
test('expired protocol 2 envelopes are rejected before CDP',async()=>{
  const {e,send,command}=await connectedPersonal();
  send(command('expired','Page.getFrameTree',{deadline_ms:Date.now()-1}));
  await e.flush();
  assert.match(responseFor(e,'expired').error,/deadline expired/i);
  assert.equal(e.commands.length,0);
});
test('protocol 2 rejects stale connection and scope envelopes before input',async()=>{
  const {e,send,command}=await connectedPersonal();
  send(command('wrong-connection','Input.dispatchMouseEvent',{connection_id:'stale-connection',params:{type:'mousePressed',x:1,y:1}}));
  send(command('wrong-scope','Input.dispatchMouseEvent',{scope_epoch:e.run('accessEpoch')+1,params:{type:'mousePressed',x:1,y:1}}));
  await e.flush();
  assert.match(responseFor(e,'wrong-connection').error,/scope changed/i);
  assert.match(responseFor(e,'wrong-scope').error,/scope changed/i);
  assert.equal(e.commands.some(call=>call.method.startsWith('Input.')),false);
});
test('legacy reads remain available while legacy mutations require protocol 2',async()=>{
  const {e,send}=await connectedPersonal();
  send({kind:'command',id:'legacy-input',tab_id:7,method:'Input.dispatchMouseEvent',params:{type:'mousePressed',x:1,y:1}});
  send({kind:'command',id:'legacy-read',tab_id:7,method:'Page.getFrameTree',params:{}});
  await e.flush();await e.flush();
  assert.match(responseFor(e,'legacy-input').error,/protocol is not supported/i);
  assert.equal(responseFor(e,'legacy-read').error,undefined);
  assert.equal(e.commands.some(call=>call.method.startsWith('Input.')),false);
  assert(e.commands.some(call=>call.method==='Page.getFrameTree'));
});
test('stale remote resume control epoch cannot clear a newer pause',async()=>{
  const {e,send,tab}=await connectedPersonal();
  send({kind:'control',action:'pause',control_epoch:2});
  await e.flush();
  assert.equal(e.run('paused'),true);
  send({kind:'control',action:'resume',control_epoch:1});
  await e.flush();
  assert.equal(e.run('paused'),true);
  assert.equal(e.run('localPaused'),false);
  assert(tab);
});
test('local pause blocks pending input before delayed storage save resolves',async()=>{
  const {e,send,command}=await connectedPersonal();
  let resolveRead,resolveStorage;
  const read=new Promise(resolve=>{resolveRead=resolve});
  const storageSave=new Promise(resolve=>{resolveStorage=resolve});
  e.chrome.tabs.get=async id=>{await read;return e.tabs.get(id)};
  e.chrome.storage.local.set=()=>storageSave;
  send(command('pending-input','Input.dispatchMouseEvent',{params:{type:'mousePressed',x:1,y:1}}));
  await e.flush();
  const pause=e.message({type:'pause'});
  await e.flush();
  assert.equal(e.run('paused'),true);
  assert.equal(e.run('localPaused'),true);
  resolveRead();await e.flush();await e.flush();
  assert.match(responseFor(e,'pending-input').error,/local control paused|cancelled/i);
  assert.equal(e.commands.some(call=>call.method.startsWith('Input.')),false);
  resolveStorage();await pause;
});
test('queued input cancellation removes it while an earlier read is delayed',async()=>{
  const {e,send,command}=await connectedPersonal();
  let resolveRead;const read=new Promise(resolve=>{resolveRead=resolve});
  e.chrome.tabs.get=async id=>{await read;return e.tabs.get(id)};
  send(command('read'));
  await e.flush();
  send(command('queued-input','Input.dispatchMouseEvent',{params:{type:'mousePressed',x:1,y:1}}));
  send({kind:'command_cancel',id:'queued-input',connection_id:e.run('commandConnection')});
  await e.flush();
  assert.equal(e.sent.findLast(message=>message.kind==='cancel_ack')?.cancelled,true);
  resolveRead();await e.flush();await e.flush();
  assert.equal(e.commands.some(call=>call.method.startsWith('Input.')),false);
  assert.equal(responseFor(e,'read').error,undefined);
});
test('queued commands that expire before a blocked read completes never execute',async()=>{
  const {e,send,command}=await connectedPersonal();
  let resolveRead;const read=new Promise(resolve=>{resolveRead=resolve});
  e.chrome.tabs.get=async id=>{await read;return e.tabs.get(id)};
  send(command('read'));await e.flush();
  const deadline=Date.now()+2000;
  send(command('expires','Input.dispatchMouseEvent',{deadline_ms:deadline,params:{type:'mousePressed',x:1,y:1}}));
  await e.flush();
  e.run(`Date.now=()=>${deadline+1}`);
  resolveRead();await e.flush();await e.flush();
  assert.match(responseFor(e,'expires').error,/deadline expired/i);
  assert.equal(e.commands.some(call=>call.method.startsWith('Input.')),false);
});
test('pause and resume invalidate commands queued under the old scope epoch',async()=>{
  const {e,send,command}=await connectedPersonal();
  let resolveRead;const read=new Promise(resolve=>{resolveRead=resolve});
  e.chrome.tabs.get=async id=>{await read;return e.tabs.get(id)};
  send(command('read'));await e.flush();
  send(command('old-input','Input.dispatchMouseEvent',{params:{type:'mousePressed',x:1,y:1}}));
  await e.message({type:'pause'});await e.message({type:'resume'});
  assert.match(responseFor(e,'old-input').error,/scope changed/i);
  resolveRead();await e.flush();await e.flush();
  assert.equal(e.commands.some(call=>call.method.startsWith('Input.')),false);
});
test('per-port queue rejects a ninth waiter without executing it',async()=>{
  const {e,send,command}=await connectedPersonal();
  let resolveRead;const read=new Promise(resolve=>{resolveRead=resolve});
  e.chrome.tabs.get=async id=>{await read;return e.tabs.get(id)};
  send(command('read'));await e.flush();
  for(let i=0;i<9;i++)send(command(`wait-${i}`,'Input.dispatchMouseEvent',{params:{type:'mousePressed',x:i,y:1}}));
  await e.flush();
  assert.match(responseFor(e,'wait-8').error,/queue is full/i);
  assert.equal(e.commands.some(call=>call.method.startsWith('Input.')),false);
  resolveRead();await e.flush();await e.flush();
  assert.equal(e.commands.filter(call=>call.method.startsWith('Input.')).length,8);
});
test('cancellation during a delayed tab read blocks the active input command',async()=>{
  const {e,send,command}=await connectedPersonal();
  let resolveRead;const read=new Promise(resolve=>{resolveRead=resolve});
  e.chrome.tabs.get=async id=>{await read;return e.tabs.get(id)};
  send(command('active-input','Input.dispatchMouseEvent',{params:{type:'mousePressed',x:1,y:1}}));await e.flush();
  send({kind:'command_cancel',id:'active-input',connection_id:e.run('commandConnection')});await e.flush();
  assert.equal(e.sent.findLast(message=>message.kind==='cancel_ack')?.cancelled,false);
  resolveRead();await e.flush();await e.flush();
  assert.match(responseFor(e,'active-input').error,/cancelled/i);
  assert.equal(e.commands.some(call=>call.method.startsWith('Input.')),false);
});
test('a new connection can process commands while the old read is still blocked',async()=>{
  const {e,send,command,tab}=await connectedPersonal();
  let resolveRead;const read=new Promise(resolve=>{resolveRead=resolve});
  let first=true;
  e.chrome.tabs.get=async id=>{if(first){first=false;await read}return e.tabs.get(id)};
  send(command('old-read'));await e.flush();
  tab.disconnect();await e.flush();
  await e.tick(2000);await e.flush();
  assert.equal(e.ports.length,2);
  const newPort=e.ports[1];
  newPort.onMessage.listener({kind:'command',id:'new-read',tab_id:7,method:'Page.getFrameTree',params:{},command_protocol:2,connection_id:e.run('commandConnection'),scope_epoch:e.run('accessEpoch'),deadline_ms:Date.now()+5000});
  await e.flush();await e.flush();
  assert(responseFor(e,'new-read'),'new port must respond before the old read is released');
  assert.equal(responseFor(e,'new-read').error,undefined);
  resolveRead();await e.flush();await e.flush();
  assert.equal(responseFor(e,'old-read'),undefined);
  assert(tab);
});
test('URL and exact-tab scope reject forbidden navigation and raw methods',async()=>{
  const e=await environment();
  for(const url of ['file:///secret','chrome://settings','javascript:alert(1)','https://other.com','http://127.0.0.1','https://user:pass@example.com'])
    assert.equal(e.run(`permitted(${JSON.stringify(url)},attached.get(7))`),false,url);
  await assert.rejects(e.run(`execute({tab_id:99,method:'Page.navigate',params:{url:'https://example.com/'}})`));
  await assert.rejects(e.run(`execute({tab_id:7,method:'Storage.getCookies'})`));
  await assert.rejects(e.run(`execute({tab_id:7,method:'Page.navigate',params:{url:'https://other.com/'}})`));
  assert.equal(e.commands.length,0);
  await e.run(`execute({tab_id:7,method:'Page.getFrameTree'})`);
  assert.equal(e.commands.length,1);
});
test('local pause wins and personal tabs cannot be closed',async()=>{
  const e=await environment();
  e.run('paused=true;localPaused=true');
  await assert.rejects(e.run(`execute({tab_id:7,method:'Page.navigate',params:{url:'https://example.com/'}})`));
  e.run('paused=false');
  await assert.rejects(e.run(`execute({tab_id:7,method:'WebCodex.closeTab'})`));
  assert.equal(e.commands.length,0);
});
test('events omit console secrets and headers and redact URL query',async()=>{
  const e=await environment();
  await e.chrome.debugger.onEvent.listener({tabId:7},'Runtime.consoleAPICalled',{type:'error',args:[{value:'PASSWORD_SECRET'}]});
  await e.chrome.debugger.onEvent.listener({tabId:7},'Network.responseReceived',{response:{status:500,url:'https://example.com/?token=SECRET',headers:{Authorization:'SECRET'}}});
  assert(!JSON.stringify(e.sent).includes('SECRET'));
  assert.equal(e.sent.at(-1).result.response.url,'https://example.com/');
});
test('cross-process iframe commands stay inside the attached tab',async()=>{
  const e=await environment();
  await e.chrome.debugger.onEvent.listener({tabId:7},'Target.attachedToTarget',{sessionId:'child-session',targetInfo:{type:'iframe',targetId:'child-frame'}});
  await e.run(`execute({tab_id:7,frame_id:'child-frame',method:'Runtime.evaluate',params:{expression:'document.title',contextId:3}})`);
  assert.equal(e.commands.at(-1).source.tabId,7);
  assert.equal(e.commands.at(-1).source.sessionId,'child-session');
});

test('persistent access is a saved local choice and catalogs tabs without debugger attachment',async()=>{
  const tabs=Array.from({length:40},(_,i)=>({id:i+1,url:`https://example.com/${i}`,active:i===39}));
  tabs.push({id:99,url:'chrome://settings'},{id:100,url:'http://localhost/'},{id:101,url:'https://user:pass@example.com/'});
  const e=await environment({manual:false,tabs});
  assert.equal(e.ports.length,0);
  assert.equal((await e.message({type:'continuous',enabled:true,allowNew:true})).continuous,true);
  assert.equal(e.storage.webcodexPersonalAccess.continuous,true);
  assert.equal(e.run('available.size'),40);
  assert.equal(e.sent.at(-1).tabs[0].tab_id,40);
  assert.equal(e.debuggerAttaches.length,0);
  assert(e.alarms.has('webcodex-reconnect'));
  await e.run(`execute({tab_id:1,method:'Page.getFrameTree'})`);
  assert.deepEqual(e.debuggerAttaches,[1]);
  await assert.rejects(e.run(`execute({tab_id:99,method:'Page.getFrameTree'})`));
  await assert.rejects(e.run(`execute({tab_id:2,method:'Storage.getCookies'})`));
  await assert.rejects(e.run(`execute({tab_id:1,method:'WebCodex.closeTab'})`));
  assert.deepEqual(e.debuggerAttaches,[1]);
  const restarted=await environment({manual:false,tabs,storage:e.storage});
  assert.equal(restarted.ports.length,1);
  assert.equal(restarted.run('available.size'),40);
  assert.equal(restarted.debuggerAttaches.length,0);
  assert.equal(restarted.run('allowNew'),true);
});

test('an agent restart reconnects automatically without erasing the persistent grant',async()=>{
  const e=await environment({manual:false,storage:{webcodexPersonalAccess:{continuous:true}}});
  await e.run(`execute({tab_id:7,method:'Page.getFrameTree'})`);
  e.setOnline(false);
  e.ports[0].disconnect();
  await e.flush();
  assert.equal(e.run('attached.size'),0);
  assert.equal(e.storage.webcodexPersonalAccess.continuous,true);
  await e.tick(2000);
  assert.equal(e.ports.length,2);
  e.setOnline(true);
  await e.tick(4000);
  assert.equal(e.ports.length,3);
  assert.equal(e.run('connected'),true);
  assert.equal(e.run('available.size'),1);
  await e.run(`execute({tab_id:7,method:'Page.getFrameTree'})`);
  assert.deepEqual(e.debuggerAttaches,[7,7]);
});

test('starting Chrome before the agent recovers through a service worker alarm',async()=>{
  const e=await environment({manual:false,online:false,storage:{webcodexPersonalAccess:{continuous:true}}});
  assert.equal(e.run('connected'),false);
  e.setOnline(true);
  e.chrome.alarms.onAlarm.listener({name:'webcodex-reconnect'});
  await e.flush();
  assert.equal(e.run('connected'),true);
  assert.equal(e.run('available.size'),1);
});

test('local pause survives Chrome restart and remote resume cannot clear it',async()=>{
  const e=await environment({manual:false,storage:{webcodexPersonalAccess:{continuous:true}}});
  await e.message({type:'pause'});
  e.ports[0].onMessage.listener({kind:'control',action:'resume'});
  await e.flush();
  assert.equal(e.run('paused'),true);
  await assert.rejects(e.run(`execute({tab_id:7,method:'Page.getFrameTree'})`));
  const restarted=await environment({manual:false,storage:e.storage});
  assert.equal(restarted.ports.length,0);
  assert.equal(restarted.run('localPaused'),true);
  await restarted.message({type:'resume'});
  assert.equal(restarted.run('connected'),true);
  assert.equal(restarted.run('paused'),false);
});

test('new and navigated tabs become available without clicking the extension',async()=>{
  const e=await environment({manual:false,storage:{webcodexPersonalAccess:{continuous:true}}});
  e.tabs.set(8,{id:8,url:'https://other.com/'});
  await e.chrome.tabs.onCreated.listener(e.tabs.get(8));
  assert.equal(e.run('available.has(8)'),true);
  assert.equal(e.debuggerAttaches.length,0);
  e.tabs.get(8).url='chrome://settings';
  e.chrome.tabs.onUpdated.listener(8,{url:'chrome://settings'});
  await e.flush();
  assert.equal(e.run('available.has(8)'),false);
  e.tabs.get(8).url='https://other.com/';
  e.chrome.tabs.onUpdated.listener(8,{url:'https://other.com/'});
  await e.flush();
  await e.run(`execute({tab_id:8,method:'Page.getFrameTree'})`);
  e.tabs.delete(8);
  e.chrome.tabs.onRemoved.listener(8);
  await e.flush();
  assert.equal(e.run('available.has(8)'),false);
});

test('closing a chat session releases debuggers but preserves permanent access',async()=>{
  const e=await environment({manual:false,storage:{webcodexPersonalAccess:{continuous:true}}});
  await e.run(`execute({tab_id:7,method:'Page.getFrameTree'})`);
  e.ports[0].onMessage.listener({kind:'control',action:'detach'});
  await e.flush();
  assert.equal(e.run('continuous'),true);
  assert.equal(e.run('attached.size'),0);
  assert.equal(e.run('available.size'),1);
  await e.run(`execute({tab_id:7,method:'Page.getFrameTree'})`);
  assert.deepEqual(e.debuggerAttaches,[7,7]);
});

test('disconnect revokes saved access, cancels retries and prevents attachment finishing in flight',async()=>{
  let finishAttach;
  const attaching=new Promise(resolve=>{finishAttach=resolve});
  const e=await environment({manual:false,storage:{webcodexPersonalAccess:{continuous:true}},attach:()=>attaching});
  const command=e.run(`execute({tab_id:7,method:'Page.getFrameTree'})`);
  const rejected=assert.rejects(command);
  await e.flush();
  await e.message({type:'disconnect'});
  finishAttach();
  await rejected;
  assert.equal(e.storage.webcodexPersonalAccess.continuous,false);
  assert.equal(e.run('available.size'),0);
  assert.equal(e.run('attached.size'),0);
  assert.equal(e.alarms.size,0);
  await e.tick(2000);
  assert.equal(e.ports.length,1);
  const restarted=await environment({manual:false,storage:e.storage});
  assert.equal(restarted.ports.length,0);
});

test('manual access does not reconnect or expand to other tabs after transport loss',async()=>{
  const e=await environment({manual:false});
  await e.message({type:'attach',publicWeb:false});
  assert.equal(e.run('attached.size'),1);
  e.ports[0].disconnect();
  await e.flush();
  assert.equal(e.run('attached.size'),0);
  assert.equal(e.run('continuous'),false);
  assert.equal(e.alarms.size,0);
  assert.equal(e.timers.size,0);
});

test('web pages cannot enable saved personal access through extension messaging',async()=>{
  const e=await environment({manual:false});
  assert.equal((await e.message({type:'continuous',enabled:true},{id:'extension',tab:{id:7}})).rejected,true);
  assert.equal((await e.message({type:'continuous',enabled:true},{id:'foreign'})).rejected,true);
  assert.equal(e.run('continuous'),false);
  assert.equal(e.ports.length,0);
});

test('personal can start on demand when Chrome only has its internal new-tab page',async()=>{
  const e=await environment({manual:false,tabs:[{id:7,url:'chrome://newtab/'}],storage:{webcodexPersonalAccess:{continuous:true}}});
  assert.equal(e.run('available.size'),0);
  await e.run(`execute({tab_id:0,method:'WebCodex.prepare'})`);
  assert.equal(e.run('available.size'),1);
  const id=e.run('[...available.keys()][0]');
  assert.equal(e.tabs.get(id).url,'about:blank');
  assert.equal(e.run(`attached.get(${id}).created`),true);
  await e.run(`execute({tab_id:${id},method:'Page.navigate',params:{url:'https://example.com/'}})`);
  assert.equal(e.commands.at(-1).method,'Page.navigate');
  assert.equal(e.debuggerAttaches.includes(7),false);
});

test('new personal tabs attach while Chrome has only a pending URL',async()=>{
  const e=await environment({manual:false,pendingCreate:true,storage:{webcodexPersonalAccess:{continuous:true,allowNew:true}}});
  const result=await e.run(`execute({tab_id:7,method:'WebCodex.newTab'})`);
  assert.equal(result.url,'about:blank');
  assert.equal(e.run(`available.has(${result.tab_id})`),true);
  assert.equal(e.run(`attached.get(${result.tab_id}).created`),true);
  await e.run(`execute({tab_id:${result.tab_id},method:'Page.enable'})`);
  await e.run(`execute({tab_id:${result.tab_id},method:'Page.navigate',params:{url:'https://www.google.com/'}})`);
  await e.run(`execute({tab_id:${result.tab_id},method:'WebCodex.closeTab'})`);
  assert.equal(e.tabs.has(result.tab_id),false);
  assert.equal(e.tabs.has(7),true);
});

test('failed attachment closes only the tab created for the failed request',async()=>{
  const e=await environment({pendingCreate:true,attach:async()=>{throw Error('Debugger rejected tab')}});
  e.run('allowNew=true');
  await assert.rejects(e.run(`execute({tab_id:7,method:'WebCodex.newTab'})`));
  assert.equal(e.tabs.size,1);
  assert.equal(e.tabs.has(7),true);
});

test('network diagnostics correlate requests without paths or raw failure messages',async()=>{
  const e=await environment();
  await e.chrome.debugger.onEvent.listener({tabId:7,sessionId:'frame'},'Network.requestWillBeSent',{requestId:'123',request:{url:'https://example.com/PRIVATE_SECRET?token=SECRET',headers:{Authorization:'SECRET'},postData:'SECRET'}});
  assert.equal(e.sent.at(-1).result.requestId,'frame:123');
  assert.equal(e.sent.at(-1).result.request.url,'https://example.com/');
  await e.chrome.debugger.onEvent.listener({tabId:7,sessionId:'frame'},'Network.loadingFailed',{requestId:'123',errorText:'net::ERR_TIMED_OUT',canceled:false});
  assert.equal(e.sent.at(-1).result.failure_type,'timeout');
  await e.chrome.debugger.onEvent.listener({tabId:7},'Network.loadingFailed',{requestId:'456',errorText:'UNKNOWN_SECRET',canceled:false});
  assert.equal(e.sent.at(-1).result.failure_type,'other');
  assert(!JSON.stringify(e.sent).includes('SECRET'));
});

test('profile identity and local alias persist without expanding page access',async()=>{
  const e=await environment({manual:false,userAgent:'Chrome/Test Edg/Test',storage:{webcodexPersonalAccess:{continuous:true}}});
  const identity=e.sent.find(x=>x.kind==='identity');
  assert(identity.profile_id.match(/^profile_[a-f0-9]{32}$/));
  assert.equal(identity.product,'edge');
  const updated=await e.message({type:'identity',alias:'edge-work'});
  assert.equal(updated.alias,'edge-work');
  assert.equal(e.sent.at(-1).profile_id,identity.profile_id);
  assert.deepEqual(await e.message({type:'identity',alias:'page-spoof'},{id:'extension',tab:{id:7}}),{rejected:true});
  const restarted=await environment({manual:false,storage:e.storage});
  assert.equal(restarted.sent.find(x=>x.kind==='identity').profile_id,identity.profile_id);
  assert.equal((await restarted.message({type:'status'})).alias,'edge-work');
});
