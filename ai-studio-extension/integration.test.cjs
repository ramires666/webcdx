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
function harness(initial = saved(), tabBehavior = {}) {
  const listeners = {}, calls = [], pageMessages = [], db = { [KEY]: structuredClone(initial) };
  let createCount = 0, deliveryCount = 0;
  const chrome = {
    runtime: { id: 'test-id', getURL: p => 'chrome-extension://test-id/' + p, onMessage: { addListener: fn => { listeners.message = fn; } } },
    storage: { session: { setAccessLevel: async () => {}, get: async k => ({ [k]: db[k] && structuredClone(db[k]) }), set: async x => { for (const [k,v] of Object.entries(x)) db[k] = structuredClone(v); } }, local: { get: async () => ({}), set: async () => {}, remove: async () => {}, setAccessLevel: async () => {} } },
    action: { onClicked: { addListener: () => {} } }, sidePanel: { open: async () => {} }, alarms: { create: () => {}, onAlarm: { addListener: fn => { listeners.alarm = fn; } } },
    permissions: { contains: async () => true },
    tabs: { get: async id => ({ id, url: 'https://aistudio.google.com/app/prompts/test' }), sendMessage: async (id, msg) => {
      pageMessages.push(structuredClone(msg));
      if (tabBehavior.sendMessage) return tabBehavior.sendMessage(id, msg, { calls, get deliveryCount() { return deliveryCount; } });
      if (msg.type === 'studio.arm') return { ok: true, supported: true, conversationKey: 'chat-1' };
      if (msg.type === 'studio.stage') return { ok: true };
      if (msg.type === 'studio.deliver') { deliveryCount++; return { ok: true, confirmed: true, supported: true, conversationKey: 'chat-1' }; }
      if (msg.type === 'studio.snapshot') return { ok: true, delivered: true, supported: true, conversationKey: 'chat-1', generating: false, editorEmpty: true };
      return { ok: true };
    } },
    identity: { getRedirectURL: () => 'https://test.chromiumapp.org/webcodex' }
  };
  const fetch = async (url, init = {}) => {
    const u = new URL(url), method = init.method || 'GET';
    if (u.pathname === '/mcp/v5') {
      const body = JSON.parse(init.body || '{}');
      if (body.method === 'initialize') return json({ jsonrpc: '2.0', id: body.id, result: {} });
      if (body.method === 'tools/list') return json({ jsonrpc: '2.0', id: body.id, result: { tools: [tool(), tool('write_file')] } });
      if (body.method === 'tools/call') return json({ jsonrpc: '2.0', id: body.id, result: { structuredContent: { agents: [{ id: 'agent-1', name: 'test agent', online: true }] } } });
      if (body.method === 'notifications/initialized') return new Response(null, { status: 202 });
    }
    if (u.pathname === '/bridge/v1/operations' && method === 'POST') {
      createCount++; calls.push({ url: u.pathname, method, body: JSON.parse(init.body) });
      if (tabBehavior.expireAuth) return json({}, 401);
      return json({ id: 'op-1', status: 'dispatching' }, 202);
    }
    if (u.pathname === '/bridge/v1/operations/op-1' && method === 'GET') {
      calls.push({ url: u.pathname, method });
      return json({ id: 'op-1', status: 'completed', result: { content: [{ type: 'text', text: 'file contents' }], isError: false } });
    }
    throw Error('Unexpected fetch ' + method + ' ' + url);
  };
  const mcp = { createClient: options => WebCodexMCP.createClient({ ...options, fetchImpl: fetch }) };
  const context = { chrome, crypto: require('node:crypto').webcrypto, URL, URLSearchParams, TextEncoder, TextDecoder, Uint8Array, ArrayBuffer, JSON, Promise, Set, Map, Date, Math, Error, String, Object, Number, RegExp, console, btoa: s => Buffer.from(s, 'binary').toString('base64'), fetch, WebCodexProtocol, WebCodexMCP: mcp, importScripts() {}, setTimeout, clearTimeout };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'background.js'), 'utf8'), context, { filename: 'background.js' });
  const panel = { url: 'chrome-extension://test-id/panel.html' }, page = { url: 'https://aistudio.google.com/app/prompts/test', frameId: 0, tab: { id: 7 } };
  const message = (msg, sender = panel) => new Promise(resolve => { const async = listeners.message(msg, sender, resolve); if (async !== true) resolve(undefined); });
  const status = () => message({ type: 'bridge.status' });
  const alarm = async () => { listeners.alarm({ name: 'webcodex-recover' }); await status(); };
  const call = async (turn, sender = page) => message({ type: 'studio.call', sessionId: 'session-1', conversationKey: 'chat-1', generating: false, text: '```webcodex-call\n' + JSON.stringify({ v: 1, turn, name: 'read_file', arguments: { path: 'safe.txt' } }) + '\n```' }, sender);
  return { chrome, message, status, call, alarm, listeners, panel, page, calls, pageMessages, db, token: TOKEN, get createCount() { return createCount; }, get deliveryCount() { return deliveryCount; } };
}
function json(value, status = 200) { return new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } }); }

test('restored token stays private; tab is bound and one operation delivers advertised next turn', async () => {
  const initial = saved(); initial.session.automatic = true;
  const h = harness(initial);
  const status = await h.status();
  assert.equal(status.ok, true); assert.equal(JSON.stringify(status).includes(TOKEN), false);
  assert.equal((await h.message({ type: 'studio.snapshot', sessionId: 'session-1', supported: true, conversationKey: 'chat-1' }, { ...h.page, tab: { id: 8 } })).error, 'Untrusted Studio sender');
  assert.equal((await h.message({ type: 'studio.snapshot', sessionId: 'session-1', supported: true, conversationKey: 'chat-1' }, h.page)).ok, true);
  assert.equal((await h.call('turn-1')).ok, true); await h.alarm();
  const resultMsg = h.pageMessages.find(m => m.type === 'studio.deliver'); assert.ok(resultMsg);
  const result = JSON.parse(resultMsg.text); assert.equal(result.type, 'webcodex-result'); assert.equal(result.result.content[0].text, 'file contents');
  assert.equal((await h.call(result.next_turn)).ok, true); await h.alarm();
  assert.equal(h.calls.filter(x => x.method === 'POST').length, 2);
  const posts = h.calls.filter(x => x.method === 'POST');
  assert.notEqual(posts[1].body.key, posts[0].body.key, 'each advertised next turn gets a fresh idempotency key');
  assert.equal(h.calls[0].body.agent_id, 'agent-1');
  await h.call('turn-1'); await h.alarm();
  assert.equal(h.createCount, 2, 'repeated old model response must not create another operation');
});

test('restoring a pending approval never dispatches it', async () => {
  const h = harness(saved('waiting_approval', 'write_file')); await h.status(); await h.alarm();
  assert.equal(h.createCount, 0, 'restored approval must wait for explicit user approval');
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
  assert.deepEqual(Array.from(injections[1].files), ['protocol.js', 'studio-adapter.js', 'content.js']);
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

test('page reload pauses the bound session and page messages expose no result state', async () => {
  const h = harness(); await h.status();
  const reloaded = await h.message({ type: 'studio.hello' }, h.page);
  assert.equal(reloaded.ok, true); assert.equal(reloaded.accepted, true); assert.equal('state' in reloaded, false);
  const state = (await h.status()).state;
  assert.equal(state.session.paused, true);
  assert.equal(state.session.resumeBlocked, true);
  assert.match(state.session.error, /restarted/);
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

test('conversation change pauses and prevents result delivery', async () => {
  const h = harness(); await h.status();
  await h.message({ type: 'studio.snapshot', sessionId: 'session-1', supported: true, conversationKey: 'different-chat' }, h.page);
  await h.call('turn-1'); await h.alarm();
  assert.equal(h.createCount, 0); assert.equal(h.deliveryCount, 0);
});

test('uncertain delivery is checked by snapshot and never submitted a second time', async () => {
  const initial = saved('result_ready'); initial.session.operationId = 'op-1'; initial.session.nextTurn = 'turn-2'; initial.session.resultText = JSON.stringify({ type: 'webcodex-result', v: 1, turn: 'turn-1', next_turn: 'turn-2', operation_id: 'op-1', name: 'read_file', result: { content: [] } });
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

test('prose, unselected tools and calls from another conversation cannot prove delivery',async()=>{
  const initial=saved('delivery_uncertain');initial.session.nextTurn='turn-2';
  const h=harness(initial);await h.status();
  const base={type:'studio.call',sessionId:'session-1',conversationKey:'chat-1',generating:false};
  for(const text of ['A final answer','```webcodex-call\n'+JSON.stringify({v:1,turn:'turn-2',name:'delete_path',arguments:{path:'x'}})+'\n```']){
    assert.equal((await h.message({...base,text},h.page)).accepted,false);
  }
  const text='```webcodex-call\n'+JSON.stringify({v:1,turn:'turn-2',name:'read_file',arguments:{path:'x'}})+'\n```';
  assert.equal((await h.message({...base,text,conversationKey:'other'},h.page)).accepted,false);
  assert.equal((await h.status()).state.session.phase,'delivery_uncertain');
  assert.equal(h.createCount,0);
});
