const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const WebCodexProtocol = require('./protocol.js');
const unwrapResult = text => {
  const match = /^```helper-result\n([\s\S]*)\n```$/.exec(text);
  return match ? match[1] : text;
};

function harness({manualClock=false}={}) {
  const listeners = {}, sent = [], timers = new Map(), state = { supported: true, generating: false, editorEmpty: true, conversationKey: 'active-chat', latestModel: { text: 'old answer' }, latestUser: null };
  let timerId = 0, now=10000, submitted = [], staged = [], observerCallback, accepted = true, sendHandler, disconnected = false, cleared = false, submitHandler;
  class TestDate extends Date {static now(){return now;}}
  const adapter = {
    inspect: () => ({ ...state, latestModel: state.latestModel && { ...state.latestModel }, latestUser: state.latestUser && { ...state.latestUser } }),
    stage: text => { staged.push(text); return state.editorEmpty && !state.generating; }, stageSystemInstructions: async () => true,
    submit: async (text, isCurrent) => { if(submitHandler)return submitHandler(text,isCurrent);if(!state.editorEmpty)return false;submitted.push(text);return true; },
    hasDelivery: (operationId, expectedText) => submitted.some(text => {
      const raw = unwrapResult(text);
      return JSON.parse(raw).operation_id === operationId && (expectedText === undefined || raw === expectedText);
    }),
    cleanup: options => cleanups.push(options), restoreCleanup: () => { restores++; }, annotateContext: () => { annotations++; }
  };
  const cleanups=[];let restores=0,annotations=0;
  const chrome = { runtime: { sendMessage: async message => { sent.push(structuredClone(message)); return sendHandler ? sendHandler(message) : { ok: accepted, accepted }; }, onMessage: { addListener: fn => { listeners.message = fn; } } } };
  const documentListeners = {};
  const document = { addEventListener(type, fn) { documentListeners[type] = fn; }, documentElement: {} };
  const window = {}; window.top = window;
  class MutationObserver { constructor(callback) { observerCallback = callback; } observe() {} disconnect() { disconnected = true; } }
  const context = { window, location: new URL('https://aistudio.google.com/app/prompts/one'), URL, document, chrome, WebCodexStudio: { createAdapter: () => adapter }, MutationObserver,
    setTimeout: (fn,ms=0) => { const id = ++timerId; timers.set(id, {fn,at:now+ms}); return id; }, clearTimeout: id => timers.delete(id), setInterval: fn => { context.interval = fn; return 1; }, clearInterval: () => { cleared = true; }, Promise, JSON, String, Set, Date:TestDate, TextEncoder, structuredClone, crypto:{subtle:{digest:async(_algorithm,data)=>require('node:crypto').createHash('sha256').update(data).digest()}}, WebCodexProtocol };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'content.js'), 'utf8'), context, { filename: 'content.js' });
  const send = message => new Promise(resolve => listeners.message(message, {}, response => resolve(structuredClone(response))));
  const flush = async () => {
    for(let round=0;round<20;round++){
      await new Promise(resolve=>setImmediate(resolve));
      if(!timers.size)return;
      if(!manualClock)now=Math.max(now,Math.min(...[...timers.values()].map(timer=>timer.at)));
      const due=[...timers].filter(([,timer])=>timer.at<=now);if(!due.length)return;
      for(const [id,timer]of due){timers.delete(id);timer.fn();}
    }
    throw Error('timers did not settle');
  };
  return { state, sent, staged, submitted, send, flush, advance:ms=>{now+=ms;}, cleanups, get restores(){return restores;}, get annotations(){return annotations;}, setSubmitHandler: fn => { submitHandler = fn; }, tick: () => context.interval(), stopped: () => disconnected && cleared, reload: () => vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'content.js'), 'utf8'), context), mutate: () => observerCallback(), click: event => documentListeners.click(event), run: () => { const button={getAttribute:name=>name==='aria-label'?null:'',innerText:'Run\nkeyboard_return',textContent:''};documentListeners.click({target:{closest:()=>button}}); }, setAccepted: value => { accepted = value; }, setSendHandler: fn => { sendHandler = fn; } };
}

test('delivery waits for async Run readiness and aborts after disarm', async () => {
  const h = harness();await h.send({type:'studio.arm',sessionId:'s1'});
  let finish;
  h.setSubmitHandler((_text,isCurrent)=>new Promise(resolve=>{finish=()=>resolve(isCurrent());}));
  const text=JSON.stringify({type:'helper-result',operation_id:'op-async'});
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

test('a retired extension context can be reinjected and arm a working session', async () => {
  const h = harness();
  await h.send({ type: 'studio.arm', sessionId: 'old-session' });
  h.setSendHandler(() => { throw Error('Extension context invalidated.'); });
  h.tick(); await h.flush();
  h.setSendHandler(() => ({ ok: true, accepted: true }));
  h.reload(); await h.flush();
  assert.equal(h.sent.filter(message => message.type === 'studio.hello').length, 2, 'the new context must install its listener and announce itself');
  await h.send({ type: 'studio.arm', sessionId: 'new-session' });
  h.state.latestModel = { text: 'a fresh reply' }; h.mutate(); await h.flush();
  assert.equal(h.sent.filter(message => message.type === 'studio.call' && message.sessionId === 'new-session').length, 1);
});

test('detects a stable empty new chat from cleared user and model turns without a labelled click', async () => {
  const h = harness();
  h.state.latestUser = { text: 'previous user message' };
  await h.send({ type: 'studio.arm', sessionId: 'old-session' });
  h.state.latestModel = null;
  h.mutate(); await h.flush();
  h.tick(); await h.flush();
  assert.equal(h.sent.some(message => message.conversationReplaced), false, 'a virtualized model with its user turn still present is not a new chat');
  h.state.latestUser = null;
  h.mutate(); await h.flush();
  assert.equal(h.sent.some(message => message.conversationReplaced), false, 'one transient empty render must not reset the session');
  h.tick(); await h.flush();
  assert.equal(h.sent.filter(message => message.conversationReplaced).length, 1);
});

test('recognizes an icon-bearing New chat control without an aria label even before any conversation',async()=>{
  const h=harness();h.state.latestModel=null;
  await h.send({type:'studio.arm',sessionId:'s1'});
  const button={getAttribute:()=>null,innerText:'add\nNew chat',textContent:'addNew chat',cloneNode:()=>({innerText:'New chat',querySelectorAll:()=>[]})};
  h.click({target:{closest:()=>button}});h.mutate();await h.flush();
  assert.equal(h.sent.filter(message=>message.conversationReplaced).length,1);
});

test('selecting saved history checks system instructions once without executing the old tool call',async()=>{
  const h=harness();
  h.state.historyAnchor={};h.state.historyStart='original first message';h.state.latestUser={text:'old user'};
  await h.send({type:'studio.arm',sessionId:'saved-session',turnless:true});
  h.state.historyAnchor={};h.state.historyStart='saved chat first message';
  h.click({target:{closest:()=>({getAttribute:name=>name==='href'?'/prompts/saved-chat':null})}});
  const oldCall='```helper-call\n{"v":1,"name":"read_file","arguments":{"path":"old.txt"}}\n```';
  h.state.latestModel={text:oldCall};h.state.latestUser={text:'saved user'};
  h.mutate();await h.flush();h.tick();await h.flush();
  assert.equal(h.sent.filter(message=>message.type==='studio.ensureInstructions').length,1);
  assert.equal(h.sent.some(message=>message.type==='studio.call'),false);
  h.tick();await h.flush();
  assert.equal(h.sent.filter(message=>message.type==='studio.ensureInstructions').length,1,'unchanged saved history is not checked repeatedly');
  h.run();h.state.latestUser={text:'new request'};h.state.latestModel={text:'fresh model answer'};
  h.mutate();await h.flush();
  assert.equal(h.sent.filter(message=>message.type==='studio.call').length,1);
  assert.equal(h.sent.find(message=>message.type==='studio.call').text,'fresh model answer');
});

test('rerendered and virtualized history after delivery cannot restage instructions or consume the next call',async()=>{
  const h=harness();h.state.historyAnchor={};h.state.historyStart='original task';
  await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  const result={type:'helper-result',operation_id:'op-directory',result:{path:'W:\\',entries:[]}};
  await h.send({type:'studio.deliver',sessionId:'s1',text:JSON.stringify(result),submit:true});
  h.state.historyAnchor={};h.state.historyStart='a different first visible turn after virtualization';
  h.state.latestUser={text:JSON.stringify(result)};h.state.modelAfterUser=true;
  const call=JSON.stringify({v:1,name:'list_directory',arguments:{path:'W:\\_python'}});
  h.state.latestModel={text:call,element:{}};
  h.mutate();await h.flush();h.tick();await h.flush();h.mutate();await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.ensureInstructions').length,0,'DOM node identity and first visible text are not chat-navigation evidence');
  assert.deepEqual(h.sent.filter(m=>m.type==='studio.call').map(m=>m.text),[call]);
});

test('ordinary links, fragment links and saved chats opened in another tab do not reinitialize the active run',async()=>{
  for(const [href,attrs,modifiers]of [['https://example.test/docs',{},{}],['#section',{},{}],['/prompts/other',{target:'_blank'},{}],['/prompts/other',{}, {ctrlKey:true}]]){
    const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
    h.run();h.state.latestUser={text:'read'};h.state.modelAfterUser=true;h.state.latestModel={text:'new answer'};
    const link={getAttribute:name=>name==='href'?href:attrs[name]??null};
    h.click({...modifiers,target:{closest:()=>link}});h.mutate();await h.flush();h.tick();await h.flush();
    assert.equal(h.sent.filter(m=>m.type==='studio.ensureInstructions').length,0,href);
    assert.equal(h.sent.filter(m=>m.type==='studio.call').length,1,href);
  }
});

test('temporary empty virtualization while awaiting an automatic followup never announces a new chat',async()=>{
  const h=harness();h.state.latestUser={text:'request'};
  await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  const result={type:'helper-result',operation_id:'op-virtualized'};
  await h.send({type:'studio.deliver',sessionId:'s1',text:JSON.stringify(result),submit:true});
  h.state.latestUser=null;h.state.latestModel=null;h.mutate();await h.flush();h.tick();await h.flush();h.tick();await h.flush();
  assert.equal(h.sent.some(m=>m.conversationReplaced),false);
  h.state.latestUser={text:JSON.stringify(result)};h.state.latestModel={text:'next reply'};h.state.modelAfterUser=true;
  h.mutate();await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,1);
});

test('a new Run during an instruction check retains its response gate',async()=>{
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  let finish;
  h.setSendHandler(message=>message.type==='studio.ensureInstructions'?new Promise(resolve=>{finish=resolve;}):{accepted:true});
  h.click({target:{closest:()=>({getAttribute:name=>name==='href'?'/prompts/saved':null})}});
  h.state.latestUser={text:'historical user'};h.state.latestModel={text:'historical reply'};h.mutate();await h.flush();h.tick();await h.flush();
  assert.equal(typeof finish,'function');
  h.run();h.state.latestUser={text:'new task'};h.state.latestModel={text:'new answer'};h.state.modelAfterUser=true;
  finish({accepted:true});h.setSendHandler(null);await h.flush();h.tick();await h.flush();h.tick();await h.flush();
  assert.deepEqual(h.sent.filter(m=>m.type==='studio.call').map(m=>m.text),['new answer']);
});

test('navigation cancels an in-progress result submit before its click',async()=>{
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  let finish;
  h.setSubmitHandler((_text,isCurrent)=>new Promise(resolve=>{finish=()=>resolve(isCurrent());}));
  const delivery=h.send({type:'studio.deliver',sessionId:'s1',text:JSON.stringify({type:'helper-result',operation_id:'op-before-nav'}),submit:true});
  await h.flush();h.click({target:{closest:()=>({getAttribute:name=>name==='href'?'/prompts/another':null})}});
  finish();assert.equal((await delivery).ok,false);
  assert.equal(h.submitted.length,0);
});

test('an icon-only new-chat link starts the blank-chat flow without a saved-history check',async()=>{
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  h.click({target:{closest:()=>({getAttribute:name=>name==='href'?'/prompts/new_chat':null})}});
  h.state.latestUser=null;h.state.latestModel=null;h.mutate();await h.flush();
  assert.equal(h.sent.filter(m=>m.conversationReplaced).length,1);
  assert.equal(h.sent.filter(m=>m.type==='studio.ensureInstructions').length,0);
});

test('a legacy result still anchors one fresh response after migration to Helper',async()=>{
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  const result={type:'webcodex-result',operation_id:'legacy-operation'};
  assert.equal((await h.send({type:'studio.deliver',sessionId:'s1',text:JSON.stringify(result),submit:true})).ok,true);
  h.state.latestUser={text:JSON.stringify(result)};h.state.modelAfterUser=true;
  h.state.latestModel={text:'```webcodex-call\n{"v":1,"name":"list_directory","arguments":{"path":"W:\\\\_python"}}\n```'};
  h.mutate();await h.flush();
  const calls=h.sent.filter(m=>m.type==='studio.call');assert.equal(calls.length,1);assert.equal(calls[0].precedingResultId,'legacy-operation');
});

test('explicit capture accepts only the latest selected call and consumes its automatic observation',async()=>{
  const h=harness(),tool={name:'list_directory',inputSchema:{type:'object',properties:{path:{type:'string'}},required:['path'],additionalProperties:false}};
  await h.send({type:'studio.arm',sessionId:'s1',turnless:true});h.run();
  h.state.latestModel={text:JSON.stringify({v:1,name:'list_directory',arguments:{path:'W:\\_python'}})};
  const captured=await h.send({type:'studio.captureCall',sessionId:'s1',tools:[tool]});assert.equal(captured.ok,true);
  h.mutate();await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0,'explicit dispatch must not race automatic observation');
  assert.equal((await h.send({type:'studio.captureCall',sessionId:'s1',tools:[]})).ok,false);
});

test('ignores history and sends one completed new reply as a call', async () => {
  const h = harness();
  assert.deepEqual(await h.send({ type: 'studio.arm', sessionId: 's1' }), { supported: true, generating: false, editorEmpty: true, conversationKey: 'active-chat', error: undefined, deliveredOperationId: '', delivered: undefined });
  h.state.latestModel = { text: 'new answer' }; h.state.generating = true; h.mutate(); await h.flush();
  assert.equal(h.sent.some(m => m.type === 'studio.call'), false);
  h.state.generating = false; h.mutate(); await h.flush();
  const calls = h.sent.filter(m => m.type === 'studio.call');
  assert.equal(calls.length, 1); assert.equal(calls[0].text, 'new answer'); assert.equal(calls[0].generating, false);
  h.mutate(); await h.flush(); assert.equal(h.sent.filter(m => m.type === 'studio.call').length, 1);
});

test('idle controls cannot submit a transient incomplete JSON frame',async()=>{
  const h=harness({manualClock:true});await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  h.state.latestUser={text:'Read the file'};h.state.modelAfterUser=true;h.run();
  const complete=JSON.stringify({v:1,name:'read_file',arguments:{path:'W:\\_python\\pacman\\kimik3-pacman\\pacman.html'}});
  h.state.latestModel={text:complete.slice(0,-7)};h.mutate();h.advance(250);await h.flush();
  h.advance(7999);h.tick();await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0,'no truncated JSON reaches background validation during the render grace period');
  h.state.latestModel={text:complete};h.mutate();h.advance(250);await h.flush();
  h.advance(999);await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
  h.advance(1);await h.flush();
  assert.deepEqual(h.sent.filter(m=>m.type==='studio.call').map(m=>m.text),[complete]);
  h.advance(2000);h.tick();await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,1);
});

test('a valid interim command is rechecked after the source text stops changing',async()=>{
  const h=harness({manualClock:true});await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  h.state.latestUser={text:'Read'};h.state.modelAfterUser=true;h.run();
  const call=JSON.stringify({v:1,name:'read_file',arguments:{path:'first.txt'}});
  h.state.latestModel={text:call,sourceText:'Reading.\n'+call};h.mutate();h.advance(250);await h.flush();
  h.advance(900);h.state.latestModel.sourceText+='\nOne more note.';h.mutate();h.advance(100);await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0,'the old deadline must inspect the latest source, even before its debounce runs');
  const final=JSON.stringify({v:1,name:'read_file',arguments:{path:'final.txt'}});
  h.state.latestModel={text:final,sourceText:'Reading.\n'+final};h.mutate();h.advance(250);await h.flush();
  h.advance(1000);await h.flush();
  assert.deepEqual(h.sent.filter(m=>m.type==='studio.call').map(m=>m.text),[final]);
});

test('resumed generation cancels the idle deadline and starts a new quiet period',async()=>{
  const h=harness({manualClock:true});await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  h.state.latestUser={text:'Read'};h.state.modelAfterUser=true;h.run();
  h.state.latestModel={text:JSON.stringify({v:1,name:'read_file',arguments:{path:'safe.txt'}})};
  h.mutate();h.advance(250);await h.flush();h.advance(900);
  h.state.generating=true;h.mutate();h.advance(5000);await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
  h.state.generating=false;h.mutate();h.advance(250);await h.flush();
  h.advance(999);await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
  h.advance(1);await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,1);
});

test('a permanently damaged JSON response is reported once after its quiet grace period',async()=>{
  const h=harness({manualClock:true});await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  h.state.latestUser={text:'Read'};h.state.modelAfterUser=true;h.run();
  h.state.latestModel={text:'{"v":1,"name":"read_file","arguments":{"path":"broken'};
  h.mutate();h.advance(250);await h.flush();h.advance(7999);await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
  h.advance(1);await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,1);
  h.advance(2000);h.tick();await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,1);
});

test('disarm and Stop cancel pending reply timers without submitting the candidate',async()=>{
  for(const stop of [false,true]){
    const h=harness({manualClock:true});await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
    h.state.latestUser={text:'Read'};h.state.modelAfterUser=true;h.run();
    h.state.latestModel={text:JSON.stringify({v:1,name:'read_file',arguments:{path:'safe.txt'}})};
    h.mutate();h.advance(250);await h.flush();
    if(stop){h.state.generating=true;h.click({target:{closest:()=>({getAttribute:()=>'',innerText:'Stop',querySelector:()=>null})}});h.state.generating=false;}
    else await h.send({type:'studio.disarm',sessionId:'s1'});
    h.advance(10000);h.tick();await h.flush();
    assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
  }
});

test('safe draft, submitted result, delivery confirmation and disarm', async () => {
  const h = harness(); await h.send({ type: 'studio.arm', sessionId: 's1' });
  const text = JSON.stringify({ type: 'helper-result', v: 1, turn: 't1', next_turn: 't2', operation_id: 'op-7', name: 'read_file', result: {} });
  const draft = await h.send({ type: 'studio.deliver', sessionId: 's1', text, submit: false });
  assert.equal(draft.ok, true); assert.equal(draft.attempted, false); assert.equal(h.submitted.length, 0);
  const delivered = await h.send({ type: 'studio.deliver', sessionId: 's1', text, submit: true });
  assert.equal(delivered.ok, true); assert.equal(delivered.attempted, true); assert.equal(delivered.confirmed, true); assert.equal(h.submitted.length, 1);
  const duplicate = await h.send({ type: 'studio.deliver', sessionId: 's1', text, submit: true });
  assert.equal(duplicate.attempted, true); assert.equal(h.submitted.length, 1);
  assert.equal(h.submitted[0], `\`\`\`helper-result\n${text}\n\`\`\``);
  assert.deepEqual(await h.send({ type: 'studio.snapshot', sessionId: 's1', operationId: 'op-7' }), { supported: true, generating: false, editorEmpty: true, conversationKey: 'active-chat', error: undefined, deliveredOperationId: '', delivered: true });
  assert.deepEqual(await h.send({ type: 'studio.disarm', sessionId: 's1' }), { ok: true });
});

test('result payload stays raw inside the submitted and staged code fence', async () => {
  const h = harness(); await h.send({ type: 'studio.arm', sessionId: 's1' });
  const text = JSON.stringify({ type: 'helper-result', operation_id: 'op-escaped', path: 'C:\\Users\\thunder\\file.json', innercontent: 'He said "hello"\\nthen left', code: '```html\n<div>sample</div>\n```' });
  const expected = `\`\`\`helper-result\n${text}\n\`\`\``;
  assert.equal((await h.send({ type: 'studio.deliver', sessionId: 's1', text, submit: false })).ok, true);
  assert.equal(h.staged[0], expected);
  const delivered = await h.send({ type: 'studio.deliver', sessionId: 's1', text, submit: true });
  assert.equal(delivered.ok, true);
  assert.equal(h.submitted[0], expected);
  assert.equal(JSON.parse(unwrapResult(h.submitted[0])).path, 'C:\\Users\\thunder\\file.json');
  assert.equal(JSON.parse(unwrapResult(h.submitted[0])).innercontent, 'He said "hello"\\nthen left');
  assert.equal(JSON.parse(unwrapResult(h.submitted[0])).code, '```html\n<div>sample</div>\n```');
  assert.equal(h.submitted[0].split('\n').length,3,'JSON.stringify keeps embedded newlines escaped inside the result line');
  assert.equal((await h.send({ type: 'studio.deliver', sessionId: 's1', text, submit: true })).attempted, true);
  assert.equal(h.submitted.length, 1);
});

test('blocks draft when editor is not safe and reports an unsupported page', async () => {
  const h = harness(); await h.send({ type: 'studio.arm', sessionId: 's1' });
  h.state.editorEmpty = false;
  const result = await h.send({ type: 'studio.deliver', sessionId: 's1', text: '{}', submit: true });
  assert.equal(result.ok, false); assert.equal(result.attempted, false); assert.equal(h.submitted.length, 0);
  h.state.supported = false; h.mutate(); await h.flush();
  assert.equal(h.sent.some(m => m.type === 'studio.snapshot' && m.supported === false), true);
  assert.equal(h.sent.some(m => m.type === 'studio.call'), false);
});

test('reobserves a completed call after background rejects it while paused', async () => {
  const h = harness(); await h.send({ type: 'studio.arm', sessionId: 's1' });
  h.state.latestModel = { text: 'new answer' }; h.setAccepted(false); h.mutate(); await h.flush();
  assert.equal(h.sent.filter(m => m.type === 'studio.call').length, 1);
  h.setAccepted(true); h.mutate(); await h.flush();
  assert.equal(h.sent.filter(m => m.type === 'studio.call').length, 2);
});

test('recovery ignores legacy URL keys and binds to the active tab',async()=>{
  const h=harness();h.state.conversationKey='active-chat';
  const armed=await h.send({type:'studio.arm',sessionId:'s2',recover:true,expectedConversationKey:'/prompts/saved-chat',recoverTurn:'next',toolNames:[]});
  assert.equal(armed.supported,true);
  assert.equal((await h.send({type:'studio.snapshot',sessionId:'s2'})).conversationKey,'active-chat');
});

test('recovery observes only the advertised valid turn and retries until accepted',async()=>{
  const h=harness();h.state.latestModel={text:'old rendered call'};
  await h.send({type:'studio.arm',sessionId:'s1',recover:true,recoverTurn:'turn-2',toolNames:[{name:'read_file',inputSchema:{type:'object',properties:{path:{type:'string'}},required:['path'],additionalProperties:false}}],expectedConversationKey:'chat-1'});
  await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
  const valid='```helper-call\n'+JSON.stringify({v:1,turn:'turn-2',name:'read_file',arguments:{path:'x'}})+'\n```';
  h.state.latestModel={text:valid};h.setAccepted(false);h.mutate();await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,1);
  h.setAccepted(true);h.mutate();await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,2);
});

test('delivered recovery snapshot keeps an already rendered next turn observable',async()=>{
  const h=harness(),tools=[{name:'read_file',inputSchema:{type:'object',properties:{path:{type:'string'}},required:['path'],additionalProperties:false}}];
  const old='```helper-call\n'+JSON.stringify({v:1,turn:'old-turn',name:'read_file',arguments:{path:'old'}})+'\n```';
  const next='```helper-call\n'+JSON.stringify({v:1,turn:'next-turn',name:'read_file',arguments:{path:'new'}})+'\n```';
  h.state.latestModel={text:old};
  await h.send({type:'studio.arm',sessionId:'s1',recover:true,recoverTurn:'next-turn',toolNames:tools,expectedConversationKey:'chat-1'});
  await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
  await h.send({type:'studio.deliver',sessionId:'s1',text:JSON.stringify({type:'helper-result',operation_id:'op-recover'}),submit:true});
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
  h.state.supported = false; h.mutate(); await h.flush();
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
  h.state.latestModel={text:'```helper-call\n{}\n```',element:turn};h.setAccepted(false);h.mutate();await h.flush();
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

test('active tab binding stays stable while a chat is saved and the response arrives', async () => {
  const h=harness();h.state.conversationKey='active-chat';
  await h.send({type:'studio.arm',sessionId:'s1',turnless:true});h.run();
  h.state.generating=true;h.mutate();await h.flush();
  h.state.generating=false;h.state.modelAfterUser=true;
  h.state.latestModel={text:'```helper-call\n'+JSON.stringify({v:1,name:'tool',arguments:{}})+'\n```',element:{}};
  h.mutate();await h.flush();h.mutate();await h.flush();
  const calls=h.sent.filter(msg=>msg.type==='studio.call');
  assert.equal(calls.length,1);assert.equal(calls[0].conversationKey,'active-chat');
});

test('turnless accepts the same call once for each user and deduplicates rerenders', async () => {
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  const call='```helper-call\n'+JSON.stringify({v:1,name:'tool',arguments:{}})+'\n```';
  h.state.latestUser={text:'first'};h.state.modelAfterUser=true;h.run();h.state.generating=true;h.mutate();await h.flush();h.state.generating=false;h.state.latestModel={text:call,element:{}};h.mutate();await h.flush();h.mutate();await h.flush();
  h.state.latestUser={text:'second'};h.run();h.state.generating=true;h.mutate();await h.flush();h.state.generating=false;h.state.latestModel={text:call,element:{}};h.mutate();await h.flush();
  const calls=h.sent.filter(m=>m.type==='studio.call');assert.equal(calls.length,2);assert.notEqual(calls[0].responseKey,calls[1].responseKey);
});

test('turnless anchors identical user prompts to distinct delivered results', async () => {
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  const call='```helper-call\n'+JSON.stringify({v:1,name:'tool',arguments:{}})+'\n```';
  h.state.modelAfterUser=true;h.state.latestUser={text:JSON.stringify({type:'helper-result',operation_id:'op-1'})};h.run();h.state.generating=true;h.mutate();await h.flush();h.state.generating=false;h.state.latestModel={text:call,element:{}};h.mutate();await h.flush();h.mutate();await h.flush();
  h.state.latestUser={text:JSON.stringify({type:'helper-result',operation_id:'op-2'})};h.run();h.state.generating=true;h.mutate();await h.flush();h.state.generating=false;h.state.latestModel={text:call,element:{}};h.mutate();await h.flush();h.mutate();await h.flush();
  const calls=h.sent.filter(m=>m.type==='studio.call');assert.equal(calls.length,2);assert.equal(calls[0].precedingResultId,'op-1');assert.equal(calls[1].precedingResultId,'op-2');assert.notEqual(calls[0].responseKey,calls[1].responseKey);
});

test('turnless ignores a stale model before the new user result', async () => {
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  h.state.latestUser={text:'new request'};h.state.latestModel={text:'```helper-call\n'+JSON.stringify({v:1,name:'tool',arguments:{}})+'\n```'};h.state.modelAfterUser=false;h.mutate();await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
});

test('turnless history rerenders do not dispatch without a new Run', async () => {
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  h.state.latestUser={text:'scrolled history'};h.state.latestModel={text:'```helper-call\n'+JSON.stringify({v:1,name:'tool',arguments:{}})+'\n```',element:{}};h.state.modelAfterUser=true;h.mutate();await h.flush();
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
  const h=harness(),call='```helper-call\n'+JSON.stringify({v:1,name:'tool',arguments:{}})+'\n```';
  h.state.latestUser={text:'request'};h.state.latestModel={text:call};h.state.modelAfterUser=true;
  await h.send({type:'studio.arm',sessionId:'s1',turnless:true,recover:true,recoverTurn:'',toolNames:[{name:'tool',inputSchema:{type:'object',properties:{},additionalProperties:false}}],expectedConversationKey:'chat-1'});
  await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
  await h.send({type:'studio.snapshot',sessionId:'s1',operationId:'unproven'});await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
  await h.send({type:'studio.deliver',sessionId:'s1',text:JSON.stringify({type:'helper-result',operation_id:'proven'}),submit:true});
  await h.send({type:'studio.snapshot',sessionId:'s1',operationId:'proven'});await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,1);
});

test('turnless recovery proof clears recovery when the new call has not rendered yet', async () => {
  const h=harness(),call='```helper-call\n'+JSON.stringify({v:1,name:'tool',arguments:{}})+'\n```';
  h.state.latestUser={text:'request'};h.state.latestModel={text:'previous prose'};h.state.modelAfterUser=false;
  await h.send({type:'studio.arm',sessionId:'s1',turnless:true,recover:true,recoverTurn:'',toolNames:[{name:'tool',inputSchema:{type:'object',properties:{},additionalProperties:false}}],expectedConversationKey:'chat-1'});
  await h.send({type:'studio.deliver',sessionId:'s1',text:JSON.stringify({type:'helper-result',operation_id:'proof'}),submit:true});
  await h.send({type:'studio.snapshot',sessionId:'s1',operationId:'proof'});
  h.state.latestModel={text:call};h.state.modelAfterUser=true;h.mutate();await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
  h.run();h.state.generating=true;h.mutate();await h.flush();h.state.generating=false;h.state.latestModel={text:call,element:{}};h.mutate();await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,1);
});

test('snapshot reports only an exactly parsed visible result as delivery proof',async()=>{
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  h.state.latestUser={text:'prefix '+JSON.stringify({type:'helper-result',operation_id:'op-visible'})};
  let snap=await h.send({type:'studio.snapshot',sessionId:'s1',operationId:'op-visible'});assert.equal(snap.delivered,false);assert.equal(snap.deliveredOperationId,'');
  h.state.latestUser={text:JSON.stringify({type:'helper-result',operation_id:'op-visible'})};
  snap=await h.send({type:'studio.snapshot',sessionId:'s1',operationId:'op-visible'});assert.equal(snap.delivered,true);assert.equal(snap.deliveredOperationId,'op-visible');
  h.state.latestUser={text:'User timestamp wrapper',operationId:'op-visible'};
  snap=await h.send({type:'studio.snapshot',sessionId:'s1',operationId:'op-visible'});assert.equal(snap.delivered,true);assert.equal(snap.deliveredOperationId,'op-visible');
  h.state.latestUser={text:'{"type":"helper-result","operation_id":'};
  snap=await h.send({type:'studio.snapshot',sessionId:'s1',operationId:'op-visible'});assert.equal(snap.delivered,false);assert.equal(snap.deliveredOperationId,'');
});

test('snapshot delivery fallback requires the exact expected result text',async()=>{
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1'});
  const text=JSON.stringify({type:'helper-result',operation_id:'op-exact',result:'expected'});
  await h.send({type:'studio.deliver',sessionId:'s1',text,submit:true});
  assert.equal((await h.send({type:'studio.snapshot',sessionId:'s1',operationId:'op-exact',expectedResultText:text})).delivered,true);
  assert.equal((await h.send({type:'studio.snapshot',sessionId:'s1',operationId:'op-exact',expectedResultText:text.replace('expected','different')})).delivered,false);
});

test('an old call acknowledgement cannot consume a newer Run gate',async()=>{
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  const releases=[];h.setSendHandler(message=>message.type==='studio.call'?new Promise(resolve=>{releases.push(resolve);}):{ok:true,accepted:true});
  h.run();h.state.generating=true;h.mutate();await h.flush();h.state.generating=false;h.state.modelAfterUser=true;h.state.latestModel={text:'first call'};h.mutate();await h.flush();
  h.run();h.state.generating=true;h.mutate();await h.flush();h.state.generating=false;h.state.latestModel={text:'second call'};h.mutate();await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,2);
  releases[1]({accepted:false});await new Promise(resolve=>setImmediate(resolve));
  releases[0]({accepted:true});await new Promise(resolve=>setImmediate(resolve));
  h.setSendHandler(null);h.mutate();await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call'&&m.text==='second call').length,2);
});

test('a delivered result anchors one fast automatic followup without replaying history',async()=>{
  const h=harness();await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
  await h.send({type:'studio.deliver',sessionId:'s1',text:JSON.stringify({type:'helper-result',operation_id:'op-follow'}),submit:true});
  h.state.latestUser={text:JSON.stringify({type:'helper-result',operation_id:'op-follow'})};h.state.modelAfterUser=true;
  h.state.latestModel={text:'```helper-call\n'+JSON.stringify({v:1,name:'tool',arguments:{}})+'\n```'};h.mutate();await h.flush();h.mutate();await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,1);
  h.state.latestModel={text:'historical call'};h.mutate();await h.flush();
  assert.equal(h.sent.filter(m=>m.type==='studio.call').length,1);
});

test('explicit capture reads only a complete selected write_file without replaying chat history',async()=>{
 const h=harness(),tool={name:'write_file',inputSchema:{type:'object',properties:{path:{type:'string'},content:{type:'string'},if_absent:{type:'boolean'}},required:['path','content','if_absent'],additionalProperties:false}};
 const content='code\n'.repeat(300000),text=JSON.stringify({v:1,name:'write_file',arguments:{path:'large.html',content,if_absent:true}});
 h.state.latestModel={text};await h.send({type:'studio.arm',sessionId:'s1',turnless:true});
 const captured=await h.send({type:'studio.captureWrite',sessionId:'s1',tool});assert.equal(captured.ok,true);assert.equal(JSON.parse(captured.text).arguments.content,content);
 h.mutate();await h.flush();assert.equal(h.sent.filter(m=>m.type==='studio.call').length,0);
 h.state.generating=true;assert.equal((await h.send({type:'studio.captureWrite',sessionId:'s1',tool})).ok,false);
 h.state.generating=false;h.state.latestModel={text:JSON.stringify({v:1,name:'exec_command',arguments:{}})};
 assert.equal((await h.send({type:'studio.captureWrite',sessionId:'s1',tool})).ok,false);
});
