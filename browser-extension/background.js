const HOST = 'org.webcodex.browser';
const attached = new Map();
const downloads = new Map();
const childSessions = new Map();
const urlChecks = new Map();
let port, paused = false, localPaused = false, allowLocal = false, allowNew = false, serial = Promise.resolve(), ready, readyResolve, readyReject;
const methods = new Set(['Page.enable','Runtime.enable','Network.enable','Page.navigate','Page.getFrameTree','Page.createIsolatedWorld','Runtime.evaluate','Page.reload','Page.getNavigationHistory','Page.navigateToHistoryEntry','Page.bringToFront','Page.captureScreenshot','Page.getLayoutMetrics','Page.handleJavaScriptDialog','Input.dispatchMouseEvent','Input.dispatchKeyEvent','DOM.getFrameOwner','DOM.getBoxModel','DOM.setFileInputFiles','Accessibility.getFullAXTree']);

function localHost(host) {
  return host === 'localhost' || !host.includes('.') || host.endsWith('.local') || /^(127|10)\./.test(host) || /^192\.168\./.test(host) || /^172\.(1[6-9]|2\d|3[01])\./.test(host) || host.startsWith('[');
}
function permitted(raw, scope, blank = false) {
  if (blank && raw === 'about:blank') return true;
  try { const url = new URL(raw); return ['http:','https:'].includes(url.protocol) && !url.username && !url.password && (allowLocal || !localHost(url.hostname)) && (scope.publicWeb || url.origin === scope.origin); }
  catch { return false; }
}
function post(message) {
  if (!port) throw Error('Native bridge disconnected');
  if (JSON.stringify(message).length > 900000) throw Error('Native response exceeds limit');
  port.postMessage(message);
}
async function scopeUpdate() {
  const tabs = [];
  for (const [id, scope] of attached) { try { const tab = await chrome.tabs.get(id); if (permitted(tab.url,scope,scope.created)) tabs.push({tab_id:id,url:tab.url}); } catch {} }
  if (port) post({kind:'scope',tabs,paused,local_paused:localPaused,allow_new:allowNew});
  await chrome.action.setBadgeText({text:attached.size ? paused ? 'PAUSE' : 'ON' : ''});
  await chrome.action.setBadgeBackgroundColor({color:paused ? '#b45309' : '#0369a1'});
}
async function disconnect() {
  paused = true;
  const ids = [...attached.keys()]; attached.clear();
  for (const id of downloads.keys()) { try { await chrome.downloads.cancel(id); } catch {} }
  downloads.clear();
  childSessions.clear();
  for (const tabId of ids) { try { await chrome.debugger.sendCommand({tabId},'Fetch.disable'); } catch {} try { await chrome.debugger.detach({tabId}); } catch {} }
  await scopeUpdate();
}
function connect() {
  if (port) return ready;
  ready = new Promise((resolve,reject) => { readyResolve=resolve; readyReject=reject; });
  const timeout = setTimeout(() => readyReject(Error('Native bridge did not connect within 5 seconds')),5000);
  port = chrome.runtime.connectNative(HOST);
  port.onDisconnect.addListener(() => { clearTimeout(timeout); readyReject(Error(chrome.runtime.lastError?.message || 'Native bridge disconnected')); port = undefined; disconnect(); });
  port.onMessage.addListener(message => {
    if (message.kind === 'url_result') { const resolve = urlChecks.get(message.id); if(resolve){urlChecks.delete(message.id);resolve(!!message.allowed)} return; }
    if (message.kind === 'hello') { clearTimeout(timeout); allowLocal = !!message.allow_local; readyResolve(); scopeUpdate(); return; }
    if (message.kind === 'control') {
      if (message.action === 'detach') disconnect();
      if (message.action === 'pause') { paused = true; scopeUpdate(); }
      if (message.action === 'resume' && !localPaused) { paused = false; scopeUpdate(); }
      return;
    }
    if (message.kind !== 'command') return;
    serial = serial.then(async () => {
      let result, error;
      try { result = await execute(message); } catch (failure) { error = failure.message; }
      try { post({kind:'response',id:message.id,result:result ?? {},error}); }
      catch { try { post({kind:'response',id:message.id,error:'Response exceeds native message limit'}); } catch {} }
    }).catch(() => {});
  });
  return ready;
}
async function attach(tab, scope) {
  if (attached.size >= 16) throw Error('Tab limit reached');
  if (!permitted(tab.url,scope,scope.created)) throw Error('Tab URL is outside the selected scope');
  if (attached.has(tab.id)) return;
  await chrome.debugger.attach({tabId:tab.id},'1.3');
  attached.set(tab.id,scope);
  try {
    await chrome.debugger.sendCommand({tabId:tab.id},'Fetch.enable',{patterns:[{urlPattern:'*',requestStage:'Request'}]});
    await chrome.debugger.sendCommand({tabId:tab.id},'Page.enable');
    await chrome.debugger.sendCommand({tabId:tab.id},'Target.setAutoAttach',{autoAttach:true,waitForDebuggerOnStart:false,flatten:true});
    await scopeUpdate();
  } catch (error) { attached.delete(tab.id); await chrome.debugger.detach({tabId:tab.id}); throw error; }
}
async function execute(message) {
  if (paused) throw Error('Local control paused');
  const scope = attached.get(message.tab_id);
  if (!scope) throw Error('Tab is not locally attached');
  const tab = await chrome.tabs.get(message.tab_id);
  if (!permitted(tab.url,scope,scope.created)) { await chrome.debugger.detach({tabId:tab.id}); attached.delete(tab.id); await scopeUpdate(); throw Error('Tab left permitted scope'); }
  const method = message.method, params = message.params || {};
  if (method === 'WebCodex.newTab') {
    if (!allowNew) throw Error('New tabs are not locally permitted');
    const child = await chrome.tabs.create({url:'about:blank'});
    await attach(child,{...scope,created:true});
    return {tab_id:child.id,url:child.url};
  }
  if (method === 'WebCodex.closeTab') {
    if (!scope.created) throw Error('Only tabs created for this task can be closed');
    attached.delete(tab.id); await chrome.tabs.remove(tab.id); await scopeUpdate(); return {};
  }
  if (method === 'WebCodex.enableDownloads') { if (!/^browser_[a-f0-9]{24}$/.test(params.session_id)) throw Error('Invalid session'); scope.downloadSession = params.session_id; return {}; }
  if (method === 'WebCodex.frames') {
    const frames=[];
    function collect(tree){if(!tree)return;frames.push(tree.frame);for(const child of tree.childFrames||[])collect(child)}
    for(const sessionId of childSessions.get(tab.id)?.values()||[]){try{const result=await chrome.debugger.sendCommand({tabId:tab.id,sessionId},'Page.getFrameTree');collect(result.frameTree)}catch{}}
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
  const sessionId = message.frame_id && childSessions.get(tab.id)?.get(message.frame_id);
  return chrome.debugger.sendCommand(sessionId ? {tabId:tab.id,sessionId} : {tabId:tab.id},method,params);
}
chrome.runtime.onMessage.addListener((message, sender, reply) => {
  if (sender.id !== chrome.runtime.id || sender.tab) return false;
  (async () => {
    if (message.type === 'attach') {
      await connect();
      const [tab] = await chrome.tabs.query({active:true,currentWindow:true});
      const url = new URL(tab.url); allowNew = !!message.allowNew; paused = false;
      await attach(tab,{origin:url.origin,publicWeb:!!message.publicWeb,created:false});
    } else if (message.type === 'pause') { localPaused = true; paused = true; await scopeUpdate(); }
    else if (message.type === 'resume') { localPaused = false; paused = false; await scopeUpdate(); }
    else if (message.type === 'disconnect') { await disconnect(); }
    return {count:attached.size,paused};
  })().then(reply,error => reply({error:error.message}));
  return true;
});
chrome.debugger.onDetach.addListener(async source => { attached.delete(source.tabId); childSessions.delete(source.tabId); await scopeUpdate(); });
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
    if (!permitted(tab.url,scope,scope.created)) { attached.delete(tab.id); try { await chrome.debugger.detach({tabId:tab.id}); } catch {} }
    await scopeUpdate();
  }
  if (!['Page.frameNavigated','Page.navigatedWithinDocument','Page.javascriptDialogOpening','Page.javascriptDialogClosed','Runtime.exceptionThrown','Runtime.consoleAPICalled','Network.loadingFailed','Network.responseReceived'].includes(method)) return;
  const sanitized = {};
  if (event.type) sanitized.type = event.type;
  if (event.response) sanitized.response = {status:event.response.status,url:event.response.url.split(/[?#]/)[0]};
  try { post({kind:'event',tab_id:source.tabId,method,result:sanitized}); } catch {}
});
chrome.tabs.onCreated.addListener(async tab => {
  const scope = attached.get(tab.openerTabId);
  if (!scope || !allowNew || paused) return;
  try { await attach(tab,{...scope,created:true}); } catch {}
});
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
