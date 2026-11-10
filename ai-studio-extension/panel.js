(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const stateEl = $('status'), errorEl = $('error');
  let state = null, toolSignature = '', agentSignature = '', toolsInitialized = false;
  const SAFE_DEFAULT_TOOLS = new Set(['list_allowed_roots', 'list_directory', 'read_file', 'search_files', 'find_files', 'list_agents', 'poll_command', 'list_commands']);

  function send(type, payload = {}) {
    return new Promise((resolve, reject) => chrome.runtime.sendMessage({ type, ...payload }, reply => {
      if (chrome.runtime.lastError) return reject(new Error(chrome.runtime.lastError.message));
      if (!reply || reply.ok !== true) return reject(new Error(reply?.error || 'Нет ответа от расширения'));
      resolve(reply.state);
    }));
  }
  function showError(error) { errorEl.textContent = error ? String(error.message || error) : ''; errorEl.hidden = !error; }
  function selected(container) { return [...container.querySelectorAll('input:checked')].map(el => el.value); }
  function renderChoices(container, items, kind) {
    const signature = JSON.stringify(items);
    if (kind === 'tool' ? signature === toolSignature : signature === agentSignature) return;
    const previous = new Set(selected(container)); container.replaceChildren();
    for (const item of items) {
      const label = document.createElement('label'), input = document.createElement('input'), name = document.createElement('span');
      input.type = kind === 'agent' ? 'radio' : 'checkbox'; input.name = kind; input.value = String(item.id ?? item.name);
      input.checked = previous.has(input.value) || (!toolsInitialized && kind === 'tool' && SAFE_DEFAULT_TOOLS.has(item.name) && !item.name.startsWith('browser_'));
      name.textContent = kind === 'agent' ? `${item.name || item.id}${item.online === false ? ' · не в сети' : ''}` : `${item.name}${item.description ? ' — ' + item.description : ''}`;
      label.append(input, name); container.append(label);
    }
    if (kind === 'tool') { toolSignature = signature; toolsInitialized = true; } else agentSignature = signature;
  }
  function render(next) {
    if (!next) return;
    state = next;
    const session = next.session || {};
    const phaseNames = { idle: 'ожидает запуска', ready: 'готов', active: 'выполняется', running: 'выполняется', waiting_approval: 'ожидает подтверждения', paused: 'на паузе', stopped: 'остановлен', delivery_uncertain: 'передача результата не подтверждена', error: 'ошибка' };
    stateEl.textContent = `${session.demo || next.demo ? 'Демо режим · ' : ''}${session.phase ? `Gate ${next.connected ? 'подключён' : 'не подключён'} · сеанс: ${phaseNames[session.phase] || session.phase}` : next.connected ? 'Gate подключён' : 'Gate не подключён'}`;
    renderChoices($('agents'), next.agents || [], 'agent');
    renderChoices($('tools'), next.tools || [], 'tool');
    if (next.gateUrl && !$('gate-url').value) $('gate-url').value = next.gateUrl;
    if (next.clientId && !$('client-id').value) $('client-id').value = next.clientId;
    $('pending').textContent = session.pendingCall ? JSON.stringify(session.pendingCall, null, 2) : 'Нет ожидающего вызова';
    if (session.instructions !== undefined && document.activeElement !== $('instructions')) $('instructions').value = session.instructions;
    showError(session.error || null);
    $('result').value = session.resultText || '';
    if (session.phase && !['idle', 'stopped'].includes(session.phase)) $('automatic').checked = !!session.automatic;
    $('connect').disabled = !!next.connected;
    $('disconnect').disabled = !next.connected;
    $('demo').disabled = !!next.connected;
    $('arm').disabled = !next.connected || (!!session.phase && session.phase !== 'stopped') || !$('consent').checked;
    $('save-instructions').disabled = !next.connected || !session.agentId;
    $('approve').disabled = session.phase !== 'waiting_approval' || !session.pendingCall || session.paused;
    $('deliver').disabled = !session.resultText || session.phase === 'delivery_uncertain';
    $('pause').disabled = !session.phase || session.paused;
    $('resume').disabled = !session.phase || !session.paused;
    $('stop').disabled = !session.phase;
  }
  async function act(type, payload = {}) {
    showError(null);
    try { const next = await send(type, payload); if (next) render(next); }
    catch (error) { showError(error); }
  }
  $('redirect-uri').textContent = `${chrome.identity.getRedirectURL('webcodex')}  ·  Extension ID: ${chrome.runtime.id}`;
  $('connect').addEventListener('click', async () => {
    showError(null);
    try {
      const url = new URL($('gate-url').value.trim());
      if (url.username || url.password || url.search || url.hash || url.pathname !== '/' || !(url.protocol === 'https:' || (url.protocol === 'http:' && ['localhost', '127.0.0.1'].includes(url.hostname)))) throw new Error('Укажите origin Gate: HTTPS или localhost по HTTP');
      const allowed = await chrome.permissions.request({ origins: [url.origin + '/*'] });
      if (!allowed) throw new Error('Доступ к адресу Gate не разрешён');
      await act('bridge.connect', { gateUrl: url.origin, clientId: $('client-id').value.trim() });
    } catch (error) { showError(error); }
  });
  $('disconnect').addEventListener('click', () => act('bridge.disconnect'));
  $('demo').addEventListener('click', () => act('bridge.demo'));
  $('arm').addEventListener('click', async () => {
    if (!$('consent').checked) return showError('Подтвердите передачу выбранных результатов.');
    try {
      const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
      await act('bridge.arm', { tabId: tab?.id, agentId: selected($('agents'))[0], toolNames: selected($('tools')), automatic: $('automatic').checked, consent: true });
    } catch (error) { showError(error); }
  });
  $('save-instructions').addEventListener('click', () => act('bridge.instructions', { submit: false }));
  $('approve').addEventListener('click', () => act('bridge.approve'));
  $('deliver').addEventListener('click', () => act('bridge.deliver', { submit: true }));
  $('pause').addEventListener('click', () => act('bridge.pause'));
  $('resume').addEventListener('click', () => act('bridge.resume'));
  $('stop').addEventListener('click', () => act('bridge.stop'));
  $('consent').addEventListener('change', () => render(state));
  $('automatic').addEventListener('change', () => { if (state) state.session.automatic = $('automatic').checked; });
  async function poll() { try { render(await send('bridge.status')); } catch (error) { showError(error); } }
  poll(); setInterval(poll, 2000);
})();
