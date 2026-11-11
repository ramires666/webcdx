// Selectors are based on published AI Studio DOM observations; verify against the live chat before release.
(function (root, factory) {
  if (typeof module === 'object' && module.exports) module.exports = factory;
  if (root) root.WebCodexStudio = { createAdapter: factory };
})(typeof globalThis !== 'undefined' ? globalThis : this, function createAdapter(document, location) {
  const visible = (el) => !!el && el.isConnected !== false && el.hidden !== true &&
    (!el.getAttribute || el.getAttribute('aria-hidden') !== 'true') &&
    (!el.getBoundingClientRect || el.getBoundingClientRect().width > 0);
  const attr = (el, name) => el.getAttribute ? el.getAttribute(name) : null;
  const text = (el) => String(el.innerText || el.textContent || '').trim();
  const elements = (selector, scope = document) => Array.from(scope.querySelectorAll(selector)).filter(visible);
  const roleValue = (el) => String(attr(el, 'data-turn-role') || '').toLowerCase();
  const roleMarkers = (el) => [el, ...elements('[data-turn-role], .chat-turn-container', el)]
    .map(node => roleValue(node) || (node.matches?.('.chat-turn-container.user') ? 'user' : node.matches?.('.chat-turn-container.model') ? 'model' : ''))
    .filter(Boolean);
  const marked = (el, kind) => roleMarkers(el).includes(kind);
  const turns = () => elements('ms-chat-turn, model-prompt-container');
  const role = (el) => marked(el, 'model') ? 'model' : marked(el, 'user') ? 'user' : '';
  const editors = () => {
    const scoped = elements('ms-prompt-input-wrapper textarea, ms-prompt-box textarea, ms-autosize-textarea textarea, ms-prompt-input-wrapper [contenteditable="true"], ms-prompt-box [contenteditable="true"], ms-autosize-textarea [contenteditable="true"]');
    const candidates = scoped.length ? scoped : elements('textarea, [contenteditable="true"]');
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
      for (const junk of copy.querySelectorAll('ms-thought-accordion, ms-thought-chunk, button')) junk.remove();
      return text(copy);
    }
    return text(el);
  };
  const finalText = (el) => {
    const content = el.cloneNode ? el.cloneNode(true) : el;
    if (content !== el && content.querySelectorAll) {
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
  const hasDelivery = (operationId) => {
    const marker = String(operationId);
    return turns().some((el) => {
      if (role(el) !== 'user') return false;
      const contents = [el, ...elements('.chat-turn-container.user, [data-turn-role]', el), ...elements('ms-text-chunk', el)];
      return contents.some((node) => text(node).includes(`[WebCodex operation ${marker}]`) || (() => {
        try { return JSON.parse(text(node)).operation_id === marker; } catch { return false; }
      })());
    });
  };
  return { inspect, stage, submit, hasDelivery };
});
