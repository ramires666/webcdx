const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');

function harness() {
  const listeners = {}, sent = [], timers = new Map(), state = { supported: true, generating: false, editorEmpty: true, conversationKey: 'chat-1', latestModel: { text: 'old answer' } };
  let timerId = 0, submitted = [], staged = [], observerCallback, accepted = true, sendHandler;
  const adapter = {
    inspect: () => ({ ...state, latestModel: state.latestModel && { ...state.latestModel } }),
    stage: text => { staged.push(text); return state.editorEmpty && !state.generating; },
    submit: text => { submitted.push(text); return true; },
    hasDelivery: operationId => submitted.some(text => JSON.parse(text).operation_id === operationId)
  };
  const chrome = { runtime: { sendMessage: async message => { sent.push(structuredClone(message)); return sendHandler ? sendHandler(message) : { ok: accepted, accepted }; }, onMessage: { addListener: fn => { listeners.message = fn; } } } };
  const documentListeners = {};
  const document = { addEventListener(type, fn) { documentListeners[type] = fn; }, documentElement: {} };
  const window = {}; window.top = window;
  class MutationObserver { constructor(callback) { observerCallback = callback; } observe() {} }
  const context = { window, location: new URL('https://aistudio.google.com/app/prompts/one'), document, chrome, WebCodexStudio: { createAdapter: () => adapter }, MutationObserver,
    setTimeout: fn => { const id = ++timerId; timers.set(id, fn); return id; }, clearTimeout: id => timers.delete(id), setInterval: fn => { context.interval = fn; return 1; }, Promise, JSON, String, Set, structuredClone };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'content.js'), 'utf8'), context, { filename: 'content.js' });
  const send = message => new Promise(resolve => listeners.message(message, {}, response => resolve(structuredClone(response))));
  const flush = async () => { const pending = [...timers.values()]; timers.clear(); for (const fn of pending) fn(); await Promise.resolve(); await Promise.resolve(); };
  return { state, sent, staged, submitted, send, flush, mutate: () => observerCallback(), click: event => documentListeners.click(event), setAccepted: value => { accepted = value; }, setSendHandler: fn => { sendHandler = fn; } };
}

test('ignores history and sends one completed new reply as a call', async () => {
  const h = harness();
  assert.deepEqual(await h.send({ type: 'studio.arm', sessionId: 's1' }), { supported: true, generating: false, editorEmpty: true, conversationKey: 'chat-1', error: undefined, delivered: undefined });
  h.state.latestModel = { text: 'new answer' }; h.state.generating = true; h.mutate(); await h.flush();
  assert.equal(h.sent.some(m => m.type === 'studio.call'), false);
  h.state.generating = false; h.mutate(); await h.flush();
  const calls = h.sent.filter(m => m.type === 'studio.call');
  assert.equal(calls.length, 1); assert.equal(calls[0].text, 'new answer'); assert.equal(calls[0].generating, false);
  h.mutate(); await h.flush(); assert.equal(h.sent.filter(m => m.type === 'studio.call').length, 1);
});

test('safe draft, submitted result, delivery confirmation and disarm', async () => {
  const h = harness(); await h.send({ type: 'studio.arm', sessionId: 's1' });
  const text = JSON.stringify({ type: 'webcodex-result', v: 1, turn: 't1', next_turn: 't2', operation_id: 'op-7', name: 'read_file', result: {} });
  const draft = await h.send({ type: 'studio.deliver', sessionId: 's1', text, submit: false });
  assert.equal(draft.ok, true); assert.equal(draft.attempted, false); assert.equal(h.submitted.length, 0);
  const delivered = await h.send({ type: 'studio.deliver', sessionId: 's1', text, submit: true });
  assert.equal(delivered.ok, true); assert.equal(delivered.attempted, true); assert.equal(delivered.confirmed, true); assert.equal(h.submitted.length, 1);
  const duplicate = await h.send({ type: 'studio.deliver', sessionId: 's1', text, submit: true });
  assert.equal(duplicate.attempted, true); assert.equal(h.submitted.length, 1);
  assert.deepEqual(await h.send({ type: 'studio.snapshot', sessionId: 's1', operationId: 'op-7' }), { supported: true, generating: false, editorEmpty: true, conversationKey: 'chat-1', error: undefined, delivered: true });
  assert.deepEqual(await h.send({ type: 'studio.disarm', sessionId: 's1' }), { ok: true });
});

test('blocks draft when editor is not safe and snapshots a changed conversation', async () => {
  const h = harness(); await h.send({ type: 'studio.arm', sessionId: 's1' });
  h.state.editorEmpty = false;
  const result = await h.send({ type: 'studio.deliver', sessionId: 's1', text: '{}', submit: true });
  assert.equal(result.ok, false); assert.equal(result.attempted, false); assert.equal(h.submitted.length, 0);
  h.state.conversationKey = 'chat-2'; h.state.latestModel = { text: 'different conversation' }; h.mutate(); await h.flush();
  assert.equal(h.sent.some(m => m.type === 'studio.snapshot' && m.conversationKey === 'chat-2'), true);
  assert.equal(h.sent.some(m => m.type === 'studio.call'), false);
});

test('reobserves a completed call after background rejects it while paused', async () => {
  const h = harness(); await h.send({ type: 'studio.arm', sessionId: 's1' });
  h.state.latestModel = { text: 'new answer' }; h.setAccepted(false); h.mutate(); await h.flush();
  assert.equal(h.sent.filter(m => m.type === 'studio.call').length, 1);
  h.setAccepted(true); h.mutate(); await h.flush();
  assert.equal(h.sent.filter(m => m.type === 'studio.call').length, 2);
});

test('does not suppress normal streaming spinner or duplicate in-flight observations', async () => {
  const h = harness(); await h.send({ type: 'studio.arm', sessionId: 's1' });
  h.state.latestModel = { text: 'streamed answer' }; h.state.generating = true; h.mutate(); await h.flush();
  h.state.generating = false; h.mutate(); await h.flush();
  assert.equal(h.sent.filter(m => m.type === 'studio.call').length, 1);
  let finish; h.setSendHandler(() => new Promise(resolve => { finish = resolve; }));
  h.state.latestModel = { text: 'second answer' }; h.mutate(); await h.flush();
  h.mutate(); await h.flush();
  assert.equal(h.sent.filter(m => m.type === 'studio.call' && m.text === 'second answer').length, 1);
  finish({ ok: false, accepted: false }); await new Promise(resolve => setTimeout(resolve, 0));
  h.setSendHandler(null); h.mutate(); await h.flush();
  assert.equal(h.sent.filter(m => m.type === 'studio.call' && m.text === 'second answer').length, 2);
});

test('a user click on the active stop spinner suppresses its partial reply', async () => {
  const h = harness(); await h.send({ type: 'studio.arm', sessionId: 's1' });
  h.state.generating = true;
  const button = { getAttribute: () => '', innerText: '', querySelector: () => ({}) };
  h.click({ target: { closest: () => button } });
  h.state.generating = false; h.state.latestModel = { text: 'partial answer' }; h.mutate(); await h.flush();
  assert.equal(h.sent.some(m => m.type === 'studio.call' && m.text === 'partial answer'), false);
});
