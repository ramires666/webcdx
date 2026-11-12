(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const stateEl = $('status'), errorEl = $('error');
  const installedVersion = chrome.runtime.getManifest?.().version || '';
  if(installedVersion)$('extension-version').textContent='AI Studio · alpha '+installedVersion;
  let state = null, toolSignature = '', agentSignature = '', toolsInitialized = false;
  let connecting = false;
  const edited = new Set();
  const editedPreferences = new Set();
  function saveConnection() {
    return chrome.storage.local.set({ bridgeConnection: { gateUrl: $('gate-url').value, clientId: $('client-id').value } });
  }
  for (const id of ['gate-url', 'client-id']) $(id).addEventListener('input', () => {
    edited.add(id);
    connectionLoaded.then(saveConnection).catch(showError);
  });
  const connectionLoaded = chrome.storage.local.get('bridgeConnection').then(({ bridgeConnection: saved }) => {
    if (!saved) return;
    if (!edited.has('gate-url')) $('gate-url').value = saved.gateUrl || '';
    if (!edited.has('client-id')) $('client-id').value = saved.clientId || '';
  }).catch(showError);
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
  function choose(container, values) { const selectedValues = new Set(values || []); for (const input of container.querySelectorAll('input')) input.checked = selectedValues.has(input.value); }
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
    if (kind === 'tool') { toolSignature = signature; if (items.length) toolsInitialized = true; } else agentSignature = signature;
  }
  function render(next) {
    if (!next) return;
    const previousError = state?.session?.error;
    state = next;
    const session = next.session || {};
    const phaseNames = { idle: 'ожидает запуска', ready: 'готов', active: 'выполняется', running: 'выполняется', waiting_approval: 'ожидает подтверждения', paused: 'на паузе', stopped: 'остановлен', delivery_uncertain: 'передача результата не подтверждена', error: 'ошибка' };
    const connectionName = next.transport === 'gate' ? 'Gate' : 'Локальный агент';
    stateEl.textContent = `${session.demo || next.demoMode ? 'Демо режим · ' : ''}${connectionName} ${next.connected ? 'подключён' : 'не подключён'}${session.phase ? ` · сеанс: ${phaseNames[session.phase] || session.phase}` : ''}`;
    $('transport-info').textContent = next.transport === 'local'
      ? `Прямое соединение · агент ${next.agentVersion || 'версия не передана'} · Gate не используется`
      : next.localError ? `Локальный агент недоступен: ${next.localError}` : '';
    $('transport-info').hidden = !$('transport-info').textContent;
    const page=next.pageScript;
    $('page-script').textContent=page
      ? page.version&&(!installedVersion||page.version===installedVersion)&&(!page.lastSeen||Date.now()-page.lastSeen<10000)
        ? `Скрипт вкладки AI Studio: ${page.version}`
        : `Скрипт вкладки AI Studio: ${page.version||'прежняя версия'}. Перезагрузите сохранённую вкладку AI Studio.`
      : next.connected?'Нет связи со скриптом вкладки AI Studio. Откройте AI Studio или перезагрузите сохранённую вкладку.':'';
    $('page-script').hidden=!$('page-script').textContent;
    const saved = next.autoSettings || {};
    renderChoices($('agents'), next.agents || [], 'agent');
    const availableTools = next.tools || [];
    renderChoices($('tools'), [...availableTools, ...(saved.toolNames || []).filter(name => !availableTools.some(tool => tool.name === name)).map(name => ({ name, description: 'недоступен, ожидаю агента' }))], 'tool');
    if (!editedPreferences.has('consent') && typeof saved.consent === 'boolean') $('consent').checked = saved.consent;
    if (!editedPreferences.has('agentId') && saved.agentId && (!session.phase || ['idle', 'stopped'].includes(session.phase))) choose($('agents'), [String(saved.agentId)]);
    if (!editedPreferences.has('toolNames') && Array.isArray(saved.toolNames)) choose($('tools'), saved.toolNames);
    if (!editedPreferences.has('automatic') && typeof saved.automatic === 'boolean') $('automatic').checked = saved.automatic;
    if (!editedPreferences.has('autoApprove') && typeof saved.autoApprove === 'boolean') $('auto-approve').checked = saved.autoApprove;
    if (session.phase && !['idle', 'stopped'].includes(session.phase)) {
      if (!editedPreferences.has('agentId') && session.agentId) choose($('agents'), [String(session.agentId)]);
    }
    if (next.gateUrl && !$('gate-url').value) $('gate-url').value = next.gateUrl;
    if (next.clientId && !$('client-id').value) $('client-id').value = next.clientId;
    $('pending').textContent = session.pendingCall ? JSON.stringify(session.pendingCall, null, 2) : 'Нет ожидающего вызова';
    if (session.instructions !== undefined && document.activeElement !== $('instructions')) $('instructions').value = session.instructions;
    if (session.error || previousError) showError(session.error);
    $('result').value = session.resultText || '';
    $('hide-service').checked = !!next.hideServiceMessages;
    if (session.phase && !['idle', 'stopped'].includes(session.phase)) $('automatic').checked = !!session.automatic;
    const autoStatus=typeof next.autoStatus==='string'?next.autoStatus:next.autoStatus?.message||'';
    if(autoStatus&&(autoStatus!=='Готово'||!session.paused&&!session.error))stateEl.textContent+=' · '+autoStatus;
    const busy = connecting || next.connecting;
    $('connect').disabled = busy || (!!next.connected && next.transport !== 'local');
    $('connect').textContent = busy ? 'Ожидается вход…' : next.transport === 'local' ? 'Настроить резерв Gate' : 'Подключить';
    $('disconnect').disabled = busy || !next.connected;
    $('demo').disabled = busy || !!next.connected;
    $('arm').disabled = !next.connected || !['idle', 'stopped'].includes(session.phase) || !$('consent').checked;
    $('tools-next-session').hidden = !session.phase || ['idle', 'stopped'].includes(session.phase);
    $('save-instructions').disabled = !next.connected || !session.agentId;
    $('approve').disabled = session.phase !== 'waiting_approval' || !session.pendingCall || session.paused;
    $('deliver').disabled = !session.resultText || session.phase !== 'result_ready' || session.paused;
    $('pause').disabled = !session.phase || ['idle', 'stopped'].includes(session.phase) || session.paused;
    $('resume').disabled = !session.phase || !session.paused || session.resumeBlocked;
    $('save-from-reply').disabled = !next.connected || !$('consent').checked || !session.toolNames?.includes('write_file') || !['waiting_model','ready','paused'].includes(session.phase) || !!session.resumeBlocked || !!(session.pendingCall||session.operationId||session.operationKey||session.resultText);
    $('run-from-reply').disabled = !next.connected || !$('consent').checked || !session.toolNames?.length || !['waiting_model','ready','paused'].includes(session.phase) || !!session.resumeBlocked || !!(session.pendingCall||session.operationId||session.operationKey||session.resultText);
    $('stop').disabled = !session.phase || ['idle', 'stopped'].includes(session.phase);
  }
  async function act(type, payload = {}) {
    showError(null);
    try { const next = await send(type, payload); if (next) render(next); }
    catch (error) { showError(error); }
  }
  $('redirect-uri').textContent = `${chrome.identity.getRedirectURL('webcodex')}  ·  Extension ID: ${chrome.runtime.id}`;
  $('connect').addEventListener('click', async () => {
    if (connecting || state?.connecting) return;
    connecting = true;
    render(state);
    showError(null);
    try {
      await saveConnection();
      const url = new URL($('gate-url').value.trim());
      if (url.username || url.password || url.search || url.hash || url.pathname !== '/' || !(url.protocol === 'https:' || (url.protocol === 'http:' && ['localhost', '127.0.0.1'].includes(url.hostname)))) throw new Error('Укажите origin Gate: HTTPS или localhost по HTTP');
      const allowed = await chrome.permissions.request({ origins: [url.origin + '/*'] });
      if (!allowed) throw new Error('Доступ к адресу Gate не разрешён');
      await act('bridge.connect', { gateUrl: url.origin, clientId: $('client-id').value.trim(), configureGate: state?.transport === 'local' });
    } catch (error) { showError(error); }
    finally { connecting = false; render(state); }
  });
  $('disconnect').addEventListener('click', () => act('bridge.disconnect'));
  $('demo').addEventListener('click', () => act('bridge.demo'));
  $('arm').addEventListener('click', async () => {
    if (!$('consent').checked) return showError('Подтвердите передачу выбранных результатов.');
    try {
      const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
      await act('bridge.arm', { tabId: tab?.id, agentId: selected($('agents'))[0], toolNames: selected($('tools')), automatic: $('automatic').checked, autoApprove: $('auto-approve').checked, consent: true });
    } catch (error) { showError(error); }
  });
  $('save-instructions').addEventListener('click', () => act('bridge.instructions', { submit: false }));
  $('approve').addEventListener('click', () => act('bridge.approve'));
  $('deliver').addEventListener('click', () => act('bridge.deliver', { submit: true }));
  $('pause').addEventListener('click', () => act('bridge.pause'));
  $('resume').addEventListener('click', () => act('bridge.resume'));
  $('save-from-reply').addEventListener('click', () => act('bridge.saveFromReply'));
  $('run-from-reply').addEventListener('click', () => act('bridge.runFromReply'));
  $('stop').addEventListener('click', () => act('bridge.stop'));
  function savePreferences(field) {
    editedPreferences.add(field);
    const savedTools = state?.autoSettings?.toolNames || [];
    const visibleTools = new Set([...$('tools').querySelectorAll('input')].map(input => input.value));
    const toolNames = [...new Set([...savedTools.filter(name => !visibleTools.has(name)), ...selected($('tools'))])];
    const preferences = { agentId: selected($('agents'))[0] || state?.autoSettings?.agentId || '', toolNames, consent: $('consent').checked, automatic: $('automatic').checked, autoApprove: $('auto-approve').checked };
    if (state) state.autoSettings = preferences;
    send('bridge.preferences', preferences).catch(showError);
  }
  $('agents').addEventListener('change', () => savePreferences('agentId'));
  $('tools').addEventListener('change', () => savePreferences('toolNames'));
  $('consent').addEventListener('change', () => { savePreferences('consent'); render(state); });
  $('automatic').addEventListener('change', () => { if (state?.session) state.session.automatic = $('automatic').checked; savePreferences('automatic'); });
  $('auto-approve').addEventListener('change', () => savePreferences('autoApprove'));
  $('hide-service').addEventListener('change', () => act('bridge.cleanupPreference', { enabled: $('hide-service').checked }));
  async function poll() { try { render(await send('bridge.status')); } catch (error) { showError(error); } }
  connectionLoaded.then(poll); setInterval(poll, 2000);
})();
