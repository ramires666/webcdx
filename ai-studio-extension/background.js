importScripts('protocol.js', 'mcp-client.js');
const KEY = 'webcodexState', SAFE = new Set(['list_agents','list_allowed_roots','list_directory','read_file','search_files','find_files','poll_command','list_commands']);
let state = {connected:false,gateUrl:'',clientId:'',agents:[],tools:[],session:{phase:'idle',agentId:'',toolNames:[],automatic:false,paused:false,turn:'',instructions:'',pendingCall:null,resultText:'',error:'',count:0,tabId:null}};
let token = '', client = null, serial = Promise.resolve(), demo = false, connecting = false, lastAutoRefresh = 0, lastAutoRefreshOK = false, autoToolsAvailable = true;
const uuid = () => crypto.randomUUID();
const clone = x => JSON.parse(JSON.stringify(x));
function publicState() { const s=clone(state); delete s.accessToken; s.connecting=connecting; return s; }
async function save() { await chrome.storage.session.set({[KEY]:state});if(state.connected&&state.accessToken&&!state.demoMode)await chrome.storage.local.set({bridgeAuth:{gateUrl:state.gateUrl,clientId:state.clientId,accessToken:state.accessToken}}); }
function enqueue(fn) { const p=serial.then(fn); serial=p.catch(()=>{}); return p; }
async function restore() {
  try {
    const data=await chrome.storage.session.get(KEY);
    if(data[KEY])state=data[KEY];
    if(state.session.conversationKey?.startsWith('/'))state.session.conversationKey='active-chat';
    delete state.session.temporaryConversationKey;
    const {bridgeAuto}=await chrome.storage.local.get('bridgeAuto');state.autoSettings=bridgeAuto?{...bridgeAuto,autoApprove:!!bridgeAuto.autoApprove}:{enabled:false,autoApprove:false};
    state.session.autoApprove=Object.hasOwn(state.session,'autoApprove')?!!state.session.autoApprove:state.autoSettings.autoApprove;
    state.hideServiceMessages=!!(await chrome.storage.local.get('hideServiceMessages')).hideServiceMessages;
    let fresh=false;
    if(!state.accessToken&&!state.demoMode){const {bridgeAuth}=await chrome.storage.local.get('bridgeAuth');if(bridgeAuth?.accessToken){Object.assign(state,bridgeAuth);state.connected=true;fresh=true;}}
    token=state.accessToken||'';demo=!!state.demoMode;
    if(token&&state.connected){client=WebCodexMCP.createClient({baseUrl:state.gateUrl,token});const init=await client.initialize();if(fresh){state.gateInstructions=init?.instructions||'';state.tools=(await client.listTools()).filter(t=>!t.name.startsWith('browser_'));state.agents=await client.listAgents();await migrateAutoSetup();}await save();}
  } catch(e) {
    if(e?.status===401)await chrome.storage.local.remove('bridgeAuth');
    state.connected=false;client=null;token='';state.accessToken='';state.session.paused=true;state.session.resumeBlocked=true;state.session.phase='paused';state.session.error='Could not restore Gate connection; reconnect to Gate';await save();
  }
}
chrome.storage.session.setAccessLevel({accessLevel:'TRUSTED_CONTEXTS'}).catch(()=>{});
chrome.storage.local.setAccessLevel({accessLevel:'TRUSTED_CONTEXTS'}).catch(()=>{});
const ready=restore();
chrome.action.onClicked.addListener(tab=>{ if(tab?.windowId!==undefined) chrome.sidePanel.open({windowId:tab.windowId}).catch(()=>{}); });
chrome.alarms.create('webcodex-recover',{periodInMinutes:0.5});
chrome.alarms.onAlarm.addListener(a=>{if(a.name==='webcodex-recover') enqueue(async()=>{await ready;await recover();});});
chrome.runtime.onMessage.addListener((msg,sender,reply)=>{
  if(connecting&&msg?.type==='bridge.status'&&sender.url===chrome.runtime.getURL('panel.html')){ready.then(()=>reply({ok:true,state:publicState()})).catch(()=>reply({ok:false,error:'Не удалось загрузить состояние'}));return true;}
  if(msg?.type?.startsWith('studio.')) { enqueue(async()=>{await ready;if(sender.frameId!==0||!Number.isInteger(sender.tab?.id)||new URL(sender.url||'').origin!=='https://aistudio.google.com')throw Error('Untrusted Studio sender');if(msg.type==='studio.hello')return autoStart(sender,msg);if(sender.tab.id!==state.session.tabId)throw Error('Untrusted Studio sender');return studioMessage(msg,sender);}).then(value=>reply({ok:!!value?.accepted,accepted:!!value?.accepted,...(value?.conversationKey?{conversationKey:value.conversationKey}:{})})).catch(e=>reply({ok:false,accepted:false,error:String(e.message||e)})); return true; }
  const connectRequest=msg?.type==='bridge.connect'&&sender.url===chrome.runtime.getURL('panel.html');
  if(connectRequest&&connecting){reply({ok:false,error:'Вход в Gate уже выполняется. Завершите или закройте окно входа.'});return;}
  if(connectRequest)connecting=true;
  enqueue(async()=>{await ready;if(sender.url!==chrome.runtime.getURL('panel.html'))throw Error('Panel sender required');return command(msg)}).then(s=>reply({ok:true,state:s||publicState()})).catch(e=>reply({ok:false,error:String(e.message||e)})).finally(()=>{if(connectRequest)connecting=false;}); return true;
});
async function command(m) {
  switch(m.type) {
    case 'bridge.status': await refreshForAutoStart(state.autoSettings);await migrateAutoSetup();updateAutoStatus();return publicState();
    case 'bridge.cleanupPreference':
      state.hideServiceMessages=!!m.enabled;
      await chrome.storage.local.set({hideServiceMessages:state.hideServiceMessages});
      if(state.session.tabId!==null)await chrome.tabs.sendMessage(state.session.tabId,{type:'studio.cleanup',sessionId:state.session.sessionId,enabled:state.hideServiceMessages,operationIds:state.session.completedOperationIds||[],final:state.session.phase==='ready'}).catch(()=>{});
      break;
    case 'bridge.connect': await connect(m); break;
    case 'bridge.disconnect': await disableAuto();await clearSession();await chrome.storage.local.remove('bridgeAuth');token='';client=null;demo=false;state.accessToken='';state.demoMode=false;state.connected=false;state.gateUrl='';state.clientId='';state.agents=[];state.tools=[];break;
    case 'bridge.demo': await disableAuto();await clearSession();await chrome.storage.local.remove('bridgeAuth');demo=true;state.demoMode=true;token='';client=null;state.accessToken='';state.connected=true;state.gateUrl='Demo mode';state.clientId='demo';state.agents=[{id:'demo',name:'Demo agent',online:true}];state.tools=[{name:'read_file',description:'Demo only: return fixed sample text.',inputSchema:{type:'object',additionalProperties:false,properties:{path:{type:'string'}},required:['path']}}];break;
    case 'bridge.arm': await arm(m);await rememberAuto(m);break;
    case 'bridge.preferences':
      {const {bridgeAuto:previous={}}=await chrome.storage.local.get('bridgeAuto');
      const agentId=m.agentId??previous.agentId??'',toolNames=Array.isArray(m.toolNames)?m.toolNames:previous.toolNames||[],consent=m.consent===true;
      const autoApprove=!!(m.autoApprove??previous.autoApprove??false),wasAutoApproving=!!state.session.autoApprove;
      const settings={enabled:consent&&!!agentId&&toolNames.length>0,agentId,toolNames,automatic:m.automatic??previous.automatic??false,autoApprove,consent};
      await chrome.storage.local.set({bridgeAuto:settings,bridgeAutoSetupV1:true});state.autoSettings=settings;
      if(m.automatic!==undefined)state.session.automatic=!!m.automatic;
      state.session.autoApprove=autoApprove;
      if(!wasAutoApproving&&m.autoApprove===true&&autoApprove&&consent&&settings.enabled&&settings.agentId===state.session.agentId&&state.session.toolNames?.includes(state.session.pendingCall?.name)&&state.session.phase==='waiting_approval'&&state.session.pendingCall&&!state.session.paused)await dispatch();
      if(!m.consent&&!['idle','stopped','paused'].includes(state.session.phase)){state.session.pausedPhase=state.session.phase;state.session.paused=true;state.session.phase='paused';}
      }
      updateAutoStatus();
      break;
    case 'bridge.instructions': if(m.submit!==false) throw Error('submit must be false');await stageInstructions();await save();break;
    case 'bridge.approve': await approve();break;
    case 'bridge.deliver': if(m.submit!==true) throw Error('submit must be true');await deliver(true);break;
    case 'bridge.pause': await disableAuto();if(state.session.phase!=='paused')state.session.pausedPhase=state.session.phase;if(state.session.phase==='delivery_uncertain'||state.session.resumeBlocked)state.session.resumeBlocked=true;state.session.paused=true;state.session.userPaused=true;state.session.phase='paused';break;
    case 'bridge.resume':
      if(state.session.phase==='stopped'||state.session.resumeBlocked||state.session.phase==='delivery_uncertain'||state.session.pausedPhase==='delivery_uncertain'||state.session.error?.includes('outcome is unknown'))throw Error('Resolve the uncertain operation or re-arm this chat before resuming');
      if(state.autoSettings?.consent===false)throw Error('Grant consent before resuming');
      {
        const oldSizeResult=state.session.error==='Result exceeds 32 KiB; reduce the tool output before delivery.'&&!!state.session.resultText&&!!state.session.nextTurn&&!!state.session.operationId;
        if(oldSizeResult){
          state.session.userPaused=false;
          const restored=await reattachDelivery({tab:{id:state.session.tabId}},{pageInstance:state.session.pageInstance});
          if(!restored.accepted)throw Error(state.session.error);
          break;
        }
        state.session.paused=false;state.session.userPaused=false;state.session.count=0;
        state.session.phase=state.session.pausedPhase|| (state.session.pendingCall?(state.session.operationKey?'recovering':'waiting_approval'):(state.session.resultText?'result_ready':'waiting_model'));
        if(state.session.phase==='ready'&&state.session.pendingCall&&!SAFE.has(state.session.pendingCall.name)&&!state.session.autoApprove)state.session.phase='waiting_approval';
        state.session.pausedPhase='';
        if(state.session.error==='Session operation limit reached; resume to continue.')state.session.error='';
        if(state.session.phase==='ready'&&state.session.pendingCall&&!state.session.operationKey&&!state.session.operationId&&!state.session.resultText)await dispatch();
      }
      break;
    case 'bridge.stop': await disableAuto();await stop();break;
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
async function rememberAuto(m){if(!demo){const settings={enabled:true,agentId:m.agentId,toolNames:m.toolNames,automatic:!!m.automatic,autoApprove:!!m.autoApprove,consent:true};await chrome.storage.local.set({bridgeAuto:settings,bridgeAutoSetupV1:true});state.autoSettings=settings;updateAutoStatus(settings);}}
async function disableAuto(){const {bridgeAuto={}}=await chrome.storage.local.get('bridgeAuto');const settings={...bridgeAuto,enabled:false};await chrome.storage.local.set({bridgeAuto:settings,bridgeAutoSetupV1:true});state.autoSettings=settings;updateAutoStatus();}
async function migrateAutoSetup(){
  const {bridgeAutoSetupV1}=await chrome.storage.local.get('bridgeAutoSetupV1');
  const {bridgeAuto:previous={}}=await chrome.storage.local.get('bridgeAuto');
  if(bridgeAutoSetupV1||!state.connected||demo||!state.agents.length)return;
  const remembered=state.agents.find(a=>String(a.id??a.name)===previous.agentId);
  const online=state.agents.filter(a=>a.online!==false);
  const agent=remembered|| (online.length===1?online[0]:online.length===0&&state.agents.length===1?state.agents[0]:null);
  const safe=state.tools.filter(t=>SAFE.has(t.name)).map(t=>t.name);
  const tools=(previous.toolNames||[]).filter(n=>safe.includes(n));
  const selected=tools.length?tools:safe;
  if(!agent||agent.online===false||!selected.length)return;
  const settings={enabled:true,agentId:String(agent.id??agent.name),toolNames:selected,automatic:true,autoApprove:!!previous.autoApprove,consent:true};
  await chrome.storage.local.set({bridgeAuto:settings,bridgeAutoSetupV1:true});state.autoSettings=settings;
}
function updateAutoStatus(settings=state.autoSettings||{enabled:false}) {
  if(!settings.enabled)return state.autoStatus='';
  if(!settings.consent||!settings.agentId||!settings.toolNames?.length)return state.autoStatus='Выберите агента и инструменты';
  if(!state.connected||demo)return state.autoStatus='Gate не подключён';
  if(lastAutoRefresh>0&&!lastAutoRefreshOK)return state.autoStatus='Не удалось обновить Gate; повторяю';
  const agent=state.agents.find(a=>String(a.id??a.name)===settings.agentId);
  if(!agent||agent.online===false)return state.autoStatus='Агент не в сети';
  if(!autoToolsAvailable)return state.autoStatus='Ожидаю выбранные инструменты';
  if(settings.toolNames.some(name=>!state.tools.some(t=>t.name===name)))return state.autoStatus='Ожидаю выбранные инструменты';
  return state.autoStatus='Готово';
}
async function refreshForAutoStart(settings) {
  settings=settings||state.autoSettings||{enabled:false,toolNames:[]};
  if(!state.connected||demo||!client||Date.now()-lastAutoRefresh<5000)return lastAutoRefreshOK;
  lastAutoRefresh=Date.now();
  try {
    const signal=AbortSignal.timeout(5000),[agents,tools]=await Promise.all([client.listAgents(signal),client.listTools(signal)]);
    if(Array.isArray(agents))state.agents=agents;
    const visibleTools=Array.isArray(tools)?tools.filter(t=>!t.name.startsWith('browser_')):[];
    autoToolsAvailable=visibleTools.length>0&&(!settings.toolNames?.length||settings.toolNames.every(n=>visibleTools.some(t=>t.name===n)));
    if(Array.isArray(tools))state.tools=visibleTools;
    lastAutoRefreshOK=true;
  } catch(e) {
    lastAutoRefreshOK=false;
    if(e?.status===401)await expireAuth(e);
  }
  state.autoSettings=settings;updateAutoStatus(settings);await save();
  return lastAutoRefreshOK;
}
async function autoStart(sender,m){
  let {bridgeAuto:settings}=await chrome.storage.local.get('bridgeAuto');
  state.autoSettings=settings||state.autoSettings||{enabled:false};
  if(settings?.enabled&&!state.connected&&!demo){
    await restore();
    if(state.connected&&!state.session.pendingCall&&state.session.error?.startsWith('Could not restore Gate connection'))await clearSession();
  }
  const s=state.session;
  if(!settings&&state.connected&&!demo&&s.agentId&&s.toolNames?.length&&!s.paused&&!['idle','stopped'].includes(s.phase)){
    await rememberAuto(s);settings=(await chrome.storage.local.get('bridgeAuto')).bridgeAuto;
  }
  state.autoSettings=settings||{enabled:false,autoApprove:false};
  if(settings?.enabled&&!state.connected&&!demo) {updateAutoStatus(settings);return {accepted:false};}
  const refreshed=await refreshForAutoStart(settings);
  await migrateAutoSetup();settings=(await chrome.storage.local.get('bridgeAuto')).bridgeAuto||settings;state.autoSettings=settings;if(settings)settings.autoApprove=!!settings.autoApprove;
  const status=updateAutoStatus(settings);
  if(!settings?.enabled||!settings.consent||!state.connected||demo||!refreshed||status!=='Готово')return {accepted:false};
  if(m.pageInstance&&s.pageInstance===m.pageInstance&&s.tabId===sender.tab.id&&!m.conversationReplaced)return {accepted:true};
  if(s.tabId===sender.tab.id&&m.pageInstance&&s.pageInstance!==m.pageInstance&&recoverableDelivery(s))return reattachDelivery(sender,m);
  if(s.pendingCall||s.operationKey||s.phase==='delivery_uncertain'||s.resultText||s.operationId){
    if(s.tabId===sender.tab.id){s.paused=true;s.resumeBlocked=true;s.phase='paused';s.error='Page restarted with an unfinished operation; inspect its outcome before starting a new session';await save();}
    return {accepted:false};
  }
  if(s.tabId!==null&&s.tabId!==sender.tab.id&&!['idle','stopped'].includes(s.phase)){
    const existing=await chrome.tabs.get(s.tabId).catch(()=>null);
    if(existing)return {accepted:false};
  }
  const changedWithoutPending=s.tabId===sender.tab.id&&s.error==='Conversation changed'&&!s.pendingCall&&!s.operationKey&&!s.resultText&&!s.operationId;
  if((s.paused&&s.phase!=='stopped'&&!changedWithoutPending&&!(m.conversationReplaced&&!s.userPaused&&['Conversation changed','Conversation was replaced'].includes(s.error)))||s.userPaused)return {accepted:false};
  await clearSession();
  try{await arm({...settings,tabId:sender.tab.id});state.session.pageInstance=m.pageInstance;await save();return {accepted:true};}
  catch(e){return {accepted:false};}
}
function recoverableDelivery(s){
  const recoverablePause=s.phase==='paused'&&!s.userPaused&&(s.error==='Conversation changed'||s.pausedPhase!=='delivery_uncertain'&&s.error==='AI Studio session is unavailable; stop and re-arm this chat'||!s.resumeBlocked&&s.error==='Result exceeds 32 KiB; reduce the tool output before delivery.');
  return !!(s.resultText&&s.nextTurn&&s.operationId&&(s.phase==='delivery_uncertain'||recoverablePause));
}
async function reattachDelivery(sender,m){
  const s=state.session;
  const unsentSizeResult=s.phase==='paused'&&!s.userPaused&&!s.resumeBlocked&&s.error==='Result exceeds 32 KiB; reduce the tool output before delivery.';
  const retainUnsent=()=>{s.phase='paused';s.paused=true;s.resumeBlocked=false;s.error='Result exceeds 32 KiB; reduce the tool output before delivery.';};
  try{
    const armed=await armPage(sender.tab.id,{type:'studio.arm',sessionId:s.sessionId,turnless:!!s.turnless,recover:true,recoverTurn:s.nextTurn,toolNames:s.toolNames.map(n=>state.tools.find(t=>t.name===n)).filter(Boolean)});
    if(!armed?.supported){s.error='AI Studio page is not ready; delivery remains uncertain';if(unsentSizeResult)retainUnsent();await save();return {accepted:false};}
    s.pageInstance=m.pageInstance;s.paused=false;s.userPaused=false;s.resumeBlocked=false;s.phase='delivery_uncertain';s.pausedPhase='';s.error='Checking whether the saved result reached this chat';await save();
    const chk=await chrome.tabs.sendMessage(sender.tab.id,{type:'studio.snapshot',sessionId:s.sessionId,operationId:s.operationId,expectedResultText:s.resultText});
    if(!chk?.supported){s.error='AI Studio page is not ready; delivery remains uncertain';if(unsentSizeResult)retainUnsent();await save();return {accepted:!unsentSizeResult};}
    if(chk.delivered){completeDelivery();await save();}else if(unsentSizeResult){s.phase='result_ready';s.paused=false;s.error='';await save();if(s.automatic)try{await deliver(true);}catch(e){s.error=String(e.message||e);await save();}}else{s.error='Result is not visible yet; waiting for delivery proof. It will not be sent again.';await save();}
    return {accepted:true};
  }catch(e){s.phase='delivery_uncertain';s.paused=false;s.resumeBlocked=false;s.error='Could not check delivery in AI Studio; the saved result will not be sent again';if(unsentSizeResult)retainUnsent();await save();return {accepted:false};}
}
async function readLimited(response,limit){if(!response.body?.getReader){const text=await response.text();if(new TextEncoder().encode(text).length>limit)throw Error('OAuth response exceeds 64 KiB');return text;}const reader=response.body.getReader(),chunks=[];let size=0;try{while(true){const part=await reader.read();if(part.done)break;size+=part.value.byteLength;if(size>limit){await reader.cancel();throw Error('OAuth response exceeds 64 KiB');}chunks.push(part.value);}}finally{reader.releaseLock?.();}const bytes=new Uint8Array(size);let offset=0;for(const chunk of chunks){bytes.set(chunk,offset);offset+=chunk.length;}return new TextDecoder().decode(bytes);}
async function armPage(tabId,message){
  try{return await chrome.tabs.sendMessage(tabId,message);}catch(e){
    if(!/Receiving end does not exist|Could not establish connection/i.test(String(e.message||e)))throw e;
    const tab=await chrome.tabs.get(tabId);
    if(!tab.url?.startsWith('https://aistudio.google.com/'))throw Error('Active tab must be AI Studio');
    await chrome.scripting.executeScript({target:{tabId},func:()=>{window.__webcodexBridgeReattach=true;}});
    await chrome.scripting.executeScript({target:{tabId},files:['protocol.js','studio-adapter.js','content.js']});
    return chrome.tabs.sendMessage(tabId,message);
  }
}
async function arm(m) {
  if(!['idle','stopped'].includes(state.session.phase))throw Error('Stop the active session before arming another one');
  if(!m.consent||!Number.isInteger(m.tabId)||!Array.isArray(m.toolNames)||!m.toolNames.length)throw Error('Consent, tab and tools are required');
  const agent=state.agents.find(a=>String(a.id??a.name)===m.agentId);if(!agent||agent.online===false)throw Error('Selected agent is unavailable');
  const selected=m.toolNames.map(name=>state.tools.find(t=>t.name===name));if(selected.some(x=>!x))throw Error('Unknown selected tool');
  const tab=await chrome.tabs.get(m.tabId);if(!tab.url?.startsWith('https://aistudio.google.com/'))throw Error('Active tab must be AI Studio');
  if(!demo&&!state.connected)throw Error('Connect to Gate first');
  state.session={phase:'arming',agentId:m.agentId,toolNames:m.toolNames,automatic:!!m.automatic,autoApprove:!!m.autoApprove,paused:false,turnless:true,turn:uuid(),instructions:'',customInstructions:'',pendingCall:null,resultText:'',error:'',count:0,tabId:m.tabId,sessionId:uuid(),conversationKey:''};await save();
  try {const snap=await armPage(m.tabId,{type:'studio.arm',sessionId:state.session.sessionId,turnless:true,hideServiceMessages:!!state.hideServiceMessages});
    if(!snap?.supported)throw Error(snap?.error||'Unsupported chat');
    state.session.conversationKey=snap.conversationKey;state.session.phase='waiting_model';await stageInstructions();await save();
  } catch(e){await chrome.tabs.sendMessage(m.tabId,{type:'studio.disarm',sessionId:state.session.sessionId}).catch(()=>{});state.session.phase='stopped';state.session.error=String(e.message||e);await save();throw e;}
}
async function clearSession(){const s=state.session;if(s?.tabId&&s.sessionId)await chrome.tabs.sendMessage(s.tabId,{type:'studio.disarm',sessionId:s.sessionId}).catch(()=>{});state.session={phase:'idle',agentId:'',toolNames:[],automatic:false,paused:false,turn:'',instructions:'',pendingCall:null,resultText:'',error:'',count:0,tabId:null};await save();}
async function stageInstructions(){const s=state.session;const selected=s.toolNames.map(n=>state.tools.find(t=>t.name===n)).filter(Boolean);const systemText=WebCodexProtocol.buildSystemInstructions({agentId:s.agentId,tools:selected,turnless:!!s.turnless,instructions:[state.gateInstructions,s.customInstructions].filter(Boolean).join('\n\n')});const text=s.turnless?'':WebCodexProtocol.buildSessionInstructions({turn:s.turn});s.instructions=[systemText,text].filter(Boolean).join('\n\n');if(new TextEncoder().encode(s.instructions).length>65536)throw Error('Selected tools and instructions exceed 64 KiB; select fewer tools.');const r=await chrome.tabs.sendMessage(s.tabId,{type:'studio.stage',sessionId:s.sessionId,systemText,text});if(!r?.ok)throw Error(r?.error||'Could not stage instructions without changing existing text');}
async function studioMessage(m,sender) {
  const s=state.session;if(m.sessionId!==s.sessionId)throw Error('Stale Studio session');
  if(!m.conversationReplaced&&s.phase==='paused'&&s.error==='Conversation changed'&&recoverableDelivery(s)&&(m.type==='studio.call'||m.supported)){
    s.phase='delivery_uncertain';s.paused=false;s.resumeBlocked=false;s.pausedPhase='';s.error='Checking whether the saved result reached this chat';
  }
  if(m.type==='studio.snapshot'||m.type==='studio.tick'){if(m.conversationReplaced||!m.supported){if(m.conversationReplaced&&!s.userPaused&&!s.pendingCall&&!s.operationKey&&!s.resultText&&!s.operationId&&s.phase!=='delivery_uncertain'){const restarted=await autoStart(sender,{pageInstance:s.pageInstance,conversationReplaced:true});if(restarted.accepted)return restarted;}if(s.phase!=='paused')s.pausedPhase=s.phase;s.paused=true;s.phase='paused';s.resumeBlocked=true;s.error=m.error||'AI Studio session is unavailable';await save();return {accepted:true};}if(!s.paused&&s.phase==='delivery_uncertain'&&typeof m.deliveredOperationId==='string'&&!!m.deliveredOperationId&&m.deliveredOperationId===(s.operationId||s.operationKey)){completeDelivery();await save();}await recover();return {accepted:true,conversationKey:s.conversationKey};}
    if(m.type==='studio.call'){
    if(s.paused)return {accepted:false};
    if(s.turnless){
      if(typeof m.responseKey!=='string'||!m.responseKey)throw Error('Missing response context');
      if(m.responseKey===s.lastResponseKey&&!m.generating)return {accepted:true};
    }
    if(s.phase==='delivery_uncertain'&&!m.generating&&s.nextTurn){
      const tools=s.toolNames.map(n=>state.tools.find(t=>t.name===n)).filter(Boolean);
      const delivered=s.turnless&&typeof m.precedingResultId==='string'&&!!m.precedingResultId&&m.precedingResultId===(s.operationId||s.operationKey);
      try{if((!s.turnless||delivered)&&WebCodexProtocol.parseCall(m.text,s.turnless?null:s.nextTurn,tools)){completeDelivery();await save();}}catch{}
      if(s.paused)return {accepted:false};
    }
    if(!['waiting_model','ready'].includes(s.phase))return {accepted:false};
    if(m.generating){if(s.phase!=='paused')s.pausedPhase=s.phase;s.paused=true;s.phase='paused';s.resumeBlocked=true;s.error='Generation still active';}
    else if(s.phase==='waiting_model'||s.phase==='ready'){
      try {const tools=s.toolNames.map(n=>state.tools.find(t=>t.name===n)).filter(Boolean);const call=WebCodexProtocol.parseCall(m.text,s.turnless?null:s.turn,tools);
        if(s.turnless)s.lastResponseKey=m.responseKey;
        if(!call){s.resultText='';s.finalText=m.text;s.phase='ready';await chrome.tabs.sendMessage(s.tabId,{type:'studio.cleanup',sessionId:s.sessionId,enabled:!!state.hideServiceMessages,final:true,operationIds:s.completedOperationIds||[]}).catch(()=>{});}
        else {s.pendingCall={...call,turn:s.turn};s.phase=SAFE.has(call.name)?'ready':s.autoApprove?'ready':'waiting_approval';s.error='';if(s.phase==='ready'){await save();await dispatch();}}
      } catch(e){s.paused=true;s.phase='paused';s.error=String(e.message||e);}
    }
    await save();return {accepted:!s.paused};
  }
  if(m.type==='studio.tick'){await recover();return {accepted:true,conversationKey:s.conversationKey};}
  throw Error('Unknown Studio message');
}
async function approve(){const s=state.session;if(s.phase!=='waiting_approval'||!s.pendingCall||s.paused)throw Error('No pending approval');await dispatch();}
async function expireAuth(e){if(e?.status!==401) return false;await chrome.storage.local.remove('bridgeAuth');state.connected=false;client=null;token='';state.accessToken='';state.session.pausedPhase=state.session.phase;state.session.phase='paused';state.session.paused=true;state.session.resumeBlocked=true;state.session.error='Gate authorization expired; stop this session, reconnect, then re-arm this chat';await save();return true;}
async function dispatch(){const s=state.session,p=s.pendingCall;if(!p)throw Error('No pending call');const tool=state.tools.find(t=>t.name===p.name);if(!tool)throw Error('Tool unavailable');
  if(!s.operationKey)s.operationKey=Date.now()+'-'+uuid();const args={...p.arguments};if(tool.inputSchema.properties?.request_key)args.request_key=s.operationKey;
  s.pendingCall={...p,arguments:p.arguments};s.dispatchArgs=args;s.phase='recovering';await save();
  try{let result;if(demo){if(p.name!=='read_file')throw Error('Demo only supports read_file');result={content:[{type:'text',text:'DEMO MODE: sample content for '+String(args.path)}],isError:false};}
    else result=await client.createOperation({key:s.operationKey,name:p.name,arguments:args,agentId:s.agentId});
    await finishResult(result);
  }catch(e){if(await expireAuth(e))return;s.error=String(e.message||e);s.phase='recovering';await save();}
}
async function recover(){const s=state.session;if(s.paused)return;if(s.phase==='delivery_uncertain'){try{const chk=await chrome.tabs.sendMessage(s.tabId,{type:'studio.snapshot',sessionId:s.sessionId,operationId:s.operationId||s.operationKey,expectedResultText:s.resultText});if(!chk?.supported){s.error='AI Studio session is unavailable; delivery remains uncertain';await save();return;}if(chk?.delivered){completeDelivery();await save();}}catch(e){s.error='AI Studio session is unavailable; delivery remains uncertain until this chat reconnects';await save();}return;}
  if(s.phase!=='recovering'||!s.pendingCall)return;
  try{let result;if(demo)result={content:[{type:'text',text:'DEMO MODE: sample content'}],isError:false};else if(s.operationId)result=await client.getOperation(s.operationId);else result=await client.createOperation({key:s.operationKey,name:s.pendingCall.name,arguments:s.dispatchArgs,agentId:s.agentId});
    if(['accepted','dispatching','starting','running'].includes(result?.status)){s.operationId=result.operation_id||result.id||s.operationId;await save();return;}
    await finishResult(result);
  }catch(e){if(await expireAuth(e))return;s.error=String(e.message||e);await save();}
}
async function finishResult(result){const s=state.session;s.operationId=result?.operation_id||result?.id||s.operationId;if(['accepted','dispatching','starting','running'].includes(result?.status)){await save();return;}if(result?.status&& !['completed','failed','outcome_unknown'].includes(result.status)){s.error='Unexpected operation status; waiting for a terminal result.';await save();return;}if(result?.status==='outcome_unknown'){s.phase='paused';s.paused=true;s.resumeBlocked=true;s.error='Operation outcome is unknown; do not retry it.';await save();return;}const hasResult=!!result&&Object.prototype.hasOwnProperty.call(result,'result'),toolResult=hasResult?result.result:result;let commandSessionId=toolResult?.structuredContent?.session_id||toolResult?.session_id;for(const item of toolResult?.content||[])if(item.type==='text'&&typeof item.text==='string')try{const parsed=JSON.parse(item.text);commandSessionId=parsed.session_id||parsed.structuredContent?.session_id||commandSessionId;}catch{}if(commandSessionId)s.commandSessionId=commandSessionId;const transportError=result?.transport_error||(result?.status==='failed'&&!hasResult?(result.error||result.message||'Operation failed'):undefined);s.nextTurn=uuid();s.resultText=WebCodexProtocol.buildResult({turn:s.turn,nextTurn:s.nextTurn,turnless:!!s.turnless,operationId:s.operationId||s.operationKey,name:s.pendingCall.name,result:toolResult,error:transportError});s.phase='result_ready';await save();if(s.automatic&&!s.paused)try{await deliver(true);}catch(e){s.error=String(e.message||e);await save();}}
async function deliver(submit){const s=state.session;if(!s.resultText)throw Error('No result to deliver');if(s.paused)throw Error(s.error||'Session is paused');let envelope;try{envelope=JSON.parse(s.resultText);}catch{throw Error('Only a webcodex-result can be delivered');}if(envelope.type!=='webcodex-result')throw Error('Only a webcodex-result can be delivered');if(s.phase==='delivery_uncertain')throw Error('Delivery status is unknown; check AI Studio before taking action');if(submit){s.phase='delivery_uncertain';await save();}let r;try{r=await chrome.tabs.sendMessage(s.tabId,{type:'studio.deliver',sessionId:s.sessionId,text:s.resultText,submit});}catch(e){throw Error('Delivery status is unknown; do not send again');}if(!r?.supported){if(!r?.attempted){s.phase='result_ready';await save();}throw Error('AI Studio session is unavailable; delivery is blocked');}if(!r?.ok){if(!r?.attempted){s.phase='result_ready';await save();}throw Error(r?.attempted?'Delivery status is unknown; do not send again':'AI Studio draft could not be safely delivered');}if(!submit){s.phase='result_ready';await save();return;}if(r.confirmed){completeDelivery();await save();}}
function completeDelivery(){const s=state.session;if(s.operationId||s.operationKey)(s.completedOperationIds||=[]).push(s.operationId||s.operationKey);s.error='';s.phase='waiting_model';s.turn=s.nextTurn;s.pendingCall=null;s.resultText='';s.operationKey='';s.operationId='';s.dispatchArgs=null;s.count++;}
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
