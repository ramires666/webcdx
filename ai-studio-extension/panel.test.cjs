const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function deferred() {
  let resolve;
  const promise = new Promise(r => { resolve = r; });
  return { promise, resolve };
}

function createPanel(store = {}, options = {}) {
  const els = new Map();
  class Element {
    constructor(id = '') { Object.assign(this, { id, children: [], listeners: {}, value: '', checked: false, disabled: false, hidden: false, textContent: '', type: '', name: '' }); }
    append(...items) { this.children.push(...items); }
    replaceChildren() { this.children = []; }
    querySelectorAll(selector) { const inputs = this.children.flatMap(x => x.children || []).filter(x => x.type === 'checkbox' || x.type === 'radio'); return selector === 'input:checked' ? inputs.filter(x => x.checked) : selector === 'input' ? inputs : []; }
    addEventListener(name, fn) { this.listeners[name] = fn; }
  }
  const document = { activeElement: null, getElementById(id) { if (!els.has(id)) els.set(id, new Element(id)); return els.get(id); }, createElement() { return new Element(); } };
  let snapshot = { connected: false, session: { phase: 'idle', paused: false }, tools: [], agents: [], autoSettings: store.autoSettings }, failAction = false, poll, connectCount = 0;
  const messages = [];
  const chrome = { runtime: { id: 'test', getManifest:()=>({version:options.manifestVersion||'0.3.13'}), lastError: null, sendMessage(msg, cb) {
    messages.push(msg);
    if (msg.type === 'bridge.status') return cb({ ok: true, state: snapshot });
    if (msg.type === 'bridge.cleanupPreference') { snapshot.hideServiceMessages=msg.enabled; return cb({ok:true,state:snapshot}); }
    if (msg.type === 'bridge.preferences') { store.autoSettings = msg; snapshot.autoSettings = msg; return cb({ ok: true, state: snapshot }); }
    if (msg.type === 'bridge.connect') connectCount++;
    if (msg.type === 'bridge.connect' && options.connectReply) return options.connectReply.promise.then(reply => cb(reply));
    if (failAction) { failAction = false; return cb({ ok: false, error: 'Action failed' }); }
    cb({ ok: true, state: snapshot });
  } }, storage: { local: { async get() { if (options.storageRead) await options.storageRead.promise; return { bridgeConnection: store.bridgeConnection }; }, async set(values) { Object.assign(store, values); } } }, tabs: { query: async () => [{ id: 1 }] }, permissions: { request: () => options.permission?.promise || Promise.resolve(true) }, identity: { getRedirectURL: () => 'https://redirect/' } };
  if(options.localOnly)delete chrome.identity;
  const context = vm.createContext({ chrome, document, URL, setInterval(fn) { poll = fn; } });
  vm.runInContext(fs.readFileSync(require.resolve('./panel.js'), 'utf8'), context);
  return { get: id => document.getElementById(id), messages, async ready() { if (options.storageRead) await options.storageRead.promise; await Promise.resolve(); await poll(); }, async update(value) { snapshot = value; await poll(); }, fail() { failAction = true; }, click(id) { return document.getElementById(id).listeners.click(); }, change(id, checked) { const el = document.getElementById(id); el.checked = checked; return el.listeners.change(); }, select(group, value, checked = true) { const container = document.getElementById(group), input = container.querySelectorAll('input').find(x => x.value === value); for (const item of container.querySelectorAll('input')) item.checked = item === input && checked; return container.listeners.change(); }, input(id, value) { const el = document.getElementById(id); el.value = value; return el.listeners.input(); }, get connectCount() { return connectCount; } };
}

test('service JSON preference reflects state and sends both checkbox values', async () => {
  const p = createPanel();
  await p.update({ connected: false, hideServiceMessages: true, session: { phase: 'idle' }, tools: [], agents: [] });
  assert.equal(p.get('hide-service').checked, true);
  await p.change('hide-service', false);
  await p.change('hide-service', true);
  assert.deepEqual(p.messages.filter(x => x.type === 'bridge.cleanupPreference').map(x => x.enabled), [false, true]);
});

test('manual chat ID button targets the active tab and is disabled without an available session',async()=>{
  const p=createPanel(),session={phase:'waiting_model',sessionMarker:'a'.repeat(32),tabId:1};
  const base={connected:true,autoSettings:{consent:true},session,tools:[],agents:[]};
  await p.update(base);assert.equal(p.get('inject-marker').disabled,false);
  assert.equal(p.get('session-marker').textContent,session.sessionMarker);
  await p.click('inject-marker');
  assert.equal(p.messages.at(-1).type,'bridge.injectMarker');assert.equal(p.messages.at(-1).tabId,1);
  for(const changes of [{connected:false},{autoSettings:{consent:false}},...[
    {phase:'idle'},{phase:'stopped'},{paused:true},{resumeBlocked:true},{sessionMarker:''},{pendingCall:{}},{operationKey:'pending'},{operationId:'pending'},{resultText:'pending'}
  ].map(change=>({session:{...session,...change}}))]){
    await p.update({...base,...changes});assert.equal(p.get('inject-marker').disabled,true,JSON.stringify(changes));
  }
  await p.update(base);p.fail();await p.click('inject-marker');
  assert.equal(p.get('error').textContent,'Action failed');
  await p.update({...base,session:{...session,phase:'stopped'}});
  assert.equal(p.get('session-marker').textContent,'Нет активного сеанса');
});

test('quick controls follow the version and local agent while allow-all persists every checkbox',async()=>{
  const html=fs.readFileSync(require.resolve('./panel.html'),'utf8');
  assert.ok(html.indexOf('id="extension-version"')<html.indexOf('id="arm"'));
  assert.ok(html.indexOf('id="arm"')<html.indexOf('id="error"'));
  for(const id of ['status','transport-info','page-script','observer-status'])assert.equal(html.includes(`id="${id}"`),false);
  assert.ok(html.indexOf('id="agents"')<html.indexOf('id="allow-all"'));
  assert.match(html, /<div class="row">\s*<button id="allow-all"[^>]*>Разрешить всё<\/button>\s*<button id="collapse-chat"[^>]*>Свернуть чат хелпера<\/button>\s*<\/div>/);
  assert.ok(html.indexOf('id="allow-all"')<html.indexOf('id="tools"'));
  assert.equal((html.match(/id="arm"/g)||[]).length,1);
  const panel=createPanel();
  await panel.update({connected:true,transport:'local',session:{phase:'idle'},agents:[{id:'local'}],tools:[{name:'read_file'},{name:'write_file'},{name:'exec_command'}],autoSettings:{agentId:'local',toolNames:['read_file'],consent:false,automatic:false,autoApprove:false,attachFiles:false}});
  await panel.click('allow-all');
  for(const id of ['consent','automatic','attach-files','auto-approve','hide-service'])assert.equal(panel.get(id).checked,true,id);
  assert.equal(panel.get('tools').querySelectorAll('input').every(input=>input.checked),true);
  const preferences=panel.messages.find(message=>message.type==='bridge.preferences');
  assert.equal(preferences.field,undefined);
  assert.deepEqual(Array.from(preferences.toolNames),['read_file','write_file','exec_command']);
  for(const key of ['consent','automatic','attachFiles','autoApprove'])assert.equal(preferences[key],true);
  assert.equal(panel.get('arm').disabled,false);
  assert.equal(panel.messages.some(message=>message.type==='bridge.arm'),false);
  await panel.ready();
  for(const id of ['consent','automatic','attach-files','auto-approve','hide-service'])assert.equal(panel.get(id).checked,true,id);
});

test('chat width slider is next to the session heading and sends persisted tab values', async () => {
  const html = fs.readFileSync(require.resolve('./panel.html'), 'utf8');
  assert.match(html, /<div class="session-heading-row">\s*<h2 id="session-heading">Сеанс<\/h2>\s*<div class="chat-width-control">/);
  assert.match(html, /id="chat-width" type="range" min="0" max="100" step="10" value="0"/);
  assert.ok(html.includes('<span>default</span><span>Max</span>'));
  assert.ok(html.includes('<span id="chat-width-value">Ширина чата</span>'));
  assert.equal((html.match(/<span>\d+%<\/span>/g) || []).length, 0);
  const panel = createPanel();
  panel.get('chat-width-value').textContent = 'Ширина чата';
  await panel.update({ connected: false, chatWidth: 30, session: { phase: 'idle' }, tools: [], agents: [] });
  assert.equal(panel.get('chat-width').value, '30');
  assert.equal(panel.get('chat-width-value').textContent, 'Ширина чата');
  panel.get('chat-width').value = '0'; await panel.input('chat-width', '0');
  assert.equal(panel.get('chat-width-value').textContent, 'Ширина чата');
  const defaultWidth = panel.messages.filter(message => message.type === 'bridge.chatWidth').at(-1);
  assert.equal(defaultWidth.tabId, 1); assert.equal(defaultWidth.value, 0);
  await panel.update({ connected: false, chatWidth: 0, session: { phase: 'idle' }, tools: [], agents: [] });
  assert.equal(panel.get('chat-width-value').textContent, 'Ширина чата');
  assert.equal(panel.get('chat-width').value, '0');
  panel.get('chat-width').value = '100'; await panel.input('chat-width', '100');
  assert.equal(panel.get('chat-width-value').textContent, 'Ширина чата');
  assert.equal(panel.messages.filter(message => message.type === 'bridge.chatWidth').at(-1).value, 100);
});

test('resume identifies the currently selected tab for saved-chat recovery',async()=>{
  const panel=createPanel();
  await panel.click('resume');
  assert.equal(panel.messages.at(-1).type,'bridge.resume');
  assert.equal(panel.messages.at(-1).tabId,1);
});

test('collapse chat dispatches for the active tab while disconnected',async()=>{
  const panel=createPanel();
  await panel.click('collapse-chat');
  assert.equal(panel.messages.at(-1).type,'bridge.collapseChat');
  assert.equal(panel.messages.at(-1).tabId,1);
  assert.equal(panel.get('error').hidden,true);
});

test('uncertain delivery offers a read-only check while execution uncertainty stays blocked',async()=>{
  const panel=createPanel();
  await panel.update({session:{phase:'delivery_uncertain',paused:false,resultText:'saved'}});
  assert.equal(panel.get('resume').disabled,false);
  assert.equal(panel.get('resume').textContent,'Проверить доставку');
  await panel.update({session:{phase:'paused',paused:true,resumeBlocked:true,reasonCode:'outcome_unknown'}});
  assert.equal(panel.get('resume').disabled,true);
});

test('native-file delivery defaults on and its preference is preserved across polls',async()=>{
  const p=createPanel();await p.update({connected:true,session:{phase:'waiting_model'},autoSettings:{consent:true,agentId:'a',toolNames:['read_file']},tools:[],agents:[]});
  assert.equal(p.get('attach-files').checked,true);
  await p.change('attach-files',false);assert.equal(p.messages.filter(message=>message.type==='bridge.preferences').at(-1).attachFiles,false);
  await p.update({connected:true,session:{phase:'waiting_model'},autoSettings:{attachFiles:false},tools:[],agents:[]});assert.equal(p.get('attach-files').checked,false);
});

test('header shows only the actual script and connected agent versions',async()=>{
  const p=createPanel({}, {manifestVersion:'0.3.2'});
  await p.update({connected:true,session:{phase:'waiting_model'},pageScript:{version:'0.3.1'},tools:[],agents:[]});
  assert.equal(p.get('extension-version').textContent,'Скрипт 0.3.1 · Агент версия не передана');
  await p.update({connected:true,session:{phase:'waiting_model'},pageScript:{version:'0.3.2'},tools:[],agents:[]});
  assert.equal(p.get('extension-version').textContent,'Скрипт 0.3.2 · Агент версия не передана');
});

test('resuming clears a resolved session error from the panel', async () => {
  const p = createPanel();
  await p.update({connected:true,session:{phase:'paused',paused:true,error:'Session operation limit reached; resume to continue.'}});
  assert.equal(p.get('error').hidden,false);
  await p.update({connected:true,session:{phase:'waiting_model',paused:false,error:''}});
  assert.equal(p.get('error').hidden,true);
  assert.equal(p.get('error').textContent,'');
});

test('reply diagnostics expose exact source and require explicit correction action',async()=>{
  const p=createPanel();
  const session={phase:'waiting_model',replyError:{code:'format',message:'Damaged reply',sourceText:'raw \\n\n  ',correctionRequested:false}};
  await p.update({connected:true,session,autoSettings:{consent:true}});
  assert.equal(p.get('reply-error').hidden,false);
  assert.equal(p.get('reply-error-source').value,session.replyError.sourceText);
  assert.equal(p.get('request-correction').disabled,false);
  await p.click('retry-reply');await p.click('request-correction');
  assert.deepEqual(p.messages.filter(m=>m.type!=='bridge.status').map(m=>m.type),['bridge.retryReply','bridge.requestCorrection']);
  session.replyError.correctionRequested=true;
  await p.update({connected:true,session,autoSettings:{consent:true}});
  assert.equal(p.get('request-correction').disabled,true);
});

test('older preference snapshots cannot replace a newer acknowledged revision',async()=>{
  const p=createPanel();
  await p.update({connected:true,session:{phase:'idle'},autoSettings:{revision:4,attachFiles:false,consent:true}});
  await p.update({connected:true,session:{phase:'idle'},autoSettings:{revision:3,attachFiles:true,consent:false}});
  assert.equal(p.get('attach-files').checked,false);assert.equal(p.get('consent').checked,true);
  await p.change('attach-files',true);
  assert.equal(p.messages.filter(m=>m.type==='bridge.preferences').at(-1).field,'attachFiles');
});

test('idle connection can arm after consent; safe tools initialize once and empty selection stays empty', async () => {
  const p = createPanel();
  await p.update({ connected: false, session: { phase: 'idle' }, tools: [], agents: [] });
  await p.update({ connected: true, session: { phase: 'idle' }, tools: [{ name: 'read_file' }, { name: 'browser_click' }], agents: [] });
  assert.equal(p.get('tools').children[0].children[0].checked, true);
  p.get('consent').checked = true; await p.update({ connected: true, session: { phase: 'idle' }, tools: [{ name: 'read_file' }, { name: 'browser_click' }], agents: [] });
  assert.equal(p.get('arm').disabled, false);
  p.get('tools').children[0].children[0].checked = false;
  await p.update({ connected: true, session: { phase: 'idle' }, tools: [{ name: 'read_file' }, { name: 'browser_click' }], agents: [] });
  assert.deepEqual(p.get('tools').querySelectorAll('input:checked').map(x => x.value), []);
});

test('demo label and session controls follow phase safety', async () => {
  const p = createPanel();
  const base = { connected: true, demoMode: true, tools: [], agents: [] };
  await p.update({ ...base, session: { phase: 'paused', paused: true, resumeBlocked: true, resultText: 'text' } });
  assert.equal(p.get('arm').disabled, false);
  assert.equal(p.get('resume').disabled, true);
  assert.equal(p.get('deliver').disabled, true);
  assert.equal(p.get('pause').disabled, true);
  assert.equal(p.get('stop').disabled, false);
  await p.update({ ...base, session: { phase: 'ready', paused: false, resultText: 'normal final prose' } });
  assert.equal(p.get('deliver').disabled, true);
  await p.update({ ...base, session: { phase: 'result_ready', paused: false, resultText: 'helper-result' } });
  assert.equal(p.get('deliver').disabled, false);
  await p.update({ ...base, session: { phase: 'idle', paused: false } });
  assert.equal(p.get('pause').disabled, true); assert.equal(p.get('stop').disabled, true);
});

test('action errors remain visible across status renders', async () => {
  const p = createPanel(); await p.update({ connected: true, session: { phase: 'idle' }, tools: [], agents: [] });
  p.fail(); await p.click('disconnect');
  assert.equal(p.get('error').textContent, 'Action failed');
  await p.update({ connected: true, session: { phase: 'idle' }, tools: [], agents: [] });
  assert.equal(p.get('error').textContent, 'Action failed');
});

test('paused or failed sessions never also claim readiness',async()=>{
  const p=createPanel();
  for(const session of [{phase:'paused',paused:true,error:'Ambiguous or damaged Helper file delimiters'},{phase:'waiting_model',error:'failed'}]){
    await p.update({connected:true,autoStatus:'Готово',session,tools:[],agents:[]});
    assert.equal(p.get('error').textContent,session.error);
  }
});

test('the panel exposes observer failures and correction progress instead of readiness',async()=>{
  const p=createPanel();
  await p.update({connected:true,autoStatus:'Готово',session:{phase:'waiting_model'},pageScript:{observerStatus:'error',observerError:'DOM failure'}});
  assert.equal(p.get('error').textContent,'DOM failure');assert.equal(p.get('arm').disabled,false);
  await p.update({connected:true,autoStatus:'Готово',session:{phase:'waiting_model',notice:'Ожидается исправление'},pageScript:{observerStatus:'waiting_model'}});
  assert.equal(p.get('error').hidden,true);
  assert.equal(p.get('arm').disabled,false);
});

test('connection inputs persist across panel reopen and a failed connection', async () => {
  const store = {}, p = createPanel(store);
  await p.input('gate-url', 'https://gate.example');
  await p.input('client-id', 'client-7');
  p.fail();
  await p.click('connect');
  assert.match(p.get('error').textContent, /Action failed/);
  const reopened = createPanel(store);
  await reopened.ready();
  assert.equal(reopened.get('gate-url').value, 'https://gate.example');
  assert.equal(reopened.get('client-id').value, 'client-7');
});

test('slow storage restore does not overwrite fields edited while it is loading', async () => {
  const storageRead = deferred(), store = { bridgeConnection: { gateUrl: 'https://old.example', clientId: 'old' } };
  const p = createPanel(store, { storageRead });
  await p.input('gate-url', 'https://fresh.example');
  await p.input('client-id', 'fresh');
  storageRead.resolve();
  await p.ready();
  assert.equal(p.get('gate-url').value, 'https://fresh.example');
  assert.equal(p.get('client-id').value, 'fresh');
});

test('saved agent and preferences restore on reopen, and polls preserve edits', async () => {
  const store = { autoSettings: { agentId: 'agent-1', toolNames: ['read_file'], consent: true, automatic: true } };
  const base = { connected: true, session: { phase: 'idle' }, tools: [{ name: 'read_file' }, { name: 'list_agents' }], agents: [{ id: 'agent-1', name: 'A' }, { id: 'agent-2', name: 'B' }], autoSettings: store.autoSettings, autoStatus: 'ready' };
  const p = createPanel(store); await p.update(base);
  assert.equal(p.get('agents').querySelectorAll('input:checked')[0].value, 'agent-1');
  assert.deepEqual(p.get('tools').querySelectorAll('input:checked').map(x => x.value), ['read_file']);
  assert.equal(p.get('consent').checked, true); assert.equal(p.get('automatic').checked, true);
  await p.select('agents', 'agent-2'); await p.change('consent', false);
  const updates = p.messages.filter(x => x.type === 'bridge.preferences');
  assert.equal(updates[0].agentId, 'agent-2'); assert.equal(updates[1].consent, false);
  await p.update(base);
  assert.equal(p.get('agents').querySelectorAll('input:checked')[0].value, 'agent-2');
  assert.equal(p.get('consent').checked, false);
  const reopened = createPanel(store); await reopened.update({ ...base, autoSettings: store.autoSettings });
  assert.equal(reopened.get('agents').querySelectorAll('input:checked')[0].value, 'agent-2');
  assert.equal(reopened.get('consent').checked, false);
  assert.equal(reopened.get('arm').textContent, 'Запустить сеанс');
});

test('remembered tools appear selected when tool metadata arrives after an offline poll', async () => {
  const saved = { agentId: 'agent-1', toolNames: ['read_file'], consent: true, automatic: false };
  const p = createPanel({ autoSettings: saved });
  await p.update({ connected: true, session: { phase: 'idle' }, tools: [{ name: 'list_agents' }], agents: [{ id: 'agent-1', name: 'A', online: false }], autoSettings: saved, autoStatus: 'Агент не в сети' });
  await p.update({ connected: true, session: { phase: 'idle' }, tools: [{ name: 'list_agents' }, { name: 'read_file' }], agents: [{ id: 'agent-1', name: 'A' }], autoSettings: saved, autoStatus: 'Готово' });
  assert.deepEqual(p.get('tools').querySelectorAll('input:checked').map(x => x.value), ['read_file']);
});

test('saved tools absent from the current list can be unchecked', async () => {
  const saved = { agentId: 'agent-1', toolNames: ['read_file'], consent: true, automatic: false };
  const p = createPanel({ autoSettings: saved });
  await p.update({ connected: true, session: { phase: 'idle' }, tools: [{ name: 'list_agents' }], agents: [{ id: 'agent-1', name: 'A', online: false }], autoSettings: saved });
  const unavailable = p.get('tools').querySelectorAll('input').find(x => x.value === 'read_file');
  assert.equal(unavailable.checked, true);
  await p.select('tools', 'read_file', false);
  assert.equal(p.messages.filter(x => x.type === 'bridge.preferences').at(-1).toolNames.length, 0);
});

test('active consent and automatic changes persist through bridge preferences', async () => {
  const p = createPanel();
  await p.update({ connected: true, session: { phase: 'ready', agentId: 'agent-1', toolNames: ['read_file'], automatic: true }, tools: [{ name: 'read_file' }], agents: [{ id: 'agent-1', name: 'A' }], autoSettings: { agentId: 'agent-1', toolNames: ['read_file'], consent: true, automatic: true } });
  await p.change('automatic', false); await p.change('consent', false);
  const updates = p.messages.filter(x => x.type === 'bridge.preferences');
  assert.equal(updates[0].automatic, false); assert.equal(updates[1].consent, false);
});

test('active polls keep saved tool choices and auto-approval preferences', async () => {
  const saved = { agentId: 'agent-1', toolNames: ['read_file'], consent: true, automatic: false, autoApprove: false };
  const active = { connected: true, session: { phase: 'waiting_model', agentId: 'agent-1', toolNames: [], automatic: false }, tools: [{ name: 'read_file' }, { name: 'search_files' }], agents: [{ id: 'agent-1', name: 'A' }], autoSettings: saved };
  const p = createPanel({ autoSettings: saved });
  await p.update(active);
  assert.deepEqual(p.get('tools').querySelectorAll('input:checked').map(x => x.value), ['read_file']);
  assert.equal(p.get('auto-approve').checked, false);
  assert.equal(p.get('tools-next-session').hidden, false);
  await p.select('tools', 'search_files');
  await p.change('auto-approve', true);
  await p.update(active);
  assert.deepEqual(p.get('tools').querySelectorAll('input:checked').map(x => x.value), ['search_files']);
  assert.equal(p.get('auto-approve').checked, true);
  const updates = p.messages.filter(x => x.type === 'bridge.preferences');
  assert.equal(updates[0].toolNames.join(','), 'search_files');
  assert.equal(updates[1].autoApprove, true);
  assert.equal(updates[1].toolNames.join(','), 'search_files');
  assert.equal(p.messages.filter(x => x.type === 'bridge.preferences').at(-1).autoApprove, true);
});

test('active polls keep a newly selected agent and persist it as the next preference', async () => {
  const active = { connected: true, session: { phase: 'waiting_model', agentId: 'agent-1', toolNames: [], automatic: false }, tools: [], agents: [{ id: 'agent-1', name: 'A' }, { id: 'agent-2', name: 'B' }], autoSettings: { agentId: 'agent-1', toolNames: [], consent: true, automatic: false } };
  const p = createPanel();
  await p.update(active);
  await p.select('agents', 'agent-2');
  await p.update(active);
  assert.equal(p.get('agents').querySelectorAll('input:checked')[0].value, 'agent-2');
  assert.equal(p.messages.find(x => x.type === 'bridge.preferences').agentId, 'agent-2');
});

test('arm sends auto-approval preference', async () => {
  const p = createPanel();
  await p.update({ connected: true, session: { phase: 'idle' }, tools: [{ name: 'read_file' }], agents: [] });
  p.get('consent').checked = true;
  await p.change('auto-approve', true);
  await p.click('arm');
  assert.equal(p.messages.find(x => x.type === 'bridge.arm').autoApprove, true);
});

test('late saved consent hydrates during an active session without replacing active choices', async () => {
  const p = createPanel();
  await p.update({ connected: true, session: { phase: 'idle' }, tools: [], agents: [], autoSettings: {} });
  const saved = { agentId: 'agent-1', toolNames: ['read_file'], consent: true, automatic: true };
  const active = { connected: true, session: { phase: 'waiting_model', agentId: 'agent-1', toolNames: ['read_file'], automatic: true }, tools: [{ name: 'read_file' }], agents: [{ id: 'agent-1', name: 'A' }], autoSettings: saved };
  await p.update(active);
  assert.equal(p.get('consent').checked, true);
  assert.equal(p.get('automatic').checked, true);
  assert.equal(p.get('agents').querySelectorAll('input:checked')[0].value, 'agent-1');
  await p.change('consent', false);
  await p.change('automatic', false);
  await p.update(active);
  assert.equal(p.get('consent').checked, false);
  assert.equal(p.get('automatic').checked, false);
});

test('local connection shows the received agent version with Gate configuration hidden', async () => {
  const p = createPanel();
  await p.ready();
  await p.update({ connected: true, transport: 'local', agentVersion: 'received-version', session: { phase: 'idle' }, tools: [], agents: [] });
  assert.match(p.get('extension-version').textContent, /Агент received-version/);
  assert.equal(p.get('connect').disabled, false);
  assert.equal(p.get('gate-connection').hidden, true);
  assert.doesNotMatch(p.get('extension-version').textContent, /Gate/);
  assert.equal(p.get('connect').textContent, 'Подключить');
  await p.input('gate-url', 'https://gate.example');
  await p.click('connect');
  assert.equal(p.messages.find(m => m.type === 'bridge.connect').configureGate, true);
});

test('connect stays locked across status polls, ignores duplicate clicks, and unlocks on error', async () => {
  const permission = deferred(), connectReply = deferred(), p = createPanel({}, { permission, connectReply });
  await p.ready();
  await p.input('gate-url', 'https://gate.example');
  const first = p.click('connect');
  assert.equal(p.get('connect').disabled, true);
  assert.equal(p.get('demo').disabled, true);
  assert.equal(p.get('disconnect').disabled, true);
  await p.update({ connected: false, session: { phase: 'idle' }, tools: [], agents: [] });
  assert.equal(p.get('connect').disabled, true);
  const second = p.click('connect');
  permission.resolve(true);
  await Promise.resolve(); await Promise.resolve();
  assert.equal(p.connectCount, 1);
  connectReply.resolve({ ok: false, error: 'Login failed' });
  await Promise.all([first, second]);
  assert.equal(p.get('connect').disabled, false);
  assert.equal(p.get('demo').disabled, false);
  assert.equal(p.get('disconnect').disabled, true);
  assert.equal(p.get('error').textContent, 'Login failed');
});

test('file recovery has an explicit action and is blocked while an operation is unfinished',async()=>{
 const p=createPanel(),snapshot={connected:true,autoSettings:{consent:true},session:{phase:'waiting_model',toolNames:['write_file']},tools:[],agents:[]};
 await p.update(snapshot);assert.equal(p.get('save-from-reply').disabled,false);
 await p.click('save-from-reply');assert.equal(p.messages.at(-1).type,'bridge.saveFromReply');
 await p.update({...snapshot,session:{...snapshot.session,operationKey:'pending'}});assert.equal(p.get('save-from-reply').disabled,true);
});


test('arming Gate intersects remembered tool choices with its currently available tools',async()=>{
  const remembered=Array.from({length:14},(_,index)=>'tool_'+index),available=remembered.slice(0,6);
  const panel=createPanel({autoSettings:{agentId:'remote',toolNames:remembered,consent:true,automatic:true,autoApprove:true}});
  await panel.update({connected:true,transport:'gate',session:{phase:'idle'},autoSettings:{agentId:'remote',toolNames:remembered,consent:true,automatic:true,autoApprove:true},agents:[{id:'remote',name:'Gate agent',online:true}],tools:available.map(name=>({name}))});
  assert.equal(panel.get('tools').querySelectorAll('input:checked').length,14);
  await panel.click('arm');
  const request=panel.messages.find(message=>message.type==='bridge.arm');
  assert.deepEqual(Array.from(request.toolNames),available);assert.equal(request.consent,true);assert.equal(request.automatic,true);assert.equal(request.autoApprove,true);
  assert.equal(panel.get('tools').querySelectorAll('input:checked').length,14);
});


test('Gate connection fields stay hidden and remote agents are absent from local-only panel',async()=>{
  const html=fs.readFileSync(require.resolve('./panel.html'),'utf8');
  assert.match(html,/<section id="gate-connection"[^>]* hidden>/);
  const panel=createPanel();
  await panel.update({connected:true,transport:'gate',gateUrl:'https://gate.example',session:{phase:'idle'},agents:[{id:'remote',name:'Gate agent'}],tools:[]});
  assert.equal(panel.get('gate-connection').hidden,true);assert.equal(panel.get('agents').hidden,true);
  assert.equal(panel.get('agents').querySelectorAll('input').length,0);assert.doesNotMatch(panel.get('extension-version').textContent,/Gate/);
});


test('session button starts, disconnects even without consent, and reflects a lost connection', async () => {
  const panel = createPanel();
  const base = {connected:true, transport:'local', agentVersion:'1.5.0-ai-studio-alpha.8', agents:[{id:'local',online:true}], tools:[{name:'read_file'}], autoSettings:{agentId:'local',consent:true,toolNames:['read_file']}};
  await panel.update({...base,session:{phase:'idle'}});
  assert.equal(panel.get('arm').textContent,'Запустить сеанс');
  assert.equal(panel.get('arm').className,'session-disconnected');
  await panel.click('arm');
  assert.equal(panel.messages.at(-1).type,'bridge.arm');
  await panel.update({...base,session:{phase:'waiting_model',agentId:'local'}});
  assert.equal(panel.get('arm').textContent,'Агент 1.5.0 подключен\nОтключить');
  assert.equal(panel.get('arm').className,'session-connected');
  await panel.update({...base,connected:false,autoSettings:{consent:false},session:{phase:'paused',agentId:'local'}});
  assert.equal(panel.get('arm').className,'session-disconnected');
  assert.equal(panel.get('arm').disabled,false);
  await panel.click('arm');
  assert.equal(panel.messages.at(-1).type,'bridge.stop');
  await panel.update({...base,session:{phase:'stopped'}});
  assert.equal(panel.get('arm').textContent,'Запустить сеанс');
  assert.equal(panel.get('arm').className,'session-disconnected');
  await panel.update({...base,agents:[{id:'local',online:false}],session:{phase:'waiting_model',agentId:'local'}});
  assert.equal(panel.get('arm').className,'session-disconnected');
});

test('permission toggle clears every checkbox and updates after individual edits and polls', async () => {
  const panel = createPanel();
  await panel.update({connected:true,session:{phase:'waiting_model',automatic:false},tools:[{name:'read_file'},{name:'write_file'}],autoSettings:{consent:false}});
  await panel.click('allow-all');
  assert.equal(panel.get('allow-all').textContent,'Снять все галочки');
  await panel.click('allow-all');
  for(const id of ['consent','automatic','attach-files','auto-approve','hide-service'])assert.equal(panel.get(id).checked,false,id);
  assert.equal(panel.get('tools').querySelectorAll('input').some(input=>input.checked),false);
  assert.equal(panel.get('allow-all').textContent,'Разрешить всё');
  const preferences = panel.messages.filter(message=>message.type==='bridge.preferences').at(-1);
  for(const key of ['consent','automatic','attachFiles','autoApprove'])assert.equal(preferences[key],false);
  assert.equal(preferences.toolNames.length,0);
  await panel.ready();
  assert.equal(panel.get('allow-all').textContent,'Разрешить всё');
  await panel.click('allow-all');
  await panel.change('attach-files',false);
  assert.equal(panel.get('allow-all').textContent,'Разрешить всё');
  await panel.click('allow-all');
  assert.equal(panel.get('allow-all').textContent,'Снять все галочки');
});


test('local extension starts without OAuth permission or identity API', async () => {
  const manifest = JSON.parse(fs.readFileSync(require.resolve('./manifest.json'),'utf8'));
  assert.equal(manifest.permissions.includes('identity'),false);
  assert.equal(manifest.optional_host_permissions,undefined);
  assert.deepEqual(manifest.host_permissions,['https://aistudio.google.com/*','http://127.0.0.1/*']);
  const panel = createPanel({}, {localOnly:true});
  await panel.ready();
  assert.equal(panel.get('error').textContent,'');
});
