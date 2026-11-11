importScripts('protocol.js', 'mcp-client.js');
const KEY = 'webcodexState', SAFE = new Set(['list_agents','list_allowed_roots','list_directory','read_file','search_files','find_files','poll_command','list_commands']);
let state = {connected:false,gateUrl:'',clientId:'',agents:[],tools:[],session:{phase:'idle',agentId:'',toolNames:[],automatic:false,paused:false,turn:'',instructions:'',pendingCall:null,resultText:'',error:'',count:0,tabId:null}};
let token = '', client = null, serial = Promise.resolve(), demo = false, connecting = false;
const uuid = () => crypto.randomUUID();
const clone = x => JSON.parse(JSON.stringify(x));
function publicState() { const s=clone(state); delete s.accessToken; s.connecting=connecting; return s; }
function save() { return chrome.storage.session.set({[KEY]:state}); }
function enqueue(fn) { const p=serial.then(fn); serial=p.catch(()=>{}); return p; }
async function restore() { try { const data=await chrome.storage.session.get(KEY); if(data[KEY]) state=data[KEY]; token=state.accessToken||''; demo=!!state.demoMode; if(token&&state.connected){client=WebCodexMCP.createClient({baseUrl:state.gateUrl,token});await client.initialize();} } catch(e) { state.connected=false;client=null;token='';state.accessToken='';state.session.paused=true;state.session.resumeBlocked=true;state.session.phase='paused';state.session.error='Could not restore Gate connection; reconnect to Gate';await save(); } }
chrome.storage.session.setAccessLevel({accessLevel:'TRUSTED_CONTEXTS'}).catch(()=>{});
chrome.storage.local.setAccessLevel({accessLevel:'TRUSTED_CONTEXTS'}).catch(()=>{});
const ready=restore();
chrome.action.onClicked.addListener(tab=>{ if(tab?.windowId!==undefined) chrome.sidePanel.open({windowId:tab.windowId}).catch(()=>{}); });
chrome.alarms.create('webcodex-recover',{periodInMinutes:0.5});
chrome.alarms.onAlarm.addListener(a=>{if(a.name==='webcodex-recover') enqueue(async()=>{await ready;await recover();});});
chrome.runtime.onMessage.addListener((msg,sender,reply)=>{
  if(connecting&&msg?.type==='bridge.status'&&sender.url===chrome.runtime.getURL('panel.html')){ready.then(()=>reply({ok:true,state:publicState()})).catch(()=>reply({ok:false,error:'Не удалось загрузить состояние'}));return true;}
  if(msg?.type?.startsWith('studio.')) { enqueue(async()=>{await ready;if(sender.frameId!==0||sender.tab?.id!==state.session.tabId||new URL(sender.url||'').origin!=='https://aistudio.google.com')throw Error('Untrusted Studio sender');return studioMessage(msg,sender);}).then(value=>reply({ok:!!value?.accepted,accepted:!!value?.accepted})).catch(e=>reply({ok:false,accepted:false,error:String(e.message||e)})); return true; }
  const connectRequest=msg?.type==='bridge.connect'&&sender.url===chrome.runtime.getURL('panel.html');
  if(connectRequest&&connecting){reply({ok:false,error:'Вход в Gate уже выполняется. Завершите или закройте окно входа.'});return;}
  if(connectRequest)connecting=true;
  enqueue(async()=>{await ready;if(sender.url!==chrome.runtime.getURL('panel.html'))throw Error('Panel sender required');return command(msg)}).then(s=>reply({ok:true,state:s||publicState()})).catch(e=>reply({ok:false,error:String(e.message||e)})).finally(()=>{if(connectRequest)connecting=false;}); return true;
});
async function command(m) {
  switch(m.type) {
    case 'bridge.status': return publicState();
    case 'bridge.connect': await connect(m); break;
    case 'bridge.disconnect': await clearSession();token='';client=null;demo=false;state.accessToken='';state.demoMode=false;state.connected=false;state.gateUrl='';state.clientId='';state.agents=[];state.tools=[];break;
    case 'bridge.demo': await clearSession();demo=true;state.demoMode=true;token='';client=null;state.accessToken='';state.connected=true;state.gateUrl='Demo mode';state.clientId='demo';state.agents=[{id:'demo',name:'Demo agent',online:true}];state.tools=[{name:'read_file',description:'Demo only: return fixed sample text.',inputSchema:{type:'object',additionalProperties:false,properties:{path:{type:'string'}},required:['path']}}];break;
    case 'bridge.arm': await arm(m);break;
    case 'bridge.instructions': if(m.submit!==false) throw Error('submit must be false');await stageInstructions();await save();break;
    case 'bridge.approve': await approve();break;
    case 'bridge.deliver': if(m.submit!==true) throw Error('submit must be true');await deliver(true);break;
    case 'bridge.pause': if(state.session.phase!=='paused')state.session.pausedPhase=state.session.phase;if(state.session.phase==='delivery_uncertain'||state.session.resumeBlocked)state.session.resumeBlocked=true;state.session.paused=true;state.session.phase='paused';break;
    case 'bridge.resume': if(state.session.phase==='stopped'||state.session.resumeBlocked||state.session.phase==='delivery_uncertain'||state.session.pausedPhase==='delivery_uncertain'||state.session.error?.includes('outcome is unknown'))throw Error('Resolve the uncertain operation or re-arm this chat before resuming');state.session.paused=false;state.session.count=0;state.session.budgetStart=Date.now();state.session.phase=state.session.pausedPhase|| (state.session.pendingCall?(state.session.operationKey?'recovering':'waiting_approval'):(state.session.resultText?'result_ready':'waiting_model'));state.session.pausedPhase='';break;
    case 'bridge.stop': await stop();break;
    default: throw Error('Unknown bridge command');
  }
  await save();return publicState();
}
async function connect({gateUrl,clientId}) {
  if(state.session.phase!=='stopped'&&state.session.resumeBlocked&&state.session.pendingCall)throw Error('Stop the paused session before reconnecting; its operation will not be retried automatically');
  const url=new URL(gateUrl); if(url.origin!==gateUrl || url.username||url.password||url.search||url.hash || !(url.protocol==='https:'||(url.protocol==='http:'&&['localhost','127.0.0.1'].includes(url.hostname)))) throw Error('Invalid Gate origin');
  if(!clientId) throw Error('OAuth client ID required');
  const granted=await chrome.permissions.contains({origins:[url.origin+'/*']});if(!granted)throw Error('Gate host permission is required');
  await clearSession();
  const verifier=Array.from(crypto.getRandomValues(new Uint8Array(32)),b=>b.toString(16).padStart(2,'0')).join(''), challenge=base64url(await crypto.subtle.digest('SHA-256',new TextEncoder().encode(verifier))), nonce=uuid(), redirect=chrome.identity.getRedirectURL('webcodex');
  const auth=new URL('/bridge/v1/authorize',url);Object.entries({client_id:clientId,redirect_uri:redirect,response_type:'code',state:nonce,code_challenge:challenge,code_challenge_method:'S256'}).forEach(([k,v])=>auth.searchParams.set(k,v));
  const returned=await chrome.identity.launchWebAuthFlow({url:auth.href,interactive:true}), callback=new URL(returned);
  if(callback.origin+callback.pathname!==redirect || callback.searchParams.get('state')!==nonce || callback.searchParams.get('iss')!==url.origin || !callback.searchParams.get('code')) throw Error('OAuth response verification failed');
  const controller=new AbortController(),timeout=setTimeout(()=>controller.abort(),15000);let response,text;try{response=await fetch(url.origin+'/bridge/v1/token',{method:'POST',signal:controller.signal,headers:{'Content-Type':'application/x-www-form-urlencoded'},body:new URLSearchParams({grant_type:'authorization_code',client_id:clientId,code:callback.searchParams.get('code'),redirect_uri:redirect,code_verifier:verifier})});if(!response.ok)throw Error('OAuth token exchange failed');text=await readLimited(response,65536);}finally{clearTimeout(timeout);}let data;try{data=JSON.parse(text);}catch{throw Error('Invalid OAuth response');}if(typeof data.access_token!=='string'||!data.access_token)throw Error('OAuth response missing access token');
  token=data.access_token;state.accessToken=token;state.demoMode=false;client=WebCodexMCP.createClient({baseUrl:url.origin,token});const init=await client.initialize();state.gateInstructions=typeof init?.instructions==='string'?init.instructions:'';const all=await client.listTools();state.tools=all.filter(t=>!t.name.startsWith('browser_'));state.agents=await client.listAgents();state.connected=true;state.gateUrl=url.origin;state.clientId=clientId;state.session.phase='idle';demo=false;
}
function base64url(bytes){return btoa(String.fromCharCode(...new Uint8Array(bytes))).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'');}
async function readLimited(response,limit){if(!response.body?.getReader){const text=await response.text();if(new TextEncoder().encode(text).length>limit)throw Error('OAuth response exceeds 64 KiB');return text;}const reader=response.body.getReader(),chunks=[];let size=0;try{while(true){const part=await reader.read();if(part.done)break;size+=part.value.byteLength;if(size>limit){await reader.cancel();throw Error('OAuth response exceeds 64 KiB');}chunks.push(part.value);}}finally{reader.releaseLock?.();}const bytes=new Uint8Array(size);let offset=0;for(const chunk of chunks){bytes.set(chunk,offset);offset+=chunk.length;}return new TextDecoder().decode(bytes);}
async function arm(m) {
  if(!['idle','stopped'].includes(state.session.phase))throw Error('Stop the active session before arming another one');
  if(!m.consent||!Number.isInteger(m.tabId)||!Array.isArray(m.toolNames)||!m.toolNames.length)throw Error('Consent, tab and tools are required');
  const agent=state.agents.find(a=>String(a.id??a.name)===m.agentId);if(!agent||agent.online===false)throw Error('Selected agent is unavailable');
  const selected=m.toolNames.map(name=>state.tools.find(t=>t.name===name));if(selected.some(x=>!x))throw Error('Unknown selected tool');
  const tab=await chrome.tabs.get(m.tabId);if(!tab.url?.startsWith('https://aistudio.google.com/'))throw Error('Active tab must be AI Studio');
  if(!demo&&!state.connected)throw Error('Connect to Gate first');
  state.session={phase:'arming',agentId:m.agentId,toolNames:m.toolNames,automatic:!!m.automatic,paused:false,turn:uuid(),instructions:'',customInstructions:'',pendingCall:null,resultText:'',error:'',count:0,budgetStart:Date.now(),tabId:m.tabId,sessionId:uuid(),conversationKey:''};await save();
  try {const snap=await chrome.tabs.sendMessage(m.tabId,{type:'studio.arm',sessionId:state.session.sessionId});if(!snap?.supported)throw Error(snap?.error||'Unsupported chat');
    state.session.conversationKey=snap.conversationKey;state.session.phase='waiting_model';await stageInstructions();await save();
  } catch(e){state.session.phase='stopped';state.session.error=String(e.message||e);await save();throw e;}
}
async function clearSession(){const s=state.session;if(s?.tabId&&s.sessionId)await chrome.tabs.sendMessage(s.tabId,{type:'studio.disarm',sessionId:s.sessionId}).catch(()=>{});state.session={phase:'idle',agentId:'',toolNames:[],automatic:false,paused:false,turn:'',instructions:'',pendingCall:null,resultText:'',error:'',count:0,tabId:null};await save();}
async function stageInstructions(){const s=state.session;const selected=s.toolNames.map(n=>state.tools.find(t=>t.name===n)).filter(Boolean);s.instructions=WebCodexProtocol.buildInstructions({turn:s.turn,agentId:s.agentId,tools:selected,instructions:[state.gateInstructions,s.customInstructions].filter(Boolean).join('\n\n')});if(new TextEncoder().encode(s.instructions).length>65536)throw Error('Selected tools and instructions exceed 64 KiB; select fewer tools.');const r=await chrome.tabs.sendMessage(s.tabId,{type:'studio.stage',sessionId:s.sessionId,text:s.instructions});if(!r?.ok)throw Error('Could not stage instructions without changing existing text');}
async function studioMessage(m,sender) {
  const s=state.session;if(m.type==='studio.hello'){if(s.phase!=='idle'&&s.phase!=='stopped'){if(s.phase!=='paused')s.pausedPhase=s.phase;s.paused=true;s.phase='paused';s.resumeBlocked=true;s.error='AI Studio content session restarted; stop and re-arm this chat';await save();}return {accepted:true};}if(m.sessionId!==s.sessionId)throw Error('Stale Studio session');
  if(m.type==='studio.snapshot'||m.type==='studio.tick'){if(m.conversationKey!==s.conversationKey||!m.supported){if(s.phase!=='paused')s.pausedPhase=s.phase;s.paused=true;s.phase='paused';s.resumeBlocked=true;s.error=m.error||'Conversation changed';await save();return {accepted:true};}await recover();return {accepted:true};}
    if(m.type==='studio.call'){
    if(s.paused)return {accepted:false};
    if(!['waiting_model','ready'].includes(s.phase))return {accepted:false};
    if(m.conversationKey!==s.conversationKey||m.generating){if(s.phase!=='paused')s.pausedPhase=s.phase;s.paused=true;s.phase='paused';s.resumeBlocked=true;s.error='Conversation changed or generation still active';}
    else if(s.phase==='waiting_model'||s.phase==='ready'){
      try {const tools=s.toolNames.map(n=>state.tools.find(t=>t.name===n)).filter(Boolean);const call=WebCodexProtocol.parseCall(m.text,s.turn,tools);
        if(!call){s.resultText='';s.finalText=m.text;s.phase='ready';}
        else {s.pendingCall={...call,turn:s.turn};s.phase=SAFE.has(call.name)?'ready':'waiting_approval';s.error='';if(s.phase==='ready'){await save();await dispatch();}}
      } catch(e){s.paused=true;s.phase='paused';s.error=String(e.message||e);}
    }
    await save();return {accepted:!s.paused};
  }
  if(m.type==='studio.tick'){await recover();return {accepted:true};}
  throw Error('Unknown Studio message');
}
async function approve(){const s=state.session;if(s.phase!=='waiting_approval'||!s.pendingCall||s.paused)throw Error('No pending approval');await dispatch();}
async function expireAuth(e){if(e?.status!==401) return false;state.connected=false;client=null;token='';state.accessToken='';state.session.pausedPhase=state.session.phase;state.session.phase='paused';state.session.paused=true;state.session.resumeBlocked=true;state.session.error='Gate authorization expired; stop this session, reconnect, then re-arm this chat';await save();return true;}
async function dispatch(){const s=state.session,p=s.pendingCall;if(!p)throw Error('No pending call');const tool=state.tools.find(t=>t.name===p.name);if(!tool)throw Error('Tool unavailable');
  if(s.count>=10||Date.now()-(s.budgetStart||Date.now())>=300000){s.pausedPhase=s.phase;s.paused=true;s.phase='paused';s.error='Session operation limit reached; resume to continue.';await save();return;}
  if(!s.operationKey)s.operationKey=Date.now()+'-'+uuid();const args={...p.arguments};if(tool.inputSchema.properties?.request_key)args.request_key=s.operationKey;
  s.pendingCall={...p,arguments:p.arguments};s.dispatchArgs=args;s.phase='recovering';await save();
  try{let result;if(demo){if(p.name!=='read_file')throw Error('Demo only supports read_file');result={content:[{type:'text',text:'DEMO MODE: sample content for '+String(args.path)}],isError:false};}
    else result=await client.createOperation({key:s.operationKey,name:p.name,arguments:args,agentId:s.agentId});
    await finishResult(result);
  }catch(e){if(await expireAuth(e))return;s.error=String(e.message||e);s.phase='recovering';await save();}
}
async function recover(){const s=state.session;if(s.paused)return;if(s.phase==='delivery_uncertain'){try{const chk=await chrome.tabs.sendMessage(s.tabId,{type:'studio.snapshot',sessionId:s.sessionId,operationId:s.operationId||s.operationKey});if(chk?.conversationKey!==s.conversationKey||!chk?.supported){s.paused=true;s.phase='paused';s.resumeBlocked=true;s.error='Conversation changed';await save();return;}if(chk?.delivered){completeDelivery();await save();}}catch(e){s.paused=true;s.phase='paused';s.resumeBlocked=true;s.error='AI Studio session is unavailable; stop and re-arm this chat';await save();}return;}
  if(s.phase!=='recovering'||!s.pendingCall)return;
  try{let result;if(demo)result={content:[{type:'text',text:'DEMO MODE: sample content'}],isError:false};else if(s.operationId)result=await client.getOperation(s.operationId);else result=await client.createOperation({key:s.operationKey,name:s.pendingCall.name,arguments:s.dispatchArgs,agentId:s.agentId});
    if(['accepted','dispatching','starting','running'].includes(result?.status)){s.operationId=result.operation_id||result.id||s.operationId;await save();return;}
    await finishResult(result);
  }catch(e){if(await expireAuth(e))return;s.error=String(e.message||e);await save();}
}
async function finishResult(result){const s=state.session;s.operationId=result?.operation_id||result?.id||s.operationId;if(['accepted','dispatching','starting','running'].includes(result?.status)){await save();return;}if(result?.status&& !['completed','failed','outcome_unknown'].includes(result.status)){s.error='Unexpected operation status; waiting for a terminal result.';await save();return;}if(result?.status==='outcome_unknown'){s.phase='paused';s.paused=true;s.resumeBlocked=true;s.error='Operation outcome is unknown; do not retry it.';await save();return;}const hasResult=!!result&&Object.prototype.hasOwnProperty.call(result,'result'),toolResult=hasResult?result.result:result;let commandSessionId=toolResult?.structuredContent?.session_id||toolResult?.session_id;for(const item of toolResult?.content||[])if(item.type==='text'&&typeof item.text==='string')try{const parsed=JSON.parse(item.text);commandSessionId=parsed.session_id||parsed.structuredContent?.session_id||commandSessionId;}catch{}if(commandSessionId)s.commandSessionId=commandSessionId;const transportError=result?.transport_error||(result?.status==='failed'&&!hasResult?(result.error||result.message||'Operation failed'):undefined);s.resultText=WebCodexProtocol.buildResult({turn:s.turn,nextTurn:uuid(),operationId:s.operationId||s.operationKey,name:s.pendingCall.name,result:toolResult,error:transportError});s.nextTurn=JSON.parse(s.resultText).next_turn;s.phase='result_ready';if(new TextEncoder().encode(s.resultText).length>32768){s.phase='paused';s.paused=true;s.error='Result exceeds 32 KiB; reduce the tool output before delivery.';}await save();if(s.automatic&&!s.paused)try{await deliver(true);}catch(e){s.error=String(e.message||e);await save();}}
async function deliver(submit){const s=state.session;if(!s.resultText)throw Error('No result to deliver');if(s.paused)throw Error(s.error||'Session is paused');let envelope;try{envelope=JSON.parse(s.resultText);}catch{throw Error('Only a webcodex-result can be delivered');}if(envelope.type!=='webcodex-result')throw Error('Only a webcodex-result can be delivered');if(s.phase==='delivery_uncertain')throw Error('Delivery status is unknown; check AI Studio before taking action');if(submit){s.phase='delivery_uncertain';await save();}let r;try{r=await chrome.tabs.sendMessage(s.tabId,{type:'studio.deliver',sessionId:s.sessionId,text:s.resultText,submit});}catch(e){throw Error('Delivery status is unknown; do not send again');}if(r?.conversationKey!==s.conversationKey||!r?.supported){if(!r?.attempted){s.phase='result_ready';await save();}throw Error('Conversation changed; delivery is blocked');}if(!r?.ok){if(!r?.attempted){s.phase='result_ready';await save();}throw Error(r?.attempted?'Delivery status is unknown; do not send again':'AI Studio draft could not be safely delivered');}if(!submit){s.phase='result_ready';await save();return;}if(r.confirmed){completeDelivery();await save();}}
function completeDelivery(){const s=state.session;s.phase='waiting_model';s.turn=s.nextTurn;s.pendingCall=null;s.resultText='';s.operationKey='';s.operationId='';s.dispatchArgs=null;s.count++;if(s.count>=10||Date.now()-(s.budgetStart||Date.now())>=300000){s.pausedPhase='waiting_model';s.phase='paused';s.paused=true;s.error='Session operation limit reached; resume to continue.';}}
async function stop(){
  const s=state.session;
  await chrome.tabs.sendMessage(s.tabId,{type:'studio.disarm',sessionId:s.sessionId}).catch(()=>{});
  if(s.commandSessionId){
    if(!client||!s.toolNames.includes('cancel_command'))s.error='Command session is known but cancel_command was not selected.';
    else try {
      const result=await client.rpc('tools/call',{name:'cancel_command',arguments:{session_id:s.commandSessionId,agent_id:s.agentId}});
      if(result?.isError===true)s.error='Command cancellation was rejected by Gate.';
    } catch(e){s.error='Command cancellation could not be confirmed: '+String(e.message||e);}
  }
  s.phase='stopped';s.paused=true;await save();
}
