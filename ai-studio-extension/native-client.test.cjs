const test = require('node:test');
const assert = require('node:assert/strict');
const { createClient } = require('./native-client.js');

function harness() {
  const port = { sent: [], onMessage: event(), onDisconnect: event(), postMessage(message) { this.sent.push(message); }, disconnect() { this.disconnected = true; } };
  const client = createClient({ runtime: { connectNative(host) { assert.equal(host, 'org.webcodex.studio'); return port; }, get lastError() { return undefined; } }, version: '1.2.3' });
  return { client, port, reply(message) { port.onMessage.emit(message); }, disconnect() { port.onDisconnect.emit(); } };
}
function event() { const listeners = []; return { addListener(fn) { listeners.push(fn); }, emit(...args) { for (const listener of listeners) listener(...args); } }; }

test('initialize and RPC use string IDs and expose requested operation methods', async () => {
  const h = harness();
  const init = h.client.initialize();
  assert.deepEqual(h.port.sent[0], { id: '1', method: 'initialize', params: { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'WebCodex AI Studio Bridge', version: '1.2.3' } } });
  h.reply({ id: '1', result: { serverInfo: { version: 'host-version' } } });
  assert.deepEqual(await init, { serverInfo: { version: 'host-version' } });
  const operation = h.client.createOperation({ key: 'k', name: 'tool', arguments: { x: 1 }, agentId: 'local' });
  assert.equal(h.port.sent[1].method, 'operations/create');
  assert.deepEqual(h.port.sent[1].params, { key: 'k', name: 'tool', arguments: { x: 1 }, agentId: 'local' });
  h.reply({ id: '2', result: { id: 'op1' } });
  assert.deepEqual(await operation, { id: 'op1' });
  const get = h.client.getOperation('op1');
  assert.deepEqual(h.port.sent[2].params, { id: 'op1' });
  h.reply({ id: '3', result: { status: 'done' } });
  assert.deepEqual(await get, { status: 'done' });
  h.client.close();
});

test('reassembles out-of-order UTF-8 chunks and preserves large responses', async () => {
  const h = harness(), promise = h.client.rpc('hello');
  const json = JSON.stringify({ id: '1', result: { text: 'snowman ☃ and emoji 🧪' } });
  const split = Math.floor(json.length / 2);
  h.reply({ id: '1', chunk: json.slice(split), index: 1, total: 2 });
  h.reply({ id: '1', chunk: json.slice(0, split), index: 0, total: 2 });
  assert.deepEqual(await promise, { text: 'snowman ☃ and emoji 🧪' });
  const huge = h.client.rpc('large');
  const text='x'.repeat(5*1024*1024);
  const payload=JSON.stringify({id:'2',result:{text}}),total=Math.ceil(payload.length/60000);
  assert.ok(total>64);
  for(let index=total-1;index>=0;index--)h.reply({id:'2',chunk:payload.slice(index*60000,(index+1)*60000),index,total});
  assert.deepEqual(await huge,{text});h.client.close();
});

test('disconnect fails pending calls and never retries or replays', async () => {
  const h = harness(), pending = h.client.rpc('write_file', { content: 'once' });
  h.disconnect();
  await assert.rejects(pending, error => error.code === 'NATIVE_DISCONNECTED');
  assert.equal(h.port.sent.length, 1);
  await assert.rejects(h.client.rpc('write_file'), error => error.code === 'NATIVE_DISCONNECTED');
  assert.equal(h.port.sent.length, 1);
});

test('late replies after abort are ignored without disturbing other pending calls', async () => {
  const h = harness(), controller = new AbortController();
  const aborted = h.client.rpc('cancelled', {}, controller.signal);
  const active = h.client.rpc('still-running');
  controller.abort();
  await assert.rejects(aborted, error => error.code === 'NATIVE_ABORTED');
  h.reply({ id: '1', result: 'late' });
  h.reply({ id: '2', result: 'ok' });
  assert.equal(await active, 'ok');
  h.client.close();
});

test('duplicate chunks clean up the request and leave other calls usable', async () => {
  const h = harness(), controller = new AbortController();
  const malformed = h.client.rpc('malformed', {}, controller.signal);
  const active = h.client.rpc('active');
  h.reply({ id: '1', chunk: '{', index: 0, total: 2 });
  h.reply({ id: '1', chunk: '{', index: 0, total: 2 });
  await assert.rejects(malformed, /Inconsistent native response chunks/);
  controller.abort();
  h.reply({ id: '2', result: 'ok' });
  assert.equal(await active, 'ok');
  h.client.close();
});

test('times out without replaying and sends large requests without a client ceiling', async () => {
  const realSetTimeout = global.setTimeout, realClearTimeout = global.clearTimeout;
  const timers = [];
  global.setTimeout = (fn) => { const timer = { fn, cleared: false }; timers.push(timer); return timer; };
  global.clearTimeout = timer => { if (timer) timer.cleared = true; };
  try {
    const h = harness(), pending = h.client.rpc('slow');
    timers.at(-1).fn();
    await assert.rejects(pending, error => error.code === 'NATIVE_TIMEOUT');
    assert.equal(h.port.sent.length, 1);
    const value='x'.repeat(2*1024*1024),large=h.client.rpc('big',{value});
    assert.equal(h.port.sent[1].params.value,value);h.reply({id:'2',result:'ok'});assert.equal(await large,'ok');
    assert.equal(h.port.sent.length, 2);
    h.client.close();
  } finally { global.setTimeout = realSetTimeout; global.clearTimeout = realClearTimeout; }
});
