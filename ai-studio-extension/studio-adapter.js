// Selectors are based on published AI Studio DOM observations; verify against the live chat before release.
(function (root, factory) {
  if (typeof module === 'object' && module.exports) module.exports = factory;
  if (root) root.WebCodexStudio = { createAdapter: factory };
})(typeof globalThis !== 'undefined' ? globalThis : this, function createAdapter(document, location) {
  const visible = (el) => !!el && el.isConnected !== false && el.hidden !== true &&
    (!el.getAttribute || el.getAttribute('aria-hidden') !== 'true') &&
    (!el.getBoundingClientRect || el.getBoundingClientRect().width > 0);
  const attr = (el, name) => el.getAttribute ? el.getAttribute(name) : null;
  const allElements = (selector, scope = document) => Array.from(scope.querySelectorAll(selector));
  const text = (el) => String(el.innerText || el.textContent || '').trim();
  const elements = (selector, scope = document) => Array.from(scope.querySelectorAll(selector)).filter(visible);
  const roleValue = (el) => String(attr(el, 'data-turn-role') || '').toLowerCase();
  const roleMarkers = (el) => [el, ...allElements('[data-turn-role], .chat-turn-container', el)]
    .map(node => roleValue(node) || (node.matches?.('.chat-turn-container.user') ? 'user' : node.matches?.('.chat-turn-container.model') ? 'model' : ''))
    .filter(Boolean);
  const marked = (el, kind) => roleMarkers(el).includes(kind);
  const turns = () => allElements('ms-chat-turn, model-prompt-container').filter(el => visible(el) || attr(el, 'data-webcodex-hidden') === 'true');
  const role = (el) => marked(el, 'model') ? 'model' : marked(el, 'user') ? 'user' : '';
  const inSystemPanel = (el) => {
    for (let node = el; node; node = node.parentElement || node.parent) {
      if (node.matches?.('ms-system-instructions, [role="dialog"]')) return true;
      if (node === systemPanelForEditor(el)) return true;
    }
    return false;
  };
  const systemPanelForEditor = (editor) => {
    if (attr(editor, 'placeholder') !== 'Optional tone and style instructions for the model') return null;
    const matches = elements('textarea').filter(el => attr(el, 'placeholder') === 'Optional tone and style instructions for the model');
    const headings = elements('h1, h2, h3').filter(el => text(el).toLowerCase() === 'system instructions');
    if (matches.length !== 1 || matches[0] !== editor) return null;
    if (headings.length !== 1) return editor.parentElement || editor.parent || null;
    for (let node = editor.parentElement || editor.parent; node; node = node.parentElement || node.parent)
      if (!['body', 'html', 'document'].includes(String(node.tagName || node.tag || '').toLowerCase()) && (node === headings[0] || allElements('h1, h2, h3', node).includes(headings[0]))) return node;
    return editor.parentElement || editor.parent || null;
  };
  const editors = () => {
    const isSystemEditor = el => attr(el, 'placeholder') === 'Optional tone and style instructions for the model';
    const scoped = elements('ms-prompt-input-wrapper textarea, ms-prompt-box textarea, ms-autosize-textarea textarea, ms-prompt-input-wrapper [contenteditable="true"], ms-prompt-box [contenteditable="true"], ms-autosize-textarea [contenteditable="true"]').filter(el => !isSystemEditor(el) && !inSystemPanel(el));
    const candidates = scoped.length ? scoped : elements('textarea, [contenteditable="true"]').filter(el => !isSystemEditor(el) && !inSystemPanel(el));
    return [...new Set(candidates)].filter((el) => !el.disabled && !el.readOnly && attr(el, 'aria-disabled') !== 'true');
  };
  const controls = () => elements('button');
  const hasSpinner = (scope = document) => elements('.stoppable-spinner, ms-stoppable-spinner', scope).length > 0;
  const buttonText = (el) => {
    const copy = el.cloneNode(true);
    for (const icon of copy.querySelectorAll('[aria-hidden="true"], .material-symbols-outlined, .material-icons, mat-icon, svg')) icon.remove();
    return text(copy);
  };
  const labels = (el) => [attr(el, 'aria-label'), attr(el, 'title'), buttonText(el)].filter(Boolean).map(value => value.trim().toLowerCase());
  const hasLabel = (el, words, keyboardSuffix = false) => labels(el).some(value => {
    const normalized = keyboardSuffix ? value.replace(/\s*keyboard_return\s*$/i, '').replace(/\s*\(?\s*(?:ctrl|control)\s*\+\s*enter\s*\)?$/i, '').trim() : value;
    return words.includes(normalized);
  });
  const submitButtons = () => controls().filter((el) => hasLabel(el, ['run', 'send', 'запустить', 'отправить'], true));
  const submitButton = () => {
    const matches = submitButtons();
    return matches.length === 1 ? matches[0] : null;
  };
  const conversationKey = () => {
    for (const node of [document.documentElement, document.body]) {
      for (const name of ['data-conversation-id', 'data-thread-id', 'data-prompt-id']) {
        const value = node && attr(node, name);
        if (value) return `${location.pathname}${location.search}#${name}=${value}`;
      }
    }
    return `${location.pathname}${location.search}`;
  };
  const editorValue = (el) => el.isContentEditable || attr(el, 'contenteditable') === 'true' ? text(el) : String(el.value || '');
  let ownedDraft = null, pendingSubmit = null;
  const cleanContentText = (el) => {
    const copy = el.cloneNode ? el.cloneNode(true) : el;
    if (copy !== el && copy.querySelectorAll) {
      for (const note of copy.querySelectorAll('[data-webcodex-context]')) note.remove();
      for (const junk of copy.querySelectorAll('ms-thought-accordion, ms-thought-chunk, button')) junk.remove();
      return text(copy);
    }
    return text(el);
  };
  const finalText = (el) => {
    const content = el.cloneNode ? el.cloneNode(true) : el;
    if (content !== el && content.querySelectorAll) {
      for (const note of content.querySelectorAll('[data-webcodex-context]')) note.remove();
      for (const junk of content.querySelectorAll('ms-thought-accordion, ms-thought-chunk, button')) junk.remove();
    }
    const chunks = content.querySelectorAll ? [...content.querySelectorAll('ms-text-chunk')] : [];
    if (chunks.length > 1) return chunks.map(cleanContentText).filter(Boolean).join('\n');
    const scope = chunks[0] || content;
    const codes = scope.querySelectorAll ? [...scope.querySelectorAll('ms-code-block')] : [];
    if (codes.length === 1) {
      const code = codes[0].querySelectorAll ? codes[0].querySelectorAll('pre code')[0] : null;
      if (code) {
        const raw = text(code);
        const prose = scope.cloneNode ? scope.cloneNode(true) : null;
        if (prose) for (const block of prose.querySelectorAll('ms-code-block')) block.remove();
        let exactCall = false;
        try {
          const parsed = JSON.parse(raw);
          exactCall = !!parsed && typeof parsed === 'object' && !Array.isArray(parsed) && Object.keys(parsed).sort().join(',') === 'arguments,name,turn,v';
        } catch {}
        if ((!prose || !text(prose).trim()) && exactCall)
          return `\`\`\`webcodex-call\n${raw}\n\`\`\``;
      }
    }
    return chunks.length ? cleanContentText(scope) : cleanContentText(content);
  };
  const inspect = () => {
    if (!location.hostname || location.hostname.toLowerCase() !== 'aistudio.google.com')
      return { conversationKey: '', generating: false, editorEmpty: false, supported: false, error: 'unsupported site', latestModel: null };
    const candidateEditors = editors();
    const recognized = turns();
    const roles = recognized.map(role);
    const unsupportedRole = recognized.some((el, i) => !roles[i] && roleMarkers(el).length > 0 || new Set(roleMarkers(el)).size > 1);
    const runs = submitButtons(), run = runs.length === 1 ? runs[0] : null;
    const explicitStops = controls().filter((el) => hasLabel(el, ['stop', 'cancel', 'остановить', 'отмена', 'генерируется']) ||
      attr(el, 'aria-label')?.toLowerCase() === 'generating');
    // AI Studio briefly removes Run while switching generation controls.
    const controlsPending = runs.length === 0 && explicitStops.length === 0 && !hasSpinner();
    const generating = explicitStops.length > 0 || hasSpinner() || controlsPending;
    let error = '';
    if (candidateEditors.length !== 1) error = 'ambiguous or missing editor';
    else if (unsupportedRole) error = 'unsupported turn role';
    else if (recognized.length && !roles.some(Boolean)) error = 'unrecognized conversation roles';
    else if (runs.length > 1 || explicitStops.length > 1) error = `ambiguous generation controls: Run=${runs.length}, Stop=${explicitStops.length}`;
    const model = recognized.filter((el) => role(el) === 'model').pop() || null;
    return {
      conversationKey: conversationKey(), generating, controlsPending,
      editorEmpty: candidateEditors.length === 1 && editorValue(candidateEditors[0]) === '',
      supported: !error, error, latestModel: model ? { element: model, text: finalText(model) } : null,
    };
  };
  const stage = (value) => {
    const state = inspect();
    const candidateEditors = editors();
    if (!state.supported || state.generating || candidateEditors.length !== 1 || !state.editorEmpty) return false;
    const editor = candidateEditors[0];
    if ('value' in editor) {
      const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(editor), 'value')?.set;
      if (setter) setter.call(editor, String(value)); else editor.value = String(value);
    } else editor.textContent = String(value);
    const EventCtor = document.defaultView && document.defaultView.InputEvent;
    if (editor.dispatchEvent && EventCtor) editor.dispatchEvent(new EventCtor('input', { bubbles: true, inputType: 'insertText', data: String(value) }));
    const staged = editorValue(editor) === String(value);
    if (staged) ownedDraft = { value: String(value), conversationKey: state.conversationKey };
    return staged;
  };
  const submit = (value, isCurrent = () => true) => {
    if (pendingSubmit) return Promise.resolve(false);
    const draft = String(value), initial = inspect();
    if (!isCurrent()) return Promise.resolve(false);
    const editor = editors()[0];
    const ownsDraft = ownedDraft?.value === draft && ownedDraft.conversationKey === initial.conversationKey && editor && editorValue(editor) === draft;
    if (!ownsDraft && !stage(draft)) return Promise.resolve(false);
    const key = initial.conversationKey;
    const promise = (async () => {
      for (let attempt = 0; attempt <= 60; attempt++) {
        const state = inspect(), currentEditors = editors();
        if (!isCurrent() || !state.supported || (state.generating && !state.controlsPending) || state.conversationKey !== key || currentEditors.length !== 1 || editorValue(currentEditors[0]) !== draft) return false;
        const button = submitButton();
        if (button && !button.disabled && attr(button, 'aria-disabled') !== 'true') {
          if (!isCurrent() || ownedDraft?.value !== draft || ownedDraft.conversationKey !== key) return false;
          ownedDraft = null;
          button.click();
          return true;
        }
        if (attempt < 60) await new Promise(resolve => (document.defaultView?.setTimeout || setTimeout)(resolve, 50));
      }
      return false;
    })().finally(() => { pendingSubmit = null; });
    pendingSubmit = promise;
    return promise;
  };
  let systemStageError = '';
  const stageSystemInstructions = async (value) => {
    systemStageError = '';
    const fail = reason => (systemStageError = reason, false);
    const exactLabel = (el, choices) => [attr(el, 'aria-label'), attr(el, 'title'), text(el)]
      .some(label => choices.includes(String(label || '').trim().toLowerCase()));
    const panels = () => {
      const known = elements('ms-system-instructions, [role="dialog"]').filter(panel =>
        panel.matches?.('ms-system-instructions') ||
        [attr(panel, 'aria-label'), attr(panel, 'title'), ...elements('h1, h2, h3', panel).map(text)]
          .some(label => /system instructions/i.test(String(label || ''))));
      const editors = elements('textarea').filter(el => attr(el, 'placeholder') === 'Optional tone and style instructions for the model');
      if (editors.length === 1) {
        const panel = systemPanelForEditor(editors[0]);
        if (panel && !known.includes(panel)) known.push(panel);
      }
      return known;
    };
    const controls = (panel) => elements('button', panel);
    const wait = () => new Promise(resolve => (document.defaultView?.setTimeout || setTimeout)(resolve, 50));
    let panel = null, openedByUs = false;
    const alreadyOpen = panels();
    if (alreadyOpen.length === 1) panel = alreadyOpen[0];
    else if (!alreadyOpen.length) {
      const openers = elements('button').filter(el => exactLabel(el, ['system instructions']));
      if (openers.length !== 1) return fail(`System instructions opener not unique (count=${openers.length})`);
      const before = new Set(alreadyOpen);
      openers[0].click(); openedByUs = true;
      for (let attempt = 0; attempt <= 60; attempt++) {
        if (elements('textarea').filter(el => attr(el, 'placeholder') === 'Optional tone and style instructions for the model').length > 1) return fail(`System instructions pane not found (duplicate editor placeholders=${elements('textarea').filter(el => attr(el, 'placeholder') === 'Optional tone and style instructions for the model').length})`);
        const matches = panels(), opened = matches.filter(candidate => !before.has(candidate));
        if (opened.length === 1) { panel = opened[0]; break; }
        if (opened.length > 1) return fail(`System instructions pane ambiguous (count=${opened.length})`);
        if (attempt < 60) await wait();
      }
    } else return fail(`System instructions pane not unique (count=${alreadyOpen.length})`);
    if (!panel) {
      const textareas = elements('textarea');
      return fail(`System instructions pane not found (exact-placeholder=${textareas.filter(el => attr(el, 'placeholder') === 'Optional tone and style instructions for the model').length}, textareas=${textareas.length})`);
    }
    const exactEditors = elements('textarea').filter(el => attr(el, 'placeholder') === 'Optional tone and style instructions for the model');
    const editors = exactEditors.length ? exactEditors.filter(el => panel === systemPanelForEditor(el)) : elements('textarea, [contenteditable="true"]', panel);
    const writable = editors.filter(el => !el.disabled && !el.readOnly && attr(el, 'aria-disabled') !== 'true');
    if (writable.length !== 1) {
      const textareas = elements('textarea', panel).length;
      return fail(`System editor ambiguous (count=${writable.length}, exact-placeholder=${exactEditors.length}, textareas=${textareas})`);
    }
    const editor = writable[0], start = '<!-- WEBCODEX MCP BEGIN -->', end = '<!-- WEBCODEX MCP END -->';
    const current = editor.isContentEditable || attr(editor, 'contenteditable') === 'true' ? text(editor) : String(editor.value || '');
    const starts = current.split(start).length - 1, ends = current.split(end).length - 1;
    if (starts !== ends || starts > 1 || (starts === 1 && current.indexOf(start) > current.indexOf(end))) return fail(`System instruction markers invalid (begin=${starts}, end=${ends})`);
    const block = `${start}\n${String(value)}\n${end}`;
    const next = starts ? current.replace(new RegExp(`${start}[\\s\\S]*?${end}`), () => block) : `${current}${current && !current.endsWith('\n') ? '\n' : ''}${block}`;
    const setValue = (target, content) => {
      if ('value' in target) {
        const proto = Object.getPrototypeOf(target);
        const setter = Object.getOwnPropertyDescriptor(proto, 'value')?.set;
        if (setter) setter.call(target, content); else target.value = content;
      } else target.textContent = content;
      const EventCtor = document.defaultView?.InputEvent || document.defaultView?.Event;
      if (EventCtor) target.dispatchEvent(new EventCtor('input', { bubbles: true, inputType: 'insertText', data: content }));
      if (document.defaultView?.Event) target.dispatchEvent(new document.defaultView.Event('change', { bubbles: true }));
    };
    setValue(editor, next);
    const valueNow = () => editor.isContentEditable || attr(editor, 'contenteditable') === 'true' ? text(editor) : String(editor.value || '');
    if (valueNow() !== next) { setValue(editor, current); return fail('System instruction write mismatch'); }
    for (let attempt = 0; attempt <= 60; attempt++) {
      const save = controls(panel).filter(el => exactLabel(el, ['save', 'done']));
      if (!save.length) break;
      if (save.length !== 1) { setValue(editor, current); return fail(`System Save ambiguous (count=${save.length})`); }
      if (!save[0].disabled && attr(save[0], 'aria-disabled') !== 'true') { save[0].click(); break; }
      if (attempt === 60) { setValue(editor, current); return fail('System Save not enabled'); }
      await wait();
    }
    if (openedByUs && !panels().includes(panel)) return true;
    const close = controls(panel).filter(el => labels(el).some(label => /close/.test(label)));
    if (openedByUs && close.length === 1) {
      close[0].click();
      for (let attempt = 0; attempt <= 60 && panels().includes(panel); attempt++) if (attempt < 60) await wait();
      if (panels().includes(panel)) return fail('System instructions close not completed');
    } else if (openedByUs && close.length > 1) return fail(`System instructions close not completed (close controls=${close.length})`);
    return true;
  };
  const deliveryTurns = (operationId) => {
    const marker = String(operationId);
    return turns().filter((el) => {
      if (role(el) !== 'user') return false;
      const nested = [...new Set(allElements('.turn-content, ms-prompt-chunk, ms-cmark-node, ms-text-chunk, pre code', el))];
      const isResult = (value, header = false) => {
        const raw = String(value).trim();
        let parsed;
        try { parsed = JSON.parse(raw); } catch {
          if (!header) return false;
          const json = raw.match(/^User\s*\n[^\n]+\n(\{[\s\S]*\})$/i)?.[1];
          try { parsed = json && JSON.parse(json); } catch { return false; }
        }
        return !!parsed && typeof parsed === 'object' && !Array.isArray(parsed) &&
          parsed.type === 'webcodex-result' && parsed.operation_id === marker;
      };
      if (nested.some((node) => text(node).includes(`[WebCodex operation ${marker}]`) || isResult(text(node)))) return true;
      return text(el).includes(`[WebCodex operation ${marker}]`) || isResult(text(el), true);
    });
  };
  const hasDelivery = (operationId) => deliveryTurns(operationId).length > 0;
  const hiddenByUs = new Map();
  const cleanup = ({ turns: requestedTurns = [], operationIds = [] } = {}) => {
    const recognized = new Set(turns());
    const targets = new Set([...requestedTurns.filter(el => recognized.has(el)), ...operationIds.flatMap(deliveryTurns)]);
    for (const el of targets) {
      if (el.hidden || attr(el, 'data-webcodex-hidden') === 'true') continue;
      const display = el.style?.getPropertyValue('display') || '';
      const priority = el.style?.getPropertyPriority('display') || '';
      if (el.setAttribute) el.setAttribute('data-webcodex-hidden', 'true');
      else if (el.attrs) el.attrs['data-webcodex-hidden'] = 'true';
      el.hidden = true;
      el.style?.setProperty('display', 'none', 'important');
      hiddenByUs.set(el, { display, priority });
    }
  };
  const restoreCleanup = () => {
    for (const [el, { display, priority }] of hiddenByUs) {
      if (el.removeAttribute) el.removeAttribute('data-webcodex-hidden');
      else if (el.attrs) delete el.attrs['data-webcodex-hidden'];
      el.hidden = false;
      if (display) el.style?.setProperty('display', display, priority);
      else el.style?.removeProperty('display');
    }
    hiddenByUs.clear();
  };
  const annotateContext = () => {
    const recognized = turns();
    const chars = recognized.reduce((total, turn) => {
      const copy = turn.cloneNode ? turn.cloneNode(true) : turn;
      if (copy !== turn && copy.querySelectorAll)
        for (const note of copy.querySelectorAll('[data-webcodex-context]')) note.remove();
      return total + String(copy.textContent || '').length;
    }, 0);
    const count = Math.ceil(chars / 4);
    const model = recognized.filter(el => role(el) === 'model').pop();
    if (!model) return count;
    const label = `Контекст загруженных сообщений: ≈ ${count} токенов (оценка)`;
    const existing = allElements('[data-webcodex-context]', model)[0];
    if (existing && text(existing) === label) return count;
    if (existing) existing.remove();
    const note = document.createElement('small');
    note.setAttribute('data-webcodex-context', 'true');
    note.textContent = label;
    model.appendChild(note);
    return count;
  };
  return { inspect, stage, submit, stageSystemInstructions, getSystemStageError: () => systemStageError, hasDelivery, deliveryTurns, cleanup, restoreCleanup, annotateContext };
});
