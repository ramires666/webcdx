const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const WebCodexProtocol = require('./protocol.js');

function harness() {
  const listeners = {}, sent = [], timers = new Map(), state = { supported: true, generating: false, editorEmpty: true, conversationKey: 'chat-1', latestModel: { text: 'old answer' } };
  let timerId = 0, submitted = [], staged = [], observerCallback, accepted = true, sendHandler, disconnected = false, cleared = false, submitHandler;
  const adapter = {
    inspect: () => ({ ...state, latestModel: state.latestModel && { ...state.latestModel } }),
    stage: text => { staged.push(text); return state.editorEmpty && !state.generating; }, stageSystemInstructions: async () => true,
    submit: async (text, isCurrent) => { if(submitHandler)return submitHandler(text,isCurrent);if(!state.editorEmpty)return false;submitted.push(text);return true; },
    hasDelivery: operationId => submitted.some(text => JSON.parse(text).operation_id === operationId),
    cleanup: options => cleanups.push(options), restoreCleanup: () => { restores++; }, annotateContext: () => { annotations++; }
  };
  const cleanups=[];let restores=0,annotations=0;
  const chrome = { runtime: { sendMessage: async message => { sent.push(structuredClone(message)); return sendHandler ? sendHandler(message) : { ok: accepted, accepted }; }, onMessage: { addListener: fn => { listeners.message = fn; } } } };
  const documentListeners = {};
  const document = { addEventListener(type, fn) { documentListeners[type] = fn; }, documentElement: {} };
  const window = {}; window.top = window;
  class MutationObserver { constructor(callback) { observerCallback = callback; } observe() {} disconnect() { disconnected = true; } }
  const context = { window, location: new URL('https://aistudio.google.com/app/prompts/one'), document, chrome, WebCodexStudio: { createAdapter: () => adapter }, MutationObserver,
    setTimeout: fn => { const id = ++timerId; timers.set(id, fn); return id; }, clearTimeout: id => timers.delete(id), setInterval: fn => { context.interval = fn; return 1; }, clearInterval: () => { cleared = true; }, Promise, JSON, String, Set, structuredClone, WebCodexProtocol };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'content.js'), 'utf8'), context, { filename: 'content.js' });
  const send = message => new Promise(resolve => listeners.message(message, {}, response => resolve(structuredClone(response))));
  const flush = async () => { const pending = [...timers.values()]; timers.clear(); for (const fn of pending) fn(); await new Promise(resolve => setImmediate(resolve)); };
  return { state, sent, staged, submitted, send, flush, cleanups, get restores(){return restores;}, get annotations(){return annotations;}, setSubmitHandler: fn => { submitHandler = fn; }, tick: () => context.interval(), stopped: () => disconnected && cleared, reload: () => vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'content.js'), 'utf8'), context), mutate: () => observerCallback(), click: event => documentListeners.click(event), setAccepted: value => { accepted = value; }, setSendHandler: fn => { sendHandler = fn; } };
}

test('delivery waits for async Run readiness and aborts after disarm', async () => {
  const h = harness();await h.send({type:'studio.arm',sessionId:'s1'});
  let finish;
  h.setSubmitHandler((_text,isCurrent)=>new Promise(resolve=>{finish=()=>resolve(isCurrent());}));
  const text=JSON.stringify({type:'webcodex-result',operation_id:'op-async'});
  let replied=false;
  const delivery=h.send({type:'studio.deliver',sessionId:'s1',text,submit:true}).then(reply=>{replied=true;return reply;});
  await h.flush();assert.equal(replied,false);
  const duplicate=await h.send({type:'studio.deliver',sessionId:'s1',text,submit:true});
  assert.equal(duplicate.attempted,true);
  await h.send({type:'studio.disarm',sessionId:'s1'});
  finish();assert.equal((await delivery).attempted,false);
});

test('extension reload retires the stale content observer and timer without throwing', async () => {
  const h = harness();
  await h.send({ type: 'studio.arm', sessionId: 's1' });
  h.setSendHandler(() => { throw Error('Extension context invalidated.'); });
  h.tick(); await h.flush();
  assert.equal(h.stopped(), true);
  const sent = h.sent.length;
  h.tick(); await h.flush();
  assert.equal(h.sent.length, sent);
});

test('reinjection does not reset a live session or duplicate observers', async () => {
  const h = harness();
  await h.send({ type: 'studio.arm', sessionId: 's1' });
  h.reload();
  assert.equal(h.sent.filter(m => m.type === 'studio.hello').length, 1);
  h.state.latestModel = { text: 'new reply after reinjection' };
  h.mutate(); await h.flush();
  assert.equal(h.sent.filter(m => m.type === 'studio.call').length, 1);
});

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

test('recovery arm rejects another conversation without replacing the active session',async()=>{
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1'});h.state.conversationKey='chat-2';
  const rejected=await h.send({type:'studio.arm',sessionId:'s2',recover:true,expectedConversationKey:'chat-1',recoverTurn:'next',toolNames:[]});
  assert.equal(rejected.supported,false);
  assert.equal((await h.send({type:'studio.snapshot',sessionId:'s1'})).conversationKey,'chat-2');
  assert.equal((await h.send({type:'studio.snapshot',sessionId:'s2'})).error,'Stale session');
});

test('recovery observes only the advertised valid turn and retries until accepted',async()=>{
  const h=harness();h.state.latestModel={text:'old rendered call'};
  await h.send({type:'studio.arm',sessionId:'s1',recover:true,recoverTurn:'turn-2',toolNames:[{name:'read_file',inputSchema:{type:'object',properties:{path:{type:'string'}},required:['path'],additionalProperties:false}}],expectedConversationKey:'chat-1'});
  await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
  const valid='```webcodex-call\n'+JSON.stringify({v:1,turn:'turn-2',name:'read_file',arguments:{path:'x'}})+'\n```';
  h.state.latestModel={text:valid};h.setAccepted(false);h.mutate();await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,1);
  h.setAccepted(true);h.mutate();await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,2);
});

test('delivered recovery snapshot keeps an already rendered next turn observable',async()=>{
  const h=harness(),tools=[{name:'read_file',inputSchema:{type:'object',properties:{path:{type:'string'}},required:['path'],additionalProperties:false}}];
  const old='```webcodex-call\n'+JSON.stringify({v:1,turn:'old-turn',name:'read_file',arguments:{path:'old'}})+'\n```';
  const next='```webcodex-call\n'+JSON.stringify({v:1,turn:'next-turn',name:'read_file',arguments:{path:'new'}})+'\n```';
  h.state.latestModel={text:old};
  await h.send({type:'studio.arm',sessionId:'s1',recover:true,recoverTurn:'next-turn',toolNames:tools,expectedConversationKey:'chat-1'});
  await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
  await h.send({type:'studio.deliver',sessionId:'s1',text:JSON.stringify({type:'webcodex-result',operation_id:'op-recover'}),submit:true});
  h.state.latestModel={text:next};
  assert.equal((await h.send({type:'studio.snapshot',sessionId:'s1',operationId:'op-recover'})).delivered,true);
  await h.flush();
  const calls=h.sent.filter(m=>m.type==='studio.call');
  assert.equal(calls.length,1);assert.equal(calls[0].text,next);assert.equal(calls.some(m=>m.text===old),false);
  h.mutate();await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,1);
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

test('only a clicked, supported empty New chat produces one conversation replacement snapshot', async () => {
  const h = harness(); await h.send({ type: 'studio.arm', sessionId: 's1' });
  const newChat = { getAttribute: name => name === 'aria-label' ? 'New chat' : '', innerText: 'add', querySelector: () => null };
  h.state.latestModel = null; h.mutate(); await h.flush();
  assert.equal(h.sent.some(m => m.conversationReplaced), false, 'virtualized model disappearance alone is ignored');
  h.click({ target: { closest: () => newChat } });
  h.state.supported = false; h.state.conversationKey = 'chat-2'; h.mutate(); await h.flush();
  assert.equal(h.sent.some(m => m.conversationReplaced), false, 'transient unsupported DOM does not rearm');
  h.tick();await h.flush();assert.equal(h.sent.some(m=>m.type==='studio.snapshot'&&!m.conversationReplaced),false,'pending transition does not pause on an unsupported intermediate snapshot');
  h.state.supported = true; h.state.latestModel = { text: 'saved conversation model' }; h.mutate(); await h.flush();
  assert.equal(h.sent.some(m => m.conversationReplaced), false, 'a saved route with a model is not treated as a new chat');
  h.state.latestModel = null; h.state.editorEmpty = true; h.mutate(); await h.flush();
  const replacements = h.sent.filter(m => m.conversationReplaced);
  assert.equal(replacements.length, 1); assert.equal(replacements[0].supported, true);
  h.mutate(); await h.flush();
  assert.equal(h.sent.filter(m => m.conversationReplaced).length, 1, 'same URL blank New chat is reported once');
});

test('cleanup annotates final answers, hides accepted service turns, and leaves context visible when hiding is off', async () => {
  const h=harness();const turn={};
  await h.send({type:'studio.arm',sessionId:'s1',hideServiceMessages:true});
  h.state.latestModel={text:'```webcodex-call\n{}\n```',element:turn};h.setAccepted(false);h.mutate();await h.flush();
  await h.send({type:'studio.cleanup',sessionId:'s1',enabled:true,final:false,operationIds:[]});
  assert.equal(h.cleanups.length,0);assert.equal(h.annotations,0);
  await h.send({type:'studio.cleanup',sessionId:'s1',enabled:true,final:true,operationIds:['op-1']});
  assert.equal(h.cleanups.length,1);assert.deepEqual(Array.from(h.cleanups[0].turns),[]);assert.deepEqual(h.cleanups[0].operationIds,['op-1']);assert.equal(h.annotations,1);
  h.setAccepted(true);h.mutate();await h.flush();
  await h.send({type:'studio.cleanup',sessionId:'s1',enabled:true,final:true,operationIds:[]});
  assert.deepEqual(Array.from(h.cleanups[1].turns),[turn]);
  await h.send({type:'studio.cleanup',sessionId:'s1',enabled:false,final:true,operationIds:[]});
  assert.equal(h.restores,1);assert.equal(h.annotations,3);assert.equal(h.cleanups.length,2);
});
