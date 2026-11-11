const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const Protocol = require('./protocol.js');

function harness({ editorEmpty = true, system = async () => true } = {}) {
  const listeners = {}, calls = [], staged = [];
  const state = { supported: true, conversationKey: 'chat-1', editorEmpty, generating: false, latestModel: null };
  const adapter = {
    inspect: () => ({ ...state }),
    stageSystemInstructions: async value => { calls.push(['system', value]); return system(value); },
    stage: value => { staged.push(value); return state.editorEmpty && !state.generating; },
    restoreCleanup() {}, hasDelivery: () => false, submit: async () => false
  };
  const chrome = { runtime: { sendMessage: async () => ({ accepted: true }), onMessage: { addListener: fn => { listeners.message = fn; } } } };
  const document = { addEventListener() {}, documentElement: {} };
  const window = {}; window.top = window;
  class MutationObserver { observe() {} disconnect() {} }
  const context = { window, location: new URL('https://aistudio.google.com/app/prompts/one'), document, chrome,
    WebCodexStudio: { createAdapter: () => adapter }, MutationObserver,
    setTimeout, clearTimeout, setInterval: () => 1, clearInterval() {}, Promise, JSON, String, Set, Date, Math };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'content.js'), 'utf8'), context, { filename: 'content.js' });
  const send = message => new Promise(resolve => listeners.message(message, {}, resolve));
  return { send, calls, staged, state };
}

test('static tool schemas stay in system instructions while the session text carries only its active turn', () => {
  const schema = { type: 'object', properties: { path: { type: 'string' } }, required: ['path'] };
  const tools = [{ name: 'read_file', description: 'Read a file', inputSchema: schema }];
  const systemText = Protocol.buildSystemInstructions({ agentId: 'agent-1', tools, instructions: 'Use the MCP tool.' });
  const sessionText = Protocol.buildSessionInstructions({ turn: 'nonce-42' });
  assert.match(systemText, /"name": "read_file"/);
  assert.match(systemText, /"path"/);
  assert.match(sessionText, /nonce-42/);
  assert.ok(sessionText.length < 200);
  assert.equal(sessionText.includes('read_file'), false);
  assert.equal(sessionText.includes('"path"'), false);
});

test('stages system instructions before the short active-turn prompt', async () => {
  const h = harness();
  assert.equal((await h.send({ type: 'studio.arm', sessionId: 's1' })).supported, true);
  const reply = await h.send({ type: 'studio.stage', sessionId: 's1', systemText: 'static rules and schemas', text: 'Active turn: "nonce-42".' });
  assert.equal(reply.ok, true);
  assert.deepEqual(h.calls, [['system', 'static rules and schemas']]);
  assert.deepEqual(h.staged, ['Active turn: "nonce-42".']);
});

test('a rejected system-instructions editor leaves the prompt untouched', async () => {
  const h = harness({ system: async () => false });
  await h.send({ type: 'studio.arm', sessionId: 's1' });
  const reply = await h.send({ type: 'studio.stage', sessionId: 's1', systemText: 'rules', text: 'nonce' });
  assert.equal(reply.ok, false);
  assert.deepEqual(h.staged, []);
});

test('an existing prompt draft prevents any system or chat write', async () => {
  const h = harness({ editorEmpty: false });
  await h.send({ type: 'studio.arm', sessionId: 's1' });
  const reply = await h.send({ type: 'studio.stage', sessionId: 's1', systemText: 'rules', text: 'nonce' });
  assert.equal(reply.ok, false);
  assert.deepEqual(h.calls, []);
  assert.deepEqual(h.staged, []);
});

test('disarming while the system editor is pending prevents a later chat write', async () => {
  let finish;
  const h = harness({ system: () => new Promise(resolve => { finish = resolve; }) });
  await h.send({ type: 'studio.arm', sessionId: 's1' });
  const staging = h.send({ type: 'studio.stage', sessionId: 's1', systemText: 'rules', text: 'nonce' });
  while (!finish) await new Promise(resolve => setImmediate(resolve));
  await h.send({ type: 'studio.disarm', sessionId: 's1' });
  finish(true);
  assert.equal((await staging).ok, false);
  assert.deepEqual(h.staged, []);
});
