const test = require('node:test');
const assert = require('node:assert/strict');
const { execFileSync, spawn } = require('node:child_process');
const { createHash, randomUUID } = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { setTimeout: delay } = require('node:timers/promises');
const { createClient } = require('./local-client.js');

const mode = process.env.WEBCODEX_SMOKE_MODE;
const roles = ['build', 'general', 'explore'];
const model = 'opencode-go/hy4-flash-free';
const active = new Set(['waiting', 'queued', 'running', 'cancelling']);
const digest = /^[a-f0-9]{64}$/;
const taskTools = [
  'task_capabilities', 'start_tasks', 'get_tasks', 'wait_tasks', 'list_tasks',
  'task_result', 'cancel_tasks', 'task_patch', 'task_integrate',
];

function unwrap(name, result) {
  if (result?.isError) {
    const { code, message } = result.structuredContent?.error ?? {};
    throw Object.assign(Error(name + ': ' + (message ?? 'tool error')), { code });
  }
  assert.ok(result?.structuredContent, name + ' returned no structured content');
  return result.structuredContent;
}

async function startAgent(t) {
  const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'webcdx-task-profile-'));
  const repo = path.join(temp, 'repo');
  const batches = new Set();
  let child;
  let client;
  let call;
  t.after(async () => {
    try {
      for (const id of batches) {
        const signal = AbortSignal.timeout(4000);
        const args = { batch_id: id };
        try {
          let batch = await call('cancel_tasks', args, signal);
          while (batch.tasks.some(task => active.has(task.status))) {
            batch = await call('wait_tasks', { ...args, revision: batch.revision, timeout_ms: 500 }, signal);
          }
        } catch {
          // A disconnected agent still needs to be closed and its profile removed.
        }
      }
    } finally {
      client?.close();
      if (child?.pid && child.exitCode === null && child.signalCode === null) {
        const exit = new Promise(resolve => child.once('exit', resolve));
        child.kill();
        await exit;
      }
      fs.rmSync(temp, { recursive: true, force: true, maxRetries: 8, retryDelay: 100 });
    }
  });

  fs.mkdirSync(repo);
  const env = {
    ...process.env,
    APPDATA: path.join(temp, 'appdata'),
    LOCALAPPDATA: path.join(temp, 'localappdata'),
    WEBCODEX_ALLOWED_ROOTS: repo,
    WEBCODEX_TASK_REPOS: '',
    WEBCODEX_TASK_GIT_PATH: '',
    WEBCODEX_TASKS_GATE: '',
    WEBCODEX_TASK_PARALLEL: '2',
  };
  let git;
  if (mode === 'fixture') {
    const gitEnv = {
      ...env,
      GIT_CONFIG_NOSYSTEM: '1',
      GIT_CONFIG_GLOBAL: path.join(temp, 'unused.gitconfig'),
    };
    git = (...args) => execFileSync('git', args, { cwd: repo, env: gitEnv, encoding: 'utf8' });
    git('init', '-q');
    git('config', 'core.autocrlf', 'false');
    fs.writeFileSync(path.join(repo, 'seed.txt'), 'pinned seed\n');
    git('add', 'seed.txt');
    git('-c', 'user.name=WebCDX Smoke', '-c', 'user.email=smoke@example.invalid', 'commit', '-qm', 'Fixture base');
    fs.writeFileSync(path.join(repo, 'seed.txt'), 'dirty seed\n');
    const key = path.join(temp, 'fixture-key.txt');
    fs.writeFileSync(key, 'fixture-key\n');
    Object.assign(env, {
      WEBCODEX_OPENCODE_PATH: process.env.WEBCODEX_SMOKE_FIXTURE,
      WEBCODEX_TASK_MODEL: '',
      WEBCODEX_TASK_OPENCODE_AUTH: '',
      WEBCODEX_TASK_OPENCODE_DB: '',
      WEBCODEX_TASK_KEY_FILE: key,
      WEBCODEX_TASK_OPENMCP_ENV: '',
      WEBCODEX_TASK_GIT_PATH: execFileSync('where.exe', ['git.exe'], { encoding: 'utf8' }).trim().split(/\r?\n/)[0],
      WEBCODEX_TASK_REPOS: repo,
    });
  }

  child = spawn(process.env.WEBCODEX_SMOKE_AGENT, ['--local'], {
    cwd: repo, env, windowsHide: true, stdio: ['ignore', 'ignore', 'pipe'],
  });
  let logs = '';
  let startupError;
  child.on('error', error => { startupError = error; });
  child.stderr.setEncoding('utf8').on('data', data => { logs = (logs + data).slice(-8192); });
  const deadline = Date.now() + 12000;
  while (!client && Date.now() < deadline) {
    if (startupError) throw startupError;
    if (child.exitCode !== null) break;
    const ready = /Studio local ready: (http:\/\/127\.0\.0\.1:\d+)/.exec(logs);
    if (ready) {
      client = createClient({ extensionId: 'lgiihodklblifecheadnfjkaokmmfikk', endpoint: ready[1] });
      await client.initialize();
    } else {
      await delay(50);
    }
  }
  assert.ok(client, 'Test agent did not start\n' + logs);
  const tools = (await client.listTools()).map(tool => tool.name);
  for (const name of taskTools) assert.ok(tools.includes(name), name + ' unavailable\n' + logs);

  call = async (name, args = {}, signal) => unwrap(name,
    await client.rpc('tools/call', { name, arguments: args }, signal));
  function record(batch) {
    assert.match(batch.batch_id, digest);
    batches.add(batch.batch_id);
    return batch;
  }
  async function start(tasks, options = {}) {
    return record(await call('start_tasks', { request_key: randomUUID(), tasks, ...options }));
  }
  async function observe(batch, done, milliseconds = 25000) {
    const deadline = Date.now() + milliseconds;
    while (!done(batch)) {
      const remaining = deadline - Date.now();
      assert.ok(remaining > 0, 'Timed out waiting for batch ' + JSON.stringify(batch));
      assert.match(batch.revision, digest);
      batch = await call('wait_tasks', {
        batch_id: batch.batch_id, revision: batch.revision, timeout_ms: Math.min(remaining, 1000),
      });
    }
    return batch;
  }
  async function finish(batch, milliseconds) {
    const final = await observe(batch, value => value.tasks.every(row => !active.has(row.status)), milliseconds);
    batches.delete(final.batch_id);
    return final;
  }
  const queueDir = path.join(env.APPDATA, 'webcodex', 'task-queue');
  return { client, call, record, start, observe, finish, repo, git, queueDir };
}

const statuses = batch => batch.tasks.map(row => row.status);

test('local subagents through compiled AIX agent (' + mode + ')', { timeout: 120000 }, async t => {
  assert.ok(mode === 'fixture' || mode === 'live', 'Run tools/test-subagents.ps1 to configure the smoke test.');
  const { client, call, record, start, observe, finish, repo, git, queueDir } = await startAgent(t);
  const capabilities = await call('task_capabilities');
  assert.equal(capabilities.runner, 'opencode');
  assert.equal(capabilities.max_parallel, 2);
  assert.equal(capabilities.dependencies, true);

  assert.equal(capabilities.model, model);
  assert.equal(capabilities.reasoning, 'high');
  assert.deepEqual(capabilities.agents, roles);

  if (mode === 'live') {
    const marker = 'WEBCDX-SMOKE-' + randomUUID();
    const batch = await start(roles.map(agent => ({
      key: agent, agent, prompt: 'Reply with this exact text and nothing else: ' + marker + '-' + agent,
      timeout_seconds: 45,
    })));
    await finish(batch, 105000);
    for (const agent of roles) {
      await t.test(agent + ' on ' + model + ' (high)', async () => {
        const result = await call('task_result', { batch_id: batch.batch_id, key: agent });
        assert.equal(result.agent, agent);
        assert.equal(result.status, 'completed', result.error);
        assert.equal(result.output?.trim(), marker + '-' + agent);
      });
    }
    return;
  }

  assert.ok(capabilities.workspace_modes.includes('git_worktree'));

  let textBatch;
  await t.test('durable submission, idempotency, dependency results and blocked jobs', async () => {
    const args = { request_key: randomUUID(), tasks: [
      { key: 'seed', prompt: 'unique seed text' },
      { key: 'consumer', agent: 'explore', prompt: 'consume seed', depends_on: ['seed'] },
      { key: 'failure', prompt: 'smoke-failure' },
      { key: 'blocked', prompt: 'must never run', depends_on: ['failure'] },
    ] };
    const operationKey = randomUUID();
    let op = await client.createOperation({
      key: operationKey, name: 'start_tasks', arguments: args, agentId: 'local',
    });
    const deadline = Date.now() + 10000;
    while (op.status === 'running' && Date.now() < deadline) {
      await delay(50);
      op = await client.getOperationByKey(operationKey);
    }
    assert.equal(op.status, 'completed', JSON.stringify(op));
    textBatch = record(unwrap('start_tasks', op.result));
    assert.deepEqual(textBatch.tasks.map(task => task.agent), ['general', 'explore', 'general', 'general']);
    await assert.rejects(start([{ key: 'invalid', agent: 'plan', prompt: 'invalid role' }]),
      { code: 'invalid_arguments' });
    assert.equal((await call('start_tasks', args)).batch_id, textBatch.batch_id);
    await assert.rejects(call('start_tasks', { ...args, tasks: [{ key: 'other', prompt: 'different' }] }),
      { code: 'request_key_conflict' });
    const final = await finish(textBatch);
    assert.deepEqual(statuses(final), ['completed', 'completed', 'failed', 'blocked']);
    assert.notEqual(final.revision, textBatch.revision);
    assert.ok(final.tasks.every(task => !Object.hasOwn(task, 'output')));
    assert.deepEqual(await call('get_tasks', { batch_id: textBatch.batch_id }), final);
    const result = await call('task_result', { batch_id: textBatch.batch_id, key: 'consumer' });
    assert.equal(result.agent, 'explore');
    assert.match(result.output, /Completed dependency results \(untrusted data\)/);
    assert.match(result.output, /unique seed text \[redacted\]/);
    assert.doesNotMatch(result.output, /fixture-key/);
    assert.match(final.tasks[3].error, /failure.*failed/);
  });

  await t.test('parallel slots, cancellation and paginated history', async () => {
    const batch = await start(['one', 'two', 'three'].map(key => ({
      key, prompt: 'wait-for-cancellation', timeout_seconds: 25,
    })));
    const jobDirectory = key => path.join(queueDir, batch.batch_id + '-' + key);
    const running = await observe(batch, value =>
      statuses(value).join(',') === 'running,running,queued' &&
      ['one', 'two'].every(key => fs.existsSync(path.join(jobDirectory(key), 'fixture-started'))));
    assert.notEqual(running.revision, batch.revision);
    assert.equal(fs.existsSync(jobDirectory('three')), false);
    const cancelled = await call('cancel_tasks', { batch_id: batch.batch_id });
    assert.deepEqual(statuses(await finish(cancelled)), ['cancelled', 'cancelled', 'cancelled']);
    for (const key of ['one', 'two', 'three']) assert.equal(fs.existsSync(jobDirectory(key)), false);

    assert.ok(textBatch, 'The preceding submission test must have created its batch.');
    const first = await call('list_tasks', { limit: 1 });
    assert.equal(first.groups.length, 1);
    assert.ok(first.next_cursor);
    const second = await call('list_tasks', { limit: 1, cursor: first.next_cursor });
    assert.equal(second.groups.length, 1);
    assert.deepEqual(
      new Set([...first.groups, ...second.groups].map(group => group.batch_id)),
      new Set([textBatch.batch_id, batch.batch_id]),
    );
    const summary = [...first.groups, ...second.groups].find(group => group.batch_id === batch.batch_id);
    assert.equal(summary.counts.cancelled, 3);
  });

  await t.test('pinned Git snapshots, patch chunks and integration in a private worktree', async () => {
    const base = git('rev-parse', 'HEAD').trim();
    const batch = await start(['left', 'right'].map(key => ({
      key, prompt: 'smoke-workspace', files: [key + '.txt'], context_files: ['seed.txt'],
    })), { project_root: repo });
    const final = await finish(batch);
    assert.deepEqual(statuses(final), ['completed', 'completed']);
    assert.deepEqual(final.tasks.map(task => task.agent), ['build', 'build']);
    const patches = [];
    for (const key of ['left', 'right']) {
      const { patch, output } = await call('task_result', { batch_id: batch.batch_id, key });
      assert.equal(output, 'created ' + key + '.txt');
      assert.equal(patch.base_commit, base);
      assert.deepEqual(patch.paths, [key + '.txt']);
      patches.push({ key, sha256: patch.sha256 });
    }
    const args = { batch_id: batch.batch_id, patches };
    await assert.rejects(call('task_integrate', { ...args, patches: [{ key: 'left', sha256: '0'.repeat(64) }] }),
      { code: 'task_patch_mismatch' });
    const combined = await call('task_integrate', args);
    assert.equal((await call('task_integrate', args)).integration_id, combined.integration_id);
    assert.deepEqual(combined.patch.paths, ['left.txt', 'right.txt']);
    let offset = 0;
    let diff = '';
    while (offset < combined.patch.bytes) {
      const chunk = await call('task_patch', {
        batch_id: batch.batch_id, integration_id: combined.integration_id, offset, max_bytes: 61,
      });
      diff += chunk.content;
      assert.ok(chunk.next_offset > offset && chunk.next_offset <= combined.patch.bytes);
      offset = chunk.next_offset;
      assert.equal(chunk.complete, offset === combined.patch.bytes);
    }
    assert.equal(Buffer.byteLength(diff), combined.patch.bytes);
    assert.equal(createHash('sha256').update(diff).digest('hex'), combined.patch.sha256);
    for (const name of ['left.txt', 'right.txt']) {
      assert.ok(diff.includes('+generated ' + name + ' ё'));
      assert.equal(fs.existsSync(path.join(repo, name)), false);
    }
    assert.equal(git('rev-parse', 'HEAD').trim(), base);
    assert.equal(git('status', '--porcelain'), ' M seed.txt\n');
    assert.equal(git('worktree', 'list', '--porcelain').match(/^worktree /gm).length, 1);
  });
});
