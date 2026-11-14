const test=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const WebCodexProtocol=require('./protocol.js');

function harness({session={},localAvailable=true,gateAvailable=true,gateAuth=false,oauthSuccess=false,bridgeAuto}={}){
  const listeners={},sessionStore={},localStore={bridgeAutoSetupV1:true,...(bridgeAuto?{bridgeAuto}:{}),...(gateAuth?{bridgeAuth:{gateUrl:'https://gate.example',clientId:'id',accessToken:'token'}}:{})},calls={local:0,gate:0};let disconnectLocal=false;
  const event=fn=>({addListener:fn});
  const chrome={runtime:{getURL:p=>'chrome-extension://test/'+p,onMessage:event(fn=>listeners.message=fn)},storage:{session:{setAccessLevel:async()=>{},get:async k=>({[k]:sessionStore[k]}),set:async x=>Object.assign(sessionStore,x)},local:{setAccessLevel:async()=>{},get:async k=>({[k]:localStore[k]}),set:async x=>Object.assign(localStore,x),remove:async k=>delete localStore[k]}},action:{onClicked:event(()=>{})},sidePanel:{open:async()=>{}},alarms:{create(){},onAlarm:event(fn=>listeners.alarm=fn)},permissions:{contains:async()=>true},tabs:{get:async()=>({url:'https://aistudio.google.com/app/prompts/a'}),sendMessage:async()=>({supported:true,ok:true,conversationKey:'chat'})},identity:{getRedirectURL:()=> 'https://extension.test/callback',launchWebAuthFlow:async({url})=>{if(!oauthSuccess)throw Error('OAuth cancelled');const auth=new URL(url);return 'https://extension.test/callback?state='+auth.searchParams.get('state')+'&iss='+encodeURIComponent(auth.origin)+'&code=code'}}};
  if(session)sessionStore.webcodexState={connected:false,gateUrl:'',clientId:'',agents:[],tools:[],session:{phase:'idle',transport:'',agentId:'',toolNames:[],automatic:false,paused:false,turn:'',instructions:'',pendingCall:null,resultText:'',error:'',count:0,tabId:null},...session};
  const native={createClient:()=>({endpoint:'http://127.0.0.1:47831',agentIdentity:'agent-one',initialize:async()=>{calls.local++;if(!localAvailable)throw Error('native host unavailable');return{}},listTools:async()=>{if(disconnectLocal){const e=Error('native disconnected');e.code='NATIVE_DISCONNECTED';throw e;}return[{name:'read_file',inputSchema:{type:'object',properties:{path:{type:'string'}},required:['path']}}]},listAgents:async()=>[{id:'local',online:true}],createOperation:async()=>({content:[{type:'text',text:'local'}]}),getOperation:async()=>({}),rpc:async()=>({}),close:async()=>{}})};
  const gate={createClient:()=>({initialize:async()=>{calls.gate++;if(!gateAvailable)throw Error('gate offline');return{}},listTools:async()=>[{name:'read_file',inputSchema:{type:'object',properties:{path:{type:'string'}},required:['path']}}],listAgents:async()=>[{id:'remote',online:true}],createOperation:async()=>{calls.gate++;return{content:[{type:'text',text:'gate'}]}},getOperation:async()=>({}),rpc:async()=>({})})};
  const context={chrome,crypto:require('node:crypto').webcrypto,URL,URLSearchParams,TextEncoder,TextDecoder,Uint8Array,ArrayBuffer,JSON,Promise,Set,Map,Date,AbortSignal,AbortController,Math,Error,String,Object,Number,RegExp,console,btoa:s=>Buffer.from(s,'binary').toString('base64'),fetch:async()=>({ok:true,text:async()=>JSON.stringify({access_token:'new-token'})}),WebCodexProtocol, WebCodexSession:require('./session-state.js'), WebCodexAttachments:require('./attachments.js'),WebCodexMCP:gate,WebCodexLocal:native,importScripts(){},setTimeout,clearTimeout};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,'background.js'),'utf8'),context,{filename:'background.js'});
  return{listeners,sessionStore,localStore,calls,chrome,disconnectLocal:()=>{disconnectLocal=true;}};
}
function message(h,msg){return new Promise(resolve=>h.listeners.message(msg,{url:'chrome-extension://test/panel.html'},resolve));}

test('restores local transport before saved Gate credentials and preserves local agent selection',async()=>{
  const h=harness({gateAuth:true});await new Promise(r=>setTimeout(r,0));
  const status=await message(h,{type:'bridge.status'});
  assert.equal(status.state.transport,'local');assert.equal(status.state.agents[0].id,'local');assert.equal(h.calls.gate,0);
  assert.equal(h.localStore.bridgeAuth.accessToken,'token');
});

test('falls back to saved Gate credentials only when local initialization fails',async()=>{
  const h=harness({localAvailable:false,gateAuth:true});await new Promise(r=>setTimeout(r,0));
  const status=await message(h,{type:'bridge.status'});
  assert.equal(status.state.transport,'gate');assert.equal(status.state.agents[0].id,'remote');assert.match(status.state.localError,/native host unavailable/);assert.equal(h.calls.gate,1);
});

test('a recovering local operation stays pinned when local reconnect fails',async()=>{
  const h=harness({localAvailable:false,gateAuth:true,session:{connected:true,transport:'local',gateUrl:'https://gate.example',clientId:'id',accessToken:'token',agents:[{id:'local',online:true}],tools:[{name:'read_file',inputSchema:{type:'object',properties:{path:{type:'string'}},required:['path']}}],session:{phase:'recovering',transport:'local',agentId:'local',toolNames:['read_file'],automatic:false,paused:false,pendingCall:{name:'read_file',arguments:{path:'x'}},operationKey:'key',dispatchArgs:{path:'x'},resultText:'',error:'',count:0,tabId:1}}});
  await new Promise(r=>setTimeout(r,0));await message(h,{type:'bridge.status'});h.listeners.alarm({name:'webcodex-recover'});await new Promise(r=>setTimeout(r,0));
  assert.equal(h.calls.gate,0);assert.equal(h.sessionStore.webcodexState.session.transport,'local');
  assert.equal(h.sessionStore.webcodexState.session.phase,'recovering');assert.equal(h.sessionStore.webcodexState.session.resumeBlocked,undefined);
});

test('Gate fallback setup preserves the active local chat and saves credentials',async()=>{
  const session={phase:'waiting_model',transport:'local',agentId:'local',toolNames:['read_file'],automatic:false,paused:false,turn:'turn',instructions:'local instructions',pendingCall:null,resultText:'',error:'',count:0,tabId:1,sessionId:'live'};
  const h=harness({oauthSuccess:true,session:{session:{...session}}});await new Promise(r=>setTimeout(r,0));
  const result=await message(h,{type:'bridge.connect',configureGate:true,gateUrl:'https://gate.example',clientId:'client'});
  assert.equal(result.ok,true,result.error);assert.equal(result.state.transport,'local');assert.equal(result.state.session.sessionId,'live');assert.equal(result.state.session.instructions,'local instructions');assert.equal(h.localStore.bridgeAuth.accessToken,'new-token');assert.equal(h.localStore.bridgeAuto.gateAgentId,'remote');
});

test('a new Gate fallback arm maps the local selection to its saved sole Gate agent',async()=>{
  const h=harness({localAvailable:false,gateAuth:true,bridgeAuto:{enabled:true,consent:true,agentId:'local',gateAgentId:'remote',toolNames:['read_file'],automatic:false,autoApprove:false}});await new Promise(r=>setTimeout(r,0));
  const armed=await message(h,{type:'bridge.arm',consent:true,agentId:'local',toolNames:['read_file'],tabId:1});
  assert.equal(armed.ok,true,armed.error);assert.equal(armed.state.transport,'gate');assert.equal(armed.state.session.transport,'gate');assert.equal(armed.state.session.agentId,'remote');
  assert.equal(h.localStore.bridgeAuto.agentId,'remote');
});

test('cancelled Gate fallback setup preserves the active local chat',async()=>{
  const h=harness({session:{session:{phase:'waiting_model',transport:'local',agentId:'local',toolNames:['read_file'],paused:false,tabId:1,sessionId:'live'}}});await new Promise(r=>setTimeout(r,0));
  const result=await message(h,{type:'bridge.connect',configureGate:true,gateUrl:'https://gate.example',clientId:'client'});
  assert.equal(result.ok,false);assert.equal(result.error,'OAuth cancelled');assert.equal(h.sessionStore.webcodexState.session.sessionId,'live');assert.equal(h.sessionStore.webcodexState.session.phase,'waiting_model');
});

test('a saved Gate session without credentials never switches to local',async()=>{
  const h=harness({session:{session:{phase:'recovering',transport:'gate',agentId:'remote',toolNames:['read_file'],paused:false,tabId:1,sessionId:'legacy',pendingCall:{name:'read_file',arguments:{path:'x'}},operationKey:'key'}}});await new Promise(r=>setTimeout(r,0));
  assert.equal(h.calls.local,0);assert.equal(h.sessionStore.webcodexState.transport,'gate');assert.match(h.sessionStore.webcodexState.session.error,/credentials are unavailable/);
});

test('a confirmed empty new chat recovers an automatic editor pause and stages instructions',async()=>{
  const h=harness({bridgeAuto:{enabled:true,consent:true,agentId:'local',toolNames:['read_file'],automatic:true},session:{session:{phase:'paused',transport:'local',localEndpoint:'http://127.0.0.1:47831',localAgentIdentity:'agent-one',agentId:'local',toolNames:['read_file'],automatic:true,paused:true,userPaused:false,resumeBlocked:true,error:'editor unavailable',tabId:1,sessionId:'old-session',pageInstance:'same-page',pendingCall:null,resultText:'',count:0}}});
  await new Promise(resolve=>setTimeout(resolve,0));
  const response=await new Promise(resolve=>h.listeners.message({type:'studio.hello',pageInstance:'same-page',conversationReplaced:true,supported:true,editorEmpty:true,generating:false},{url:'https://aistudio.google.com/app/prompts/new',frameId:0,tab:{id:1}},resolve));
  assert.equal(response.accepted,true);
  assert.equal(h.sessionStore.webcodexState.session.phase,'waiting_model');
  assert.notEqual(h.sessionStore.webcodexState.session.sessionId,'old-session');
  assert.match(h.sessionStore.webcodexState.session.instructions,/Available tools/);
});

test('a newly loaded empty chat recovers an automatic pause without a prior New chat click',async()=>{
  const h=harness({bridgeAuto:{enabled:true,consent:true,agentId:'local',toolNames:['read_file'],automatic:true},session:{session:{phase:'paused',transport:'local',agentId:'local',toolNames:['read_file'],paused:true,userPaused:false,error:'editor unavailable',tabId:1,sessionId:'old-session',pageInstance:'old-page',pendingCall:null,resultText:'',count:0}}});
  await new Promise(resolve=>setTimeout(resolve,0));
  const response=await new Promise(resolve=>h.listeners.message({type:'studio.hello',pageInstance:'new-page',blankChat:true,supported:true,editorEmpty:true,generating:false},{url:'https://aistudio.google.com/app/prompts/new',frameId:0,tab:{id:1}},resolve));
  assert.equal(response.accepted,true);
  assert.equal(h.sessionStore.webcodexState.session.phase,'waiting_model');
});

test('a selected empty tab in the same window can take over an idle chat without switching pending work',async()=>{
  const auto={enabled:true,consent:true,agentId:'local',toolNames:['read_file'],automatic:true};
  for(const pending of [false,true]){
    const h=harness({bridgeAuto:auto,session:{session:{phase:'waiting_model',transport:'local',agentId:'local',toolNames:['read_file'],paused:false,error:'',tabId:1,sessionId:'old-session',pageInstance:'old-page',pendingCall:pending?{name:'read_file',arguments:{path:'safe.txt'}}:null,resultText:'',count:0}}});
    h.chrome.tabs.get=async()=>({windowId:5,url:'https://aistudio.google.com/app/prompts/old'});
    await new Promise(resolve=>setTimeout(resolve,0));
    const response=await new Promise(resolve=>h.listeners.message({type:'studio.hello',pageInstance:'new-page',blankChat:true,supported:true,editorEmpty:true,generating:false},{url:'https://aistudio.google.com/app/prompts/new',frameId:0,tab:{id:2,active:true,windowId:5}},resolve));
    assert.equal(response.accepted,!pending);
    assert.equal(h.sessionStore.webcodexState.session.tabId,pending?1:2);
  }
});

test('saved-chat instruction checks only stage system text and preserve pending operations',async()=>{
  for(const pending of [false,true]){
    const h=harness({bridgeAuto:{enabled:true,consent:true,agentId:'local',toolNames:['read_file'],automatic:true},session:{session:{phase:'waiting_model',transport:'local',localEndpoint:'http://127.0.0.1:47831',localAgentIdentity:'agent-one',agentId:'local',toolNames:['read_file'],automatic:true,paused:false,error:'',tabId:1,sessionId:'current-session',pendingCall:pending?{name:'read_file',arguments:{path:'pending.txt'}}:null,resultText:'',count:0}}});
    const staged=[];h.chrome.tabs.sendMessage=async(_tab,message)=>{staged.push(message);return{ok:true,supported:true};};
    await new Promise(resolve=>setTimeout(resolve,0));
    const response=await new Promise(resolve=>h.listeners.message({type:'studio.ensureInstructions',sessionId:'current-session'},{url:'https://aistudio.google.com/app/prompts/saved',frameId:0,tab:{id:1}},resolve));
    assert.equal(response.accepted,!pending);
    assert.equal(staged.length,pending?0:2);
    if(!pending){const system=staged.find(message=>message.type==='studio.stage');assert.equal(system.text,'');assert.equal(system.ensureOnly,true);assert.match(staged.find(message=>message.type==='studio.bindMarker').sessionMarker,/^[a-f0-9]{32}$/);}
    assert.equal(h.sessionStore.webcodexState.session.sessionId,'current-session');
    assert.equal(h.calls.gate,0);
  }
});

test('opening a selected saved-chat tab starts its instructions without taking over pending work',async()=>{
  for(const pending of [false,true]){
    const h=harness({bridgeAuto:{enabled:true,consent:true,agentId:'local',toolNames:['read_file'],automatic:true},session:{session:{phase:'waiting_model',transport:'local',localEndpoint:'http://127.0.0.1:47831',localAgentIdentity:'agent-one',agentId:'local',toolNames:['read_file'],paused:false,tabId:1,sessionId:'old-session',pageInstance:'old-page',pendingCall:pending?{name:'read_file',arguments:{path:'pending.txt'}}:null,resultText:'',count:0}}});
    h.chrome.tabs.get=async()=>({windowId:5,url:'https://aistudio.google.com/app/prompts/chat'});
    await new Promise(resolve=>setTimeout(resolve,0));
    const response=await new Promise(resolve=>h.listeners.message({type:'studio.hello',pageInstance:'saved-page',hasHistory:true,supported:true,editorEmpty:false,generating:false},{url:'https://aistudio.google.com/app/prompts/saved',frameId:0,tab:{id:2,active:true,windowId:5}},resolve));
    assert.equal(response.accepted,!pending);
    assert.equal(h.sessionStore.webcodexState.session.tabId,pending?1:2);
  }
});
