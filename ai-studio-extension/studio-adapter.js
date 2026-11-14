// Selectors are based on published AI Studio DOM observations; verify against the live chat before release.
(function (root, factory) {
  if (typeof module === 'object' && module.exports) {const protocol=require('./protocol.js');module.exports=(document,location)=>factory(document,location,protocol);}
  if (root) root.WebCodexStudio = { createAdapter: (document,location)=>factory(document,location,root.WebCodexProtocol) };
})(typeof globalThis !== 'undefined' ? globalThis : this, function createAdapter(document, location, protocol) {
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
  const turns = () => {
    const candidates=allElements('ms-chat-turn, model-prompt-container, [data-turn-role], .chat-turn-container.user, .chat-turn-container.model')
      .filter(el=>visible(el)||attr(el,'data-webcodex-hidden')==='true'),set=new Set(candidates);
    return candidates.filter(el=>{for(let parent=el.parentElement||el.parent;parent;parent=parent.parentElement||parent.parent)if(set.has(parent))return false;return true;});
  };
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
    return 'active-chat';
  };
  const editorValue = (el) => el.isContentEditable || attr(el, 'contenteditable') === 'true' ? text(el) : String(el.value || '');
  let ownedDraft = null, pendingSubmit = null;
  const sourceIDs=new WeakMap(),responseRevisions=new WeakMap();
  let nextSourceID=0,nextRevision=0;
  const sourceID=node=>{if(!sourceIDs.has(node))sourceIDs.set(node,++nextSourceID);return sourceIDs.get(node);};
  const answerCopy = (el) => {
    const copy=el.cloneNode?el.cloneNode(true):el;
    if(copy===el||!copy.querySelectorAll)return copy;
    const originals=[el,...el.querySelectorAll('*')],copies=[copy,...copy.querySelectorAll('*')];
    for(let i=0;i<originals.length;i++)if(copies[i])copies[i].helperSourceNode=originals[i];
    for(let i=1;i<originals.length;i++){
      const source=originals[i];let style;
      try{style=document.defaultView?.getComputedStyle?.(source);}catch{}
      if(copies[i])copies[i].helperBlockBoundary=/^(?:block|flex|grid|flow-root|table|list-item)/.test(style?.display||'');
      const inline=attr(source,'style')||'';
      if(source.hidden||attr(source,'hidden')!==null||attr(source,'aria-hidden')==='true'||style?.display==='none'||style?.visibility==='hidden'||/(?:^|;)\s*(?:display\s*:\s*none|visibility\s*:\s*hidden)\b/i.test(inline))copies[i]?.remove();
    }
    for(const node of copy.querySelectorAll('ms-thought-accordion, ms-thought-chunk, [data-thought="true"], [data-is-thought="true"], [data-role="thought"], [data-webcodex-context], button, [role="button"], mat-icon, svg, .material-icons, .material-symbols-outlined'))node.remove();
    for(const node of copy.querySelectorAll('details')){
      const summary=node.querySelectorAll('summary')[0];
      if(attr(node,'open')===null||/^(thoughts|thinking|model thoughts|мысли|размышления)$/i.test(text(summary||{})))node.remove();
    }
    return copy;
  };
  const blockTags=new Set(['pre','header','footer','p','article','section','ms-code-block']);
  const codeText = (node) => {
    if(node.nodeType===3)return node.textContent||'';
    if(node.nodeType===8)return '';
    if(String(node.tagName||node.tag||'').toLowerCase()==='br')return '\n';
    return node.childNodes?Array.from(node.childNodes,codeText).join(''):String(node.textContent||'');
  };
  const modelText = (node) => {
    if(node.nodeType===3)return node.textContent||'';
    if(node.nodeType===8)return '';
    const tag=String(node.tagName||node.tag||'').toLowerCase();
    if(tag==='br')return '\n';
    // Code widgets contain the exact source as text nodes. Their layout adds a
    // boundary around the source, never between syntax-highlighted code tokens.
    if(tag==='pre'||tag==='code')return '\n'+codeText(node)+'\n';
    const value=node.childNodes?Array.from(node.childNodes,modelText).join(''):String(node.textContent||'');
    return blockTags.has(tag)||node.helperBlockBoundary?'\n'+value+'\n':value;
  };
  const finalText = (el) => String(answerCopy(el).textContent||'').trim();
  const chatMarkers = recognized => recognized.filter(el=>role(el)==='user').map(el=>{
    const body=userBody(el),chunks=allElements('ms-prompt-chunk.text-chunk',body);
    const raw=modelText(answerCopy(chunks[0]||body)).trimStart();
    const match=/^\[Helper session ([a-f0-9]{32})\]\r?\n/.exec(stripUserHeader(raw));
    return match?.[1];
  }).filter(Boolean);
  const sourceBlocks = copy => {
    if(!copy)return [];
    const candidates=allElements('pre, code, [role="code"], ms-code-block',copy);
    // PRE/CODE nesting is one source. Independent equal blocks retain their IDs.
    const roots=candidates.filter(node=>{
      for(let parent=node.parentElement||node.parent;parent&&parent!==copy;parent=parent.parentElement||parent.parent){
        if(candidates.includes(parent)&&String(parent.tagName||parent.tag||'').toLowerCase()!=='ms-code-block')return false;
      }
      return String(node.tagName||node.tag||'').toLowerCase()!=='ms-code-block'||!allElements('pre, code, [role="code"]',node).length;
    });
    return roots.map(node=>({id:sourceID(node.helperSourceNode||node),text:codeText(node),kind:'code'}));
  };
  const userBody = (el) => {
    const roleContent=allElements('[data-turn-role], .chat-turn-container',el).find(node=>visible(node)&&(roleValue(node)==='user'||node.matches?.('.chat-turn-container.user')));
    if(roleContent)return roleContent;
    const bodies=allElements('.turn-content',el).filter(visible);
    return bodies.length===1?bodies[0]:el;
  };
  const parseResultOperationId = (raw) => {
    const candidate=stripUserHeader(raw).replace(/^(?:helper-result|webcodex-result|json|code)\s*(?=\{)/i,'');
    try{JSON.parse(candidate);}catch{return '';}
    const roots=protocol.findJSONValues(candidate);
    if(roots.length!==1)return '';
    const value=roots[0];
    return value&&typeof value==='object'&&!Array.isArray(value)&&protocol.isResult(value)&&typeof value.operation_id==='string'&&value.operation_id?value.operation_id:'';
  };
  const stripUserHeader = (raw) => {
    const value=String(raw).trim(),start=value.indexOf('{');
    if(start>0&&/^User(?:[ \t]+[^\n]*|[ \t]*\n[^\n]*)?[\r\n \t]*$/i.test(value.slice(0,start)))return value.slice(start);
    return value.replace(/^User(?:[ \t]+[^\n]*)?\n+/i,'');
  };
  const commonMarkUnescape = (raw) => {
    let result = '';
    for (let i = 0; i < raw.length; i++) {
      if (raw[i] === '\\' && i + 1 < raw.length) {
        const code = raw.charCodeAt(i + 1);
        if ((code >= 33 && code <= 47) || (code >= 58 && code <= 64) || (code >= 91 && code <= 96) || (code >= 123 && code <= 126)) {
          result += raw[++i];
          continue;
        }
      }
      result += raw[i];
    }
    return result;
  };
  const expectedDeliveryText = (operationId, expectedText) => {
    if (typeof expectedText !== 'string') return '';
    try {
      const expected = JSON.parse(expectedText);
      return protocol.isResult(expected) && expected.operation_id === operationId ? expectedText : '';
    } catch { return ''; }
  };
  const matchesExpectedDelivery = (raw, expectedText) => {
    const candidate = stripUserHeader(raw);
    if(candidate===expectedText||candidate===commonMarkUnescape(expectedText))return true;
    try{const values=protocol.findJSONValues(candidate);return values.length===1&&JSON.stringify(values[0])===JSON.stringify(JSON.parse(expectedText));}catch{return false;}
  };
  const inspect = () => {
    if (!location.hostname || location.hostname.toLowerCase() !== 'aistudio.google.com')
      return { conversationKey: '', generating: false, editorEmpty: false, supported: false, error: 'unsupported site', latestModel: null, latestUser: null };
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
    const modelContent = model && [...new Set(allElements('[data-turn-role], .chat-turn-container', model))]
      .find(el => visible(el)&&(roleValue(el) === 'model' || el.matches?.('.chat-turn-container.model')));
    const modelCopy=model?answerCopy(modelContent||model):null;
    const blocks=sourceBlocks(modelCopy);
    const renderedSource=modelCopy?modelText(modelCopy):'';
    const signature=JSON.stringify([renderedSource,blocks]);
    if(model&&responseRevisions.get(model)?.signature!==signature)responseRevisions.set(model,{signature,revision:++nextRevision});
    let modelSource=String(modelCopy?.textContent||'').trim();
    let parsedModel=model?protocol.analyzeReply(modelSource):null;
    // Preserve already valid JSON byte for byte, including strings spanning DOM
    // nodes. A raw attachment additionally needs the rendered block boundaries
    // that textContent drops between the language header and the PRE element.
    if(model&&(parsedModel.kind!=='call'||parsedModel.fileSource)&&/<<<HELPER_FILE_(?:BEGIN|END):/.test(modelSource)){
      modelSource=modelText(modelCopy).trim();
      parsedModel=protocol.analyzeReply(modelSource);
    }
    let sourceError='';
    if(parsedModel?.kind==='call'&&parsedModel.call.name==='exec_command'){
      const exactSources=allElements('pre, code, ms-code-block, [role="code"]',modelCopy).filter(node=>{
        const candidate=protocol.analyzeReply(codeText(node));
        return candidate.kind==='call'&&JSON.stringify(candidate.call)===JSON.stringify(parsedModel.call);
      });
      if(!exactSources.length)sourceError='Command JSON was rendered as Markdown prose, which can remove operators, backslashes or emphasis markers. Resend the call inside a real fenced code block: place a blank line BEFORE the opening helper-call fence, and put its JSON on the following line. Do not place the opening fence after a sentence.';
    }
    const modelError=/^(?:An internal error has occurred\.?|Internal error\.?)$/i.test(modelSource.trim())?'AI Studio: An internal error has occurred. Generation failed; no new command was executed.':'';
    const user = recognized.filter((el) => role(el) === 'user').pop() || null;
    const latestUserText = user ? finalText(userBody(user)) : '';
    const latestUserOperationId = user && parseResultOperationId(latestUserText) || '';
    return {
      conversationKey: conversationKey(), chatMarkers:chatMarkers(recognized), chatAddress:/^\/(?:app\/)?prompts\/(?!new_chat\b|new\b)[^/]+\/?$/.test(location.pathname||'')?location.pathname:'', generating, controlsPending,
      editorEmpty: candidateEditors.length === 1 && editorValue(candidateEditors[0]) === '',
      supported: !error, error, modelError, latestModel: model ? { element: model, sourceError, sourceText:modelSource, sourceBlocks:blocks, renderedSource, sourceRevision:responseRevisions.get(model).revision, text:parsedModel.kind==='call'?'```helper-call\n'+parsedModel.raw+'\n```':modelSource } : null,
      latestUser: user ? { element: user, text: latestUserText, operationId: latestUserOperationId } : null,
      modelAfterUser: !user || recognized.indexOf(model) > recognized.indexOf(user),
    };
  };
  const setEditorValue = (editor,value) => {
    if ('value' in editor) {
      const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(editor), 'value')?.set;
      if (setter) setter.call(editor, String(value)); else editor.value = String(value);
    } else editor.textContent = String(value);
    const EventCtor = document.defaultView && document.defaultView.InputEvent;
    if (editor.dispatchEvent && EventCtor) editor.dispatchEvent(new EventCtor('input', { bubbles: true, inputType: 'insertText', data: String(value) }));
    return editorValue(editor) === String(value);
  };
  const stage = (value) => {
    const state = inspect();
    const candidateEditors = editors();
    if (!state.supported || (state.generating && !(state.controlsPending && !state.latestModel)) || candidateEditors.length !== 1 || !state.editorEmpty) return false;
    const staged = setEditorValue(candidateEditors[0],value);
    if (staged) ownedDraft = { value: String(value) };
    return staged;
  };
  const prepareSessionMessage = marker => {
    if(!/^[a-f0-9]{32}$/.test(marker))return false;
    const state=inspect(),current=editors();
    if(!state.supported||state.generating&&!state.controlsPending||current.length!==1)return false;
    if(state.chatMarkers.includes(marker))return true;
    const value=editorValue(current[0]);
    if(!value)return false;
    if(ownedDraft?.value===value)return true;
    const prefix='[Helper session '+marker+']\nSession marker for this chat is '+marker+'. Follow the Helper system instructions.\n[/Helper session]\n\n';
    if(value.startsWith(prefix))return true;
    return setEditorValue(current[0],prefix+value);
  };
  const submit = (value, isCurrent = () => true) => {
    if (pendingSubmit) return Promise.resolve(false);
    const draft = String(value);
    if (!isCurrent()) return Promise.resolve(false);
    const editor = editors()[0];
    const ownsDraft = ownedDraft?.value === draft && editor && editorValue(editor) === draft;
    if (!ownsDraft && !stage(draft)) return Promise.resolve(false);
    const promise = (async () => {
      for (let attempt = 0; attempt <= 60; attempt++) {
        const state = inspect(), currentEditors = editors();
        if (!isCurrent() || !state.supported || (state.generating && !state.controlsPending) || currentEditors.length !== 1 || editorValue(currentEditors[0]) !== draft) return false;
        const button = submitButton();
        if (button && !button.disabled && attr(button, 'aria-disabled') !== 'true') {
          if (!isCurrent() || ownedDraft?.value !== draft) return false;
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
  let uploadOwner=null;
  const promptScope=()=>{const editor=editors()[0];return editor?.closest?.('ms-prompt-input-wrapper')||editor?.closest?.('ms-prompt-box')||editor?.closest?.('ms-autosize-textarea')||editor?.parentElement||editor?.parent;};
  const ownsDraft=value=>{const current=editors();return current.length===1&&ownedDraft?.value===value&&editorValue(current[0])===value;};
  const attachFiles=async(files,{key,isCurrent=()=>true}={})=>{
    const scope=promptScope(),view=document.defaultView||globalThis;
    if(!scope||!files.length||!isCurrent())return{ok:false,error:'Поле прикрепления файлов недоступно'};
    const names=files.map(file=>file.name),signature=JSON.stringify([key,...files.map(file=>[file.name,file.size])]);
    const scopeLabels=()=>{const copy=scope.cloneNode(true);for(const node of copy.querySelectorAll('textarea,[contenteditable="true"],[hidden],[aria-hidden="true"]'))node.remove();return[copy,...allElements('*',copy)].map(node=>String(node.textContent||'').trim());};
    if(uploadOwner&&uploadOwner.signature!==signature)return{ok:false,error:'Сначала завершите предыдущее прикрепление файлов'};
    if(!uploadOwner){
      const inputs=allElements('input[type="file"]',scope).filter(input=>!input.disabled);
      const globalInputs=inputs;
      if(globalInputs.length>1)return{ok:false,error:'Найдено несколько полей загрузки файлов; загрузка не начата'};
      const input=globalInputs[0];
      const existingAssets=allElements('img, video, audio, ms-prompt-attachment, ms-file-preview, ms-attachment, .attachment, .file-preview, .file-chip, [data-file-name], [data-attachment-id]',scope);
      const removeAssets=allElements('button',scope).some(button=>labels(button).some(label=>/^(?:remove|delete|удалить)(?:\s+(?:file|attachment|asset|файл|вложение))?(?:\s|$)/i.test(label)));
      if(input?.files?.length||existingAssets.length||removeAssets||names.some(name=>scopeLabels().includes(name)))return{ok:false,error:'В поле уже есть вложения. Они сохранены; новое прикрепление не начато'};
      if(input&&!input.multiple&&files.length>1)return{ok:false,error:'Штатное поле принимает только один файл'};
      const transfer=new view.DataTransfer();for(const file of files)transfer.items.add(file);
      uploadOwner={signature,key,names,input,attempted:true,started:Date.now(),acknowledged:false};
      try{
        if(input){
          input.files=transfer.files;
          uploadOwner.inputFiles=Array.from(input.files);
          input.dispatchEvent(new view.Event('input',{bubbles:true}));
          input.dispatchEvent(new view.Event('change',{bubbles:true}));
        }else{
          for(const type of ['dragenter','dragover','drop'])scope.dispatchEvent(new view.DragEvent(type,{bubbles:true,cancelable:true,dataTransfer:transfer}));
        }
      }catch(error){return{ok:false,error:'Не удалось передать файл штатному загрузчику: '+error.message};}
    }
    while(isCurrent()){
      const errors=elements('[role="alert"], .error-message',scope).map(text).filter(Boolean);
      if(errors.length)return{ok:false,error:'AI Studio отклонила вложение: '+errors.join(' ')};
      const busy=elements('[role="progressbar"], mat-progress-bar, mat-spinner, mat-progress-spinner, [aria-busy="true"]',scope).length>0;
      const labels=scopeLabels(),named=names.every(name=>labels.includes(name));
      if(named||busy)uploadOwner.acknowledged=true;
      if(!uploadOwner.acknowledged&&Date.now()-uploadOwner.started>10000)return{ok:false,error:'AI Studio не подтвердила прикрепление файла. Результат сохранён; сообщение не отправлено'};
      const run=submitButton();
      if(named&&!busy&&run&&!run.disabled&&attr(run,'aria-disabled')!=='true')return{ok:true};
      await new Promise(resolve=>(view.setTimeout||setTimeout)(resolve,250));
    }
    return{ok:false,error:'Прикрепление остановлено. Уже выбранные файлы сохранены в поле ввода'};
  };
  const releaseFiles=key=>{
    if(!uploadOwner||uploadOwner.key!==key)return;
    const input=uploadOwner.input,owned=uploadOwner.inputFiles;
    if(input&&owned&&input.files?.length===owned.length&&Array.from(input.files).every((file,index)=>file===owned[index]))input.value='';
    uploadOwner=null;
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
    const presetName = 'Helper',legacyPresetName='WebCodex MCP';
    const presetNames=[presetName,legacyPresetName];
    const preferredPreset=items=>{const current=items.filter(el=>text(el)===presetName);return current.length?current:items.filter(el=>text(el)===legacyPresetName);};
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
    const namedPresets = (scope) => preferredPreset(allElements('option',scope));
    const selects = elements('select', panel);
    let presetManaged = false;
    if (selects.length) {
      if (selects.length !== 1) return fail(`System preset selector ambiguous (count=${selects.length})`);
      const options = namedPresets(selects[0]);
      if (options.length > 1) return fail(`System preset Helper not unique (count=${options.length})`);
      if (!options.length) {
        const selected = allElements('option', selects[0]).find(option => option.value === selects[0].value);
        if (!selected) return fail('Current system preset is not selected');
        // A personal preset keeps its name and text; append only our managed block.
        presetManaged = ['create new instruction', 'untitled'].includes(text(selected).toLowerCase().replace(/^\+\s*/, ''));
      } else {
        if (selects[0].value !== options[0].value) {
          selects[0].value = options[0].value;
          const EventCtor = document.defaultView?.Event;
          if (EventCtor) selects[0].dispatchEvent(new EventCtor('change', { bubbles: true }));
          else selects[0].dispatchEvent?.({ type: 'change', bubbles: true });
          await wait();
        }
        presetManaged = true;
      }
    } else {
      const selectorText = el => text(el).toLowerCase().replace(/^\+\s*/, '');
      const selectors = elements('button, [role="combobox"], [role="button"]', panel).filter(el =>
        attr(el,'role')==='combobox'||['create new instruction', 'untitled',...presetNames.map(name=>name.toLowerCase())].includes(selectorText(el)));
      const named = preferredPreset(selectors);
      if (named.length > 1) return fail(`System preset Helper ambiguous (count=${named.length})`);
      if (!named.length) {
        if (selectors.length > 1) return fail(`System preset selector ambiguous (count=${selectors.length})`);
        if (selectors.length === 1) {
          selectors[0].click();
          await wait();
          const options = preferredPreset(elements('[role="option"], [role="menuitem"]',document));
          if (options.length === 1) { options[0].click(); presetManaged = true; await wait(); }
          else {
            const KeyEvent = document.defaultView?.KeyboardEvent;
            if (KeyEvent) selectors[0].dispatchEvent(new KeyEvent('keydown', { key: 'Escape', bubbles: true }));
            if (options.length > 1) return fail(`System preset Helper ambiguous (count=${options.length})`);
            presetManaged = ['create new instruction','untitled'].includes(selectorText(selectors[0]));
          }
        }
      } else presetManaged = true;
    }
    const exactEditors = elements('textarea').filter(el => attr(el, 'placeholder') === 'Optional tone and style instructions for the model');
    const editors = exactEditors.length ? exactEditors.filter(el => panel === systemPanelForEditor(el)) : elements('textarea, [contenteditable="true"]', panel);
    const writable = editors.filter(el => !el.disabled && !el.readOnly && attr(el, 'aria-disabled') !== 'true');
    if (writable.length !== 1) {
      const textareas = elements('textarea', panel).length;
      return fail(`System editor ambiguous (count=${writable.length}, exact-placeholder=${exactEditors.length}, textareas=${textareas})`);
    }
    let editor = writable[0];
    const titles = elements('input', panel).filter(el => attr(el, 'placeholder') === 'Title');
    if (titles.length > 1) return fail(`System preset title ambiguous (count=${titles.length})`);
    const start = '<!-- HELPER BEGIN -->', end = '<!-- HELPER END -->';
    let current = editor.isContentEditable || attr(editor, 'contenteditable') === 'true' ? text(editor) : String(editor.value || '');
    const normalized=current.replaceAll('<!-- WEBCODEX MCP BEGIN -->',start).replaceAll('<!-- WEBCODEX MCP END -->',end);
    const onlyManaged = normalized.startsWith(`${start}\n`) && normalized.endsWith(`\n${end}`) && normalized.split(start).length === 2 && normalized.split(end).length === 2;
    const starts = normalized.split(start).length - 1, ends = normalized.split(end).length - 1;
    if (starts !== ends || starts > 1 || (starts === 1 && normalized.indexOf(start) > normalized.indexOf(end))) return fail(`System instruction markers invalid (begin=${starts}, end=${ends})`);
    let titleChanged=false;
    const oldTitle=titles.length?String(titles[0].value||'').trim():'';
    const ownedTitle=presetManaged||oldTitle===legacyPresetName&&starts===1;
    if (ownedTitle && titles.length && (oldTitle===legacyPresetName||['', 'untitled'].includes(oldTitle.toLowerCase())&&(!current.trim()||onlyManaged))) {
      const title = titles[0], proto = Object.getPrototypeOf(title), setter = Object.getOwnPropertyDescriptor(proto, 'value')?.set;
      if (setter) setter.call(title, presetName); else title.value = presetName;
      const EventCtor = document.defaultView?.InputEvent || document.defaultView?.Event;
      if (EventCtor) title.dispatchEvent(new EventCtor('input', { bubbles: true }));
      if (document.defaultView?.Event) title.dispatchEvent(new document.defaultView.Event('change', { bubbles: true }));
      if (String(title.value || '') !== presetName) return fail('System preset title write mismatch');
      titleChanged=true;
    }
    const block = `${start}\n${String(value)}\n${end}`;
    const next = starts ? normalized.replace(new RegExp(`${start}[\\s\\S]*?${end}`), () => block) : `${current}${current && !current.endsWith('\n') ? '\n' : ''}${block}`;
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
    const unchanged = current === next;
    if (!unchanged) setValue(editor, next);
    const valueNow = () => editor.isContentEditable || attr(editor, 'contenteditable') === 'true' ? text(editor) : String(editor.value || '');
    if (valueNow() !== next) { setValue(editor, current); return fail('System instruction write mismatch'); }
    if (!unchanged||titleChanged) {
      for (let attempt = 0; attempt <= 60; attempt++) {
        const save = controls(panel).filter(el => exactLabel(el, ['save', 'done']));
        if (!save.length) break;
        if (save.length !== 1) { setValue(editor, current); return fail(`System Save ambiguous (count=${save.length})`); }
        if (!save[0].disabled && attr(save[0], 'aria-disabled') !== 'true') { save[0].click(); break; }
        if (attempt === 60) { setValue(editor, current); return fail('System Save not enabled'); }
        await wait();
      }
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
  const deliveryTurns = (operationId, expectedText) => {
    const marker = String(operationId);
    const expected = expectedDeliveryText(marker, expectedText);
    return turns().filter((el) => {
      if (role(el) !== 'user') return false;
      const raw=finalText(userBody(el));
      if (expected) return matchesExpectedDelivery(raw,expected);
      return raw.includes(`[WebCodex operation ${marker}]`) || parseResultOperationId(raw) === marker;
    });
  };
  const hasDelivery = (operationId, expectedText) => deliveryTurns(operationId, expectedText).length > 0;
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
    const label = `Текст сообщений: ≈ ${count} токенов (оценка; содержимое вложений не учтено)`;
    const existing = allElements('[data-webcodex-context]', model)[0];
    if (existing && text(existing) === label) return count;
    if (existing) existing.remove();
    const note = document.createElement('small');
    note.setAttribute('data-webcodex-context', 'true');
    note.textContent = label;
    model.appendChild(note);
    return count;
  };
  return { inspect, stage, submit, prepareSessionMessage, attachFiles, ownsDraft, releaseFiles, stageSystemInstructions, getSystemStageError: () => systemStageError, hasDelivery, deliveryTurns, cleanup, restoreCleanup, annotateContext };
});
