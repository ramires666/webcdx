const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const WebCodexProtocol = require('./protocol.js');

function harness() {
  const listeners = {}, sent = [], timers = new Map(), state = { supported: true, generating: false, editorEmpty: true, conversationKey: 'chat-1', latestModel: { text: 'old answer' }, latestUser: null };
  let timerId = 0, submitted = [], staged = [], observerCallback, accepted = true, sendHandler, disconnected = false, cleared = false, submitHandler;
  const adapter = {
    inspect: () => ({ ...state, latestModel: state.latestModel && { ...state.latestModel }, latestUser: state.latestUser && { ...state.latestUser } }),
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
    setTimeout: fn => { const id = ++timerId; timers.set(id, fn); return id; }, clearTimeout: id => timers.delete(id), setInterval: fn => { context.interval = fn; return 1; }, clearInterval: () => { cleared = true; }, Promise, JSON, String, Set, TextEncoder, structuredClone, WebCodexProtocol };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'content.js'), 'utf8'), context, { filename: 'content.js' });
  const send = message => new Promise(resolve => listeners.message(message, {}, response => resolve(structuredClone(response))));
  const flush = async () => { const pending = [...timers.values()]; timers.clear(); for (const fn of pending) fn(); await new Promise(resolve => setImmediate(resolve)); };
  return { state, sent, staged, submitted, send, flush, cleanups, get restores(){return restores;}, get annotations(){return annotations;}, setSubmitHandler: fn => { submitHandler = fn; }, tick: () => context.interval(), stopped: () => disconnected && cleared, reload: () => vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'content.js'), 'utf8'), context), mutate: () => observerCallback(), click: event => documentListeners.click(event), run: () => { const button={getAttribute:name=>name==='aria-label'?null:'',innerText:'Run\nkeyboard_return',textContent:''};documentListeners.click({target:{closest:()=>button}}); }, setAccepted: value => { accepted = value; }, setSendHandler: fn => { sendHandler = fn; } };
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

test('only a clicked, supported empty New chat produces a replacement snapshot', async () => {
  const h = harness(); await h.send({ type: 'studio.arm', sessionId: 's1' });
  const newChat = { getAttribute: name => name === 'aria-label' ? 'New chat' : '', innerText: 'add', querySelector: () => null };
  h.state.latestModel = null; h.mutate(); await h.flush();
  assert.equal(h.sent.some(m => m.conversationReplaced), false, 'virtualized model disappearance alone is ignored');
  h.click({ target: { closest: selector => selector.includes('a,') ? newChat : null } });
  h.state.supported = false; h.state.conversationKey = 'chat-2'; h.mutate(); await h.flush();
  assert.equal(h.sent.some(m => m.conversationReplaced), false, 'transient unsupported DOM does not rearm');
  h.tick();await h.flush();assert.equal(h.sent.some(m=>m.type==='studio.snapshot'&&!m.conversationReplaced),false,'pending transition does not pause on an unsupported intermediate snapshot');
  h.state.supported = true; h.state.latestModel = { text: 'saved conversation model' }; h.mutate(); await h.flush();
  assert.equal(h.sent.some(m => m.conversationReplaced), false, 'a saved route with a model is not treated as a new chat');
  h.state.latestModel = null; h.state.editorEmpty = true; h.mutate(); await h.flush();
  const replacements = h.sent.filter(m => m.conversationReplaced);
  assert.equal(replacements.length, 1); assert.equal(replacements[0].supported, true);
  await h.send({ type: 'studio.arm', sessionId: 's2' });
  h.mutate(); await h.flush();
  assert.equal(h.sent.filter(m => m.conversationReplaced).length, 1, 'the arm handshake clears the pending transition');
});

test('retries a failed replacement request until arm and prevents concurrent requests', async () => {
  const h = harness(); await h.send({ type: 'studio.arm', sessionId: 's1' });
  const newChat = { getAttribute: name => name === 'aria-label' ? 'New chat' : '', innerText: '', textContent: '', querySelector: () => null };
  h.click({ target: { closest: () => newChat } });
  h.state.latestModel = null;
  let finish;
  h.setSendHandler(message => message.conversationReplaced ? new Promise(resolve => { finish = resolve; }) : { ok: true, accepted: true });
  h.mutate(); await h.flush();
  h.mutate(); await h.flush();
  assert.equal(h.sent.filter(m => m.conversationReplaced).length, 1, 'only one transition request may be active');
  finish({ ok: true, accepted: false }); await new Promise(resolve => setImmediate(resolve));
  h.setSendHandler(message => ({ ok: true, accepted: true }));
  h.tick(); await h.flush();
  assert.equal(h.sent.filter(m => m.conversationReplaced).length, 2, 'a rejected request is retried');
  await h.send({ type: 'studio.arm', sessionId: 's2' });
  h.tick(); await h.flush();
  assert.equal(h.sent.filter(m => m.conversationReplaced).length, 2, 'arm ends retries');
});

test('allows blank clicked New chat while controls are pending but not while generation is active', async () => {
  const h = harness(); await h.send({ type: 'studio.arm', sessionId: 's1' });
  const newChat = { getAttribute: name => name === 'aria-label' ? 'New chat' : '', innerText: '', textContent: '' };
  h.click({ target: { closest: () => newChat } });
  h.state.latestModel = null; h.state.generating = true; h.state.controlsPending = true;
  h.mutate(); await h.flush();
  assert.equal(h.sent.filter(m => m.conversationReplaced).length, 1);

  const active = harness(); await active.send({ type: 'studio.arm', sessionId: 's1' });
  active.click({ target: { closest: () => newChat } });
  active.state.latestModel = null; active.state.generating = true; active.state.controlsPending = false;
  active.mutate(); await active.flush();
  assert.equal(active.sent.some(m => m.conversationReplaced), false);
});

test('reports a clicked blank New chat through hello when the prior session is unarmed', async () => {
  const h = harness(); h.setSendHandler(() => ({ ok: false, accepted: false }));
  h.tick(); await h.flush();
  h.setSendHandler(message => ({ ok: true, accepted: true }));
  const newChat = { getAttribute: name => name === 'aria-label' ? 'New chat' : '', innerText: '', textContent: '' };
  h.click({ target: { closest: () => newChat } });
  h.state.latestModel = null; h.mutate(); await h.flush();
  const transition = h.sent.find(message => message.type === 'studio.hello' && message.conversationReplaced);
  assert.ok(transition, 'unarmed content sends the replacement through studio.hello');
  assert.equal(transition.supported, true);
  await h.send({ type: 'studio.arm', sessionId: 's2' });
  h.state.latestModel = { text: 'new chat reply' }; h.mutate(); await h.flush();
  assert.equal(h.sent.some(message => message.type === 'studio.call' && message.text === 'new chat reply'), true);
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

test('turnless system-only stage preserves an occupied prompt', async () => {
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});h.state.editorEmpty=false;
  const result=await h.send({type:'studio.stage',sessionId:'s1',text:'',systemText:'instructions'});
  assert.equal(result.ok,true);assert.deepEqual(h.staged,[]);
});

test('a saved-address acknowledgement updates the local binding without losing the active response', async () => {
  const h=harness();h.state.conversationKey='/prompts/new_chat';
  await h.send({type:'studio.arm',sessionId:'s1',turnless:true});h.run();
  h.state.generating=true;h.mutate();await h.flush();
  h.state.conversationKey='/prompts/saved-chat';h.state.generating=false;h.state.modelAfterUser=true;
  h.state.latestModel={text:'```webcodex-call\n'+JSON.stringify({v:1,name:'tool',arguments:{}})+'\n```',element:{}};
  h.setSendHandler(msg=>({accepted:true,conversationKey:msg.type==='studio.snapshot'?h.state.conversationKey:undefined}));
  h.mutate();await h.flush();h.mutate();await h.flush();
  const calls=h.sent.filter(msg=>msg.type==='studio.call');
  assert.equal(calls.length,1);assert.equal(calls[0].conversationKey,'/prompts/saved-chat');
});

test('turnless accepts the same call once for each user and deduplicates rerenders', async () => {
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  const call='```webcodex-call\n'+JSON.stringify({v:1,name:'tool',arguments:{}})+'\n```';
  h.state.latestUser={text:'first'};h.state.modelAfterUser=true;h.run();h.state.generating=true;h.mutate();await h.flush();h.state.generating=false;h.state.latestModel={text:call,element:{}};h.mutate();await h.flush();h.mutate();await h.flush();
  h.state.latestUser={text:'second'};h.run();h.state.generating=true;h.mutate();await h.flush();h.state.generating=false;h.state.latestModel={text:call,element:{}};h.mutate();await h.flush();
  const calls=h.sent.filter(m=>m.type==='studio.call');assert.equal(calls.length,2);assert.notEqual(calls[0].responseKey,calls[1].responseKey);
});

test('turnless anchors identical user prompts to distinct delivered results', async () => {
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  const call='```webcodex-call\n'+JSON.stringify({v:1,name:'tool',arguments:{}})+'\n```';
  h.state.modelAfterUser=true;h.state.latestUser={text:JSON.stringify({type:'webcodex-result',operation_id:'op-1'})};h.run();h.state.generating=true;h.mutate();await h.flush();h.state.generating=false;h.state.latestModel={text:call,element:{}};h.mutate();await h.flush();h.mutate();await h.flush();
  h.state.latestUser={text:JSON.stringify({type:'webcodex-result',operation_id:'op-2'})};h.run();h.state.generating=true;h.mutate();await h.flush();h.state.generating=false;h.state.latestModel={text:call,element:{}};h.mutate();await h.flush();h.mutate();await h.flush();
  const calls=h.sent.filter(m=>m.type==='studio.call');assert.equal(calls.length,2);assert.equal(calls[0].precedingResultId,'op-1');assert.equal(calls[1].precedingResultId,'op-2');assert.notEqual(calls[0].responseKey,calls[1].responseKey);
});

test('turnless ignores a stale model before the new user result', async () => {
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  h.state.latestUser={text:'new request'};h.state.latestModel={text:'```webcodex-call\n'+JSON.stringify({v:1,name:'tool',arguments:{}})+'\n```'};h.state.modelAfterUser=false;h.mutate();await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
});

test('turnless history rerenders do not dispatch without a new Run', async () => {
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  h.state.latestUser={text:'scrolled history'};h.state.latestModel={text:'```webcodex-call\n'+JSON.stringify({v:1,name:'tool',arguments:{}})+'\n```',element:{}};h.state.modelAfterUser=true;h.mutate();await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
});

test('turnless Run does not replay a cloned copy of the previous model', async () => {
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});h.run();
  h.state.latestModel={text:'old answer',element:{}};h.mutate();await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
});

test('stopping a turnless response consumes its Run gate', async () => {
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});h.run();h.state.generating=true;h.mutate();await h.flush();
  const stop={getAttribute:name=>name==='aria-label'?'Stop':'',innerText:'',textContent:''};h.click({target:{closest:()=>stop}});
  h.state.generating=false;h.state.latestModel={text:'partial answer',element:{}};h.mutate();await h.flush();
  h.state.latestModel={text:'historical call',element:{}};h.mutate();await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
});

test('turnless recovery waits for delivered snapshot proof', async () => {
  const h=harness(),call='```webcodex-call\n'+JSON.stringify({v:1,name:'tool',arguments:{}})+'\n```';
  h.state.latestUser={text:'request'};h.state.latestModel={text:call};h.state.modelAfterUser=true;
  await h.send({type:'studio.arm',sessionId:'s1',turnless:true,recover:true,recoverTurn:'',toolNames:[{name:'tool',inputSchema:{type:'object',properties:{},additionalProperties:false}}],expectedConversationKey:'chat-1'});
  await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
  await h.send({type:'studio.snapshot',sessionId:'s1',operationId:'unproven'});await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
  await h.send({type:'studio.deliver',sessionId:'s1',text:JSON.stringify({type:'webcodex-result',operation_id:'proven'}),submit:true});
  await h.send({type:'studio.snapshot',sessionId:'s1',operationId:'proven'});await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,1);
});

test('turnless recovery proof clears recovery when the new call has not rendered yet', async () => {
  const h=harness(),call='```webcodex-call\n'+JSON.stringify({v:1,name:'tool',arguments:{}})+'\n```';
  h.state.latestUser={text:'request'};h.state.latestModel={text:'previous prose'};h.state.modelAfterUser=false;
  await h.send({type:'studio.arm',sessionId:'s1',turnless:true,recover:true,recoverTurn:'',toolNames:[{name:'tool',inputSchema:{type:'object',properties:{},additionalProperties:false}}],expectedConversationKey:'chat-1'});
  await h.send({type:'studio.deliver',sessionId:'s1',text:JSON.stringify({type:'webcodex-result',operation_id:'proof'}),submit:true});
  await h.send({type:'studio.snapshot',sessionId:'s1',operationId:'proof'});
  h.state.latestModel={text:call};h.state.modelAfterUser=true;h.mutate();await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
  h.run();h.state.generating=true;h.mutate();await h.flush();h.state.generating=false;h.state.latestModel={text:call,element:{}};h.mutate();await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,1);
});
