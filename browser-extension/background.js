importScripts('command_queue.js');
const HOST = 'org.webcodex.browser';
const ACCESS_KEY = 'webcodexPersonalAccess';
const PROFILE_KEY='webcodexBrowserProfile';
let profileID='',profileAlias='';
const reportedAgent=globalThis.navigator?.userAgent||'';
const browserProduct=/Edg\//.test(reportedAgent)?'edge':/Chrome\//.test(reportedAgent)?'chrome':/Chromium\//.test(reportedAgent)?'chromium':'unknown';
const RECONNECT_ALARM = 'webcodex-reconnect';
const PERSONAL_TAB_LIMIT = 128;
const attached = new Map();
const available = new Map();
const downloads = new Map();
const childSessions = new Map();
const urlChecks = new Map();
let port, paused = false, localPaused = false, allowLocal = false, allowNew = false, continuous = false, connected = false;
let scopeScheduled = false, scopePublishing, scopeQueued;
let settingsWrites = Promise.resolve(), ready, reconnectTimer, reconnectDelay = 2000, accessEpoch = 0;
const methods = new Set(['Page.enable','Runtime.enable','Network.enable','Page.navigate','Page.getFrameTree','Page.createIsolatedWorld','Runtime.evaluate','Page.reload','Page.getNavigationHistory','Page.navigateToHistoryEntry','Page.bringToFront','Page.captureScreenshot','Page.getLayoutMetrics','Page.handleJavaScriptDialog','Input.dispatchMouseEvent','Input.dispatchKeyEvent','DOM.getFrameOwner','DOM.getBoxModel','DOM.setFileInputFiles','Accessibility.getFullAXTree']);
methods.add('DOM.scrollIntoViewIfNeeded');

function localHost(host) {
  return host === 'localhost' || !host.includes('.') || host.endsWith('.local') || /^(127|10)\./.test(host) || /^192\.168\./.test(host) || /^172\.(1[6-9]|2\d|3[01])\./.test(host) || host.startsWith('[');
}
function permitted(raw, scope, blank = false) {
  if (blank && raw === 'about:blank') return true;
  try { const url = new URL(raw); return ['http:','https:'].includes(url.protocol) && !url.username && !url.password && (allowLocal || !localHost(url.hostname)) && (scope.publicWeb || url.origin === scope.origin); }
  catch { return false; }
}
function diagnosticOrigin(raw) { try { const url=new URL(raw);return ['http:','https:'].includes(url.protocol)?url.origin+'/':''; } catch { return ''; } }
function networkFailure(raw,canceled) {
  if(canceled)return 'cancelled';
  if(['net::ERR_TIMED_OUT','net::ERR_CONNECTION_TIMED_OUT'].includes(raw))return 'timeout';
  if(raw==='net::ERR_NAME_NOT_RESOLVED')return 'dns';
  if(['net::ERR_CONNECTION_RESET','net::ERR_CONNECTION_REFUSED','net::ERR_CONNECTION_CLOSED','net::ERR_INTERNET_DISCONNECTED'].includes(raw))return 'connection';
  if(['net::ERR_BLOCKED_BY_CLIENT','net::ERR_BLOCKED_BY_RESPONSE'].includes(raw))return 'blocked';
  return 'other';
}
function tabURL(tab) { return tab.url || tab.pendingUrl || ''; }
async function createTaskTab(scope,guard=()=>{}) {
  guard();
  const child = await chrome.tabs.create({url:'about:blank'});
  try { guard();await attach(child,{...scope,created:true},guard); }
  catch (error) {
    // This tab was created by this call; preserve every existing user tab.
    try { await chrome.tabs.remove(child.id); } catch {}
    throw error;
  }
  return {tab_id:child.id,url:tabURL(child)};
}
function post(message) {
  if (!port) throw Error('Native bridge disconnected');
  const serialized = JSON.stringify(message);
  if (new TextEncoder().encode(serialized).byteLength > 900000) throw Error('Native response exceeds limit');
  port.postMessage(message);
}
function publishIdentity(){if(port&&connected)post({command_protocol:commandProtocol,kind:'identity',profile_id:profileID,alias:profileAlias,product:browserProduct});}
function tabMetadata(tab){return {tab_id:tab.id,url:tabURL(tab),title:String(tab.title||'').slice(0,256),window_id:Number.isInteger(tab.windowId)&&tab.windowId>=0?tab.windowId:0,active:!!tab.active};}
function saveAccess() {
  const access = {continuous, localPaused, allowNew};
  settingsWrites = settingsWrites.catch(() => {}).then(() => chrome.storage.local.set({[ACCESS_KEY]:access}));
  return settingsWrites;
}
async function setPaused(value, locally = false) {
  if (locally) localPaused = value;
  paused = value || localPaused;
  accessEpoch++; invalidateCommands();
  const epoch=accessEpoch;
  if(locally)await saveAccess();
  for (const tabId of attached.keys()) {
    if(epoch!==accessEpoch)return;
    try { await chrome.debugger.sendCommand({tabId},paused ? 'Fetch.disable' : 'Fetch.enable',paused ? {} : {patterns:[{urlPattern:'*',requestStage:'Request'}]}); } catch {}
  }
  await scopeUpdate();
}
async function publishScope() {
  const epoch = accessEpoch;
  const tabs = [];
  if (continuous) {
    const candidates = await chrome.tabs.query({});
    candidates.sort((a,b) => Number(b.active)-Number(a.active) || (b.lastAccessed || 0)-(a.lastAccessed || 0));
    for (const tab of candidates) {
      const scope = attached.get(tab.id) || {publicWeb:true,created:false};
      const url = tabURL(tab);
      if (tabs.length < PERSONAL_TAB_LIMIT && permitted(url,scope,scope.created)) tabs.push(tabMetadata(tab));
    }
  } else {
    for (const [id, scope] of attached) { try { const tab = await chrome.tabs.get(id); const url = tabURL(tab); if (permitted(url,scope,scope.created)) tabs.push(tabMetadata(tab)); } catch {} }
  }
  if (epoch !== accessEpoch) return;
  available.clear();
  for (const tab of tabs) available.set(tab.tab_id,tab.url);
  if (port && connected) post({kind:'scope',tabs,paused,local_paused:localPaused,allow_new:allowNew,scope_epoch:accessEpoch,control_epoch:commandControlEpoch});
  await chrome.action.setBadgeText({text:paused ? 'PAUSE' : continuous ? connected ? 'AUTO' : 'WAIT' : attached.size ? 'ON' : ''});
  await chrome.action.setBadgeBackgroundColor({color:paused ? '#b45309' : '#0369a1'});
}
function newScopeBatch() {
  let resolve, reject;
  const promise = new Promise((ok,fail) => { resolve=ok;reject=fail; });
  return {promise,resolve,reject};
}
async function drainScopeUpdates() {
  scopeScheduled = false;
  if (scopePublishing || !scopeQueued) return;
  const batch = scopeQueued;
  scopeQueued = undefined;
  scopePublishing = batch;
  try {
    await publishScope();
    batch.resolve();
  } catch (error) {
    batch.reject(error);
  } finally {
    scopePublishing = undefined;
    if (scopeQueued && !scopeScheduled) {
      scopeScheduled = true;
      Promise.resolve().then(drainScopeUpdates);
    }
  }
}
function scopeUpdate() {
  if (scopePublishing) {
    if (!scopeQueued) scopeQueued = newScopeBatch();
    return scopeQueued.promise;
  }
  if (!scopeQueued) scopeQueued = newScopeBatch();
  if (!scopeScheduled) {
    scopeScheduled = true;
    Promise.resolve().then(drainScopeUpdates);
  }
  return scopeQueued.promise;
}
async function releaseAttachments() {
  accessEpoch++; invalidateCommands();
  const ids = [...attached.keys()]; attached.clear();
  for (const id of downloads.keys()) { try { await chrome.downloads.cancel(id); } catch {} }
  downloads.clear();
  childSessions.clear();
  for (const resolve of urlChecks.values()) resolve(false);
  urlChecks.clear();
  for (const tabId of ids) { try { await chrome.debugger.sendCommand({tabId},'Fetch.disable'); } catch {} try { await chrome.debugger.detach({tabId}); } catch {} }
  await scopeUpdate();
}
async function disconnect() {
  continuous = false; localPaused = false; paused = true;
  clearTimeout(reconnectTimer); reconnectTimer = undefined;
  await saveAccess();
  await chrome.alarms.clear(RECONNECT_ALARM);
  await releaseAttachments();
  const oldPort = port; port = undefined; connected = false;
  oldPort?.disconnect();
}
function scheduleReconnect() {
  if (!continuous || localPaused || reconnectTimer) return;
  reconnectTimer = setTimeout(() => {
    reconnectTimer = undefined;
    startContinuous().catch(() => {});
  },reconnectDelay);
  reconnectDelay = Math.min(reconnectDelay * 2,30000);
}
async function startContinuous() {
  await settingsLoaded;
  if (!continuous || localPaused) return;
  const alarm = await chrome.alarms.get(RECONNECT_ALARM);
  if (!alarm) await chrome.alarms.create(RECONNECT_ALARM,{periodInMinutes:0.5});
  try { await connect(); await scopeUpdate(); }
  catch { scheduleReconnect(); }
}
function connect() {
  if (port) return ready;
  let resolveReady, rejectReady;
  ready = new Promise((resolve,reject) => { resolveReady=resolve; rejectReady=reject; });
  const nativePort = chrome.runtime.connectNative(HOST);
  port = nativePort; connected = false;
  const timeout = setTimeout(() => {
    rejectReady(Error('Native bridge did not connect within 5 seconds'));
    nativePort.disconnect();
  },5000);
  nativePort.onDisconnect.addListener(() => {
    const failure = chrome.runtime.lastError?.message || 'Native bridge disconnected';
    clearTimeout(timeout); rejectReady(Error(failure));
    if (port !== nativePort) return;
    port = undefined; connected = false; paused = localPaused || !continuous;
    releaseAttachments().catch(() => {}).finally(scheduleReconnect);
  });
  nativePort.onMessage.addListener(message => {
    if (port !== nativePort) return;
    if (message.kind === 'url_result') { const resolve = urlChecks.get(message.id); if(resolve){urlChecks.delete(message.id);resolve(!!message.allowed)} return; }
    if (message.kind === 'hello') {
      clearTimeout(timeout); clearTimeout(reconnectTimer); reconnectTimer = undefined; reconnectDelay = 2000;
      allowLocal = !!message.allow_local; connected = true; paused = localPaused;
      commandConnection=message.connection_id||'';commandProtocol=Math.min(2,message.command_protocol||1);commandControlEpoch=0;
      publishIdentity(); resolveReady(); scopeUpdate().catch(() => {}); return;
    }
    if(message.kind==='command_cancel'){cancelCommand(message,nativePort);return;}
    if (message.kind === 'control') {
      if(message.control_epoch!=null){if(!Number.isSafeInteger(message.control_epoch)||message.control_epoch<commandControlEpoch)return;commandControlEpoch=message.control_epoch;}
      if (message.action === 'detach') { paused = localPaused || !continuous; releaseAttachments().catch(() => {}); }
      if (message.action === 'pause') setPaused(true).catch(() => {});
      if (message.action === 'resume' && !localPaused) setPaused(false).catch(() => {});
      return;
    }
    if (message.kind !== 'command') return;
    enqueueCommand(message,nativePort);
  });
  return ready;
}
async function attach(tab, scope,guard=()=>{}) {
  guard();
  if (attached.size >= PERSONAL_TAB_LIMIT) throw Error('Tab limit reached');
  if (paused || localPaused) throw Error('Local control paused');
  if (!permitted(tabURL(tab),scope,scope.created)) throw Error('Tab URL is outside the selected scope');
  if (attached.has(tab.id)) return;
  const epoch = accessEpoch;
  await chrome.debugger.attach({tabId:tab.id},'1.3');
  try{guard()}catch(error){await chrome.debugger.detach({tabId:tab.id});throw error;}
  if (epoch !== accessEpoch || paused || localPaused) { await chrome.debugger.detach({tabId:tab.id}); throw Error('Local access changed'); }
  attached.set(tab.id,scope);
  try {
    guard();await chrome.debugger.sendCommand({tabId:tab.id},'Fetch.enable',{patterns:[{urlPattern:'*',requestStage:'Request'}]});
    for (const method of ['Page.enable','Runtime.enable','Network.enable']) {guard();await chrome.debugger.sendCommand({tabId:tab.id},method);}
    guard();await chrome.debugger.sendCommand({tabId:tab.id},'Target.setAutoAttach',{autoAttach:true,waitForDebuggerOnStart:false,flatten:true});
    await scopeUpdate();
  } catch (error) { attached.delete(tab.id); await chrome.debugger.detach({tabId:tab.id}); throw error; }
}
async function execute(message,guard=()=>{}) {
  guard();
  if (paused || localPaused) throw Error('Local control paused');
  const method = message.method, params = message.params || {};
  if (!methods.has(method) && !['WebCodex.prepare','WebCodex.newTab','WebCodex.closeTab','WebCodex.enableDownloads','WebCodex.frames','WebCodex.downloads'].includes(method)) throw Error('CDP method is not permitted');
  if (method === 'WebCodex.prepare') {
    if (!continuous || !connected) throw Error('Enable permanent access locally');
    await scopeUpdate();
    if (!available.size) {
      guard();await createTaskTab({publicWeb:true},guard);
    }
    return {};
  }
  let tab = await chrome.tabs.get(message.tab_id);guard();
  if (!attached.has(tab.id) && continuous && connected && available.has(tab.id)) {
    await attach(tab,{origin:new URL(tabURL(tab)).origin,publicWeb:true,created:false},guard);
    tab = await chrome.tabs.get(message.tab_id);guard();
  }
  const scope = attached.get(message.tab_id);
  if (!scope) throw Error('Tab is not locally attached');
  if (!permitted(tabURL(tab),scope,scope.created)) { await chrome.debugger.detach({tabId:tab.id}); attached.delete(tab.id); await scopeUpdate(); throw Error('Tab left permitted scope'); }
  if (method === 'WebCodex.newTab') {
    if (!allowNew) throw Error('New tabs are not locally permitted');
    return createTaskTab(scope,guard);
  }
  if (method === 'WebCodex.closeTab') {
    if (!scope.created) throw Error('Only tabs created for this task can be closed');
    guard();attached.delete(tab.id); await chrome.tabs.remove(tab.id); await scopeUpdate(); return {};
  }
  if (method === 'WebCodex.enableDownloads') { if (!/^browser_[a-f0-9]{24}$/.test(params.session_id)) throw Error('Invalid session'); scope.downloadSession = params.session_id; return {}; }
  if (method === 'WebCodex.frames') {
    const frames=[];
    function collect(tree){if(!tree)return;frames.push(tree.frame);for(const child of tree.childFrames||[])collect(child)}
    for(const sessionId of childSessions.get(tab.id)?.values()||[]){try{guard();const result=await chrome.debugger.sendCommand({tabId:tab.id,sessionId},'Page.getFrameTree');collect(result.frameTree)}catch{}}
    return frames;
  }
  if (method === 'WebCodex.downloads') {
    const result = [];
    for (const [id, owner] of downloads) { if (owner !== scope.downloadSession) continue; const [item] = await chrome.downloads.search({id}); if (item) result.push({download_id:String(id),status:item.state,bytes:item.bytesReceived,filename:item.filename}); }
    return result;
  }
  if (!methods.has(method)) throw Error('CDP method is not permitted');
  if (method === 'Page.navigate' && !permitted(params.url,scope)) throw Error('Navigation URL is outside selected scope');
  if (method === 'Page.navigateToHistoryEntry') { const history = await chrome.debugger.sendCommand({tabId:tab.id},'Page.getNavigationHistory'); const entry = history.entries.find(x => x.id === params.entryId); if (!entry || !permitted(entry.url,scope)) throw Error('History URL outside scope'); }
  guard();
  const sessionId = message.frame_id && childSessions.get(tab.id)?.get(message.frame_id);
  return chrome.debugger.sendCommand(sessionId ? {tabId:tab.id,sessionId} : {tabId:tab.id},method,params);
}
chrome.runtime.onMessage.addListener((message, sender, reply) => {
  if (sender.id !== chrome.runtime.id || sender.tab) return false;
  (async () => {
    await settingsLoaded;
    if (message.type==='identity') {
      const alias=String(message.alias??'').trim();
      if(Array.from(alias).length>64||/\p{Cc}/u.test(alias))throw Error('????????? ?????? ???? ?? ??????? 64 ???????? ? ??? ??????????? ??????');
      profileAlias=alias;settingsWrites=settingsWrites.catch(()=>{}).then(()=>chrome.storage.local.set({[PROFILE_KEY]:{id:profileID,alias:profileAlias}}));await settingsWrites;publishIdentity();
    } else if (message.type === 'attach') {
      if (continuous) await disconnect();
      accessEpoch++;invalidateCommands();
      localPaused = false; paused = false; allowNew = !!message.allowNew;
      await saveAccess();
      await connect();
      const [tab] = await chrome.tabs.query({active:true,currentWindow:true});
      if (!tab) throw Error('Откройте вкладку сайта');
      const url = new URL(tabURL(tab));
      await attach(tab,{origin:url.origin,publicWeb:!!message.publicWeb,created:false});
    } else if (message.type === 'continuous') {
      if (message.enabled) {
        continuous = true; localPaused = false; paused = false; allowNew = !!message.allowNew; accessEpoch++;
        await saveAccess();
        await releaseAttachments();
        await startContinuous();
      } else { await disconnect(); }
    } else if (message.type === 'options') { accessEpoch++;invalidateCommands();allowNew = !!message.allowNew; await saveAccess(); await scopeUpdate(); }
    else if (message.type === 'pause') { await setPaused(true,true); }
    else if (message.type === 'resume') { await setPaused(false,true); if (continuous) await startContinuous(); }
    else if (message.type === 'disconnect') { await disconnect(); }
    return {count:available.size,paused,continuous,allowNew,connected,alias:profileAlias,product:browserProduct};
  })().then(reply,error => reply({error:error.message}));
  return true;
});
chrome.debugger.onDetach.addListener(source => {
  attached.delete(source.tabId); childSessions.delete(source.tabId);
  if (connected) { try { post({kind:'event',tab_id:source.tabId,method:'WebCodex.detached',result:{}}); } catch {} }
  scopeUpdate().catch(() => {});
});
chrome.debugger.onEvent.addListener(async (source,method,event) => {
  const scope = attached.get(source.tabId); if (!scope) return;
  if (method === 'Target.attachedToTarget' && event.targetInfo.type === 'iframe') {
    let sessions=childSessions.get(source.tabId);if(!sessions){sessions=new Map();childSessions.set(source.tabId,sessions)}
    if(sessions.size>=64)return;
    sessions.set(event.targetInfo.targetId,event.sessionId);
    const child={tabId:source.tabId,sessionId:event.sessionId};
    for(const domain of ['Page.enable','Runtime.enable','Network.enable'])try{await chrome.debugger.sendCommand(child,domain)}catch{}
    try{await chrome.debugger.sendCommand(child,'Fetch.enable',{patterns:[{urlPattern:'*',requestStage:'Request'}]});await chrome.debugger.sendCommand(child,'Target.setAutoAttach',{autoAttach:true,waitForDebuggerOnStart:false,flatten:true})}catch{}
    return;
  }
  if (method === 'Fetch.requestPaused') {
    let allowed = !paused && permitted(event.request.url,scope);
    if (allowed) {
      if (urlChecks.size >= 64) allowed = false;
      else { const id = crypto.randomUUID(); allowed = await new Promise(resolve => { const timer=setTimeout(()=>{urlChecks.delete(id);resolve(false)},4000);urlChecks.set(id,answer=>{clearTimeout(timer);resolve(answer)});try{post({kind:'url_check',id,url:event.request.url})}catch{clearTimeout(timer);urlChecks.delete(id);resolve(false)} }); }
    }
    try { await chrome.debugger.sendCommand(source,allowed?'Fetch.continueRequest':'Fetch.failRequest',allowed?{requestId:event.requestId}:{requestId:event.requestId,errorReason:'BlockedByClient'}); } catch {}
    return;
  }
  if (['Page.frameNavigated','Page.navigatedWithinDocument'].includes(method)) {
    const tab = await chrome.tabs.get(source.tabId);
    if (!permitted(tabURL(tab),scope,scope.created)) { attached.delete(tab.id); try { await chrome.debugger.detach({tabId:tab.id}); } catch {} }
    await scopeUpdate();
  }
  if (!['Page.frameNavigated','Page.navigatedWithinDocument','Page.javascriptDialogOpening','Page.javascriptDialogClosed','Runtime.executionContextsCleared','Runtime.executionContextDestroyed','Runtime.exceptionThrown','Runtime.consoleAPICalled','Network.loadingFailed','Network.responseReceived','Network.requestWillBeSent','Network.loadingFinished'].includes(method)) return;
  const sanitized = {};
  if (event.type) sanitized.type = event.type;
  if (event.requestId) sanitized.requestId = `${source.sessionId||'root'}:${String(event.requestId).slice(0,120)}`;
  if (event.request) sanitized.request = {url:diagnosticOrigin(event.request.url)};
  if (event.response) sanitized.response = {status:event.response.status,url:diagnosticOrigin(event.response.url)};
  if (method==='Network.loadingFailed') {sanitized.canceled=!!event.canceled;sanitized.failure_type=networkFailure(event.errorText,event.canceled);}
  try { post({kind:'event',tab_id:source.tabId,method,result:sanitized}); } catch {}
});
chrome.tabs.onCreated.addListener(async tab => {
  if (continuous) { await scopeUpdate(); return; }
  const scope = attached.get(tab.openerTabId);
  if (!scope || !allowNew || paused) return;
  try { await attach(tab,{...scope,created:true}); } catch {}
});
chrome.tabs.onUpdated.addListener((id,change) => {
  if (continuous && (change.url || change.status === 'complete')) scopeUpdate().catch(() => {});
});
chrome.tabs.onRemoved.addListener(id => {
  attached.delete(id); available.delete(id); childSessions.delete(id);
  scopeUpdate().catch(() => {});
});
chrome.tabs.onActivated.addListener(() => { if (continuous) scopeUpdate().catch(() => {}); });
chrome.alarms.onAlarm.addListener(alarm => { if (alarm.name === RECONNECT_ALARM) startContinuous().catch(() => {}); });
chrome.runtime.onStartup.addListener(() => { startContinuous().catch(() => {}); });
chrome.runtime.onInstalled.addListener(() => { startContinuous().catch(() => {}); });
const settingsLoaded = Promise.all([chrome.storage.local.get(ACCESS_KEY),chrome.storage.local.get(PROFILE_KEY)]).then(async ([values,profiles]) => {
  const access=values[ACCESS_KEY]||{},profile=profiles[PROFILE_KEY]||{};
  continuous=access.continuous===true;localPaused=access.localPaused===true;allowNew=access.allowNew===true;paused=localPaused;
  profileID=/^profile_[a-f0-9]{32}$/.test(profile.id||'')?profile.id:'profile_'+crypto.randomUUID().replace(/-/g,'');
  profileAlias=typeof profile.alias==='string'&&!/\p{Cc}/u.test(profile.alias)?Array.from(profile.alias.trim()).slice(0,64).join(''):'';
  await chrome.storage.local.set({[PROFILE_KEY]:{id:profileID,alias:profileAlias}});
});
settingsLoaded.then(() => startContinuous()).catch(() => {});
chrome.downloads.onDeterminingFilename.addListener((item,suggest) => {
  for (const scope of attached.values()) {
    if (!scope.downloadSession || paused || !item.referrer || !permitted(item.referrer,scope) || !permitted(item.url,scope)) continue;
    if (downloads.size >= 16 || item.totalBytes > 67108864) { chrome.downloads.cancel(item.id); suggest(); return; }
    downloads.set(item.id,scope.downloadSession);
    monitorDownload(item.id);
    suggest({filename:`WebCodex/${scope.downloadSession}/${item.id}`,conflictAction:'uniquify'}); return;
  }
  suggest();
});
async function monitorDownload(id) {
  if (!downloads.has(id)) return;
  try {
    const [item]=await chrome.downloads.search({id});if(!item)return;
    if(item.bytesReceived>67108864){await chrome.downloads.cancel(id);return}
    // shortcut: byte progress is polled, use an OS disk quota when downloads need a hard limit.
    if(item.state==='in_progress')setTimeout(()=>monitorDownload(id),1000);
  }catch{}
}
