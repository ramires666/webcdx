const test=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const WebCodexProtocol=require('./protocol.js');
const WebCodexMCP=require('./mcp-client.js');

function harness(stored={}, failInitialize=false, localStore={}, clientOverride=null) {
  if (!Object.hasOwn(localStore,'bridgeAutoSetupV1')) localStore.bridgeAutoSetupV1=true;
  const listeners={};const data={...stored};const sent=[],tabMessages=[];
  const event=()=>({addListener:fn=>{listeners[fn.name||Math.random()]=fn;}});
  const chrome={runtime:{id:'test-id',getURL:p=>'chrome-extension://test-id/'+p,onMessage:{addListener(fn){listeners.message=fn;}}},
    storage:{session:{setAccessLevel:async x=>sent.push(['session-access',x]),get:async k=>({[k]:data[k]}),set:async x=>Object.assign(data,x)},local:{get:async k=>({[k]:localStore[k]}),set:async x=>Object.assign(localStore,x),remove:async k=>{delete localStore[k];},setAccessLevel:async x=>sent.push(['local-access',x])}},
    action:{onClicked:{addListener(fn){listeners.click=fn;}}},sidePanel:{open:async()=>{}},alarms:{create(){},onAlarm:{addListener(){}}},
    permissions:{contains:async()=>true},tabs:{get:async()=>({url:'https://aistudio.google.com/app/prompts/abc'}),sendMessage:async(...args)=>{tabMessages.push(args);return{supported:true,conversationKey:'c',ok:true};}},
    identity:{getRedirectURL:()=> 'https://test.chromiumapp.org/webcodex',launchWebAuthFlow:async()=>''}};
  const context={chrome,crypto:require('node:crypto').webcrypto,URL,URLSearchParams,TextEncoder,TextDecoder,Uint8Array,ArrayBuffer,JSON,Promise,Set,Map,Date,AbortSignal,Math,Error,String,Object,Number,RegExp,console,btoa:s=>Buffer.from(s,'binary').toString('base64'),fetch:async()=>{throw Error('fetch unexpected')},
    WebCodexProtocol, WebCodexSession:require('./session-state.js'), WebCodexAttachments:require('./attachments.js'),WebCodexMCP:clientOverride|| (failInitialize?{createClient:()=>({initialize:async()=>{throw Error('expired')}})}:WebCodexMCP),importScripts(){},setTimeout,clearTimeout};
  // Keep exercising the retained remote implementation while this release is local-only.
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,'background.js'),'utf8').replace('const GATE_ENABLED = false;','const GATE_ENABLED = true;'),context,{filename:'background.js'});
  return {listeners,data,sent,chrome,tabMessages};
}
function message(h,msg,sender){return new Promise(resolve=>{const async=h.listeners.message(msg,sender,resolve);if(async!==true)resolve(undefined);});}
test('manual marker injection targets only the active session and preserves permissions and pending work',async()=>{
  const session={phase:'waiting_model',sessionId:'s1',sessionMarker:'a'.repeat(32),tabId:7,toolNames:['read_file'],bindingAddress:'/prompts/current'};
  const panel={url:'chrome-extension://test-id/panel.html'},inject={type:'bridge.injectMarker',tabId:7};
  function create(changes={},consent=true){return harness({webcodexState:{connected:true,demoMode:true,session:{...session,...changes}}},false,{bridgeAuto:{consent}});}
  const h=create(),reply=await message(h,inject,panel);
  assert.equal(reply.ok,true);assert.deepEqual(reply.state.session.toolNames,['read_file']);
  assert.equal(h.tabMessages.length,1);
  assert.equal(h.tabMessages[0][0],7);
  assert.deepEqual(JSON.parse(JSON.stringify(h.tabMessages[0][1])),{type:'studio.injectMarker',sessionId:'s1',bindingAddress:'/prompts/current'});
  assert.equal((await message(h,{...inject,tabId:8},panel)).ok,false);
  h.chrome.tabs.get=async()=>({url:'https://example.com/'});
  assert.equal((await message(h,inject,panel)).ok,false);
  for(const changes of [{phase:'idle'},{phase:'stopped'},{paused:true},{resumeBlocked:true},{sessionMarker:''},{pendingCall:{}},{operationKey:'pending'},{operationId:'pending'},{resultText:'pending'}]){
    const blocked=create(changes);assert.equal((await message(blocked,inject,panel)).ok,false);
    assert.equal(blocked.tabMessages.length,0);
  }
  assert.equal((await message(create({},false),inject,panel)).ok,false);
  const failed=create();failed.chrome.tabs.sendMessage=async()=>({ok:false,error:'Editor unavailable'});
  assert.equal((await message(failed,inject,panel)).error,'Editor unavailable');
});
test('chat width changes work while idle and persist as validated global preference',async()=>{
  const localStore={chatWidth:40},h=harness({},false,localStore),panel={url:'chrome-extension://test-id/panel.html'};
  const restored=await message(h,{type:'bridge.status'},panel);assert.equal(restored.state.chatWidth,40);
  const reply=await message(h,{type:'bridge.chatWidth',tabId:7,value:100},panel);
  assert.equal(reply.ok,true);assert.equal(reply.state.chatWidth,100);assert.equal(localStore.chatWidth,100);
  assert.equal(h.data.webcodexState.session.phase,'idle');assert.equal(h.data.webcodexState.connected,false);
  assert.deepEqual(h.tabMessages.map(([,msg])=>[msg.type,msg.value]),[['studio.chatWidth',100]]);
});
test('chat width rejects invalid values and non-AI-Studio tabs without saving',async()=>{
  const localStore={chatWidth:20},h=harness({},false,localStore),panel={url:'chrome-extension://test-id/panel.html'};
  for(const value of [-10,110,15,10.5,'10']){
    const reply=await message(h,{type:'bridge.chatWidth',tabId:7,value},panel);
    assert.equal(reply.ok,false);assert.match(reply.error,/Ширина должна быть/);
  }
  h.chrome.tabs.get=async()=>({url:'https://example.com/'});
  const wrongOrigin=await message(h,{type:'bridge.chatWidth',tabId:7,value:30},panel);
  assert.equal(wrongOrigin.ok,false);assert.match(wrongOrigin.error,/Google AI Studio/);
  assert.equal(localStore.chatWidth,20);assert.equal(h.tabMessages.length,0);
});
test('chat folding targets a selected AI Studio tab while disconnected and idle',async()=>{
  const h=harness(),panel={url:'chrome-extension://test-id/panel.html'};
  const reply=await message(h,{type:'bridge.collapseChat',tabId:7},panel);
  assert.equal(reply.ok,true);
  assert.deepEqual(h.tabMessages.map(([id,msg])=>[id,msg.type]),[[7,'studio.collapseChat']]);
  assert.equal(h.data.webcodexState.session.phase,'idle');
  assert.equal(h.data.webcodexState.connected,false);
  assert.equal(h.tabMessages.some(([,msg])=>/call|arm|deliver|stage/i.test(msg.type)),false);
});
test('chat folding rejects non-Studio origins and malformed tab IDs',async()=>{
  const h=harness(),panel={url:'chrome-extension://test-id/panel.html'};
  h.chrome.tabs.get=async()=>({url:'https://example.com/'});
  const wrongOrigin=await message(h,{type:'bridge.collapseChat',tabId:7},panel);
  assert.equal(wrongOrigin.ok,false);assert.match(wrongOrigin.error,/Google AI Studio/);
  const badTab=await message(h,{type:'bridge.collapseChat',tabId:'7'},panel);
  assert.equal(badTab.ok,false);assert.match(badTab.error,/Google AI Studio/);
  assert.equal(h.tabMessages.length,0);
});
test('browser restart restores only credentials, refreshes tools and never starts a chat',async()=>{
  const auto={enabled:true,agentId:'a',toolNames:['read_file'],automatic:true,autoApprove:false,consent:true};
  const localStore={bridgeAuth:{gateUrl:'https://gate.example',clientId:'client',accessToken:'saved-secret'},bridgeAuto:auto};
  const mcp={createClient:()=>({initialize:async()=>({}),listTools:async()=>[{name:'read_file'}],listAgents:async()=>[{id:'a',online:true}]})};
  const h=harness({},false,localStore,mcp),panel={url:'chrome-extension://test-id/panel.html'};
  const reply=await message(h,{type:'bridge.status'},panel);
  assert.equal(reply.state.connected,true);assert.equal(reply.state.session.phase,'idle');
  assert.equal(reply.state.tools[0].name,'read_file');
  assert.deepEqual(reply.state.autoSettings,{...auto,revision:reply.state.autoSettings.revision});
  assert.ok(Number.isInteger(reply.state.autoSettings.revision)&&reply.state.autoSettings.revision>=0);
  assert.equal(JSON.stringify(reply).includes('saved-secret'),false);
  await message(h,{type:'bridge.disconnect'},panel);
  assert.equal(localStore.bridgeAuth,undefined);
});
test('revoked saved credentials are removed and never silently reused',async()=>{
  const localStore={bridgeAuth:{gateUrl:'https://gate.example',clientId:'client',accessToken:'revoked'}};
  const mcp={createClient:()=>({initialize:async()=>{const e=Error('revoked');e.status=401;throw e;}})};
  const h=harness({},false,localStore,mcp);
  const reply=await message(h,{type:'bridge.status'},{url:'chrome-extension://test-id/panel.html'});
  assert.equal(reply.state.connected,false);assert.equal(localStore.bridgeAuth,undefined);
});
test('one pending login remains visible and duplicate connects never queue another flow',async()=>{
  const h=harness();const panel={url:'chrome-extension://test-id/panel.html'};
  let cancel;let started=false;
  h.chrome.identity.launchWebAuthFlow=()=>{started=true;return new Promise((_,reject)=>{cancel=()=>reject(Error('cancelled'));});};
  const first=message(h,{type:'bridge.connect',gateUrl:'https://gate.example',clientId:'client'},panel);
  while(!started)await new Promise(r=>setTimeout(r,1));
  const status=await message(h,{type:'bridge.status'},panel);
  assert.equal(status.state.connecting,true);
  const second=await message(h,{type:'bridge.connect',gateUrl:'https://gate.example',clientId:'client'},panel);
  assert.equal(second.ok,false);assert.match(second.error,/уже выполняется/);
  cancel();assert.equal((await first).ok,false);
  await new Promise(r=>setTimeout(r,0));
  assert.equal((await message(h,{type:'bridge.status'},panel)).state.connecting,false);
});
test('rejects panel privileges from page and frame senders',async()=>{
  const h=harness();await new Promise(r=>setTimeout(r,0));
  const page=await message(h,{type:'bridge.status'},{url:'https://aistudio.google.com/'});assert.equal(page.ok,false);assert.equal(page.error,'Panel sender required');
  const frame=await message(h,{type:'studio.call'},{url:'https://aistudio.google.com/',frameId:3,tab:{id:1}});assert.equal(frame.ok,false);assert.equal(frame.error,'Untrusted Studio sender');
});
test('session restore keeps access token private from panel state',async()=>{
  const h=harness({webcodexState:{connected:false,gateUrl:'',clientId:'',agents:[],tools:[],accessToken:'secret',session:{phase:'idle',agentId:'',toolNames:[],automatic:false,paused:false,turn:'',instructions:'',pendingCall:null,resultText:'',error:'',count:0,tabId:null}}});
  await new Promise(r=>setTimeout(r,0));
  const reply=await message(h,{type:'bridge.status'},{url:'chrome-extension://test-id/panel.html'});
  assert.equal(reply.ok,true);assert.equal(JSON.stringify(reply).includes('secret'),false);
  assert.equal(h.sent.some(x=>x[0]==='session-access'&&x[1].accessLevel==='TRUSTED_CONTEXTS'),true);
  assert.equal(h.sent.some(x=>x[0]==='local-access'&&x[1].accessLevel==='TRUSTED_CONTEXTS'),true);
});
test('failed Gate restore clears connected state and blocks the stale session',async()=>{
  const stored={webcodexState:{connected:true,gateUrl:'https://gate.example',clientId:'client',accessToken:'secret',session:{phase:'recovering',agentId:'a',toolNames:['read_file'],automatic:false,paused:false,pendingCall:{name:'read_file',arguments:{path:'x'}},resultText:'',error:'',count:0,tabId:7}}};
  const h=harness(stored,true);
  await new Promise(r=>setTimeout(r,0));
  const status=await message(h,{type:'bridge.status'},{url:'chrome-extension://test-id/panel.html'});
  assert.equal(status.state.connected,false);assert.equal(status.state.session.paused,true);assert.equal(status.state.session.resumeBlocked,true);
  assert.match(status.state.session.error,/reconnect/);assert.equal(JSON.stringify(status).includes('secret'),false);
  const panel={url:'chrome-extension://test-id/panel.html'};
  const connect={type:'bridge.connect',gateUrl:'https://gate.example',clientId:'client'};
  let oauthStarted=false;
  h.chrome.identity.launchWebAuthFlow=async()=>{oauthStarted=true;throw Error('OAuth cancelled in test');};
  assert.match((await message(h,connect,panel)).error,/Stop the paused session/);
  assert.equal(oauthStarted,false);
  assert.equal((await message(h,{type:'bridge.stop'},panel)).ok,true);
  assert.equal((await message(h,connect,panel)).error,'OAuth cancelled in test');
  assert.equal(oauthStarted,true);
});
