const test=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const WebCodexProtocol=require('./protocol.js');

function harness({session={},localAvailable=true,gateAvailable=true,gateAuth=false,gateEnabled=true,oauthSuccess=false,bridgeAuto,operations={},storage={}}={}){
  const listeners={},sessionStore={},localStore={bridgeAutoSetupV1:true,...(bridgeAuto?{bridgeAuto}:{}),...(gateAuth?{bridgeAuth:{gateUrl:'https://gate.example',clientId:'id',accessToken:'token'}}:{}),...structuredClone(storage)},calls={local:0,gate:0};let disconnectLocal=false;
  const event=fn=>({addListener:fn});
  const chrome={runtime:{getURL:p=>'chrome-extension://test/'+p,onMessage:event(fn=>listeners.message=fn)},storage:{session:{setAccessLevel:async()=>{},get:async k=>({[k]:structuredClone(sessionStore[k])}),set:async x=>Object.assign(sessionStore,structuredClone(x))},local:{setAccessLevel:async()=>{},get:async k=>({[k]:structuredClone(localStore[k])}),set:async x=>Object.assign(localStore,structuredClone(x)),remove:async k=>delete localStore[k]}},action:{onClicked:event(()=>{})},sidePanel:{open:async()=>{}},alarms:{create(){},onAlarm:event(fn=>listeners.alarm=fn)},permissions:{contains:async()=>true},tabs:{get:async()=>({url:'https://aistudio.google.com/app/prompts/a'}),sendMessage:async()=>({supported:true,ok:true,conversationKey:'chat'})},identity:{getRedirectURL:()=> 'https://extension.test/callback',launchWebAuthFlow:async({url})=>{if(!oauthSuccess)throw Error('OAuth cancelled');const auth=new URL(url);return 'https://extension.test/callback?state='+auth.searchParams.get('state')+'&iss='+encodeURIComponent(auth.origin)+'&code=code'}}};
  if(session)sessionStore.webcodexState={connected:false,gateUrl:'',clientId:'',agents:[],tools:[],session:{phase:'idle',transport:'',agentId:'',toolNames:[],automatic:false,paused:false,turn:'',instructions:'',pendingCall:null,resultText:'',error:'',count:0,tabId:null},...session};
  const native={createClient:()=>({endpoint:'http://127.0.0.1:47831',agentIdentity:'agent-one',initialize:async()=>{calls.local++;if(!localAvailable)throw Error('native host unavailable');return{}},listTools:async()=>{if(disconnectLocal){const e=Error('native disconnected');e.code='NATIVE_DISCONNECTED';throw e;}return[{name:'read_file',inputSchema:{type:'object',properties:{path:{type:'string'}},required:['path']}}]},listAgents:async()=>[{id:'local',online:true}],createOperation:async()=>({content:[{type:'text',text:'local'}]}),getOperation:async()=>({}),rpc:async()=>({}),close:async()=>{}})};
  const makeLocal = native.createClient;
  native.createClient = operations.clientFactory || (() => ({...makeLocal(),
    getOperationByKey: operations.unsupported ? undefined : async key => {
      calls.lookups = (calls.lookups || 0) + 1;
      assert.equal(key, 'saved-key');
      if(operations.beforeLookup)await operations.beforeLookup();
      return operations.result || {status:'not_found'};
    },
    createOperation: async request => {
      calls.creates = (calls.creates || 0) + 1;
      assert.equal(request.key, 'saved-key');
      return {operation_id:'saved-id',status:'completed',result:{content:[{type:'text',text:'saved result'}]}};
    }
  }));
  const gate={createClient:()=>({initialize:async()=>{calls.gate++;if(!gateAvailable)throw Error('gate offline');return{}},listTools:async()=>[{name:'read_file',inputSchema:{type:'object',properties:{path:{type:'string'}},required:['path']}}],listAgents:async()=>[{id:'remote',online:true}],createOperation:async()=>{calls.gate++;return{content:[{type:'text',text:'gate'}]}},getOperation:async()=>({}),rpc:async()=>({})})};
  if(operations.gateClientFactory)gate.createClient=operations.gateClientFactory;
  const context={chrome,crypto:require('node:crypto').webcrypto,URL,URLSearchParams,TextEncoder,TextDecoder,Uint8Array,ArrayBuffer,JSON,Promise,Set,Map,Date,AbortSignal,AbortController,Math,Error,String,Object,Number,RegExp,console,btoa:s=>Buffer.from(s,'binary').toString('base64'),fetch:async()=>({ok:true,text:async()=>JSON.stringify({access_token:'new-token'})}),WebCodexProtocol, WebCodexSession:require('./session-state.js'), WebCodexAttachments:require('./attachments.js'),WebCodexMCP:gate,WebCodexLocal:native,importScripts(){},setTimeout,clearTimeout};
  const source=fs.readFileSync(path.join(__dirname,'background.js'),'utf8');
  vm.runInNewContext(gateEnabled?source.replace('const GATE_ENABLED = false;','const GATE_ENABLED = true;'):source,context,{filename:'background.js'});
  return{listeners,sessionStore,localStore,calls,chrome,disconnectLocal:()=>{disconnectLocal=true;}};
}
function message(h,msg){return new Promise(resolve=>h.listeners.message(msg,{url:'chrome-extension://test/panel.html'},resolve));}

test('production local-only build ignores saved remote authorization and never starts OAuth',async()=>{
  assert.match(fs.readFileSync(path.join(__dirname,'background.js'),'utf8'),/const GATE_ENABLED = false;/);
  for(const localAvailable of [false,true]){
    const h=harness({gateEnabled:false,gateAuth:true,localAvailable,oauthSuccess:true});
    let oauthCalls=0;h.chrome.identity.launchWebAuthFlow=async()=>{oauthCalls++;throw Error('unexpected OAuth');};
    const status=await message(h,{type:'bridge.status'});
    assert.equal(status.state.connected,localAvailable);
    assert.notEqual(status.state.transport,'gate');
    const attempt=await message(h,{type:'bridge.connect',configureGate:true,gateUrl:'https://gate.example',clientId:'client'});
    assert.equal(attempt.ok,localAvailable);
    assert.equal(h.calls.gate,0);assert.equal(oauthCalls,0);
  }
});

test('production local-only build preserves a remote operation without replaying it locally',async()=>{
  const session=recoveringLocal();session.session.transport='gate';session.session.agentId='remote';session.session.operationId='remote-operation';
  const h=harness({gateEnabled:false,session,gateAuth:true});
  const result=await message(h,{type:'bridge.status'}),saved=result.state.session;
  assert.equal(saved.phase,'paused');assert.equal(saved.resumeBlocked,true);
  assert.equal(saved.operationKey,'saved-key');assert.equal(saved.operationId,'remote-operation');
  assert.equal(saved.pendingCall.name,'read_file');assert.equal(h.calls.gate,0);
  await recoverOnce(h);assert.equal(h.calls.creates||0,0);assert.equal(h.calls.gate,0);
});

function recoveringLocal(paused=false){
  return {session:{phase:paused?'paused':'recovering',pausedPhase:paused?'recovering':'',transport:'local',
    localEndpoint:'http://127.0.0.1:47831',localAgentIdentity:'agent-one',agentId:'local',toolNames:['read_file'],
    automatic:false,paused,tabId:1,sessionId:'saved-session',pendingCall:{name:'read_file',arguments:{path:'x'}},
    operationKey:'saved-key',dispatchArgs:{path:'x'},resultText:'',count:0}};
}
async function recoverOnce(h){
  await message(h,{type:'bridge.status'});
  h.listeners.alarm({name:'webcodex-recover'});
  return (await message(h,{type:'bridge.status'})).state.session;
}

test('a lost Gate response is looked up without creating again or switching to local',async()=>{
  for(const paused of [false,true]){
    const session=recoveringLocal(paused);Object.assign(session,{connected:true,transport:'gate',gateUrl:'https://gate.example',accessToken:'token'});
    Object.assign(session.session,{transport:'gate',agentId:'remote'});
    const methods=[];
    const h=harness({session,gateAuth:true,operations:{gateClientFactory:()=>({
      initialize:async()=>({}),
      listTools:async()=>[{name:'read_file',inputSchema:{type:'object',properties:{path:{type:'string'}},required:['path']}}],
      listAgents:async()=>[{id:'remote',online:true}],
      getOperationByKey:async key=>{methods.push(['lookup',key]);return{id:'saved-gate-id',status:'completed',result:{content:[{type:'text',text:'Gate recovered'}]}};},
      createOperation:async()=>{throw Error('must not create a recovered operation');}
    })}});
    const state=await recoverOnce(h);
    assert.deepEqual(methods,[['lookup','saved-key']]);
    assert.equal(state.transport,'gate');assert.equal(state.agentId,'remote');
    assert.equal(state.operationId,'saved-gate-id');assert.match(state.resultText,/Gate recovered/);
    assert.equal(state.phase,paused?'paused':'result_ready');assert.equal(h.calls.local,0);
  }
});

test('lost create response recovers by key with no create or Gate request, including while paused',async()=>{
  for(const paused of [false,true]){
    const h=harness({session:recoveringLocal(paused),operations:{result:{operation_id:'saved-id',status:'completed',result:{content:[{type:'text',text:'recovered'}]}}}});
    const s=await recoverOnce(h);
    assert.equal(h.calls.lookups,1);assert.equal(h.calls.creates||0,0);assert.equal(h.calls.gate,0);
    assert.equal(s.operationKey,'saved-key');assert.equal(s.operationId,'saved-id');assert.match(s.resultText,/recovered/);
    assert.equal(s.phase,paused?'paused':'result_ready');assert.equal(s.paused,paused);
  }
});

test('missing key is created only after an unpaused lookup and retains its original intent',async()=>{
  for(const paused of [false,true]){
    const h=harness({session:recoveringLocal(paused)});
    const s=await recoverOnce(h);
    assert.equal(h.calls.lookups,1);assert.equal(h.calls.creates||0,paused?0:1);assert.equal(h.calls.gate,0);
    assert.equal(s.operationKey,'saved-key');assert.equal(s.phase,paused?'paused':'result_ready');
  }
});

test('old agent, expired key and unknown outcome preserve intent without resending',async()=>{
  for(const operations of [{unsupported:true},{result:{status:'expired'}},{result:{operation_id:'saved-id',status:'outcome_unknown'}},{result:{}}]){
    const h=harness({session:recoveringLocal(),operations});
    const s=await recoverOnce(h);
    assert.equal(h.calls.creates||0,0);assert.equal(h.calls.gate,0);assert.equal(s.operationKey,'saved-key');
    if(operations.unsupported)assert.match(s.error,/Обновите агент/);
    else if(operations.result.status==='expired')assert.equal(s.reasonCode,'operation_expired');
    else if(operations.result.status==='outcome_unknown')assert.equal(s.reasonCode,'outcome_unknown');
    else assert.match(s.error,/Invalid operation lookup/);
  }
});

test('pause during a missing-key lookup cancels creation before the queued pause is applied',async()=>{
  let enter,release;
  const entered=new Promise(resolve=>enter=resolve),blocked=new Promise(resolve=>release=resolve);
  const h=harness({session:recoveringLocal(),operations:{beforeLookup:async()=>{enter();await blocked;}}});
  const recovering=recoverOnce(h);
  await entered;
  const pausing=message(h,{type:'bridge.pause'});
  release();
  await recovering;await pausing;
  const s=(await message(h,{type:'bridge.status'})).state.session;
  assert.equal(h.calls.creates||0,0);assert.equal(h.calls.gate,0);assert.equal(s.paused,true);assert.equal(s.operationKey,'saved-key');
});

async function directSequence(fault,noise){
  const endpoint='http://127.0.0.1:47831',agentIdentity='sequence-agent',marker='0123456789abcdef0123456789abcdef';
  const records=new Map(),requests=[],deliveries=[];
  let effects=0,failed=false,h;
  const tools=[{name:'write_file',inputSchema:{type:'object',properties:{path:{type:'string'},content:{type:'string'},if_absent:{const:true}},required:['path','content','if_absent'],additionalProperties:false}}];
  const clientFactory=options=>require('./local-client.js').createClient({...options,fetchImpl:async(url,request)=>{
    assert.equal(url,endpoint+'/studio/rpc');
    const rpc=JSON.parse(request.body);requests.push(rpc);
    let result;
    if(rpc.method==='initialize')result={localAgent:{id:agentIdentity},studioOperations:{lookupByKey:true}};
    else if(rpc.method==='tools/list')result={tools};
    else if(rpc.method==='tools/call')result={structuredContent:{agents:[{id:'local',online:true}]}};
    else if(rpc.method==='operations/lookup')result=records.get(rpc.params.key)||{status:'not_found'};
    else if(rpc.method==='operations/get')result=[...records.values()].find(record=>record.operation_id===rpc.params.id);
    else if(rpc.method==='operations/create'){
      const {key,arguments:args}=rpc.params;
      assert.equal(args.content,'Привет 😀\r\n"quotes" \\path\n');
      if(!failed&&fault==='before_accept'){failed=true;throw new TypeError('request lost before acceptance');}
      result=records.get(key);
      if(!result){
        effects++;
        result={operation_id:'op-'+key,status:fault==='unknown'?'outcome_unknown':'completed',result:{content:[{type:'text',text:'written'}]}};
        records.set(key,result);
      }
      if(!failed&&['after_effect','unknown'].includes(fault)){failed=true;throw new TypeError('response lost before ID');}
    }else throw Error('unexpected local method '+rpc.method);
    return new Response(JSON.stringify({id:rpc.id,result}));
  }});
  const initial={session:{phase:'waiting_model',transport:'local',localEndpoint:endpoint,localAgentIdentity:agentIdentity,agentId:'local',toolNames:['write_file'],sessionId:'sequence',tabId:1,turnless:true,sessionMarker:marker,turn:'turn',automatic:false,autoApprove:true,paused:false,count:0}};
  function start(storage){
    h=harness({session:initial,storage,operations:{clientFactory},bridgeAuto:{enabled:true,consent:true,autoApprove:true,automatic:false,agentId:'local',toolNames:['write_file']}});
    h.chrome.tabs.sendMessage=async(_id,message)=>{deliveries.push(message);return{supported:true,ok:true,delivered:false};};
  }
  start();await message(h,{type:'bridge.status'});
  const page={url:'https://aistudio.google.com/app/prompts/a',frameId:0,tab:{id:1}};
  const call={type:'studio.call',sessionId:'sequence',responseKey:'answer-one',text:'HELPER_'+marker+' write_file\r\npath: W:/test.txt\r\nif_absent: true\r\n---\r\nПривет 😀\r\n"quotes" \\path\n\r\nEND_'+marker,generating:false};
  await new Promise(resolve=>h.listeners.message(call,page,resolve));
  const key=(await message(h,{type:'bridge.status'})).state.session.operationKey;
  for(const event of noise){
    if(event==='restart'){start(h.localStore);await message(h,{type:'bridge.status'});}
    else if(event==='poll')await recoverOnce(h);
    else if(event==='pause')await message(h,{type:'bridge.pause'});
    else if(event==='resume')await message(h,{type:'bridge.resume'});
    else if(event==='duplicate')await new Promise(resolve=>h.listeners.message(call,page,resolve));
    assert.ok(effects<=1,'duplicate side effect');
    const s=(await message(h,{type:'bridge.status'})).state.session;
    assert.equal(s.operationKey,key);assert.equal(s.transport,'local');assert.equal(h.calls.gate,0);
  }
  if(fault!=='unknown')await message(h,{type:'bridge.resume'});
  await recoverOnce(h);
  const s=(await message(h,{type:'bridge.status'})).state.session;
  assert.equal(effects,1);assert.equal(s.phase,fault==='unknown'?'paused':'result_ready');
  if(fault==='unknown')assert.equal(s.reasonCode,'outcome_unknown');
  else assert.match(s.resultText,/written/);
  assert.equal(deliveries.filter(message=>message.type==='studio.deliver').length,0);
  assert.equal(requests.filter(request=>request.method==='operations/create').length,fault==='before_accept'?2:1);
}

test('seeded direct-route failures preserve one effect through restarts, duplicates and pauses',async()=>{
  const events=['restart','poll','pause','resume','duplicate'];
  for(const fault of ['before_accept','after_effect','unknown'])for(let seed=1;seed<=12;seed++){
    let random=seed;
    const noise=Array.from({length:8},()=>{random=(Math.imul(random,1664525)+1013904223)>>>0;return events[random%events.length];});
    try{await directSequence(fault,noise);}
    catch(error){
      let reduced=[...noise];
      for(let i=0;i<reduced.length;){
        const candidate=reduced.filter((_,index)=>index!==i);
        try{await directSequence(fault,candidate);i++;}catch{reduced=candidate;i=0;}
      }
      throw Error('seed='+seed+' fault='+fault+' reduced='+JSON.stringify(reduced),{cause:error});
    }
  }
});

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


function recoveringGate(paused=false){
  const session=recoveringLocal(paused);
  Object.assign(session,{connected:true,transport:'gate',gateUrl:'https://gate.example',accessToken:'token'});
  Object.assign(session.session,{transport:'gate',agentId:'remote',operationId:'original-gate-id'});
  return session;
}

test('known Gate ID with missing agent journal explicitly recreates the same saved intent once',async()=>{
  const session=recoveringGate(),requests=[];session.session.dispatchArgs={path:'exact-path',delivery:'file'};
  const h=harness({session,gateAuth:true,operations:{gateClientFactory:()=>({
    initialize:async()=>({}),getOperation:async id=>{requests.push(['lookup',id]);return{id,status:'not_found'};},
    createOperation:async request=>{requests.push(['create',structuredClone(request)]);return{id:'original-gate-id',status:'completed',result:{content:[{type:'text',text:'recovered'}]}};}
  })}});
  const state=await recoverOnce(h);
  assert.deepEqual(requests,[['lookup','original-gate-id'],['create',{key:'saved-key',name:'read_file',arguments:{path:'exact-path',delivery:'file'},agentId:'remote'}]]);
  assert.equal(state.operationId,'original-gate-id');assert.equal(state.operationKey,'saved-key');assert.equal(state.phase,'result_ready');assert.equal(h.calls.local,0);
});

test('paused or revoked Gate recovery never posts a missing known-ID operation',async()=>{
  for(const paused of [true,false]){
    const session=recoveringGate(paused);session.session.pendingCall={name:'write_file',arguments:{path:'x'}};session.session.autoApprove=false;
    let creates=0;
    const h=harness({session,gateAuth:true,operations:{gateClientFactory:()=>({initialize:async()=>({}),getOperation:async id=>({id,status:'not_found'}),createOperation:async()=>{creates++;throw Error('must not post');}})}});
    const state=await recoverOnce(h);assert.equal(creates,0);assert.equal(state.operationId,'original-gate-id');
    assert.equal(state.phase,paused?'paused':'waiting_approval');
  }
});

test('lost Gate recovery POST looks up the original ID and pinned route on the next attempt',async()=>{
  const requests=[];let posted=false;
  const h=harness({session:recoveringGate(),gateAuth:true,operations:{gateClientFactory:()=>({
    initialize:async()=>({}),getOperation:async id=>{requests.push(['lookup',id]);return posted?{id,status:'completed',result:{content:[{type:'text',text:'saved'}]}}:{id,status:'not_found'};},
    createOperation:async request=>{requests.push(['create',request.key]);posted=true;throw Error('response lost');}
  })}});
  const first=await recoverOnce(h);assert.equal(first.phase,'recovering');assert.equal(first.operationId,'original-gate-id');
  const second=await recoverOnce(h);assert.equal(second.phase,'result_ready');assert.equal(second.operationId,'original-gate-id');
  assert.deepEqual(requests,[['lookup','original-gate-id'],['create','saved-key'],['lookup','original-gate-id']]);assert.equal(h.calls.local,0);
});


test('Gate fallback setup selects Gate when the saved local connection is disconnected',async()=>{
  const h=harness({localAvailable:false,oauthSuccess:true,session:{transport:'local',session:{phase:'waiting_model',transport:'local',agentId:'local',toolNames:['read_file'],paused:false,tabId:1,sessionId:'stale-local',pendingCall:null}}});
  await message(h,{type:'bridge.status'});
  const result=await message(h,{type:'bridge.connect',configureGate:true,gateUrl:'https://gate.example',clientId:'client'});
  assert.equal(result.ok,true,result.error);assert.equal(result.state.connected,true);assert.equal(result.state.transport,'gate');
  assert.equal(result.state.session.phase,'idle');assert.notEqual(result.state.session.sessionId,'stale-local');
  assert.equal(h.localStore.bridgeAuth.accessToken,'new-token');assert.equal(result.state.agents[0].id,'remote');
});


test('reopening a saved chat restores its registered reply marker when earlier turns are virtualized',async()=>{
  const marker='93b548f38bc24fe986e5817d27af4f43';
  const address='/prompts/saved-chat';
  for(const record of [{tabId:1,address},{tabId:2,address},{tabId:1,address:'/prompts/other-chat'}]){
    const h=harness({gateEnabled:false,storage:{bridgeChatBindings:{[marker]:record}}});
    await new Promise(resolve=>setTimeout(resolve,0));
    h.chrome.tabs.sendMessage=async()=>({ok:true,supported:true,conversationKey:'chat',chatAddress:address,chatMarkers:[],replyMarker:marker});
    const response=await message(h,{type:'bridge.arm',tabId:1,agentId:'local',toolNames:['read_file'],consent:true});
    assert.equal(response.ok,true,response.error);
    assert.equal(response.state.session.sessionMarker===marker,record.tabId===1&&record.address===address);
  }
});
