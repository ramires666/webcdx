const test=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const WebCodexProtocol=require('./protocol.js');
const WebCodexMCP=require('./mcp-client.js');

function harness(stored={}, failInitialize=false, localStore={}, clientOverride=null) {
  const listeners={};const data={...stored};const sent=[];
  const event=()=>({addListener:fn=>{listeners[fn.name||Math.random()]=fn;}});
  const chrome={runtime:{id:'test-id',getURL:p=>'chrome-extension://test-id/'+p,onMessage:{addListener(fn){listeners.message=fn;}}},
    storage:{session:{setAccessLevel:async x=>sent.push(['session-access',x]),get:async k=>({[k]:data[k]}),set:async x=>Object.assign(data,x)},local:{get:async k=>({[k]:localStore[k]}),set:async x=>Object.assign(localStore,x),remove:async k=>{delete localStore[k];},setAccessLevel:async x=>sent.push(['local-access',x])}},
    action:{onClicked:{addListener(fn){listeners.click=fn;}}},sidePanel:{open:async()=>{}},alarms:{create(){},onAlarm:{addListener(){}}},
    permissions:{contains:async()=>true},tabs:{get:async()=>({url:'https://aistudio.google.com/app/prompts/abc'}),sendMessage:async()=>({supported:true,conversationKey:'c',ok:true})},
    identity:{getRedirectURL:()=> 'https://test.chromiumapp.org/webcodex',launchWebAuthFlow:async()=>''}};
  const context={chrome,crypto:require('node:crypto').webcrypto,URL,URLSearchParams,TextEncoder,TextDecoder,Uint8Array,ArrayBuffer,JSON,Promise,Set,Map,Date,AbortSignal,Math,Error,String,Object,Number,RegExp,console,btoa:s=>Buffer.from(s,'binary').toString('base64'),fetch:async()=>{throw Error('fetch unexpected')},
    WebCodexProtocol,WebCodexMCP:clientOverride|| (failInitialize?{createClient:()=>({initialize:async()=>{throw Error('expired')}})}:WebCodexMCP),importScripts(){},setTimeout,clearTimeout};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,'background.js'),'utf8'),context,{filename:'background.js'});
  return {listeners,data,sent,chrome};
}
function message(h,msg,sender){return new Promise(resolve=>{const async=h.listeners.message(msg,sender,resolve);if(async!==true)resolve(undefined);});}
test('browser restart restores only credentials, refreshes tools and never starts a chat',async()=>{
  const auto={enabled:true,agentId:'a',toolNames:['read_file'],automatic:true,consent:true};
  const localStore={bridgeAuth:{gateUrl:'https://gate.example',clientId:'client',accessToken:'saved-secret'},bridgeAuto:auto};
  const mcp={createClient:()=>({initialize:async()=>({}),listTools:async()=>[{name:'read_file'}],listAgents:async()=>[{id:'a',online:true}]})};
  const h=harness({},false,localStore,mcp),panel={url:'chrome-extension://test-id/panel.html'};
  const reply=await message(h,{type:'bridge.status'},panel);
  assert.equal(reply.state.connected,true);assert.equal(reply.state.session.phase,'idle');
  assert.equal(reply.state.tools[0].name,'read_file');
  assert.deepEqual(reply.state.autoSettings,auto);
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
