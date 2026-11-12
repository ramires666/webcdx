const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const WebCodexProtocol = require('./protocol.js');

const KEY = 'webcodexState', TOKEN = 'test-token';
const tool = { name: 'read_file', description: 'test', inputSchema: { type: 'object', properties: { path: { type: 'string' } }, required: ['path'], additionalProperties: false } };

function harness({manualClock=false,tools=[tool]}={}) {
  let model = { text: 'existing answer' }, latestUser = null, modelAfterUser = true, editorEmpty = true, supported = true;
  let refreshFails = 1, systemStageFails = 1, now = 10000, generating = false, observerCallback, currentPath = '/prompts/new_chat', saveOnSubmit=false, legacyProofMissing=false;
  let historyAnchor=null,historyStart='';
  const conversationKey = 'active-chat';
  const autoReplies = [], operations = [], messages = [], staged = [], bgListeners = [], contentListeners = [], timers = new Map(), intervals = new Map(); let timerId = 0;
  const session = { phase: 'waiting_model', agentId: 'agent-1', toolNames: tools.map(t=>t.name), automatic: true, paused: false, turn: 'old-turn', instructions: '', pendingCall: null, resultText: '', error: '', count: 0, tabId: 7, sessionId: 'old-session', conversationKey, pageInstance: 'page-1' };
  const db = { [KEY]: { connected: true, gateUrl: 'https://gate.example', clientId: 'client', accessToken: TOKEN, agents: [{ id: 'agent-1', name: 'test', online: true }], tools, session }, };
  const local = { bridgeAuth: { gateUrl: 'https://gate.example', clientId: 'client', accessToken: TOKEN }, bridgeAuto: { enabled: true, consent: true, agentId: 'agent-1', toolNames: tools.map(t=>t.name), automatic: true, autoApprove: false }, bridgeAutoSetupV1: true };
  const chrome = {
    runtime: { id: 'test', getURL: p => `chrome-extension://test/${p}`, onMessage: { addListener: fn => bgListeners.push(fn) }, sendMessage: msg => new Promise(resolve => { messages.push(structuredClone(msg)); const async = bgListeners[0](msg, { frameId: 0, url: 'https://aistudio.google.com/app/prompts/chat', tab: { id: 7 } }, resolve); if (async !== true) resolve(undefined); }) },
    storage: { session: { setAccessLevel: async () => {}, get: async key => ({ [key]: structuredClone(db[key]) }), set: async values => Object.assign(db, structuredClone(values)) }, local: { setAccessLevel: async () => {}, get: async key => ({ [key]: structuredClone(local[key]) }), set: async values => Object.assign(local, structuredClone(values)), remove: async key => delete local[key] } },
    action: { onClicked: { addListener() {} } }, alarms: { create() {}, onAlarm: { addListener() {} } }, permissions: { contains: async () => true },
    tabs: { get: async id => ({ id, url: 'https://aistudio.google.com/app/prompts/chat' }), sendMessage: async (_id, msg) => new Promise(resolve => { messages.push(structuredClone(msg)); const listener = contentListeners[0]; if (!listener) throw Error('No content listener'); const async = listener(msg, {}, resolve); if (async !== true && !['studio.stage'].includes(msg.type)) resolve(undefined); }) }
  };
  const adapters = { createAdapter: () => ({
    inspect: () => ({ supported, generating, controlsPending: false, editorEmpty, conversationKey, latestModel: model, latestUser, modelAfterUser,historyAnchor,historyStart, error: supported ? '' : 'editor unavailable' }),
    stageSystemInstructions: async text => { if (systemStageFails-- > 0) return false; staged.push({ kind: 'system', text }); return true; },
    getSystemStageError: () => 'transient system editor', stage: text => { staged.push({ kind: 'turn', text }); editorEmpty = false; return true; },
    restoreCleanup() {}, hasDelivery: operationId => !legacyProofMissing&&latestUser?.operationId===operationId, submit: async draft => { const text=draft.replace(/^```helper-result\n/,'').replace(/\n```$/,''); if(saveOnSubmit){currentPath='/prompts/saved-chat';saveOnSubmit=false;}latestUser={text:legacyProofMissing?'User\n11:49 PM\n'+text:text,operationId:JSON.parse(text).operation_id};modelAfterUser=false;if(autoReplies.length){model={text:autoReplies.shift(),element:{}};modelAfterUser=true;observerCallback();}return true; }
  }) };
  class TestDate extends Date { static now() { return now; } }
  const context = { chrome, window: null, document: { documentElement: {}, addEventListener(type, fn) { this[type] = fn; } }, location: { origin: 'https://aistudio.google.com', get pathname() { return currentPath; } }, WebCodexStudio: adapters, WebCodexProtocol, URL, URLSearchParams, TextEncoder, TextDecoder, Uint8Array, ArrayBuffer, JSON, Promise, Set, Map, Date: TestDate, AbortSignal, Math, Error, String, Object, Number, RegExp, console: { error() {}, warn() {}, log() {} }, crypto: {randomUUID:require('node:crypto').randomUUID,subtle:{digest:async(_algorithm,data)=>require('node:crypto').createHash('sha256').update(data).digest()}}, btoa: s => Buffer.from(s, 'binary').toString('base64'), setTimeout: (fn,ms=0) => { timers.set(++timerId, {fn,at:now+ms}); return timerId; }, clearTimeout: id => timers.delete(id), setInterval: fn => { intervals.set(++timerId, fn); return timerId; }, clearInterval: id => intervals.delete(id), MutationObserver: class { constructor(fn) { observerCallback = fn; } observe() {} disconnect() {} }, importScripts() {}, WebCodexMCP: { createClient: () => ({ initialize: async () => ({}), listAgents: async () => [{ id: 'agent-1', name: 'test', online: true }], createOperation: async operation => { operations.push(operation); return {status:'completed',operation_id:'op-'+operations.length,result:{content:[{type:'text',text:'file contents'}]}}; }, listTools: async () => { if (refreshFails-- > 0) throw Error('temporary Gate refresh failure'); return tools; } }) } };
  context.window = { top: null, __webcodexBridgeLoaded: false }; context.window.top = context.window;
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'background.js'), 'utf8'), context, { filename: 'background.js' });
  // Content owns its listener on runtime.onMessage; separate the background listener installed first.
  context.chrome.runtime.onMessage.addListener = fn => contentListeners.push(fn);
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'content.js'), 'utf8'), context, { filename: 'content.js' });
  const clearChatWithoutClick=()=>{model=null;latestUser=null;modelAfterUser=false;editorEmpty=true;historyAnchor=null;historyStart='';};
  const openSavedChat=(userText,modelText)=>{context.document.click({target:{closest:()=>({getAttribute:name=>name==='href'?'/prompts/saved-chat':null})}});historyAnchor={};historyStart=userText;latestUser={text:userText};model={text:modelText,element:{}};modelAfterUser=true;};
  const setSupported=value=>{supported=value;};
  return { chrome, local, messages, staged, db, operations, rerenderHistory: text=>{historyAnchor={};historyStart=text;observerCallback();}, clearChatWithoutClick, openSavedChat, setSupported, replyImmediatelyAfterDelivery: text => autoReplies.push(text), renderModelFragment: text => {model={text,element:{}};modelAfterUser=true;observerCallback();}, missLegacyDeliveryProof: () => { legacyProofMissing=true; }, saveChat: () => { currentPath='/prompts/saved-chat'; }, navigate: path => { currentPath=path; }, saveDuringDelivery: () => { saveOnSubmit=true; }, userMessage: text => { latestUser={text};modelAfterUser=false; }, modelMessage: text => { context.document.click({target:{closest:()=>({getAttribute:name=>name==='aria-label'?'Run':'',innerText:'Run',querySelector:()=>null})}}); generating=true;observerCallback();model={text,element:{}};modelAfterUser=true;generating=false;observerCallback(); }, armExisting: () => chrome.tabs.sendMessage(7, {type:'studio.arm',sessionId:'old-session'}), clickNewChat() { context.document.click({ target: { closest: () => ({ getAttribute: name => name === 'aria-label' ? 'New chat' : '', innerText: '' }) } }); model = null; }, async flush() { for (let i = 0; i < 8; i++) await new Promise(resolve => setImmediate(resolve)); }, async tick() {
    for(const fn of intervals.values())fn();await this.flush();
    for(let round=0;round<20&&timers.size;round++){
      if(!manualClock)now=Math.max(now,Math.min(...[...timers.values()].map(timer=>timer.at)));
      const due=[...timers].filter(([,timer])=>timer.at<=now);if(!due.length)break;
      for(const[id,timer]of due){timers.delete(id);timer.fn();}await this.flush();
    }
  }, advance(ms) { now += ms; }, get state() { return db[KEY].session; } };
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

test('a cleared chat recovers from an unavailable editor, stages system instructions and executes the next call once',async()=>{
  const h=harness();await h.flush();h.userMessage('previous request');await h.armExisting();
  h.setSupported(false);await h.tick();
  assert.equal(h.state.phase,'paused');
  h.setSupported(true);h.clearChatWithoutClick();h.advance(6000);
  await h.tick();await h.tick();h.advance(6000);await h.tick();await h.tick();
  assert.equal(h.state.phase,'waiting_model');
  assert.notEqual(h.state.sessionId,'old-session');
  assert.ok(h.staged.some(item=>item.kind==='system'&&item.text.includes('Available tools')));
  assert.equal(h.operations.length,0,'clearing the old chat never replays history');
  h.userMessage('Read safe.txt');h.modelMessage('```helper-call\n'+JSON.stringify({v:1,name:'read_file',arguments:{path:'safe.txt'}})+'\n```');
  await h.tick();await h.tick();
  assert.equal(h.operations.length,1);
  assert.equal(h.operations[0].name,'read_file');
});

test('a new chat runs repeated calls without inserting an ID or replaying an older response', async () => {
  const h=harness();await h.flush();await h.armExisting();h.clickNewChat();
  await h.tick();h.advance(6000);await h.tick();h.advance(6000);await h.tick();
  assert.equal(h.state.turnless,true);assert.equal(h.staged.some(item=>item.kind==='turn'),false);
  const call='```helper-call\n'+JSON.stringify({v:1,name:'read_file',arguments:{path:'safe.txt'}})+'\n```';
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

test('a damaged tool envelope shows an error instead of silently becoming a completed prose answer',async()=>{
  const h=harness();await h.flush();await h.armExisting();h.clickNewChat();
  await h.tick();h.advance(6000);await h.tick();h.advance(6000);await h.tick();
  h.userMessage('Read a file');h.modelMessage('Проверю файл.\n{"v":1,"name":"read_file","arguments":{"path":"safe.txt"}');
  await h.tick();
  assert.equal(h.state.phase,'paused');
  assert.match(h.state.error,/JSON/);
  assert.equal(h.operations.length,0);
});

test('an automatic followup waits for a fully rendered path even if generation controls report idle',async()=>{
  const tools=[{name:'list_allowed_roots',inputSchema:{type:'object',properties:{},additionalProperties:false}},{...tool,name:'list_directory'}];
  const h=harness({manualClock:true,tools});await h.flush();await h.armExisting();h.clickNewChat();
  await h.tick();h.advance(6000);await h.tick();h.advance(6000);await h.tick();
  h.userMessage('Open pacman in Chrome');h.modelMessage(JSON.stringify({v:1,name:'list_allowed_roots',arguments:{}}));
  h.advance(250);await h.tick();h.advance(1000);await h.tick();await h.tick();
  assert.equal(h.operations.length,1);
  const complete='Проверю наличие файла по пути W:\\_python\\pacman\\kimik3-pacman.\n'+JSON.stringify({v:1,name:'list_directory',arguments:{path:'W:\\_python\\pacman\\kimik3-pacman'}});
  h.renderModelFragment(complete.slice(0,-12));h.advance(250);await h.tick();h.advance(500);await h.tick();
  assert.equal(h.state.phase,'waiting_model','a transient incomplete JSON frame must never pause the background session');
  assert.equal(h.operations.length,1);
  h.renderModelFragment(complete);h.advance(250);await h.tick();h.advance(1000);await h.tick();await h.tick();
  assert.equal(h.operations.length,2);
  assert.deepEqual(h.operations.map(op=>op.name),['list_allowed_roots','list_directory']);
  assert.equal(h.operations[1].arguments.path,'W:\\_python\\pacman\\kimik3-pacman');
});

test('real DOM extraction reaches operation dispatch exactly once and preserves command arguments',async()=>{
  const {Node,setup}=require('./test-fixtures/studio-dom.cjs');
  const h=harness();await h.flush();await h.armExisting();h.clickNewChat();
  await h.tick();h.advance(6000);await h.tick();h.advance(6000);await h.tick();
  const path='W:\\Игра {test}\\"quoted"\\safe.txt';
  for(const tag of ['future-renderer','pre']){
    const state=setup();state.chunk.remove();const body=state.model.children[0];
    body.add(new Node('div',{},'Сначала прочитаю файл.\n'));
    const code=body.add(new Node(tag));code.add(new Node('header',{},'v\n'));
    const raw=JSON.stringify({v:1,name:'read_file',arguments:{path}});
    for(const char of raw)code.add(new Node('span',{},char));
    const before=h.operations.length;
    h.userMessage('Read the file again');h.modelMessage(state.adapter.inspect().latestModel.text);
    await h.tick();await h.tick();await h.tick();
    assert.equal(h.operations.length,before+1);
    assert.deepEqual(JSON.parse(JSON.stringify(h.operations[before].arguments)),{path});
    assert.equal(h.state.phase,'waiting_model','the completed operation returns its result and awaits the model');
  }
});

test('malformed, duplicated and injected calls extracted from generic DOM never dispatch',async()=>{
  const {Node,setup}=require('./test-fixtures/studio-dom.cjs');
  const call=JSON.stringify({v:1,name:'read_file',arguments:{path:'safe.txt'}});
  for(const raw of [call.slice(0,-1),call+'\n'+call,JSON.stringify({v:1,name:'read_file',arguments:{path:'safe.txt',request_key:'injected'}})]){
    const h=harness();await h.flush();await h.armExisting();h.clickNewChat();
    await h.tick();h.advance(6000);await h.tick();h.advance(6000);await h.tick();
    const state=setup();state.chunk.remove();state.model.children[0].add(new Node('future-renderer',{},raw));
    h.userMessage('Read file');h.modelMessage(state.adapter.inspect().latestModel.text);await h.tick();await h.tick();
    assert.equal(h.operations.length,0);assert.equal(h.state.phase,'paused');assert.ok(h.state.error);
  }
});

test('selecting a saved chat stages only system instructions and never dispatches its historical call',async()=>{
  const h=harness();await h.flush();await h.armExisting();h.clickNewChat();
  await h.tick();h.advance(6000);await h.tick();h.advance(6000);await h.tick();
  const sessionId=h.state.sessionId,stagedBefore=h.staged.filter(item=>item.kind==='system').length;
  const call=path=>'```helper-call\n'+JSON.stringify({v:1,name:'read_file',arguments:{path}})+'\n```';
  h.openSavedChat('historical request',call('old.txt'));
  await h.tick();await h.tick();await h.tick();
  assert.equal(h.state.sessionId,sessionId);
  assert.equal(h.staged.filter(item=>item.kind==='system').length,stagedBefore+1);
  assert.equal(h.staged.some(item=>item.kind==='turn'),false);
  assert.equal(h.operations.length,0);
  h.userMessage('Read new.txt');h.modelMessage(call('new.txt'));await h.tick();await h.tick();
  assert.equal(h.operations.length,1);
  assert.equal(h.operations[0].arguments.path,'new.txt');
});

test('saving the current chat during its first response does not pause or lose the call', async () => {
  const h=harness();await h.flush();await h.armExisting();h.clickNewChat();
  await h.tick();h.advance(6000);await h.tick();h.advance(6000);await h.tick();
  const sessionId=h.state.sessionId;
  h.userMessage('Read the file');h.saveChat();
  h.modelMessage('Проверю файл.\n```helper-call\n'+JSON.stringify({v:1,name:'read_file',arguments:{path:'safe.txt'}})+'\n```');
  await h.tick();await h.tick();
  assert.equal(h.state.sessionId,sessionId);assert.equal(h.state.conversationKey,'active-chat');
  assert.equal(h.state.phase,'waiting_model');assert.equal(h.operations.length,1);
  assert.equal(h.messages.find(msg=>msg.type==='studio.call').conversationKey,'active-chat');
});

test('saving during result submission preserves the operation and allows the corrected follow-up', async () => {
  const h=harness();await h.flush();await h.armExisting();h.clickNewChat();
  await h.tick();h.advance(6000);await h.tick();h.advance(6000);await h.tick();
  const sessionId=h.state.sessionId;
  h.userMessage('Read the file');h.saveDuringDelivery();
  h.modelMessage('Проверю файл.\n```helper-call\n'+JSON.stringify({v:1,name:'read_file',arguments:{path:'first.txt'}})+'\n```');
  await h.tick();await h.tick();
  assert.equal(h.state.sessionId,sessionId);assert.equal(h.state.conversationKey,'active-chat');
  assert.equal(h.state.phase,'waiting_model');assert.equal(h.operations.length,1);
  h.modelMessage('Исправлю путь.\n```helper-call\n'+JSON.stringify({v:1,name:'read_file',arguments:{path:'corrected.txt'}})+'\n```');
  await h.tick();await h.tick();
  assert.equal(h.state.phase,'waiting_model');assert.equal(h.operations.length,2);
  assert.equal(h.operations[0].arguments.path,'first.txt');assert.equal(h.operations[1].arguments.path,'corrected.txt');
  assert.notEqual(h.operations[0].key,h.operations[1].key);
});

test('the active tab keeps working across arbitrary chat URL changes', async () => {
  const h=harness();await h.flush();await h.armExisting();h.clickNewChat();
  await h.tick();h.advance(6000);await h.tick();h.advance(6000);await h.tick();
  const sessionId=h.state.sessionId;
  for(const path of ['/prompts/new_chat','/prompts/first-saved','/prompts/other-saved','/app/prompts/changed-again']){
    h.navigate(path);h.userMessage('Read '+path);
    h.modelMessage('```helper-call\n'+JSON.stringify({v:1,name:'read_file',arguments:{path:'safe.txt'}})+'\n```');
    await h.tick();await h.tick();
    assert.equal(h.state.sessionId,sessionId);assert.equal(h.state.phase,'waiting_model');
  }
  assert.equal(h.operations.length,4);
  assert.equal(h.messages.some(msg=>msg.type==='studio.call'&&msg.conversationKey!=='active-chat'),false);
});

test('an immediate automatic follow-up is executed once even before the previous request is acknowledged', async () => {
  const h=harness();await h.flush();await h.armExisting();h.clickNewChat();
  await h.tick();h.advance(6000);await h.tick();h.advance(6000);await h.tick();
  h.userMessage('Read the files');h.missLegacyDeliveryProof();
  h.replyImmediatelyAfterDelivery('Второй файл.\n```helper-call\n'+JSON.stringify({v:1,name:'read_file',arguments:{path:'second.txt'}})+'\n```');
  h.modelMessage('Первый файл.\n```helper-call\n'+JSON.stringify({v:1,name:'read_file',arguments:{path:'first.txt'}})+'\n```');
  for(let tick=0;tick<5;tick++)await h.tick();
  assert.equal(h.operations.length,2);
  assert.equal(h.operations[0].arguments.path,'first.txt');assert.equal(h.operations[1].arguments.path,'second.txt');
  assert.equal(h.state.phase,'waiting_model');
  assert.equal(h.messages.filter(msg=>msg.type==='studio.deliver').length,2);
  await h.tick();assert.equal(h.operations.length,2,'no repeat after both results are submitted');
});

test('a directory result followed by rerendered history preserves the next automatic call and system instructions',async()=>{
 const h=harness({tools:[{...tool,name:'list_directory'}]});await h.flush();await h.armExisting();h.clickNewChat();
 await h.tick();h.advance(6000);await h.tick();h.advance(6000);await h.tick();
 const session=h.state.sessionId,systemStages=h.staged.filter(item=>item.kind==='system').length;
 h.userMessage('Create the game');h.modelMessage(JSON.stringify({v:1,name:'list_directory',arguments:{path:'W:\\'}}));
 await h.tick();await h.tick();assert.equal(h.operations.length,1);
 h.rerenderHistory('first visible turn is now the directory result');
 h.renderModelFragment(JSON.stringify({v:1,name:'list_directory',arguments:{path:'W:\\_python'}}));
 await h.tick();await h.tick();await h.tick();
 assert.equal(h.operations.length,2);assert.equal(h.operations[1].arguments.path,'W:\\_python');
 assert.equal(h.state.sessionId,session);assert.equal(h.staged.filter(item=>item.kind==='system').length,systemStages);
 assert.equal(h.messages.filter(item=>item.type==='studio.ensureInstructions').length,0);
});
