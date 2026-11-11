const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const net = require('node:net');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { createClient } = require('./mcp-client.js');

const enabled = process.env.WEBCODEX_BRIDGE_REAL_TEST === '1';
const gatePath = path.resolve(__dirname, '..', 'bin', 'ai-studio-bridge', 'webcodex-gate.exe');
const agentPath = path.resolve(__dirname, '..', 'bin', 'ai-studio-bridge', 'webcodex-agent.exe');

test('compiled Gate and agent complete real bridge file operations and reconnect', { skip: !enabled || process.platform !== 'win32', timeout: 45_000 }, async t => {
  assert.ok(fs.existsSync(gatePath), 'compiled Gate binary is missing');
  assert.ok(fs.existsSync(agentPath), 'compiled agent binary is missing');
  const tempParent = path.resolve(__dirname, '..', 'tmp');
  fs.mkdirSync(tempParent, { recursive: true });
  const temp = fs.mkdtempSync(path.join(tempParent, 'real-gate-'));
  const root = path.join(temp, 'files');
  fs.mkdirSync(root);
  const port = await freePort();
  const baseUrl = `http://127.0.0.1:${port}`;
  const password = `test-${require('node:crypto').randomBytes(24).toString('hex')}`;
  const auth = 'Basic ' + Buffer.from(`admin:${password}`).toString('base64');
  const fixture = path.join(root, 'fixture.txt');
  fs.writeFileSync(fixture, 'real agent read\n');
  const children = [];
  let gate, agent;
  let phase = 'start Gate';

  function start(binary, env) {
    const child = spawn(binary, [], { cwd: temp, env: { ...process.env, ...env }, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
    child.output = '';
    child.stdout.setEncoding('utf8').on('data', text => { child.output += text; });
    child.stderr.setEncoding('utf8').on('data', text => { child.output += text; });
    children.push(child);
    return child;
  }
  function startGate() {
    gate = start(gatePath, {
      WEBCODEX_ADDR: `127.0.0.1:${port}`, WEBCODEX_PUBLIC_URL: baseUrl,
      WEBCODEX_DB_PATH: path.join(temp, 'gate.db'), WEBCODEX_ADMIN_USER: 'admin', WEBCODEX_ADMIN_PASSWORD: password,
    });
  }
  function startAgent(token) {
    agent = start(agentPath, {
      WEBCODEX_GATE_URL: baseUrl, WEBCODEX_AGENT_TOKEN: token, WEBCODEX_ALLOWED_ROOTS: root,
      WEBCODEX_BROWSER_ENABLED: '0', WEBCODEX_BROWSER_BACKEND: 'disabled', WEBCODEX_LOG_DIR: path.join(temp, 'agent-logs'),
    });
  }

  t.after(async () => {
    for (const child of children) if (child.exitCode === null) child.kill();
    await Promise.all(children.map(child => child.exitCode !== null ? Promise.resolve() : new Promise(resolve => {
      const timer = setTimeout(resolve, 1500);
      child.once('exit', () => { clearTimeout(timer); resolve(); });
    })));
    assert.equal(path.dirname(path.resolve(temp)), tempParent);
    assert.ok(path.basename(temp).startsWith('real-gate-'));
    fs.rmSync(temp, { recursive: true, force: true });
  });

  try {
    startGate();
    phase = 'wait for Gate';
    await waitFor(async () => (await request('/healthz')).ok);
    phase = 'load admin page';
    const index = await admin('/admin');
    phase = 'parse admin CSRF';
    const csrf = hidden(index, 'csrf');
    phase = 'submit agent registration';
    const createdAgent = await admin('/admin/agents', form({ csrf, id: 'real-agent', name: 'Real agent', allowed_roots: root }));
    phase = 'read agent credential';
    const agentToken = between(createdAgent, 'id="val-agent-token">', '</div>');
    assert.ok(agentToken.startsWith('wc_agent_'), 'agent registration did not return its token');
    phase = 'register MCP client';
    const createdClient = await admin('/admin/mcp-clients', form({ csrf, id: 'real-bridge', name: 'Real bridge' }));
    const oauthClientId = between(createdClient, 'id="val-client-id">', '</div>');
    assert.ok(oauthClientId.startsWith('wc_client_'), 'MCP registration did not return its client ID');
    await admin('/admin/mcp-clients/real-bridge/agents', form({ csrf, agent_id: 'real-agent' }));
    const bridgePage = await admin('/admin/mcp-clients/real-bridge/bridge');
    const bridgeCsrf = hidden(bridgePage, 'csrf');
    const extensionId = 'a'.repeat(32);
    await admin('/admin/mcp-clients/real-bridge/bridge', form({ csrf: bridgeCsrf, extension_id: extensionId, allowed_tools: 'list_agents,read_file,write_file' }));
    phase = 'connect agent';
    startAgent(agentToken);
    await waitFor(async () => (await admin('/admin')).includes('Online'));
    assert.match(agent.output, /webcodex-agent version=\S+/);
    assert.match(agent.output, /connected to Gate; agent version=\S+ gate version=\S+/);
    assert.match(gate.output, /webcodex-gate version=\S+/);
    assert.match(gate.output, /agent stream connected .*agent_version=\S+ gate_version=\S+/);

    phase = 'authorize extension bridge';
    const accessToken = await authorizeBridge(oauthClientId, extensionId);
    phase = 'initialize real MCP client';
    const client = createClient({ baseUrl, token: accessToken });
    await client.initialize();
    const tools = await client.listTools();
    assert.ok(tools.some(tool => tool.name === 'read_file'));
    assert.ok(tools.some(tool => tool.name === 'write_file'));
    const agents = await client.listAgents();
    assert.ok(agents.some(item => item.id === 'real-agent' && item.online));

    const readArgs = { path: fixture };
    phase = 'read fixture through bridge';
    const readKey = operationKey();
    const read = await completeOperation(client, readKey, 'read_file', readArgs);
    assert.equal(read.status, 'completed');
    assert.equal(read.result.structuredContent.content, 'real agent read\n');
    const repeated = await client.createOperation({ key: readKey, name: 'read_file', arguments: readArgs, agentId: 'real-agent' });
    assert.equal(repeated.id, read.id);
    await assert.rejects(client.createOperation({ key: readKey, name: 'read_file', arguments: { path: path.join(root, 'other.txt') }, agentId: 'real-agent' }));

    const writtenPath = path.join(root, 'written.txt');
    phase = 'write fixture through bridge';
    const write = await completeOperation(client, operationKey(), 'write_file', { path: writtenPath, content: 'real agent write\n', if_absent: true });
    assert.equal(write.status, 'completed');
    assert.equal(fs.readFileSync(writtenPath, 'utf8'), 'real agent write\n');

    const previousGate = gate;
    phase = 'restart Gate';
    previousGate.kill();
    await exited(previousGate);
    startGate();
    await waitFor(async () => (await request('/healthz')).ok);
    await waitFor(() => (agent.output.match(/connected to Gate; agent version=\S+ gate version=\S+/g) || []).length >= 2);
    await waitFor(async () => (await admin('/admin')).includes('Online'));
    assert.match(agent.output, /connected to Gate; agent version=\S+ gate version=\S+/);
    assert.match(gate.output, /agent stream connected .*agent_version=\S+ gate_version=\S+/);
    assert.match(agent.output, /agent version=\S+ gate version=\S+/);
    const persisted = await client.createOperation({ key: readKey, name: 'read_file', arguments: readArgs, agentId: 'real-agent' });
    assert.equal(persisted.id, read.id);
    assert.equal(fs.readFileSync(writtenPath, 'utf8'), 'real agent write\n');
  } catch (_) {
    // Keep assertions and request failures from exposing generated credentials in test output.
    throw new Error(`Real Gate bridge smoke test failed during ${phase}; details suppressed to protect generated credentials.`);
  }

  async function request(route, init = {}) {
    return fetch(baseUrl + route, { ...init, method: init.method || (init.body ? 'POST' : 'GET'), signal: AbortSignal.timeout(3000) });
  }
  async function admin(route, init = {}) {
    if (init instanceof URLSearchParams) init = { body: init };
    const response = await request(route, { ...init, headers: { Authorization: auth, ...(init.headers || {}) } });
    const body = await response.text();
    if (!response.ok && response.status !== 303) { phase = `Gate admin HTTP ${response.status}`; throw new Error('admin request failed'); }
    return body;
  }
  async function authorizeBridge(clientId, extension) {
    const crypto = require('node:crypto');
    const verifier = crypto.randomBytes(32).toString('base64url');
    const challenge = crypto.createHash('sha256').update(verifier).digest('base64url');
    const state = crypto.randomBytes(16).toString('hex');
    const redirect = `https://${extension}.chromiumapp.org/webcodex`;
    const query = new URLSearchParams({ client_id: clientId, redirect_uri: redirect, response_type: 'code', code_challenge: challenge, code_challenge_method: 'S256', state });
    const consentResponse = await request('/bridge/v1/authorize?' + query, { headers: { Authorization: auth } });
    const consent = await consentResponse.text();
    assert.equal(consentResponse.status, 200);
    const formFields = Object.fromEntries(['csrf', 'config_hash'].map(name => [name, hidden(consent, name)]));
    const allowed = await request('/bridge/v1/authorize', { method: 'POST', redirect: 'manual', headers: { Authorization: auth, 'Content-Type': 'application/x-www-form-urlencoded' }, body: form({ ...queryEntries(query), ...formFields, decision: 'allow' }) });
    assert.equal(allowed.status, 302);
    const callback = new URL(allowed.headers.get('location'));
    assert.equal(callback.searchParams.get('state'), state);
    const tokenResponse = await request('/bridge/v1/token', { method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded' }, body: form({ grant_type: 'authorization_code', client_id: clientId, redirect_uri: redirect, code: callback.searchParams.get('code'), code_verifier: verifier }) });
    assert.equal(tokenResponse.status, 200);
    return (await tokenResponse.json()).access_token;
  }
  async function completeOperation(client, key, name, args) {
    const created = await client.createOperation({ key, name, arguments: args, agentId: 'real-agent' });
    return await waitFor(async () => {
      const current = await client.getOperation(created.id);
      if (['completed', 'failed', 'outcome_unknown'].includes(current.status)) return current;
      return false;
    });
  }
  async function waitFor(fn) {
    const deadline = Date.now() + 10_000;
    while (Date.now() < deadline) {
      try { const value = await fn(); if (value) return value; } catch (_) {}
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    throw new Error('Timed out waiting for real Gate bridge state');
  }
  function form(value) { return new URLSearchParams(value); }
  function operationKey() { return `${Date.now()}-${require('node:crypto').randomUUID()}`; }
  function queryEntries(query) { return Object.fromEntries(query.entries()); }
  function hidden(html, name) {
    const escaped = name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const match = html.match(new RegExp(`<input[^>]*name="${escaped}"[^>]*value="([^"]*)"`));
    assert.ok(match, `Gate response omitted ${name}`);
    return match[1];
  }
  function between(html, start, end) {
    const index = html.indexOf(start);
    assert.notEqual(index, -1, 'Gate response omitted a generated credential');
    const value = html.slice(index + start.length, html.indexOf(end, index + start.length));
    assert.ok(value, 'Gate generated credential is empty');
    return value;
  }
  async function exited(child) {
    if (child.exitCode !== null) return;
    await Promise.race([new Promise(resolve => child.once('exit', resolve)), new Promise((_, reject) => setTimeout(() => reject(new Error('Gate did not stop')), 3000))]);
  }
});

async function freePort() {
  const server = net.createServer();
  await new Promise((resolve, reject) => server.once('error', reject).listen(0, '127.0.0.1', resolve));
  const { port } = server.address();
  await new Promise(resolve => server.close(resolve));
  return port;
}
