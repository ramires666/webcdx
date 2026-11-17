(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const errorEl = $('error');
  $('gate-connection').hidden = true;
  const installedVersion = chrome.runtime.getManifest?.().version || '';
  let state = null, toolSignature = '', agentSignature = '', toolsInitialized = false, chatWidthEditing = false;
  let connecting = false, sessionChanging = false, refreshingTasks = false, taskSignature = '';
  const taskStatusLabels = new Map([
    ['waiting', ['Ждут зависимостей', 'ждёт зависимости']],
    ['queued', ['В очереди', 'в очереди']],
    ['running', ['В работе', 'в работе']],
    ['cancelling', ['Отменяются', 'отменяется']],
    ['completed', ['Выполнены', 'выполнена']],
    ['failed', ['С ошибкой', 'с ошибкой']],
    ['blocked', ['Заблокированы', 'заблокирована']],
    ['cancelled', ['Отменены', 'отменена']],
    ['timed_out', ['Истёк тайм-аут', 'истёк тайм-аут']],
    ['interrupted', ['Прерваны', 'прервана']],
    ['outcome_unknown', ['Исход неизвестен', 'исход неизвестен']]
  ]);
  const permissionFields = {consent: 'consent', automatic: 'automatic', attachFiles: 'attach-files', autoApprove: 'auto-approve'};
  function allPermissionsSelected() {
    return [...$('tools').querySelectorAll('input'), ...Object.values(permissionFields).map($), $('hide-service')].every(input => input.checked);
  }
  function renderPermissionToggle() {
    $('allow-all').textContent = allPermissionsSelected() ? 'Снять все галочки' : 'Разрешить всё';
  }
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
  function canRefreshTasks(next) {
    return next?.connected && next.session?.taskBatch?.id && next.session?.toolNames?.includes('get_tasks');
  }
  function renderTaskBatch(next) {
    const section = $('task-batch');
    if (!section) return;
    const batch = next?.session?.taskBatch;
    section.hidden = !batch?.id;
    $('task-refresh').disabled = refreshingTasks || !canRefreshTasks(next);
    const tasks = batch?.tasks || [];
    const signature = JSON.stringify([batch?.id, batch?.error, tasks.map(({ key, status }) => [key, status])]);
    if (signature === taskSignature) return;
    taskSignature = signature;
    $('task-batch-id').textContent = batch?.id || '';
    const error = $('task-batch-error');
    error.textContent = batch?.error || '';
    error.hidden = !batch?.error;
    const rows = $('task-rows');
    rows.replaceChildren();
    const counts = new Map();
    for (const task of tasks) {
      const [label, detail] = taskStatusLabels.get(task.status) || ['Неизвестный статус', 'неизвестный статус'];
      counts.set(label, (counts.get(label) || 0) + 1);
      const row = document.createElement('li');
      row.textContent = `${task.key} · ${detail}`;
      rows.append(row);
    }
    $('task-summary').textContent = [...counts].map(([label, count]) => `${label}: ${count}`).join(' · ');
  }
  function render(next) {
    if (!next) return;
    if(Number.isInteger(next.autoSettings?.revision)&&Number.isInteger(state?.autoSettings?.revision)&&next.autoSettings.revision<state.autoSettings.revision)next={...next,autoSettings:state.autoSettings};
    const previousError = state?.session?.error||state?.pageScript?.observerError;
    state = next;
    if (Number.isInteger(next.chatWidth) && !chatWidthEditing && document.activeElement !== $('chat-width')) $('chat-width').value = String(next.chatWidth);
    const session = next.session || {};
    renderTaskBatch(next);
    const page = next.pageScript;
    const agentVersion = next.connected && next.transport !== 'gate' ? next.agentVersion || 'версия не передана' : 'не подключён';
    $('extension-version').textContent = `Скрипт ${page?.version || installedVersion || 'версия не передана'} · Агент ${agentVersion}`;
    const saved = next.autoSettings || {};
    const localAgents = next.transport === 'gate' ? [] : next.agents || [];
    renderChoices($('agents'), localAgents, 'agent');
    $('agents').hidden = !localAgents.length;
    const availableTools = next.tools || [];
    renderChoices($('tools'), [...availableTools, ...(saved.toolNames || []).filter(name => !availableTools.some(tool => tool.name === name)).map(name => ({ name, description: 'недоступен, ожидаю агента' }))], 'tool');
    if (!editedPreferences.has('consent') && typeof saved.consent === 'boolean') $('consent').checked = saved.consent;
    if (!editedPreferences.has('agentId') && saved.agentId && (!session.phase || ['idle', 'stopped'].includes(session.phase))) choose($('agents'), [String(saved.agentId)]);
    if (!editedPreferences.has('toolNames') && Array.isArray(saved.toolNames)) choose($('tools'), saved.toolNames);
    if (!editedPreferences.has('automatic') && typeof saved.automatic === 'boolean') $('automatic').checked = saved.automatic;
    if(!editedPreferences.has('attachFiles'))$('attach-files').checked=saved.attachFiles!==false;
    if (!editedPreferences.has('autoApprove') && typeof saved.autoApprove === 'boolean') $('auto-approve').checked = saved.autoApprove;
    if (session.phase && !['idle', 'stopped'].includes(session.phase)) {
      if (!editedPreferences.has('agentId') && session.agentId) choose($('agents'), [String(session.agentId)]);
    }
    if (next.gateUrl && !$('gate-url').value) $('gate-url').value = next.gateUrl;
    if (next.clientId && !$('client-id').value) $('client-id').value = next.clientId;
    $('pending').textContent = session.pendingCall ? JSON.stringify(session.pendingCall, null, 2) : 'Нет ожидающего вызова';
    if (session.instructions !== undefined && document.activeElement !== $('instructions')) $('instructions').value = session.instructions;
    if (session.error || page?.observerError || previousError) showError(session.error||page?.observerError);
    $('result').value = session.resultText || '';
    const rejected=session.replyError;
    $('reply-error').hidden=!rejected;
    $('reply-error-reason').textContent=rejected?`[${rejected.code}] ${rejected.message}`:'';
    $('reply-error-source').value=rejected?.sourceText||'';
    const canRetry=!!rejected&&next.connected&&saved.consent===true&&!session.paused&&['waiting_model','ready'].includes(session.phase)&&!session.resumeBlocked&&!(session.pendingCall||session.operationKey||session.operationId||session.resultText);
    $('retry-reply').disabled=!canRetry;
    $('request-correction').disabled=!canRetry||!!rejected?.correctionRequested;
    $('hide-service').checked = !!next.hideServiceMessages;
    if (session.phase && !['idle', 'stopped'].includes(session.phase)) $('automatic').checked = !!session.automatic;
    const busy = connecting || next.connecting;
    $('connect').disabled = busy || (!!next.connected && next.transport !== 'local');
    $('connect').textContent = busy ? 'Ожидается вход…' : 'Подключить';
    $('disconnect').disabled = busy || !next.connected;
    $('demo').disabled = busy || !!next.connected;
    const activeSession = !!session.phase && !['idle', 'stopped'].includes(session.phase);
    $('session-marker').textContent = activeSession && session.sessionMarker ? session.sessionMarker : 'Нет активного сеанса';
    const agent = (next.agents || []).find(item => String(item.id) === String(session.agentId));
    const sessionConnected = activeSession && next.connected && next.transport !== 'gate' && agent?.online !== false;
    const buttonVersion = next.agentVersion?.match(/^\d+\.\d+\.\d+/)?.[0] || next.agentVersion || 'версия не передана';
    $('arm').textContent = activeSession ? `Агент ${buttonVersion} ${sessionConnected ? 'подключен' : 'отключён'}\nОтключить` : 'Запустить сеанс';
    $('arm').className = sessionConnected ? 'session-connected' : 'session-disconnected';
    $('arm').disabled = sessionChanging || (!activeSession && (!next.connected || !$('consent').checked));
    renderPermissionToggle();
    $('tools-next-session').hidden = !session.phase || ['idle', 'stopped'].includes(session.phase);
    $('save-instructions').disabled = !next.connected || !session.agentId;
    $('inject-marker').disabled = !next.connected || !$('consent').checked || !session.sessionMarker || session.paused || !!session.resumeBlocked || !['waiting_model','ready'].includes(session.phase) || !!(session.pendingCall||session.operationKey||session.operationId||session.resultText);
    $('approve').disabled = session.phase !== 'waiting_approval' || !session.pendingCall || session.paused;
    $('deliver').disabled = !session.resultText || session.phase !== 'result_ready' || session.paused;
    $('pause').disabled = !session.phase || ['idle', 'stopped'].includes(session.phase) || session.paused;
    $('resume').disabled = !session.phase || (!session.paused && session.phase !== 'delivery_uncertain') || !!session.resumeBlocked;
    $('resume').textContent = session.phase === 'delivery_uncertain' ? 'Проверить доставку' : 'Продолжить';
    $('save-from-reply').disabled = !next.connected || !$('consent').checked || !session.toolNames?.includes('write_file') || !['waiting_model','ready','paused'].includes(session.phase) || !!session.resumeBlocked || !!(session.pendingCall||session.operationId||session.operationKey||session.resultText);
    $('run-from-reply').disabled = !next.connected || !$('consent').checked || !session.toolNames?.length || !['waiting_model','ready','paused'].includes(session.phase) || !!session.resumeBlocked || !!(session.pendingCall||session.operationId||session.operationKey||session.resultText);
    $('stop').disabled = !session.phase || ['idle', 'stopped'].includes(session.phase);
  }
  async function act(type, payload = {}) {
    showError(null);
    try { const next = await send(type, payload); if (next) render(next); }
    catch (error) { showError(error); }
  }
  $('task-refresh')?.addEventListener('click', async () => {
    if (refreshingTasks || !canRefreshTasks(state)) return;
    const batchId = state.session.taskBatch.id;
    refreshingTasks = true;
    renderTaskBatch(state);
    showError(null);
    try {
      const next = await send('bridge.taskRefresh');
      if (state.session?.taskBatch?.id === batchId && next?.session?.taskBatch?.id === batchId) render(next);
    } catch (error) {
      if (state.session?.taskBatch?.id === batchId) showError(error);
    }
    finally { refreshingTasks = false; renderTaskBatch(state); }
  });
  $('redirect-uri').textContent = `${chrome.identity?.getRedirectURL('webcodex') || ''}  ·  Extension ID: ${chrome.runtime.id}`;
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
    if (sessionChanging) return;
    const activeSession = state?.session?.phase && !['idle', 'stopped'].includes(state.session.phase);
    if (!activeSession && !$('consent').checked) return showError('Подтвердите передачу выбранных результатов.');
    sessionChanging = true;
    render(state);
    try {
      if (activeSession) return await act('bridge.stop');
      const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
      await act('bridge.arm', { tabId: tab?.id, agentId: selected($('agents'))[0], toolNames: selected($('tools')).filter(name => state.tools.some(tool => tool.name === name)), automatic: $('automatic').checked, autoApprove: $('auto-approve').checked, consent: true });
    } catch (error) { showError(error); }
    finally { sessionChanging = false; render(state); }
  });
  $('save-instructions').addEventListener('click', () => act('bridge.instructions', { submit: false }));
  $('inject-marker').addEventListener('click', async () => {
    try {
      const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
      await act('bridge.injectMarker', { tabId: tab?.id });
    } catch (error) { showError(error); }
  });
  $('approve').addEventListener('click', () => act('bridge.approve'));
  $('deliver').addEventListener('click', () => act('bridge.deliver', { submit: true }));
  $('retry-reply').addEventListener('click', () => act('bridge.retryReply'));
  $('request-correction').addEventListener('click', () => act('bridge.requestCorrection'));
  $('pause').addEventListener('click', () => act('bridge.pause'));
  $('resume').addEventListener('click', async () => {
    try {
      const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
      await act('bridge.resume', { tabId: tab?.id });
    } catch (error) { showError(error); }
  });
  $('save-from-reply').addEventListener('click', () => act('bridge.saveFromReply'));
  $('run-from-reply').addEventListener('click', () => act('bridge.runFromReply'));
  $('stop').addEventListener('click', () => act('bridge.stop'));
  $('collapse-chat').addEventListener('click', async () => {
    try {
      const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
      await act('bridge.collapseChat', { tabId: tab?.id });
    } catch (error) { showError(error); }
  });
  $('chat-width').addEventListener('input', async () => {
    chatWidthEditing = true;
    const value = Number($('chat-width').value);
    try {
      const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
      await act('bridge.chatWidth', { tabId: tab?.id, value });
    } catch (error) { showError(error); }
  });
  $('chat-width').addEventListener('blur', () => { chatWidthEditing = false; render(state); });
  $('allow-all').addEventListener('click', async () => {
    if ($('allow-all').disabled) return;
    const enabled = !allPermissionsSelected();
    $('allow-all').disabled = true;
    for(const input of $('tools').querySelectorAll('input'))input.checked=enabled;
    for(const [field,id] of Object.entries(permissionFields)){
      $(id).checked=enabled;editedPreferences.add(field);
    }
    editedPreferences.add('toolNames');
    $('hide-service').checked=enabled;
    renderPermissionToggle();
    const preferences={agentId:selected($('agents'))[0]||state?.autoSettings?.agentId||'',toolNames:selected($('tools')),consent:enabled,automatic:enabled,attachFiles:enabled,autoApprove:enabled};
    if(state){state.autoSettings={...state.autoSettings,...preferences};if(state.session)state.session.automatic=enabled;}
    try{
      render(await send('bridge.preferences',preferences));
      render(await send('bridge.cleanupPreference',{enabled}));
    }catch(error){showError(error);}
    finally { $('allow-all').disabled = false; }
  });
  function savePreferences(field) {
    renderPermissionToggle();
    editedPreferences.add(field);
    const savedTools = state?.autoSettings?.toolNames || [];
    const visibleTools = new Set([...$('tools').querySelectorAll('input')].map(input => input.value));
    const toolNames = [...new Set([...savedTools.filter(name => !visibleTools.has(name)), ...selected($('tools'))])];
    const preferences = { agentId: selected($('agents'))[0] || state?.autoSettings?.agentId || '', toolNames, consent: $('consent').checked, automatic: $('automatic').checked, attachFiles:$('attach-files').checked, autoApprove: $('auto-approve').checked };
    if (state) state.autoSettings = {...state.autoSettings,...preferences};
    send('bridge.preferences', {...preferences,field}).catch(showError);
  }
  $('agents').addEventListener('change', () => savePreferences('agentId'));
  $('tools').addEventListener('change', () => savePreferences('toolNames'));
  $('consent').addEventListener('change', () => { savePreferences('consent'); render(state); });
  $('automatic').addEventListener('change', () => { if (state?.session) state.session.automatic = $('automatic').checked; savePreferences('automatic'); });
  $('attach-files').addEventListener('change',()=>savePreferences('attachFiles'));
  $('auto-approve').addEventListener('change', () => savePreferences('autoApprove'));
  $('hide-service').addEventListener('change', () => { renderPermissionToggle(); return act('bridge.cleanupPreference', { enabled: $('hide-service').checked }); });
  async function poll() { try { render(await send('bridge.status')); } catch (error) { showError(error); } }
  connectionLoaded.then(poll); setInterval(poll, 2000);
})();
