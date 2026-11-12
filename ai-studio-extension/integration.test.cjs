const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const WebCodexProtocol = require('./protocol.js');
const WebCodexMCP = require('./mcp-client.js');

const KEY = 'webcodexState', TOKEN = 'integration-token';
const tool = (name = 'read_file') => ({ name, description: 'test', inputSchema: { type: 'object', properties: { path: { type: 'string' } }, required: ['path'], additionalProperties: false } });
function saved(phase = 'waiting_model', name = 'read_file') {
  return { connected: true, gateUrl: 'https://gate.example', clientId: 'oauth-client', accessToken: TOKEN,
    agents: [{ id: 'agent-1', name: 'test agent', online: true }], tools: [tool(name)],
    session: { phase, agentId: 'agent-1', toolNames: [name], automatic: false, paused: false, turn: 'turn-1', instructions: '',
      pendingCall: phase === 'waiting_approval' ? { name, arguments: { path: 'safe.txt' }, turn: 'turn-1' } : null,
      resultText: '', error: '', count: 0, tabId: 7, sessionId: 'session-1', conversationKey: 'chat-1' } };
}
function harness(initial = saved(), tabBehavior = {}, initialLocalData = {}) {
  const localData = structuredClone(initialLocalData);
  if (!Object.hasOwn(localData, 'bridgeAutoSetupV1')) localData.bridgeAutoSetupV1 = true;
  const listeners = {}, calls = [], pageMessages = [], db = { [KEY]: structuredClone(initial) };
  let createCount = 0, deliveryCount = 0, clock = Date.now();
  class TestDate extends Date { static now() { return clock; } }
  const chrome = {
    runtime: { id: 'test-id', getURL: p => 'chrome-extension://test-id/' + p, onMessage: { addListener: fn => { listeners.message = fn; } } },
    storage: { session: { setAccessLevel: async () => {}, get: async k => ({ [k]: db[k] && structuredClone(db[k]) }), set: async x => { if(tabBehavior.sessionQuota&&JSON.stringify(x).length>tabBehavior.sessionQuota)throw Error('QUOTA_BYTES exceeded');for (const [k,v] of Object.entries(x)) db[k] = structuredClone(v); } }, local: { get: async k => ({[k]:localData[k]}), set: async x => Object.assign(localData,x), remove: async k => {delete localData[k];}, setAccessLevel: async () => {} } },
    action: { onClicked: { addListener: () => {} } }, sidePanel: { open: async () => {} }, alarms: { create: () => {}, onAlarm: { addListener: fn => { listeners.alarm = fn; } } },
    permissions: { contains: async () => true },
    tabs: { get: async id => ({ id, url: 'https://aistudio.google.com/app/prompts/test' }), sendMessage: async (id, msg) => {
      pageMessages.push(structuredClone(msg));
      if (tabBehavior.sendMessage) return tabBehavior.sendMessage(id, msg, { calls, get deliveryCount() { return deliveryCount; } });
      if (msg.type === 'studio.arm') return { ok: true, supported: true, conversationKey: 'chat-1' };
      if (msg.type === 'studio.stage') return { ok: true };
      if (msg.type === 'studio.deliver') { deliveryCount++; return { ok: true, confirmed: true, supported: true, conversationKey: 'chat-1' }; }
      if (msg.type === 'studio.snapshot') return { ok: true, delivered: tabBehavior.snapshotDelivered ?? true, supported: true, conversationKey: 'chat-1', generating: false, editorEmpty: true };
      return { ok: true };
    } },
    identity: { getRedirectURL: () => 'https://test.chromiumapp.org/webcodex' }
  };
  const fetch = async (url, init = {}) => {
    const u = new URL(url), method = init.method || 'GET';
    if (u.pathname === '/mcp/v5') {
      const body = JSON.parse(init.body || '{}');
      if (body.method === 'initialize') return json({ jsonrpc: '2.0', id: body.id, result: {} });
      if (body.method === 'tools/list') return json({ jsonrpc: '2.0', id: body.id, result: { tools: tabBehavior.listTools ? tabBehavior.listTools() : [tool(), tool('write_file')] } });
      if (body.method === 'tools/call') return json({ jsonrpc: '2.0', id: body.id, result: { structuredContent: { agents: tabBehavior.listAgents ? tabBehavior.listAgents() : [{ id: 'agent-1', name: 'test agent', online: true }] } } });
      if (body.method === 'notifications/initialized') return new Response(null, { status: 202 });
    }
    if (u.pathname === '/bridge/v1/operations' && method === 'POST') {
      createCount++; calls.push({ url: u.pathname, method, body: JSON.parse(init.body) });
      if (tabBehavior.expireAuth) return json({}, 401);
      return json({ id: 'op-1', status: 'dispatching' }, 202);
    }
    if (u.pathname === '/bridge/v1/operations/op-1' && method === 'GET') {
      calls.push({ url: u.pathname, method });
      return json({ id: 'op-1', status: 'completed', result: { content: [{ type: 'text', text: tabBehavior.resultText ?? 'file contents' }], isError: false } });
    }
    throw Error('Unexpected fetch ' + method + ' ' + url);
  };
  const mcp = { createClient: options => WebCodexMCP.createClient({ ...options, fetchImpl: fetch }) };
  const context = { chrome, crypto: require('node:crypto').webcrypto, URL, URLSearchParams, TextEncoder, TextDecoder, Uint8Array, ArrayBuffer, JSON, Promise, Set, Map, Date:TestDate, AbortSignal, Math, Error, String, Object, Number, RegExp, console, btoa: s => Buffer.from(s, 'binary').toString('base64'), fetch, WebCodexProtocol, WebCodexMCP: mcp, importScripts() {}, setTimeout, clearTimeout };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'background.js'), 'utf8'), context, { filename: 'background.js' });
  const panel = { url: 'chrome-extension://test-id/panel.html' }, page = { url: 'https://aistudio.google.com/app/prompts/test', frameId: 0, tab: { id: 7 } };
  const message = (msg, sender = panel) => new Promise(resolve => { const async = listeners.message(msg, sender, resolve); if (async !== true) resolve(undefined); });
  const status = () => message({ type: 'bridge.status' });
  const alarm = async () => { listeners.alarm({ name: 'webcodex-recover' }); await status(); };
  const call = async (turn, sender = page, name = 'read_file') => message({ type: 'studio.call', sessionId: 'session-1', conversationKey: 'chat-1', generating: false, text: '```helper-call\n' + JSON.stringify({ v: 1, turn, name, arguments: { path: 'safe.txt' } }) + '\n```' }, sender);
  return { localData, chrome, message, status, call, alarm, listeners, panel, page, calls, pageMessages, db, token: TOKEN, advanceTime(ms){clock+=ms;}, get createCount() { return createCount; }, get deliveryCount() { return deliveryCount; } };
}
function json(value, status = 200) { return new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } }); }

test('uncertain validation feedback is checked without a resend or any agent operation',async()=>{
  const initial=saved();Object.assign(initial.session,{turnless:true,automatic:true});
  const h=harness(initial,{sendMessage:async(_id,msg)=>msg.type==='studio.deliver'?{ok:true,attempted:true,confirmed:false,supported:true}:msg.type==='studio.snapshot'?{supported:true,delivered:false}:{ok:true,supported:true}}, {bridgeAuto:{enabled:true,consent:true,agentId:'agent-1',toolNames:['read_file'],automatic:true}});
  await h.status();
  const result=await h.message({type:'studio.call',sessionId:'session-1',responseKey:'invalid-one',text:'{"v":1,"name":"read_file","arguments":',generating:false},h.page);
  assert.equal(result.accepted,true);
  await h.alarm();await h.alarm();
  const state=(await h.status()).state.session;
  assert.equal(state.phase,'delivery_uncertain');assert.match(state.operationId,/^validation-/);
  assert.equal(h.createCount,0);assert.equal(h.pageMessages.filter(message=>message.type==='studio.deliver').length,1);
});

test('validation feedback cannot bypass automatic mode or revoked consent',async()=>{
  for(const [automatic,consent]of [[false,true],[true,false]]){
    const initial=saved();Object.assign(initial.session,{turnless:true,automatic});
    const h=harness(initial,{}, {bridgeAuto:{enabled:true,consent,agentId:'agent-1',toolNames:['read_file'],automatic}});
    await h.status();
    const result=await h.message({type:'studio.call',sessionId:'session-1',responseKey:'invalid-one',text:'{"v":1,"name":"read_file","arguments":',generating:false},h.page);
    assert.equal(result.accepted,false);assert.equal(h.createCount,0);
    assert.equal(h.pageMessages.some(message=>message.type==='studio.deliver'),false);
  }
});

test('sessions continue beyond ten operations and five minutes', async () => {
  const h = harness(); await h.status(); h.advanceTime(6 * 60 * 1000);
  let turn = 'turn-1';
  for (let i = 0; i < 12; i++) {
    assert.equal((await h.call(turn)).accepted, true);
    await h.alarm(); await h.alarm();
    const state = (await h.status()).state.session;
    assert.equal(state.phase, 'result_ready');
    turn = JSON.parse(state.resultText).next_turn;
    assert.equal((await h.message({ type: 'bridge.deliver', submit: true })).ok, true);
  }
  const session = (await h.status()).state.session;
  assert.equal(session.phase, 'waiting_model'); assert.equal(session.paused, false);
  assert.equal(h.createCount, 12);
});

test('one bound chat executes calls without model turn IDs and deduplicates the same response', async () => {
  const initial=saved();initial.session.turnless=true;initial.session.automatic=true;
  const h=harness(initial);await h.status();
  const text='```helper-call\n'+JSON.stringify({v:1,name:'read_file',arguments:{path:'safe.txt'}})+'\n```';
  const message={type:'studio.call',sessionId:'session-1',conversationKey:'chat-1',generating:false,text,responseKey:'user-task-1-and-model-response'};
  assert.equal((await h.message(message,h.page)).accepted,true);await h.alarm();
  assert.equal(h.createCount,1);
  const result=JSON.parse(h.pageMessages.find(item=>item.type==='studio.deliver').text);
  assert.equal(Object.hasOwn(result,'turn'),false);assert.equal(Object.hasOwn(result,'next_turn'),false);
  assert.equal(result.operation_id,'op-1');
  assert.equal((await h.message(message,h.page)).accepted,true);
  assert.equal(h.createCount,1,'rerendering or resending the same observed response never replays it');
  assert.equal((await h.message({...message,responseKey:'next-tool-result-and-model-response'},h.page)).accepted,true);
  assert.equal(h.createCount,2,'identical arguments are allowed for a new response after a distinct tool result');
});

test('turnless mode retains selected tools, approval and tab binding checks', async () => {
  const initial=saved('waiting_model','write_file');initial.session.turnless=true;
  const h=harness(initial);await h.status();
  const text='```helper-call\n'+JSON.stringify({v:1,name:'write_file',arguments:{path:'safe.txt'}})+'\n```';
  const message={type:'studio.call',sessionId:'session-1',conversationKey:'chat-1',generating:false,text,responseKey:'user-and-write-response'};
  assert.equal((await h.message(message,{...h.page,tab:{id:8}})).accepted,false);
  assert.equal((await h.message(message,h.page)).accepted,true);
  assert.equal((await h.status()).state.session.phase,'waiting_approval');assert.equal(h.createCount,0);
});

test('a turnless follow-up proves delivery only with an observed matching result envelope', async () => {
  const initial=saved('delivery_uncertain');
  Object.assign(initial.session,{turnless:true,nextTurn:'internal-next',operationId:'old-op',operationKey:'old-key',resultText:'saved result',pendingCall:{name:'read_file',arguments:{path:'old'}}});
  const h=harness(initial);await h.status();
  const text='```helper-call\n'+JSON.stringify({v:1,name:'read_file',arguments:{path:'safe.txt'}})+'\n```';
  const message={type:'studio.call',sessionId:'session-1',conversationKey:'chat-1',generating:false,text,responseKey:'result-and-next-response'};
  for(const precedingResultId of [undefined,'other-op']){
    assert.equal((await h.message({...message,precedingResultId},h.page)).accepted,false);
    assert.equal(h.createCount,0);
  }
  assert.equal((await h.message({...message,precedingResultId:'old-op'},h.page)).accepted,true);
  assert.equal(h.createCount,1);assert.equal(h.deliveryCount,0);
  assert.notEqual(h.calls.find(item=>item.method==='POST').body.key,'old-key');
});

test('the visible result envelope confirms delivery without resending or rerunning the tool', async () => {
  const initial=saved('delivery_uncertain');
  Object.assign(initial.session,{turnless:true,nextTurn:'internal-next',operationId:'old-op',operationKey:'old-key',resultText:'saved result',pendingCall:{name:'read_file',arguments:{path:'old'}}});
  const h=harness(initial,{snapshotDelivered:false});await h.status();
  const snapshot={type:'studio.tick',sessionId:'session-1',supported:true,deliveredOperationId:'wrong-op'};
  await h.message(snapshot,h.page);assert.equal((await h.status()).state.session.phase,'delivery_uncertain');
  await h.message({...snapshot,deliveredOperationId:'old-op'},h.page);
  const session=(await h.status()).state.session;
  assert.equal(session.phase,'waiting_model');assert.equal(session.resultText,'');assert.equal(session.pendingCall,null);
  assert.equal(h.createCount,0);assert.equal(h.deliveryCount,0);
});

test('recovery supplies the saved result for full matching of a legacy Markdown-rendered message', async () => {
  const resultText=JSON.stringify({type:'helper-result',v:1,operation_id:'old-op',name:'read_file',result:{content:[{type:'text',text:JSON.stringify({path:'C:\\projects'})}]}});
  const initial=saved('delivery_uncertain');
  Object.assign(initial.session,{turnless:true,nextTurn:'internal-next',operationId:'old-op',operationKey:'old-key',resultText,pendingCall:{name:'read_file',arguments:{path:'safe.txt'}},pageInstance:'old-page'});
  const settings={enabled:true,consent:true,agentId:'agent-1',toolNames:['read_file'],automatic:true};
  const h=harness(initial,{sendMessage:async(_id,msg)=>({ok:true,supported:true,delivered:msg.type==='studio.snapshot'&&msg.operationId==='old-op'&&msg.expectedResultText===resultText})},{bridgeAuto:settings});await h.status();
  assert.equal((await h.message({type:'studio.hello',pageInstance:'new-page'},h.page)).accepted,true);
  assert.equal((await h.status()).state.session.phase,'waiting_model');
  assert.equal(h.pageMessages.find(msg=>msg.type==='studio.snapshot').expectedResultText,resultText);
  assert.equal(h.createCount,0);assert.equal(h.pageMessages.some(msg=>msg.type==='studio.deliver'),false);
});

test('legacy limit pauses wait for explicit resume and preserve approval and uncertainty guards', async () => {
  const legacy = saved('paused'); Object.assign(legacy.session, { paused: true, pausedPhase: 'ready', error: 'Session operation limit reached; resume to continue.', pendingCall: { name: 'read_file', arguments: { path: 'safe.txt' }, turn: 'turn-1' } });
  const consent = { bridgeAuto: { enabled: true, consent: true, agentId: 'agent-1', toolNames: ['read_file'], autoApprove: false } };
  const resumed = harness(legacy, {}, consent); await resumed.status();
  assert.equal(resumed.createCount, 0, 'loading the extension does not resume an old operation');
  assert.equal((await resumed.message({ type: 'bridge.resume' })).ok, true);
  assert.equal(resumed.createCount, 1, 'explicit resume dispatches a previously approved ready call once');
  assert.equal((await resumed.status()).state.session.phase, 'recovering');

  for (const flags of [{ userPaused: true }, { resumeBlocked: true }]) {
    const guarded = saved('paused'); Object.assign(guarded.session, { paused: true, pausedPhase: 'ready', error: 'Session operation limit reached; resume to continue.', pendingCall: { name: 'read_file', arguments: { path: 'safe.txt' }, turn: 'turn-1' }, ...flags });
    const h = harness(guarded); await h.status();
    assert.equal(h.createCount, 0); assert.equal((await h.status()).state.session.paused, true);
  }
  const approval = saved('paused', 'write_file'); Object.assign(approval.session, { paused: true, pausedPhase: 'waiting_approval', error: 'Session operation limit reached; resume to continue.', pendingCall: { name: 'write_file', arguments: { path: 'safe.txt' }, turn: 'turn-1' } });
  const h = harness(approval, {}, { bridgeAuto: { enabled: true, consent: true, agentId: 'agent-1', toolNames: ['write_file'], autoApprove: false } }); await h.status();
  assert.equal(h.createCount, 0);assert.equal((await h.message({ type: 'bridge.resume' })).ok, true);
  assert.equal(h.createCount, 0); assert.equal((await h.status()).state.session.phase, 'waiting_approval');
});

test('resuming a paused write requires current consent and auto-approval', async () => {
  const make = () => { const s = saved('paused', 'write_file'); Object.assign(s.session, { paused: true, pausedPhase: 'ready', error: 'Session operation limit reached; resume to continue.', autoApprove: true, pendingCall: { name: 'write_file', arguments: { path: 'safe.txt' }, turn: 'turn-1' } }); return s; };
  const autoOff = harness(make(), {}, { bridgeAuto: { enabled: true, consent: true, agentId: 'agent-1', toolNames: ['write_file'], autoApprove: true } }); await autoOff.status();
  await autoOff.message({ type: 'bridge.preferences', agentId: 'agent-1', toolNames: ['write_file'], consent: true, autoApprove: false });
  assert.equal((await autoOff.message({ type: 'bridge.resume' })).ok, true);
  assert.equal((await autoOff.status()).state.session.phase, 'waiting_approval'); assert.equal(autoOff.createCount, 0);

  const noConsent = harness(make(), {}, { bridgeAuto: { enabled: true, consent: true, agentId: 'agent-1', toolNames: ['write_file'], autoApprove: true } }); await noConsent.status();
  await noConsent.message({ type: 'bridge.preferences', agentId: 'agent-1', toolNames: ['write_file'], consent: false, autoApprove: true });
  assert.equal((await noConsent.message({ type: 'bridge.resume' })).ok, false); assert.equal(noConsent.createCount, 0);
});

test('large UTF-8 result is delivered intact once without a size pause', async () => {
  const fullText='данные 🧪'.repeat(12000), initial=saved();initial.session.automatic=true;
  const h=harness(initial,{resultText:fullText});await h.status();
  assert.equal((await h.call('turn-1')).accepted,true);await h.alarm();await h.alarm();
  const delivered=h.pageMessages.filter(m=>m.type==='studio.deliver');
  assert.equal(delivered.length,1);
  assert.equal(JSON.parse(delivered[0].text).result.content[0].text,fullText);
  assert.equal((await h.status()).state.session.phase,'waiting_model');assert.equal(h.createCount,1);
});

test('legacy oversized result resumes without replaying its pending operation', async () => {
  const envelope=JSON.stringify({type:'helper-result',v:1,turn:'turn-1',next_turn:'turn-2',operation_id:'op-old',name:'read_file',result:{content:[]}});
  const make=automatic=>{const initial=saved('paused');Object.assign(initial.session,{paused:true,pausedPhase:'ready',error:'Result exceeds 32 KiB; reduce the tool output before delivery.',pendingCall:{name:'read_file',arguments:{path:'safe.txt'},turn:'turn-1'},operationId:'op-old',operationKey:'key-old',nextTurn:'turn-2',resultText:envelope,automatic});return initial;};
  const manual=harness(make(false),{snapshotDelivered:false});await manual.status();
  assert.equal((await manual.message({type:'bridge.resume'})).ok,true);
  let state=(await manual.status()).state.session;
  assert.equal(state.phase,'result_ready');assert.equal(state.turn,'turn-1');assert.equal(state.resultText,envelope);assert.equal(state.error,'');
  assert.equal(manual.createCount,0);assert.equal(manual.deliveryCount,0);
  const automatic=harness(make(true),{snapshotDelivered:false});await automatic.status();
  assert.equal((await automatic.message({type:'bridge.resume'})).ok,true);
  state=(await automatic.status()).state.session;
  assert.equal(automatic.createCount,0);assert.equal(automatic.deliveryCount,1);assert.equal(state.phase,'waiting_model');assert.equal(state.turn,'turn-2');
});

test('restored token stays private; tab is bound and one operation delivers advertised next turn', async () => {
  const initial = saved(); initial.session.automatic = true;
  const h = harness(initial);
  const status = await h.status();
  assert.equal(status.ok, true); assert.equal(JSON.stringify(status).includes(TOKEN), false);
  assert.equal((await h.message({ type: 'studio.snapshot', sessionId: 'session-1', supported: true, conversationKey: 'chat-1' }, { ...h.page, tab: { id: 8 } })).error, 'Untrusted Studio sender');
  assert.equal((await h.message({ type: 'studio.snapshot', sessionId: 'session-1', supported: true, conversationKey: 'chat-1' }, h.page)).ok, true);
  assert.equal((await h.call('turn-1')).ok, true); await h.alarm();
  const resultMsg = h.pageMessages.find(m => m.type === 'studio.deliver'); assert.ok(resultMsg);
  const result = JSON.parse(resultMsg.text); assert.equal(result.type, 'helper-result'); assert.equal(result.result.content[0].text, 'file contents');
  assert.equal((await h.call(result.next_turn)).ok, true); await h.alarm();
  assert.equal(h.calls.filter(x => x.method === 'POST').length, 2);
  const posts = h.calls.filter(x => x.method === 'POST');
  assert.notEqual(posts[1].body.key, posts[0].body.key, 'each advertised next turn gets a fresh idempotency key');
  assert.equal(h.calls[0].body.agent_id, 'agent-1');
  await h.call('turn-1'); await h.alarm();
  assert.equal(h.createCount, 2, 'repeated old model response must not create another operation');
});

test('an oversized saved result resumes after extension reload without refreshing the chat', async () => {
  const initial=saved('paused');
  const resultText=JSON.stringify({type:'helper-result',v:1,turn:'turn-1',next_turn:'turn-2',operation_id:'old-op',name:'read_file',result:{content:[{type:'text',text:'данные'.repeat(12000)}]}});
  Object.assign(initial.session,{paused:true,error:'Result exceeds 32 KiB; reduce the tool output before delivery.',resultText,nextTurn:'turn-2',operationId:'old-op',operationKey:'old-key',pendingCall:{name:'read_file',arguments:{path:'safe.txt'}},automatic:true});
  const injections=[];
  const h=harness(initial,{sendMessage:async(_id,msg)=>{
    if(msg.type==='studio.arm'&&!injections.some(item=>item.files))throw Error('Could not establish connection. Receiving end does not exist.');
    return {ok:true,supported:true,conversationKey:'chat-1',delivered:false,confirmed:msg.type==='studio.deliver'};
  }});
  h.chrome.scripting={executeScript:async args=>{injections.push(args);}};
  await h.status();
  assert.equal((await h.message({type:'bridge.resume'})).ok,true);
  assert.equal(injections.length,2);
  const delivered=h.pageMessages.filter(msg=>msg.type==='studio.deliver');
  assert.equal(delivered.length,1);assert.equal(delivered[0].text,resultText);
  assert.equal(h.createCount,0);assert.equal((await h.status()).state.session.turn,'turn-2');
  assert.equal(h.pageMessages.some(msg=>msg.type==='studio.stage'),false,'the current chat instructions and draft are preserved');
});

test('resume never injects the bridge into a tab that left AI Studio', async () => {
  const initial=saved('paused');
  const resultText=JSON.stringify({type:'helper-result',operation_id:'old-op',next_turn:'turn-2'});
  Object.assign(initial.session,{paused:true,error:'Result exceeds 32 KiB; reduce the tool output before delivery.',resultText,nextTurn:'turn-2',operationId:'old-op',automatic:true});
  let returned=false;
  const h=harness(initial,{sendMessage:async(_id,msg)=>{
    if(!returned)throw Error('Receiving end does not exist.');
    return {ok:true,supported:true,conversationKey:'chat-1',delivered:false,confirmed:msg.type==='studio.deliver'};
  }});
  let injections=0;
  h.chrome.tabs.get=async id=>({id,url:'https://other.example/'});
  h.chrome.scripting={executeScript:async()=>{injections++;}};
  await h.status();
  assert.equal((await h.message({type:'bridge.resume'})).ok,false);
  assert.equal(injections,0);assert.equal(h.createCount,0);assert.equal(h.deliveryCount,0);
  assert.equal((await h.status()).state.session.resultText,resultText);
  returned=true;
  h.chrome.tabs.get=async id=>({id,url:'https://aistudio.google.com/prompts/new_chat'});
  assert.equal((await h.message({type:'bridge.resume'})).ok,true);
  assert.equal(h.pageMessages.filter(msg=>msg.type==='studio.deliver').length,1);
});

test('restoring a pending approval never dispatches it', async () => {
  const h = harness(saved('waiting_approval', 'write_file')); await h.status(); await h.alarm();
  assert.equal(h.createCount, 0, 'restored approval must wait for explicit user approval');
});

test('auto approval is opt-in, applies live, and never replays a restored approval', async () => {
  const h=harness(saved('waiting_model','write_file'));await h.status();
  assert.equal((await h.call('turn-1',h.page,'write_file')).ok,true);
  assert.equal((await h.status()).state.session.phase,'waiting_approval');assert.equal(h.createCount,0);
  await h.message({type:'bridge.preferences',agentId:'agent-1',toolNames:['write_file'],automatic:false,consent:true,autoApprove:true});
  assert.equal(h.createCount,1);
  await h.message({type:'bridge.preferences',agentId:'agent-1',toolNames:['write_file'],automatic:false,consent:true,autoApprove:true});
  assert.equal(h.createCount,1,'repeated preference messages cannot dispatch twice');
  const fresh=harness(saved('waiting_model','write_file'));await fresh.status();
  await fresh.message({type:'bridge.preferences',agentId:'agent-1',toolNames:['write_file'],consent:true,autoApprove:true});
  await fresh.call('turn-1',fresh.page,'write_file');assert.equal(fresh.createCount,1);

  const off=harness(saved('waiting_model','write_file'));await off.status();
  await off.message({type:'bridge.preferences',agentId:'agent-1',toolNames:['write_file'],consent:true,autoApprove:true});
  await off.message({type:'bridge.preferences',agentId:'agent-1',toolNames:['write_file'],consent:true,autoApprove:false});
  await off.call('turn-1',off.page,'write_file');
  assert.equal((await off.status()).state.session.phase,'waiting_approval');assert.equal(off.createCount,0);

  const noConsent=harness(saved('waiting_model','write_file'));await noConsent.status();
  await noConsent.call('turn-1',noConsent.page,'write_file');
  await noConsent.message({type:'bridge.preferences',agentId:'agent-1',toolNames:['write_file'],consent:false,autoApprove:true});
  assert.equal(noConsent.createCount,0);

  const restored=harness(saved('waiting_approval','write_file'),{}, {bridgeAuto:{enabled:true,agentId:'agent-1',toolNames:['write_file'],automatic:false,autoApprove:true,consent:true}});
  await restored.status();await restored.alarm();
  assert.equal(restored.createCount,0);
  await restored.message({type:'bridge.preferences',agentId:'agent-1',toolNames:['write_file'],consent:true,autoApprove:true});
  assert.equal(restored.createCount,0,'re-sending an already enabled preference is not a new opt-in');
});

test('arming injects a missing content receiver only into a verified AI Studio tab', async () => {
  const initial = saved('stopped');
  const h = harness(initial, { sendMessage: async (_id, msg) => {
    if (msg.type === 'studio.arm' && !injections.some(x => x.files)) throw Error('Could not establish connection. Receiving end does not exist.');
    return { ok: true, supported: true, conversationKey: 'chat-1' };
  } });
  const injections = [];
  h.chrome.scripting = { executeScript: async args => { injections.push(args); } };
  await h.status();
  const response = await h.message({ type: 'bridge.arm', consent: true, tabId: 7, agentId: 'agent-1', toolNames: ['read_file'] });
  assert.equal(response.ok, true);
  assert.equal(response.state.session.phase, 'waiting_model');
  assert.equal(injections.length, 2);
  assert.equal(injections[1].target.tabId, 7);
  assert.deepEqual(Array.from(injections[1].files), ['vendor/json-tools.js', 'protocol.js', 'studio-adapter.js', 'content.js']);
});

test('expired authorization pauses a pending operation without retrying or losing its key', async () => {
  const h = harness(saved(), { expireAuth: true });
  await h.status(); await h.call('turn-1');
  const state = (await h.status()).state;
  assert.equal(state.connected, false);
  assert.equal(state.session.phase, 'paused');
  assert.equal(state.session.resumeBlocked, true);
  assert.equal(state.session.pendingCall.name, 'read_file');
  assert.equal(state.session.operationKey, h.calls[0].body.key);
  assert.equal(h.db[KEY].accessToken, '');
  assert.equal((await h.message({ type: 'bridge.resume' })).ok, false);
  await h.alarm(); await h.call('turn-1');
  assert.equal(h.createCount, 1);
});

test('a call received during pending approval stays retryable and does not dispatch', async () => {
  const h = harness(saved('waiting_approval', 'write_file')); await h.status();
  const rejected = await h.call('turn-1');
  assert.equal(rejected.accepted, false);
  const state = (await h.status()).state;
  assert.equal(state.session.phase, 'waiting_approval');
  assert.equal(state.session.pendingCall.name, 'write_file');
  assert.equal(h.createCount, 0);
});

test('page reload restores the configured session without exposing state or executing history', async () => {
  const h = harness(); await h.status();
  const reloaded = await h.message({ type: 'studio.hello' }, h.page);
  assert.equal(reloaded.ok, true); assert.equal(reloaded.accepted, true); assert.equal('state' in reloaded, false);
  const state = (await h.status()).state;
  assert.equal(state.session.paused, false);
  assert.equal(state.session.phase, 'waiting_model');
  assert.equal(h.localData.bridgeAuto.enabled, true);
  const rejected = await h.call('turn-1');
  assert.equal(rejected.ok, false); assert.equal(rejected.accepted, false); assert.equal('state' in rejected, false);
  assert.equal(h.createCount, 0);
});

test('normal model text is not exposed as a deliverable tool result', async () => {
  const h = harness(); await h.status();
  const response = await h.message({ type: 'studio.call', sessionId: 'session-1', conversationKey: 'chat-1', generating: false, text: 'A normal answer' }, h.page);
  assert.equal(response.ok, true); assert.equal(response.accepted, true); assert.equal('state' in response, false);
  const state = (await h.status()).state;
  assert.equal(state.session.phase, 'ready');
  assert.equal(state.session.resultText, '');
});

test('cleanup preference survives worker restart and final prose includes completed operation IDs', async () => {
  const h=harness();await h.status();
  await h.message({type:'bridge.cleanupPreference',enabled:true});
  assert.equal(h.localData.hideServiceMessages,true);
  const restarted=harness(saved(),{},h.localData);await restarted.status();
  assert.equal((await restarted.status()).state.hideServiceMessages,true);
  await restarted.call('turn-1');await restarted.alarm();
  await restarted.message({type:'bridge.deliver',submit:true});
  assert.deepEqual((await restarted.status()).state.session.completedOperationIds,['op-1']);
  await restarted.message({type:'studio.call',sessionId:'session-1',conversationKey:'chat-1',generating:false,text:'Final answer'},restarted.page);
  const final=restarted.pageMessages.filter(m=>m.type==='studio.cleanup').at(-1);
  assert.deepEqual(final,{type:'studio.cleanup',sessionId:'session-1',enabled:true,final:true,operationIds:['op-1']});
  await restarted.message({type:'bridge.cleanupPreference',enabled:false});
  assert.equal(restarted.localData.hideServiceMessages,false);
  await restarted.message({type:'studio.call',sessionId:'session-1',conversationKey:'chat-1',generating:false,text:'Another final answer'},restarted.page);
  const disabled=restarted.pageMessages.filter(m=>m.type==='studio.cleanup').at(-1);
  assert.equal(disabled.enabled,false);assert.equal(disabled.final,true);
});

test('automatic start respects drafts, pending operations, another bound tab and explicit stop',async()=>{
  const settings={enabled:true,agentId:'agent-1',toolNames:['read_file'],automatic:true,autoApprove:false,consent:true};
  let empty=false;
  const h=harness(saved('idle'),{sendMessage:async(_id,msg)=>({ok:msg.type!=='studio.stage'||empty,supported:true,conversationKey:'chat-1'})});
  h.localData.bridgeAuto=settings;
  await h.status();
  assert.equal((await h.message({type:'studio.hello'},h.page)).accepted,false);
  assert.equal(h.pageMessages.filter(m=>m.type==='studio.deliver').length,0);
  empty=true;
  assert.equal((await h.message({type:'studio.hello',pageInstance:'same-page'},h.page)).accepted,true);
  const armCount=h.pageMessages.filter(m=>m.type==='studio.arm').length;
  assert.equal((await h.message({type:'studio.hello',pageInstance:'same-page'},h.page)).accepted,true);
  assert.equal(h.pageMessages.filter(m=>m.type==='studio.arm').length,armCount);
  const other={...h.page,tab:{id:99}};
  assert.equal((await h.message({type:'studio.hello'},other)).accepted,false);
  assert.equal((await h.status()).state.session.tabId,7);
  await h.message({type:'bridge.stop'});
  assert.equal(h.localData.bridgeAuto.enabled,false);
  assert.equal((await h.message({type:'studio.hello'},h.page)).accepted,false);

  const initial=saved('recovering');Object.assign(initial.session,{pendingCall:{name:'read_file',arguments:{path:'x'}},operationKey:'do-not-retry'});
  const pending=harness(initial);pending.localData.bridgeAuto=settings;await pending.status();
  assert.equal((await pending.message({type:'studio.hello'},pending.page)).accepted,false);
  const state=(await pending.status()).state.session;
  assert.equal(state.phase,'paused');assert.equal(state.operationKey,'do-not-retry');
  assert.equal(pending.createCount,0);
});

test('saved preferences survive restart and offline discovery; hello retries after agent returns without OAuth',async()=>{
  let online=false, toolLookups=0, oauth=0;
  const settings={enabled:true,agentId:'agent-1',toolNames:['read_file'],automatic:true,autoApprove:false,consent:true};
  const initial=saved('idle');initial.agents=[];initial.tools=[];
  const h=harness(initial,{listAgents:()=>[{id:'agent-1',online}],listTools:()=>{toolLookups++;return online?[tool()]:[{name:'list_agents'}];}}, {bridgeAuto:settings});
  h.chrome.identity.launchWebAuthFlow=async()=>{oauth++;throw Error('should not request OAuth');};
  const offline=await h.status();
  assert.deepEqual(offline.state.autoSettings,settings);
  assert.equal(offline.state.autoStatus,'Агент не в сети');
  assert.equal((await h.message({type:'studio.hello'},h.page)).accepted,false);
  assert.deepEqual(h.localData.bridgeAuto,settings);
  online=true;h.advanceTime(5001);
  assert.equal((await h.message({type:'studio.hello'},h.page)).accepted,true);
  assert.equal((await h.status()).state.session.phase,'waiting_model');
  assert.equal(toolLookups>=2,true);
  assert.equal(oauth,0);
  await h.message({type:'bridge.stop'});
  assert.equal(h.localData.bridgeAuto.enabled,false);assert.equal(h.localData.bridgeAuto.consent,true);
  assert.equal(h.localData.bridgeAuto.agentId,'agent-1');assert.deepEqual(Array.from(h.localData.bridgeAuto.toolNames),['read_file']);
});

test('Studio hello without saved preferences safely stays unarmed',async()=>{
  const h=harness(saved('idle'));
  await h.status();
  const result=await h.message({type:'studio.hello'},h.page);
  assert.equal(result.accepted,false);assert.equal(result.error,undefined);
});

test('one-time migration selects a sole online agent and safe tools without a click or OAuth',async()=>{
  for(const startOnline of [true,false]){
    let online=startOnline;
    const initial=saved('idle');initial.session={...initial.session,agentId:'',toolNames:[],pendingCall:null};
    const h=harness(initial,{listAgents:()=>[{id:'agent-1',online}],listTools:()=>[tool(),tool('write_file')]},{bridgeAutoSetupV1:false});
    let oauth=0;h.chrome.identity.launchWebAuthFlow=async()=>{oauth++;throw Error('unexpected OAuth');};
    const status=await h.status();
    if(startOnline){assert.deepEqual(status.state.autoSettings,{enabled:true,agentId:'agent-1',toolNames:['read_file'],automatic:true,autoApprove:false,consent:true});assert.equal(h.localData.bridgeAutoSetupV1,true);}
    else {assert.equal(h.localData.bridgeAutoSetupV1,false);assert.equal(h.localData.bridgeAuto,undefined);}
    assert.equal((await h.message({type:'studio.hello'},h.page)).accepted,startOnline);
    if(!startOnline){online=true;h.advanceTime(5001);const returned=await h.status();assert.deepEqual(returned.state.autoSettings,{enabled:true,agentId:'agent-1',toolNames:['read_file'],automatic:true,autoApprove:false,consent:true});assert.equal(h.localData.bridgeAutoSetupV1,true);assert.equal((await h.message({type:'studio.hello'},h.page)).accepted,true);}
    assert.equal(oauth,0);
  }
});

test('legacy disabled settings migrate once, then stopped settings survive worker restart',async()=>{
  const initial=saved('idle');initial.session={...initial.session,agentId:'',toolNames:[],pendingCall:null};
  const h=harness(initial,{}, {bridgeAuto:{enabled:false},bridgeAutoSetupV1:false});
  await h.status();assert.equal(h.localData.bridgeAuto.enabled,true);
  await h.message({type:'bridge.stop'});
  const restarted=harness(saved('idle'),{},h.localData);await restarted.status();
  assert.equal(restarted.localData.bridgeAuto.enabled,false);
  assert.equal(restarted.localData.bridgeAutoSetupV1,true);
});

test('migration waits on ambiguous agents and manual stop prevents future migration',async()=>{
  const initial=saved('idle');initial.agents=[];initial.session={...initial.session,agentId:'',toolNames:[],pendingCall:null};
  const h=harness(initial,{listAgents:()=>[{id:'a',online:true},{id:'b',online:true}],listTools:()=>[tool()]},{bridgeAutoSetupV1:false});
  await h.status();assert.equal(h.localData.bridgeAuto,undefined);assert.equal(h.localData.bridgeAutoSetupV1,false);
  await h.message({type:'bridge.stop'});
  h.advanceTime(5001);await h.status();
  assert.equal(h.localData.bridgeAuto.enabled,false);assert.equal(h.localData.bridgeAutoSetupV1,true);
});

test('preferences persist revoked consent and an empty tool selection without restoring old values',async()=>{
  const h=harness(saved('idle'),{}, {bridgeAuto:{enabled:true,agentId:'agent-1',toolNames:['read_file'],automatic:true,autoApprove:false,consent:true}});
  await h.status();
  await h.message({type:'bridge.preferences',agentId:'agent-1',toolNames:['read_file'],automatic:true,consent:false});
  assert.equal(h.localData.bridgeAuto.enabled,false);assert.equal(h.localData.bridgeAuto.consent,false);
  await h.message({type:'bridge.preferences',agentId:'agent-1',toolNames:[],automatic:true,consent:true});
  assert.equal(h.localData.bridgeAuto.enabled,false);assert.equal(h.localData.bridgeAuto.consent,true);
  assert.deepEqual(Array.from(h.localData.bridgeAuto.toolNames),[]);
});

test('conversation change pauses and prevents result delivery', async () => {
  const h = harness(); await h.status();
  await h.message({ type: 'studio.snapshot', sessionId: 'session-1', supported: false, error:'AI Studio session unavailable' }, h.page);
  await h.call('turn-1'); await h.alarm();
  assert.equal(h.createCount, 0); assert.equal(h.deliveryCount, 0);
});

test('route changes do not change internal chat identity or replay pending work', async () => {
  const initial=saved('delivery_uncertain');Object.assign(initial.session,{turnless:true,conversationKey:'active-chat',resultText:'saved result',operationId:'op-1',nextTurn:'turn-2'});
  const h=harness(initial);await h.status();
  for(const url of ['https://aistudio.google.com/prompts/new_chat','https://aistudio.google.com/app/prompts/saved?x=1']){
    assert.equal((await h.message({type:'studio.tick',sessionId:'session-1',supported:true,conversationKey:'active-chat'},{...h.page,url})).accepted,true);
    assert.equal((await h.status()).state.session.conversationKey,'active-chat');
  }
  assert.equal(h.createCount,0);assert.equal(h.deliveryCount,0);
});

test('reload resets an old paused no-pending session on the same trusted tab',async()=>{
  const initial=saved('paused');Object.assign(initial.session,{turnless:true,paused:true,resumeBlocked:true,pausedPhase:'waiting_model',error:'Conversation changed',conversationKey:'/prompts/old'});
  const settings={enabled:true,agentId:'agent-1',toolNames:['read_file'],automatic:true,consent:true};
  const h=harness(initial,{sendMessage:async(_id,msg)=>msg.type==='studio.arm'?{supported:true,conversationKey:'active-chat'}:{ok:true}},{bridgeAuto:settings});await h.status();
  assert.equal((await h.message({type:'studio.hello',pageInstance:'new-page'},{...h.page,url:'https://aistudio.google.com/a-different-route'})).accepted,true);
  const session=(await h.status()).state.session;
  assert.equal(session.phase,'waiting_model');assert.equal(session.conversationKey,'active-chat');assert.equal(session.turnless,true);
  assert.equal(h.createCount,0);assert.equal(h.pageMessages.find(msg=>msg.type==='studio.stage').text,'');
});

test('a new chat can retry startup after a temporary Gate refresh failure', async () => {
  let offline = true;
  const settings = {enabled:true,agentId:'agent-1',toolNames:['read_file'],automatic:true,consent:true};
  const h = harness(saved(), {listAgents: () => {
    if (offline) throw Error('Gate temporarily unavailable');
    return [{id:'agent-1',name:'test agent',online:true}];
  }}, {bridgeAuto:settings});
  await h.status();
  const transition = {type:'studio.snapshot',sessionId:'session-1',supported:true,conversationKey:'chat-1',conversationReplaced:true,error:'Conversation was replaced'};
  await h.message(transition,h.page);
  assert.equal(h.pageMessages.some(m => m.type === 'studio.stage'),false);
  offline = false; h.advanceTime(6000);
  await h.message(transition,h.page);
  const session = (await h.status()).state.session;
  assert.notEqual(session.turn,'turn-1');
  assert.equal(session.phase,'waiting_model');
  assert.equal(h.pageMessages.filter(m => m.type === 'studio.stage').length,1);
  assert.equal(h.pageMessages.find(m => m.type === 'studio.stage').text, '');
  assert.equal(h.createCount,0);
});

test('an explicit New chat can rearm after reload of an automatically paused replacement', async () => {
  const initial=saved('paused');
  Object.assign(initial.session,{paused:true,resumeBlocked:true,error:'Conversation was replaced'});
  const settings={enabled:true,agentId:'agent-1',toolNames:['read_file'],automatic:true,consent:true};
  const h=harness(initial,{}, {bridgeAuto:settings});await h.status();
  assert.equal((await h.message({type:'studio.hello',pageInstance:'new-page'},h.page)).accepted,false);
  assert.equal((await h.message({type:'studio.hello',pageInstance:'new-page',conversationReplaced:true},h.page)).accepted,true);
  const session=(await h.status()).state.session;
  assert.equal(session.phase,'waiting_model');assert.notEqual(session.turn,'turn-1');
  assert.equal(h.pageMessages.find(m=>m.type==='studio.stage').text, '');
  assert.equal(h.createCount,0);
});

test('a cleared chat automatically gets a fresh turn once, while unsafe states stay untouched', async () => {
  const settings={enabled:true,agentId:'agent-1',toolNames:['read_file'],automatic:true,autoApprove:false,consent:true};
  const fresh=()=>{const initial=saved('waiting_model');initial.session.nextTurn='stale-next-turn';const h=harness(initial,{}, {bridgeAuto:settings});return h;};
  const h=fresh();await h.status();
  await h.message({type:'studio.snapshot',sessionId:'session-1',supported:true,conversationKey:'chat-1',conversationReplaced:true},h.page);
  let state=(await h.status()).state.session;
  assert.equal(state.phase,'waiting_model');assert.equal(state.tabId,7);assert.notEqual(state.sessionId,'session-1');assert.notEqual(state.turn,'turn-1');
  assert.equal(h.pageMessages.filter(m=>m.type==='studio.arm').length,1);
  const staged=h.pageMessages.find(m=>m.type==='studio.stage');assert.equal(staged.text,'');assert.equal(state.turnless,true);assert.ok(staged.systemText);
  await h.message({type:'studio.snapshot',sessionId:'session-1',supported:true,conversationKey:'chat-1',conversationReplaced:true},h.page);
  assert.equal(h.pageMessages.filter(m=>m.type==='studio.arm').length,1,'repeated observer reports do not create extra turns');

  const paused=fresh();await paused.status();await paused.message({type:'bridge.pause'});
  await paused.message({type:'studio.snapshot',sessionId:'session-1',supported:true,conversationKey:'chat-1',conversationReplaced:true},paused.page);
  assert.equal((await paused.status()).state.session.phase,'paused');assert.equal(paused.pageMessages.some(m=>m.type==='studio.arm'),false);

  const disabled=harness(saved('stopped'),{}, {bridgeAuto:{...settings,enabled:false}});await disabled.status();
  await disabled.message({type:'studio.snapshot',sessionId:'session-1',supported:true,conversationKey:'chat-1',conversationReplaced:true},disabled.page);
  assert.equal(disabled.pageMessages.some(m=>m.type==='studio.arm'),false);

  for(const phase of ['recovering','delivery_uncertain']){
    const initial=saved(phase);Object.assign(initial.session,{pendingCall:{name:'read_file',arguments:{path:'x'}},operationKey:'do-not-replay'});
    const guarded=harness(initial,{}, {bridgeAuto:settings});await guarded.status();
    await guarded.message({type:'studio.snapshot',sessionId:'session-1',supported:true,conversationKey:'chat-1',conversationReplaced:true},guarded.page);
    const savedSession=(await guarded.status()).state.session;
    assert.equal(savedSession.operationKey,'do-not-replay');assert.equal(savedSession.pendingCall.arguments.path,'x');assert.equal(guarded.pageMessages.some(m=>m.type==='studio.arm'),false);
    assert.equal(guarded.createCount,0);
  }

  const resultOnly=saved('waiting_model');Object.assign(resultOnly.session,{resultText:'unconfirmed result',operationId:'op-unknown'});
  const uncertain=harness(resultOnly,{}, {bridgeAuto:settings});await uncertain.status();
  await uncertain.message({type:'studio.snapshot',sessionId:'session-1',supported:true,conversationKey:'chat-1',conversationReplaced:true},uncertain.page);
  assert.equal((await uncertain.status()).state.session.resultText,'unconfirmed result');assert.equal(uncertain.pageMessages.some(m=>m.type==='studio.arm'),false);

  const draft=harness(saved('waiting_model'),{sendMessage:async(_id,msg)=>msg.type==='studio.arm'?{supported:true,conversationKey:'chat-1'}:msg.type==='studio.stage'?{ok:false,error:'AI Studio prompt field is not empty; existing text was preserved'}:{ok:true}}, {bridgeAuto:settings});
  await draft.status();
  await draft.message({type:'studio.snapshot',sessionId:'session-1',supported:false,conversationKey:'chat-1',conversationReplaced:true},draft.page);
  assert.equal(draft.pageMessages.some(m=>m.type==='studio.stage'),true);
  assert.equal(draft.pageMessages.some(m=>m.type==='studio.deliver'),false);
  assert.equal((await draft.status()).state.session.phase,'stopped');
});

test('uncertain delivery is checked by snapshot and never submitted a second time', async () => {
  const initial = saved('result_ready'); initial.session.operationId = 'op-1'; initial.session.nextTurn = 'turn-2'; initial.session.resultText = JSON.stringify({ type: 'helper-result', v: 1, turn: 'turn-1', next_turn: 'turn-2', operation_id: 'op-1', name: 'read_file', result: { content: [] } });
  const h = harness(initial, { sendMessage: async (_id, msg, ctx) => {
    if (msg.type === 'studio.deliver') { if (ctx.deliveryCount) throw Error('counter unavailable'); return { ok: false, attempted: true }; }
    if (msg.type === 'studio.snapshot') return { ok: true, delivered: false, supported: true, conversationKey: 'chat-1', generating: false, editorEmpty: true };
    if (msg.type === 'studio.stage' || msg.type === 'studio.arm') return { ok: true, supported: true, conversationKey: 'chat-1' };
    return { ok: true };
  } });
  await h.status();
  await h.message({ type: 'bridge.deliver', submit: true });
  assert.equal(h.pageMessages.filter(m => m.type === 'studio.deliver').length, 1, 'one user delivery command makes one attempt');
  await h.alarm();
  assert.equal(h.pageMessages.filter(m => m.type === 'studio.deliver').length, 1, 'delivery is never blindly retried');
  assert.equal(h.pageMessages.filter(m => m.type === 'studio.snapshot').length >= 1, true);
});

test('a valid call for the advertised next turn proves delivery without resending the previous result', async () => {
  const initial=saved('delivery_uncertain');
  Object.assign(initial.session,{pendingCall:{name:'read_file',arguments:{path:'old.txt'}},nextTurn:'turn-2',operationId:'old-op',operationKey:'old-key',resultText:'old result'});
  const h=harness(initial);
  await h.status();
  for(const turn of ['turn-1','wrong-turn'])assert.equal((await h.call(turn)).accepted,false);
  assert.equal((await h.status()).state.session.phase,'delivery_uncertain');
  assert.equal(h.createCount,0);
  const response=await h.call('turn-2');
  assert.equal(response.accepted,true);
  const state=(await h.status()).state.session;
  assert.equal(state.turn,'turn-2');assert.equal(state.count,1);
  assert.equal(state.pendingCall.arguments.path,'safe.txt');
  assert.equal(h.createCount,1);assert.notEqual(h.calls[0].body.key,'old-key');
  assert.equal(h.pageMessages.filter(m=>m.type==='studio.deliver').length,0);
});

test('prose and unselected tools cannot prove delivery; a valid call can across route identities',async()=>{
  const initial=saved('delivery_uncertain');initial.session.nextTurn='turn-2';
  const h=harness(initial);await h.status();
  const base={type:'studio.call',sessionId:'session-1',conversationKey:'chat-1',generating:false};
  for(const text of ['A final answer','```helper-call\n'+JSON.stringify({v:1,turn:'turn-2',name:'delete_path',arguments:{path:'x'}})+'\n```']){
    assert.equal((await h.message({...base,text},h.page)).accepted,false);
  }
  const text='```helper-call\n'+JSON.stringify({v:1,turn:'turn-2',name:'read_file',arguments:{path:'x'}})+'\n```';
  assert.equal((await h.message({...base,text,conversationKey:'unrelated-legacy-value'},h.page)).accepted,true);
  assert.equal((await h.status()).state.session.phase,'recovering');
  assert.equal(h.createCount,1);
});

test('lost receiver reattaches the same tab and completes only from delivery proof',async()=>{
  const initial=saved('delivery_uncertain');Object.assign(initial.session,{resultText:JSON.stringify({type:'helper-result',operation_id:'op-1'}),operationId:'op-1',nextTurn:'turn-2',pageInstance:'old-page'});
  const h=harness(initial,{}, {bridgeAuto:{enabled:true,agentId:'agent-1',toolNames:['read_file'],automatic:false,consent:true}});await h.status();
  const hello=await h.message({type:'studio.hello',pageInstance:'new-page'},h.page);
  assert.equal(hello.accepted,true);assert.equal(h.createCount,0);assert.equal(h.deliveryCount,0);
  assert.equal((await h.status()).state.session.phase,'waiting_model');
  const arm=h.pageMessages.find(m=>m.type==='studio.arm');assert.equal(arm.sessionId,'session-1');assert.equal(arm.recover,true);assert.equal(arm.expectedConversationKey,undefined);assert.equal(arm.recoverTurn,'turn-2');
});

test('reattach with no delivery proof stays uncertain and never resends',async()=>{
  const initial=saved('delivery_uncertain');Object.assign(initial.session,{resultText:'saved result',operationId:'op-1',nextTurn:'turn-2',pageInstance:'old-page'});
  const h=harness(initial,{sendMessage:async(_id,msg)=>msg.type==='studio.arm'?{supported:true,conversationKey:'chat-1'}:msg.type==='studio.snapshot'?{supported:true,conversationKey:'chat-1',delivered:false}:{ok:true}}, {bridgeAuto:{enabled:true,agentId:'agent-1',toolNames:['read_file'],automatic:false,consent:true}});await h.status();
  assert.equal((await h.message({type:'studio.hello',pageInstance:'new-page'},h.page)).accepted,true);
  const state=(await h.status()).state.session;assert.equal(state.phase,'delivery_uncertain');assert.equal(state.resultText,'saved result');
  assert.equal(h.createCount,0);assert.equal(h.deliveryCount,0);
});

test('recovery requires an available receiver and does not recover a manually paused session',async()=>{
  const make=()=>{const initial=saved('delivery_uncertain');Object.assign(initial.session,{resultText:'saved',operationId:'op-1',nextTurn:'turn-2',pageInstance:'old-page'});return initial;};
  const auto={enabled:true,agentId:'agent-1',toolNames:['read_file'],automatic:false,consent:true};
  const other=harness(make(),{sendMessage:async(_id,msg)=>msg.type==='studio.arm'?{supported:false}:{supported:false,delivered:true}}, {bridgeAuto:auto});await other.status();
  assert.equal((await other.message({type:'studio.hello',pageInstance:'new-page'},other.page)).accepted,false);
  assert.equal((await other.status()).state.session.phase,'delivery_uncertain');assert.equal(other.createCount,0);assert.equal(other.deliveryCount,0);
  const paused=harness(make(),{}, {bridgeAuto:auto});await paused.status();await paused.message({type:'bridge.pause'},paused.panel);
  assert.equal((await paused.message({type:'studio.hello',pageInstance:'new-page'},paused.page)).accepted,false);
  assert.equal((await paused.status()).state.session.phase,'paused');assert.equal(paused.pageMessages.some(m=>m.type==='studio.arm'&&m.recover),false);
  const legacy=make();legacy.session.phase='paused';legacy.session.paused=true;legacy.session.error='AI Studio session is unavailable; stop and re-arm this chat';
  const restored=harness(legacy,{}, {bridgeAuto:auto});await restored.status();
  assert.equal((await restored.message({type:'studio.hello',pageInstance:'new-page'},restored.page)).accepted,true);
  assert.equal((await restored.status()).state.session.phase,'waiting_model');assert.equal(restored.createCount,0);assert.equal(restored.deliveryCount,0);
});

test('legacy oversized result reload is verified before one automatic delivery',async()=>{
  const key='Result exceeds 32 KiB; reduce the tool output before delivery.',result=JSON.stringify({type:'helper-result',turn:'turn-1',next_turn:'turn-2',operation_id:'op-old'});
  const make=()=>{const initial=saved('paused');Object.assign(initial.session,{paused:true,phase:'paused',error:key,pausedPhase:'ready',resultText:result,operationId:'op-old',operationKey:'key-old',nextTurn:'turn-2',pageInstance:'old-page',pendingCall:{name:'read_file',arguments:{path:'safe.txt'},turn:'turn-1'},automatic:true});return initial;};
  const settings={enabled:true,agentId:'agent-1',toolNames:['read_file'],automatic:true,autoApprove:false,consent:true};
  const h=harness(make(),{sendMessage:async(_id,msg)=>msg.type==='studio.arm'?{supported:true,conversationKey:'chat-1'}:msg.type==='studio.snapshot'?{supported:true,conversationKey:'chat-1',delivered:false}:{ok:true,confirmed:true,supported:true,conversationKey:'chat-1'}},{bridgeAuto:settings});await h.status();
  assert.equal((await h.message({type:'studio.hello',pageInstance:'new-page'},h.page)).accepted,true);
  let state=(await h.status()).state.session;
  assert.equal(state.phase,'waiting_model');assert.equal(state.turn,'turn-2');assert.equal(h.createCount,0);assert.equal(h.deliveryCount,0);
  assert.equal(h.pageMessages.filter(m=>m.type==='studio.deliver').length,1);

  const wrong=harness(make(),{sendMessage:async(_id,msg)=>msg.type==='studio.arm'?{supported:false}:{supported:true,delivered:true}},{bridgeAuto:settings});await wrong.status();
  assert.equal((await wrong.message({type:'studio.hello',pageInstance:'new-page'},wrong.page)).accepted,false);
  assert.equal(wrong.pageMessages.some(m=>m.type==='studio.deliver'),false);assert.equal(wrong.createCount,0);

  const manual=harness(make(),{}, {bridgeAuto:settings});await manual.status();await manual.message({type:'bridge.pause'});
  assert.equal((await manual.message({type:'studio.hello',pageInstance:'new-page'},manual.page)).accepted,false);
  assert.equal(manual.pageMessages.some(m=>m.type==='studio.arm'&&m.recover),false);assert.equal(manual.createCount,0);assert.equal(manual.deliveryCount,0);
});

test('legacy route identities migrate to the internal active chat key',async()=>{
  const initial=saved('delivery_uncertain');
  initial.session.conversationKey='/app/prompts/test?legacy=1#data-conversation-id=chat-1';
  initial.session.temporaryConversationKey='/app/prompts/new_chat';
  Object.assign(initial.session,{resultText:'saved',operationId:'op-1',nextTurn:'turn-2'});
  const key='active-chat';
  const h=harness(initial,{sendMessage:async(_id,msg)=>msg.type==='studio.arm'?{supported:true,conversationKey:key}:msg.type==='studio.snapshot'?{supported:true,conversationKey:key,delivered:true}:{ok:true}}, {bridgeAuto:{enabled:true,agentId:'agent-1',toolNames:['read_file'],automatic:false,consent:true}});
  await h.status();
  assert.equal((await h.status()).state.session.conversationKey,key);
  assert.equal((await h.message({type:'studio.hello',pageInstance:'new-page'},h.page)).accepted,true);
  assert.equal((await h.status()).state.session.phase,'waiting_model');
  assert.equal((await h.status()).state.session.temporaryConversationKey,undefined);
  const arm=h.pageMessages.find(m=>m.type==='studio.arm');
  assert.equal(arm.expectedConversationKey,undefined);
  assert.equal(h.createCount,0);assert.equal(h.deliveryCount,0);
});

test('paused conversation change recovers on the same internal key regardless of route',async()=>{
  const key='active-chat';
  const make=()=>{const initial=saved('paused');Object.assign(initial.session,{paused:true,resumeBlocked:true,conversationKey:key,resultText:'saved',operationId:'op-1',nextTurn:'turn-2',pageInstance:'old-page',error:'Conversation changed'});return initial;};
  const auto={enabled:true,agentId:'agent-1',toolNames:['read_file'],automatic:false,consent:true};
  const h=harness(make(),{sendMessage:async(_id,msg)=>msg.type==='studio.arm'?{supported:true,conversationKey:key}:msg.type==='studio.snapshot'?{supported:true,conversationKey:key,delivered:true}:{ok:true}}, {bridgeAuto:auto});
  await h.status();
  assert.equal((await h.message({type:'studio.hello',pageInstance:'new-page'},{...h.page,url:'https://aistudio.google.com/any/route?x=1'})).accepted,true);
  const state=(await h.status()).state.session;
  assert.equal(state.phase,'waiting_model');assert.equal(state.turn,'turn-2');
  assert.equal(h.createCount,0);assert.equal(h.deliveryCount,0);
});

test('paused conversation change accepts same-key tick proof and exact next turn without resending old result',async()=>{
  const initial=saved('paused');Object.assign(initial.session,{paused:true,resumeBlocked:true,resultText:'saved result',operationId:'old-op',nextTurn:'turn-2',error:'Conversation changed'});
  const h=harness(initial,{sendMessage:async(_id,msg)=>msg.type==='studio.snapshot'?{supported:true,conversationKey:'chat-1',delivered:true}:{ok:true}});
  await h.status();
  await h.message({type:'studio.tick',sessionId:'session-1',supported:true,conversationKey:'chat-1'},h.page);
  let state=(await h.status()).state.session;
  assert.equal(state.phase,'waiting_model');assert.equal(state.turn,'turn-2');assert.equal(state.operationId,'');
  assert.equal(h.deliveryCount,0);assert.equal(h.createCount,0);

  const next=await h.call('turn-2');
  assert.equal(next.accepted,true);
  state=(await h.status()).state.session;
  assert.equal(state.phase,'recovering');assert.equal(state.pendingCall.turn,'turn-2');
  assert.notEqual(h.calls.find(x=>x.method==='POST').body.key,'old-op');
  assert.equal(h.deliveryCount,0);assert.equal(h.createCount,1);
});

test('manual pause still blocks recovery after a conversation change',async()=>{
  const initial=saved('paused');Object.assign(initial.session,{paused:true,resumeBlocked:true,resultText:'saved',operationId:'op-1',nextTurn:'turn-2',error:'Conversation changed'});
  const h=harness(initial);await h.status();
  await h.message({type:'bridge.pause'});
  assert.equal((await h.message({type:'studio.tick',sessionId:'session-1',supported:true,conversationKey:'chat-1'},h.page)).accepted,true);
  assert.equal((await h.status()).state.session.phase,'paused');
  assert.equal(h.pageMessages.some(m=>m.type==='studio.snapshot'),false);
  assert.equal(h.createCount,0);assert.equal(h.deliveryCount,0);
});

test('exact next turn proves delivery directly from a paused conversation change',async()=>{
  const initial=saved('paused');Object.assign(initial.session,{paused:true,resumeBlocked:true,resultText:'saved result',operationId:'old-op',nextTurn:'turn-2',error:'Conversation changed'});
  const h=harness(initial);
  await h.status();
  assert.equal((await h.call('wrong-turn')).accepted,false);
  assert.equal(h.createCount,0);
  assert.equal((await h.call('turn-2')).accepted,true);
  assert.equal(h.createCount,1);
  assert.equal(h.calls.filter(x=>x.method==='POST')[0].body.arguments.path,'safe.txt');
  assert.equal(h.deliveryCount,0);
});

const largeWriteTool={name:'write_file',inputSchema:{type:'object',properties:{path:{type:'string'},content:{type:'string'},if_absent:{type:'boolean',const:true}},required:['path','content','if_absent'],additionalProperties:false}};
test('an explicit save recovers a large write from the existing answer and persists past the session quota',async()=>{
 const content='code \u044f \ud83d\ude00\n'.repeat(200000),text=JSON.stringify({v:1,name:'write_file',arguments:{path:'large.html',content,if_absent:true}});
 const initial=saved('paused','write_file');initial.tools=[largeWriteTool];initial.session.error='Reply exceeds 64 KiB';initial.session.paused=true;
 const h=harness(initial,{sessionQuota:65536,listTools:()=>[largeWriteTool],sendMessage:async(_id,msg)=>msg.type==='studio.captureWrite'?{ok:true,text,pageInstance:'page-large',conversationKey:'chat-1'}:{ok:true,supported:true,delivered:true,conversationKey:'chat-1'}},{bridgeAuto:{enabled:true,consent:true,autoApprove:true,agentId:'agent-1',toolNames:['write_file']}});
 await h.status();assert.equal((await h.message({type:'bridge.saveFromReply'})).ok,true);
 assert.equal(h.createCount,1);assert.equal(h.calls.find(call=>call.method==='POST').body.arguments.content,content);
 assert.equal(h.db[KEY].largeState,true);assert.equal(h.localData.webcodexLargeSessionState.session.pendingCall.arguments.content,content);
 await h.alarm();assert.equal((await h.status()).state.session.phase,'result_ready');
});
test('explicit file recovery never bypasses consent, selected tools or unfinished operations',async()=>{
 for(const variant of ['consent','tool','pending']){
  const initial=saved('paused','write_file');initial.tools=[largeWriteTool];initial.session.paused=true;
  if(variant==='tool')initial.session.toolNames=[];
  if(variant==='pending')initial.session.operationKey='saved-key';
  const h=harness(initial,{listTools:()=>[largeWriteTool]},{bridgeAuto:{enabled:true,consent:variant!=='consent',agentId:'agent-1',toolNames:['write_file']}});
  await h.status();assert.equal((await h.message({type:'bridge.saveFromReply'})).ok,false);assert.equal(h.createCount,0);
 }
});

test('explicit recovery executes the latest selected call once and keeps its result recoverable',async()=>{
  const selected=tool('list_directory'),initial=saved('waiting_model','list_directory');initial.tools=[selected];
  const text=JSON.stringify({v:1,name:'list_directory',arguments:{path:'W:\\_python'}});
  const h=harness(initial,{listTools:()=>[selected],sendMessage:async(_id,message)=>message.type==='studio.captureCall'?{ok:true,text,pageInstance:'page-last-call',conversationKey:'chat-1'}:{ok:true,supported:true,conversationKey:'chat-1'}},{bridgeAuto:{enabled:true,consent:true,agentId:'agent-1',toolNames:['list_directory']}});
  await h.status();assert.equal((await h.message({type:'bridge.runFromReply'})).ok,true);assert.equal(h.createCount,1);
  assert.equal(h.calls.find(call=>call.method==='POST').body.arguments.path,'W:\\_python');
  assert.equal((await h.message({type:'bridge.runFromReply'})).ok,false,'an unfinished operation cannot be repeated');assert.equal(h.createCount,1);
});
