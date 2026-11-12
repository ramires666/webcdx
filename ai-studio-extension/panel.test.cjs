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
  const chrome = { runtime: { id: 'test', getManifest:()=>({version:options.manifestVersion||'0.3.10'}), lastError: null, sendMessage(msg, cb) {
    messages.push(msg);
    if (msg.type === 'bridge.status') return cb({ ok: true, state: snapshot });
    if (msg.type === 'bridge.preferences') { store.autoSettings = msg; snapshot.autoSettings = msg; return cb({ ok: true, state: snapshot }); }
    if (msg.type === 'bridge.connect') connectCount++;
    if (msg.type === 'bridge.connect' && options.connectReply) return options.connectReply.promise.then(reply => cb(reply));
    if (failAction) { failAction = false; return cb({ ok: false, error: 'Action failed' }); }
    cb({ ok: true, state: snapshot });
  } }, storage: { local: { async get() { if (options.storageRead) await options.storageRead.promise; return { bridgeConnection: store.bridgeConnection }; }, async set(values) { Object.assign(store, values); } } }, tabs: { query: async () => [{ id: 1 }] }, permissions: { request: () => options.permission?.promise || Promise.resolve(true) }, identity: { getRedirectURL: () => 'https://redirect/' } };
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

test('shows the actual installed version and identifies an older script still running in the tab',async()=>{
  const p=createPanel({}, {manifestVersion:'0.3.2'});
  await p.update({connected:true,session:{phase:'waiting_model'},pageScript:{version:'0.3.1'},tools:[],agents:[]});
  assert.match(p.get('extension-version').textContent,/0\.3\.2/);
  assert.match(p.get('page-script').textContent,/0\.3\.1.*Перезагрузите/);
  await p.update({connected:true,session:{phase:'waiting_model'},pageScript:{version:'0.3.2'},tools:[],agents:[]});
  assert.match(p.get('page-script').textContent,/0\.3\.2/);
  assert.doesNotMatch(p.get('page-script').textContent,/Перезагрузите/);
});

test('resuming clears a resolved session error from the panel', async () => {
  const p = createPanel();
  await p.update({connected:true,session:{phase:'paused',paused:true,error:'Session operation limit reached; resume to continue.'}});
  assert.equal(p.get('error').hidden,false);
  await p.update({connected:true,session:{phase:'waiting_model',paused:false,error:''}});
  assert.equal(p.get('error').hidden,true);
  assert.equal(p.get('error').textContent,'');
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
  assert.match(p.get('status').textContent, /Демо режим/);
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
  assert.match(reopened.get('status').textContent, /ready/);
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

test('local connection shows the received agent version and allows configuring Gate fallback', async () => {
  const p = createPanel();
  await p.ready();
  await p.update({ connected: true, transport: 'local', agentVersion: 'received-version', session: { phase: 'idle' }, tools: [], agents: [] });
  assert.match(p.get('status').textContent, /Локальный агент подключён/);
  assert.match(p.get('transport-info').textContent, /received-version/);
  assert.equal(p.get('connect').disabled, false);
  assert.equal(p.get('connect').textContent, 'Настроить резерв Gate');
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
