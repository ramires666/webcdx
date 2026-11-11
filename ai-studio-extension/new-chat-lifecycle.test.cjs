const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const WebCodexProtocol = require('./protocol.js');

const KEY = 'webcodexState', TOKEN = 'test-token';
const tool = { name: 'read_file', description: 'test', inputSchema: { type: 'object', properties: { path: { type: 'string' } }, required: ['path'], additionalProperties: false } };

function harness() {
  let model = { text: 'existing answer' }, latestUser = null, modelAfterUser = true, editorEmpty = true, supported = true;
  let refreshFails = 1, systemStageFails = 1, now = 10000, generating = false, observerCallback;
  const operations = [], messages = [], staged = [], bgListeners = [], contentListeners = [], timers = new Map(), intervals = new Map(); let timerId = 0;
  const session = { phase: 'waiting_model', agentId: 'agent-1', toolNames: ['read_file'], automatic: true, paused: false, turn: 'old-turn', instructions: '', pendingCall: null, resultText: '', error: '', count: 0, tabId: 7, sessionId: 'old-session', conversationKey: 'chat-1', pageInstance: 'page-1' };
  const db = { [KEY]: { connected: true, gateUrl: 'https://gate.example', clientId: 'client', accessToken: TOKEN, agents: [{ id: 'agent-1', name: 'test', online: true }], tools: [tool], session }, };
  const local = { bridgeAuth: { gateUrl: 'https://gate.example', clientId: 'client', accessToken: TOKEN }, bridgeAuto: { enabled: true, consent: true, agentId: 'agent-1', toolNames: ['read_file'], automatic: true, autoApprove: false }, bridgeAutoSetupV1: true };
  const chrome = {
    runtime: { id: 'test', getURL: p => `chrome-extension://test/${p}`, onMessage: { addListener: fn => bgListeners.push(fn) }, sendMessage: msg => new Promise(resolve => { messages.push(structuredClone(msg)); const async = bgListeners[0](msg, { frameId: 0, url: 'https://aistudio.google.com/app/prompts/chat', tab: { id: 7 } }, resolve); if (async !== true) resolve(undefined); }) },
    storage: { session: { setAccessLevel: async () => {}, get: async key => ({ [key]: structuredClone(db[key]) }), set: async values => Object.assign(db, structuredClone(values)) }, local: { setAccessLevel: async () => {}, get: async key => ({ [key]: structuredClone(local[key]) }), set: async values => Object.assign(local, structuredClone(values)), remove: async key => delete local[key] } },
    action: { onClicked: { addListener() {} } }, alarms: { create() {}, onAlarm: { addListener() {} } }, permissions: { contains: async () => true },
    tabs: { get: async id => ({ id, url: 'https://aistudio.google.com/app/prompts/chat' }), sendMessage: async (_id, msg) => new Promise(resolve => { messages.push(structuredClone(msg)); const listener = contentListeners[0]; if (!listener) throw Error('No content listener'); const async = listener(msg, {}, resolve); if (async !== true && !['studio.stage'].includes(msg.type)) resolve(undefined); }) }
  };
  const adapters = { createAdapter: () => ({
    inspect: () => ({ supported, generating, controlsPending: false, editorEmpty, conversationKey: 'chat-1', latestModel: model, latestUser, modelAfterUser, error: supported ? '' : 'editor unavailable' }),
    stageSystemInstructions: async text => { if (systemStageFails-- > 0) return false; staged.push({ kind: 'system', text }); return true; },
    getSystemStageError: () => 'transient system editor', stage: text => { staged.push({ kind: 'turn', text }); editorEmpty = false; return true; },
    restoreCleanup() {}, hasDelivery: operationId => { try { return JSON.parse(latestUser?.text || '{}').operation_id === operationId; } catch { return false; } }, submit: async text => { latestUser={text};modelAfterUser=false;return true; }
  }) };
  class TestDate extends Date { static now() { return now; } }
  const context = { chrome, window: null, document: { documentElement: {}, addEventListener(type, fn) { this[type] = fn; } }, location: { origin: 'https://aistudio.google.com' }, WebCodexStudio: adapters, WebCodexProtocol, URL, URLSearchParams, TextEncoder, TextDecoder, Uint8Array, ArrayBuffer, JSON, Promise, Set, Map, Date: TestDate, AbortSignal, Math, Error, String, Object, Number, RegExp, console: { error() {}, warn() {}, log() {} }, crypto: require('node:crypto').webcrypto, btoa: s => Buffer.from(s, 'binary').toString('base64'), setTimeout: fn => { timers.set(++timerId, fn); return timerId; }, clearTimeout: id => timers.delete(id), setInterval: fn => { intervals.set(++timerId, fn); return timerId; }, clearInterval: id => intervals.delete(id), MutationObserver: class { constructor(fn) { observerCallback = fn; } observe() {} disconnect() {} }, importScripts() {}, WebCodexMCP: { createClient: () => ({ initialize: async () => ({}), listAgents: async () => [{ id: 'agent-1', name: 'test', online: true }], createOperation: async operation => { operations.push(operation); return {status:'completed',operation_id:'op-'+operations.length,result:{content:[{type:'text',text:'file contents'}]}}; }, listTools: async () => { if (refreshFails-- > 0) throw Error('temporary Gate refresh failure'); return [tool]; } }) } };
  context.window = { top: null, __webcodexBridgeLoaded: false }; context.window.top = context.window;
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'background.js'), 'utf8'), context, { filename: 'background.js' });
  // Content owns its listener on runtime.onMessage; separate the background listener installed first.
  context.chrome.runtime.onMessage.addListener = fn => contentListeners.push(fn);
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'content.js'), 'utf8'), context, { filename: 'content.js' });
  return { chrome, local, messages, staged, db, operations, userMessage: text => { latestUser={text};modelAfterUser=false; }, modelMessage: text => { context.document.click({target:{closest:()=>({getAttribute:name=>name==='aria-label'?'Run':'',innerText:'Run',querySelector:()=>null})}}); generating=true;observerCallback();model={text,element:{}};modelAfterUser=true;generating=false;observerCallback(); }, armExisting: () => chrome.tabs.sendMessage(7, {type:'studio.arm',sessionId:'old-session'}), clickNewChat() { context.document.click({ target: { closest: () => ({ getAttribute: name => name === 'aria-label' ? 'New chat' : '', innerText: '' }) } }); model = null; }, async flush() { for (let i = 0; i < 8; i++) await new Promise(resolve => setImmediate(resolve)); }, async tick() { const pending = [...timers.values()]; timers.clear(); for (const fn of [...pending, ...intervals.values()]) fn(); await this.flush(); }, advance(ms) { now += ms; }, get state() { return db[KEY].session; } };
}

test('New chat retries startup after Gate refresh and system instruction staging failures', async () => {
  const h = harness();
  await h.flush();
  await h.armExisting();
  h.clickNewChat();
  await h.tick(); // Gate refresh fails; the content observer must retain the clicked transition.
  h.advance(6000);
  await h.tick(); // Gate refresh succeeds; system editor staging fails transiently.
  h.advance(6000);
  await h.tick(); // Retry starts and stages the new session.
  const stageMessages = h.messages.filter(message => message.type === 'studio.stage');
  assert.equal(stageMessages.length, 2, 'first staging attempt fails, then one retry succeeds');
  assert.equal(h.state.phase, 'waiting_model');
  assert.notEqual(h.state.turn, 'old-turn');
  assert.equal(h.staged.filter(item => item.kind === 'turn').length, 0, 'the composer stays empty');
  assert.equal(h.state.turnless, true);
  assert.doesNotMatch(h.staged.find(item => item.kind === 'system').text, /Active turn|active_turn|next_turn/);
  await h.tick();
  assert.equal(h.messages.filter(message => message.type === 'studio.stage').length, 2, 'successful session is not armed again');
  assert.equal(h.messages.some(message => message.type === 'studio.call'), false, 'New chat never submits a tool command');
});

test('a new chat runs repeated calls without inserting an ID or replaying an older response', async () => {
  const h=harness();await h.flush();await h.armExisting();h.clickNewChat();
  await h.tick();h.advance(6000);await h.tick();h.advance(6000);await h.tick();
  assert.equal(h.state.turnless,true);assert.equal(h.staged.some(item=>item.kind==='turn'),false);
  const call='```webcodex-call\n'+JSON.stringify({v:1,name:'read_file',arguments:{path:'safe.txt'}})+'\n```';
  h.userMessage('Read the file');await h.tick();h.modelMessage(call);await h.tick();
  assert.equal(h.operations.length,1);
  await h.tick();assert.equal(h.operations.length,1,'the old model response before its result never replays');
  h.modelMessage(call);await h.tick();
  assert.equal(h.operations.length,2,'the same arguments after a distinct tool result execute normally');
  assert.notEqual(h.operations[0].key,h.operations[1].key);
  for(const delivery of h.messages.filter(item=>item.type==='studio.deliver')){
    assert.equal(Object.hasOwn(JSON.parse(delivery.text),'turn'),false);
    assert.equal(Object.hasOwn(JSON.parse(delivery.text),'next_turn'),false);
  }
});
