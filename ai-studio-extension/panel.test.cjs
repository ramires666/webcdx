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
    querySelectorAll(selector) { return selector === 'input:checked' ? this.children.flatMap(x => x.children || []).filter(x => x.type === 'checkbox' && x.checked) : []; }
    addEventListener(name, fn) { this.listeners[name] = fn; }
  }
  const document = { activeElement: null, getElementById(id) { if (!els.has(id)) els.set(id, new Element(id)); return els.get(id); }, createElement() { return new Element(); } };
  let snapshot = { connected: false, session: { phase: 'idle', paused: false }, tools: [], agents: [] }, failAction = false, poll, connectCount = 0;
  const chrome = { runtime: { id: 'test', lastError: null, sendMessage(msg, cb) {
    if (msg.type === 'bridge.status') return cb({ ok: true, state: snapshot });
    if (msg.type === 'bridge.connect') connectCount++;
    if (msg.type === 'bridge.connect' && options.connectReply) return options.connectReply.promise.then(reply => cb(reply));
    if (failAction) { failAction = false; return cb({ ok: false, error: 'Action failed' }); }
    cb({ ok: true, state: snapshot });
  } }, storage: { local: { async get() { if (options.storageRead) await options.storageRead.promise; return { bridgeConnection: store.bridgeConnection }; }, async set(values) { Object.assign(store, values); } } }, tabs: { query: async () => [{ id: 1 }] }, permissions: { request: () => options.permission?.promise || Promise.resolve(true) }, identity: { getRedirectURL: () => 'https://redirect/' } };
  const context = vm.createContext({ chrome, document, URL, setInterval(fn) { poll = fn; } });
  vm.runInContext(fs.readFileSync(require.resolve('./panel.js'), 'utf8'), context);
  return { get: id => document.getElementById(id), async ready() { if (options.storageRead) await options.storageRead.promise; await Promise.resolve(); await poll(); }, async update(value) { snapshot = value; await poll(); }, fail() { failAction = true; }, click(id) { return document.getElementById(id).listeners.click(); }, input(id, value) { const el = document.getElementById(id); el.value = value; return el.listeners.input(); }, get connectCount() { return connectCount; } };
}

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
  await p.update({ ...base, session: { phase: 'result_ready', paused: false, resultText: 'webcodex-result' } });
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
