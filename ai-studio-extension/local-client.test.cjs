const test = require('node:test');
const assert = require('node:assert/strict');
const { createClient, ENDPOINTS } = require('./local-client.js');
function reply(request, result) { return new Response(JSON.stringify({ id: JSON.parse(request.body).id, result })); }

test('discovers the first available local agent without Native Messaging or Gate and pins later calls', async () => {
  const calls = [];
  const client = createClient({ extensionId: 'test-extension', fetchImpl: async (url, request) => {
    calls.push({ url, request });
    if (url.startsWith(ENDPOINTS[0] + '/')) throw new TypeError('offline');
    const message = JSON.parse(request.body);
    if (message.method === 'initialize') return reply(request, { localAgent: { id: 'agent-two' }, serverInfo: { version: 'agent-version' } });
    return reply(request, { tools: [] });
  } });
  assert.equal((await client.initialize()).serverInfo.version, 'agent-version');
  await client.listTools();
  assert.equal(calls.length, 3);
  assert.equal(client.endpoint, ENDPOINTS[1]);
  assert.equal(calls[2].request.headers['X-WebCodex-Agent'], 'agent-two');
  assert.equal(calls[2].request.headers['X-WebCodex-Extension'], 'test-extension');
  assert.equal(JSON.parse(calls[1].request.body).params.clientInfo.version, '0.3.4');
  client.close();
});

test('a lost operation response never causes discovery, replay or a switch to another agent', async () => {
  const calls = [];
  const client = createClient({ endpoint: ENDPOINTS[0], agentIdentity: 'one', fetchImpl: async (url, request) => {
    calls.push(url);
    if (JSON.parse(request.body).method === 'initialize') return reply(request, { localAgent: { id: 'one' } });
    throw new TypeError('response lost');
  } });
  await client.initialize();
  await assert.rejects(client.createOperation({ key: 'once', name: 'write_file', arguments: {}, agentId: 'local' }), error => error.code === 'NATIVE_DISCONNECTED');
  assert.deepEqual(calls, [ENDPOINTS[0] + '/studio/rpc', ENDPOINTS[0] + '/studio/rpc']);
  client.close();
});

test('a saved session refuses an endpoint taken by another agent and does not scan other ports', async () => {
  let count = 0;
  const client = createClient({ endpoint: ENDPOINTS[0], agentIdentity: 'one', fetchImpl: async () => { count++; return new Response('', { status: 409 }); } });
  await assert.rejects(client.initialize(), /another agent will not receive/);
  assert.equal(count, 1);
  assert.throws(() => createClient({ endpoint: 'https://example.com' }), /Invalid saved local endpoint/);
  client.close();
});

test('rejects wrong response IDs and bounded streamed responses before accepting a result', async () => {
  const client = createClient({ endpoint: ENDPOINTS[0], agentIdentity: 'one', fetchImpl: async () => new Response(JSON.stringify({ id: 'wrong', result: {} })) });
  await assert.rejects(client.initialize(), /ID mismatch/);
  client.close();
  let cancelled = false;
  const large = createClient({ endpoint: ENDPOINTS[0], agentIdentity: 'one', fetchImpl: async () => new Response(new ReadableStream({ pull(controller) { controller.enqueue(new Uint8Array(1024 * 1024)); }, cancel() { cancelled = true; } })) });
  await assert.rejects(large.initialize(), /exceeds 4 MiB/);
  assert.equal(cancelled, true);
  large.close();
});
