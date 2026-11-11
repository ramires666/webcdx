const test = require('node:test');
const assert = require('node:assert/strict');
const { createClient } = require('./mcp-client.js');

function response(body, { status = 200, type = 'application/json' } = {}) {
  return { ok: status >= 200 && status < 300, status, headers: { get: () => type }, text: async () => body,
    body: new ReadableStream({ start(c) { c.enqueue(new TextEncoder().encode(body)); c.close(); } }) };
}
function clientWith(handler) {
  const calls = [], client = createClient({ baseUrl: 'https://example.test', token: 'secret', fetchImpl: async (url, init) => { calls.push({ url, init }); return handler(url, init, calls.length); } });
  return { client, calls };
}
const rpcResult = (body, id = 1) => response(JSON.stringify({ jsonrpc: '2.0', id, result: body }));

test('validates base URL and keeps credentials out of errors', () => {
  for (const baseUrl of ['https://u:p@example.test', 'https://example.test/path', 'https://example.test?q=1', 'http://example.test', 'ftp://localhost']) assert.throws(() => createClient({ baseUrl, token: 'secret' }));
  assert.doesNotThrow(() => createClient({ baseUrl: 'http://127.0.0.1', token: 'secret' }));
});
test('rpc sends authenticated JSON-RPC and rejects bad HTTP, RPC errors, and IDs', async () => {
  const { client, calls } = clientWith((_, init) => rpcResult({ ok: true }));
  assert.deepEqual(await client.rpc('ping'), { ok: true });
  assert.equal(calls[0].url, 'https://example.test/mcp/v5');
  assert.equal(calls[0].init.headers.Authorization, 'Bearer secret');
  assert.equal(calls[0].init.headers['MCP-Protocol-Version'], '2025-06-18');
  assert.deepEqual(JSON.parse(calls[0].init.body), { jsonrpc: '2.0', id: 1, method: 'ping', params: {} });
  const bad = clientWith(() => response('{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"no"}}'));
  await assert.rejects(bad.client.rpc('x'), /RPC error/);
  const mismatch = clientWith(() => rpcResult({}, 7));
  await assert.rejects(mismatch.client.rpc('x'), /ID mismatch/);
  const http = clientWith(() => response('{}', { status: 503 }));
  await assert.rejects(http.client.rpc('x'), /HTTP error 503/);
});
test('reads matching SSE data events and enforces response byte limit', async () => {
  const { client } = clientWith(() => response('event: ping\ndata: {"jsonrpc":"2.0","id":99,"result":{}}\n\nevent: message\ndata: {"jsonrpc":"2.0","id":1,"result":{"ok":1}}\n\n', { type: 'text/event-stream' }));
  assert.deepEqual(await client.rpc('x'), { ok: 1 });
  const tooBig = clientWith(() => response('x'.repeat(2 * 1024 * 1024 + 1)));
  await assert.rejects(tooBig.client.rpc('x'), /exceeds 2 MiB/);
});
test('returns a matching SSE RPC result without waiting for the stream to close', async () => {
  let cancelled = false;
  const stream = new ReadableStream({
    start(controller) { controller.enqueue(new TextEncoder().encode('event: message\ndata: {"jsonrpc":"2.0","id":1,"result":{"ok":true}}\n\n')); },
    cancel() { cancelled = true; }
  });
  const c = createClient({ baseUrl: 'https://example.test', token: 'secret', fetchImpl: async () => ({ ok: true, status: 200, headers: { get: () => 'text/event-stream' }, body: stream }) });
  assert.deepEqual(await c.rpc('x'), { ok: true });
  assert.equal(cancelled, true);
});
test('initialize sends initialized notification and listTools paginates safely', async () => {
  const { client, calls } = clientWith((_, init, count) => {
    const req = JSON.parse(init.body);
    if (req.method === 'notifications/initialized') return response('', { status: 202 });
    if (req.method === 'initialize') return rpcResult({ protocolVersion: '2025-06-18' }, 1);
    return rpcResult(req.params.cursor ? { tools: [{ name: 'b' }] } : { tools: [{ name: 'a' }], nextCursor: 'next' }, req.id);
  });
  assert.equal((await client.initialize()).protocolVersion, '2025-06-18');
  assert.deepEqual(await client.listTools(), [{ name: 'a' }, { name: 'b' }]);
  const notification = JSON.parse(calls[1].init.body);
  assert.equal(notification.method, 'notifications/initialized'); assert.equal('id' in notification, false);
});
test('listAgents unwraps MCP content and propagates isError', async () => {
  const c = clientWith(() => rpcResult({ content: [{ type: 'text', text: '{"agents":[{"id":"a"}]}' }] }));
  assert.deepEqual(await c.client.listAgents(), [{ id: 'a' }]);
  const err = clientWith(() => rpcResult({ isError: true, content: [] }));
  await assert.rejects(err.client.listAgents(), /tool returned an error/);
  const preserve = clientWith(() => rpcResult({ isError: true, content: [{ type: 'text', text: 'details' }] }));
  assert.deepEqual(await preserve.client.rpc('tools/call', { name: 'x', arguments: {} }), { isError: true, content: [{ type: 'text', text: 'details' }] });
});
test('bridge operations use authenticated endpoints and preserve status objects', async () => {
  const { client, calls } = clientWith((url, init) => response(JSON.stringify({ status: 'pending', id: url.endsWith('/op%2F1') ? 'op/1' : 'new' })));
  assert.deepEqual(await client.createOperation({ key: 'k', name: 'tool', arguments: { x: 1 }, agentId: 'a' }), { status: 'pending', id: 'new' });
  assert.deepEqual(JSON.parse(calls[0].init.body), { key: 'k', name: 'tool', arguments: { x: 1 }, agent_id: 'a' });
  assert.deepEqual(await client.getOperation('op/1'), { status: 'pending', id: 'op/1' });
  assert.equal(calls[1].url, 'https://example.test/bridge/v1/operations/op%2F1');
  assert.equal(calls[1].init.headers.Authorization, 'Bearer secret');
  assert.ok(calls[0].init.signal instanceof AbortSignal);
  assert.ok(calls[1].init.signal instanceof AbortSignal);
});

test('all transport phases propagate cancellation and authentication status', async () => {
  const controller = new AbortController();
  const { client, calls } = clientWith((_url, init) => {
    const rpc = init.body && JSON.parse(init.body);
    return rpc?.method === 'initialize' ? rpcResult({}) : response('', { status: 202 });
  });
  await client.initialize(controller.signal);
  assert.equal(calls.length, 2);
  controller.abort();
  for (const call of calls) assert.equal(call.init.signal.aborted, true);
  const unauthorized = clientWith(() => response('{}', { status: 401 }));
  await assert.rejects(unauthorized.client.getOperation('existing'), error => error.status === 401);
  await assert.rejects(unauthorized.client.rpc('ping'), error => error.status === 401);
});
