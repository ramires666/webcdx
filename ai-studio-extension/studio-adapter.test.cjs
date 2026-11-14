const { test } = require('node:test');
const assert = require('node:assert/strict');
const createAdapter = require('./studio-adapter.js');

const { Node, Textarea, setup } = require('./test-fixtures/studio-dom.cjs');

test('session context accompanies the existing user request once and preserves its exact text',()=>{
  const marker='a'.repeat(32),request='  Моя задача\r\n\\literal "text"\n';
  const state=setup({draft:request,empty:true});
  assert.equal(state.adapter.prepareSessionMessage(marker),true);
  const staged=state.editor.value;
  assert.ok(staged.startsWith('[Helper session '+marker+']\n'));
  assert.ok(staged.endsWith('\n\n'+request));
  assert.equal(state.run.clicked,undefined);
  assert.equal(state.adapter.prepareSessionMessage(marker),true);
  assert.equal(state.editor.value,staged);
  const user=state.root.add(new Node('ms-chat-turn',{'data-turn-role':'User'},staged));
  assert.deepEqual(state.adapter.inspect().chatMarkers,[marker]);
  state.editor.value='Next request';state.adapter.prepareSessionMessage(marker);
  assert.equal(state.editor.value,'Next request');
  user.textContent='Example\n'+staged;
  assert.deepEqual(state.adapter.inspect().chatMarkers,[]);
});

test('session context stays isolated across tabs and preserves extension-owned results',()=>{
  const left=setup({draft:'Left',empty:true}),right=setup({draft:'Right',empty:true});
  left.adapter.prepareSessionMessage('a'.repeat(32));right.adapter.prepareSessionMessage('b'.repeat(32));
  assert.ok(!left.editor.value.includes('b'.repeat(32)));
  assert.ok(!right.editor.value.includes('a'.repeat(32)));
  const result=setup({empty:true});assert.equal(result.adapter.stage('```helper-result\n{}\n```'),true);
  result.adapter.prepareSessionMessage('a'.repeat(32));
  assert.equal(result.editor.value,'```helper-result\n{}\n```');
});

test('source blocks preserve exact raw bytes, provenance and stable revisions',()=>{
  const state=setup();state.chunk.remove();
  const widget=state.model.children[0].add(new Node('ms-code-block'));
  widget.add(new Node('header',{},'Helper'));
  const pre=widget.add(new Node('pre'));
  const code=pre.add(new Node('code',{},'HELPER_marker write_file\r\n---\r\n  \\n "😀"\r\n\r\nEND_marker'));
  widget.add(new Node('button',{},'Copy'));
  const first=state.adapter.inspect().latestModel;
  assert.equal(first.sourceBlocks.length,1);
  assert.equal(first.sourceBlocks[0].text,code.textContent);
  const same=state.adapter.inspect().latestModel;
  assert.equal(same.sourceRevision,first.sourceRevision);
  assert.equal(same.sourceBlocks[0].id,first.sourceBlocks[0].id);
  code.textContent+=' changed';
  assert.notEqual(state.adapter.inspect().latestModel.sourceRevision,first.sourceRevision);
});

test('independent identical sources stay separate while hidden copies and thoughts disappear',()=>{
  const state=setup();state.chunk.remove();const body=state.model.children[0];
  for(const tag of ['pre','code'])body.add(new Node(tag,{},'same source'));
  body.add(new Node('pre',{'aria-hidden':'true'},'same source'));
  body.add(new Node('ms-thought-chunk')).add(new Node('pre',{},'same source'));
  const blocks=state.adapter.inspect().latestModel.sourceBlocks;
  assert.equal(blocks.length,2);
  assert.equal(blocks[0].text,blocks[1].text);
  assert.notEqual(blocks[0].id,blocks[1].id);
});

test('conversation key stays bound to the active tab across URL and DOM changes', () => {
  const state = setup();
  const original = state.adapter.inspect().conversationKey;
  state.location.search = '?settings=changed';
  assert.equal(state.adapter.inspect().conversationKey, original);
  state.document.documentElement.attrs['data-conversation-id'] = 'thread-1';
  assert.equal(state.adapter.inspect().conversationKey, original);
  state.document.documentElement.attrs['data-conversation-id'] = 'thread-2';
  assert.equal(state.adapter.inspect().conversationKey, original);
  state.location.pathname = '/prompts/p2';
  state.location.search = '?chat=another';
  state.document.documentElement.attrs['data-conversation-id'] = 'thread-3';
  assert.equal(original, 'active-chat');
  assert.equal(state.adapter.inspect().conversationKey, original);
});

function systemSetup({ original = 'Keep these user instructions.', saveDelay = false, malformed = false, duplicatePanels = false, saveCloses = false } = {}) {
  const state = setup({ draft: 'chat draft' });
  const opener = new Node('button', { 'aria-label': 'System instructions' });
  state.root.add(opener);
  const dialogs = [];
  opener.click = () => {
    for (let i = 0; i < (duplicatePanels ? 2 : 1); i++) {
      const panel = new Node('section', { role: 'dialog', 'aria-label': 'System instructions' });
      panel.add(new Node('h2', {}, 'System instructions'));
      const editor = panel.add(new Textarea(malformed ? `${original}\n<!-- HELPER BEGIN --> duplicate` : original));
      const save = panel.add(new Node('button', { 'aria-label': 'Save' }));
      if (saveCloses) save.click = () => { save.clicked = true; state.root.children = state.root.children.filter(child => child !== panel); panel.isConnected = false; };
      if (saveDelay) {
        save.disabled = true;
        state.document.defaultView.setTimeout = callback => setTimeout(() => { save.disabled = false; callback(); }, 5);
      }
      const close = panel.add(new Node('button', { 'aria-label': 'Close' }));
      close.click = () => { state.root.children = state.root.children.filter(child => child !== panel); panel.isConnected = false; };
      state.root.add(panel); dialogs.push({ panel, editor, save, close });
    }
  };
  return { ...state, opener, dialogs, system: (value) => state.adapter.stageSystemInstructions(value) };
}

test('stages a bounded system instruction block and preserves surrounding user text', async () => {
  const state = systemSetup();
  assert.equal(await state.system('managed instructions'), true);
  assert.equal(state.dialogs[0].editor.value, 'Keep these user instructions.\n<!-- HELPER BEGIN -->\nmanaged instructions\n<!-- HELPER END -->');
  assert.equal(state.dialogs[0].save.clicked, true);
  assert.equal(state.dialogs[0].panel.isConnected, false, 'closes only the panel it opened');
  assert.equal(state.editor.value, 'chat draft');
  assert.equal(state.run.clicked, undefined, 'staging never runs the prompt');
});

test('skips input and Save when the managed block is already current, then closes its panel', async () => {
  const original = 'Keep these user instructions.\n<!-- HELPER BEGIN -->\nmanaged instructions\n<!-- HELPER END -->';
  const state = systemSetup({ original });
  assert.equal(await state.system('managed instructions'), true);
  assert.equal(state.dialogs[0].editor.value, original);
  assert.deepEqual(state.dialogs[0].editor.events, []);
  assert.equal(state.dialogs[0].save.clicked, undefined);
  assert.equal(state.dialogs[0].panel.isConnected, false);
});

test('migrates the legacy preset name and managed block once without duplicating personal instructions',async()=>{
  const original='Keep personal rules.\n<!-- WEBCODEX MCP BEGIN -->\nold tools\n<!-- WEBCODEX MCP END -->';
  const state=systemSetup({original});const open=state.opener.click;
  let saves=0,title;
  state.opener.click=()=>{open();const dialog=state.dialogs.at(-1);title=dialog.panel.add(new Node('input',{placeholder:'Title'}));title.value='WebCodex MCP';dialog.save.click=()=>{saves++;};};
  assert.equal(await state.system('Helper tools'),true);
  const migrated=state.dialogs[0].editor.value;
  assert.equal(title.value,'Helper');assert.doesNotMatch(migrated,/webcodex/i);
  assert.equal(migrated,'Keep personal rules.\n<!-- HELPER BEGIN -->\nHelper tools\n<!-- HELPER END -->');assert.equal(saves,1);
  const unchanged=systemSetup({original:migrated});assert.equal(await unchanged.system('Helper tools'),true);
  assert.deepEqual(unchanged.dialogs[0].editor.events,[]);assert.equal(unchanged.dialogs[0].save.clicked,undefined);
  const mixed=systemSetup({original:original+'\n<!-- HELPER BEGIN -->\nother\n<!-- HELPER END -->'});
  assert.equal(await mixed.system('Helper tools'),false,'two managed blocks are rejected rather than silently duplicated');
});

test('updates only its existing bounded block, waits for Save, and rejects malformed or ambiguous panels', async () => {
  const state = systemSetup({ original: 'User text $&\n<!-- HELPER BEGIN -->\nold\n<!-- HELPER END -->', saveDelay: true });
  assert.equal(await state.system('$&'), true);
  assert.equal(state.dialogs[0].editor.value, 'User text $&\n<!-- HELPER BEGIN -->\n$&\n<!-- HELPER END -->');
  assert.equal(state.dialogs[0].save.clicked, true);
  assert.equal(await systemSetup({ malformed: true }).system('new'), false);
  assert.equal(await systemSetup({ original: '<!-- HELPER END -->\n<!-- HELPER BEGIN -->' }).system('new'), false);
  assert.equal(await systemSetup({ duplicatePanels: true }).system('new'), false);
  const closesOnSave = systemSetup({ saveCloses: true });
  assert.equal(await closesOnSave.system('saved'), true);
  assert.equal(closesOnSave.dialogs[0].save.clicked, true);
});

test('stages the observed system editor without a dialog or Save button and leaves Title and chat untouched', async () => {
  const state = setup({ draft: 'chat draft' });
  const opener = state.root.add(new Node('button', { 'aria-label': 'System instructions' }));
  let pane, title, editor;
  opener.click = () => {
    pane = state.root.add(new Node('section'));
    pane.add(new Node('h2', {}, 'System instructions'));
    title = pane.add(new Node('input', { placeholder: 'Title' }, ''));
    title.value = 'My title';
    editor = pane.add(new Textarea('Keep these user instructions.'));
    editor.attrs.placeholder = 'Optional tone and style instructions for the model';
  };
  assert.equal(await state.adapter.stageSystemInstructions('managed instructions'), true);
  assert.equal(title.value, 'My title');
  assert.equal(editor.value, 'Keep these user instructions.\n<!-- HELPER BEGIN -->\nmanaged instructions\n<!-- HELPER END -->');
  assert.equal(state.editor.value, 'chat draft');
  assert.equal(state.adapter.inspect().supported, true, 'system textarea stays excluded from chat editor discovery');
});

test('rejects duplicate observed system editor placeholders', async () => {
  const state = setup();
  const opener = state.root.add(new Node('button', { 'aria-label': 'System instructions' }));
  opener.click = () => {
    const pane = state.root.add(new Node('section'));
    pane.add(new Node('h2', {}, 'System instructions'));
    for (let i = 0; i < 2; i++) {
      const editor = pane.add(new Textarea('Keep this'));
      editor.attrs.placeholder = 'Optional tone and style instructions for the model';
    }
  };
  assert.equal(await state.adapter.stageSystemInstructions('managed instructions'), false);
  assert.match(state.adapter.getSystemStageError(), /pane not found.*duplicate editor placeholders=2/);
});

test('stages the exact system textarea when its heading is a div and exposes safe failure diagnostics', async () => {
  const state = setup({ draft: 'chat draft' });
  const opener = state.root.add(new Node('button', { 'aria-label': 'System instructions' }));
  let pane, title, editor;
  opener.click = () => {
    pane = state.root.add(new Node('section'));
    pane.add(new Node('div', {}, 'System instructions'));
    title = pane.add(new Node('input', { placeholder: 'Title' })); title.value = 'Untouched title';
    editor = pane.add(new Textarea('Keep this'));
    editor.attrs.placeholder = 'Optional tone and style instructions for the model';
  };
  assert.equal(await state.adapter.stageSystemInstructions('managed'), true);
  assert.equal(title.value, 'Untouched title');
  assert.equal(state.editor.value, 'chat draft');
  assert.match(editor.value, /managed/);
  assert.equal(state.adapter.getSystemStageError(), '');

  const missing = setup();
  const missingOpener = missing.root.add(new Node('button', { 'aria-label': 'System instructions' }));
  missingOpener.click = () => missing.root.add(new Node('section'));
  assert.equal(await missing.adapter.stageSystemInstructions('managed'), false);
  assert.match(missing.adapter.getSystemStageError(), /System instructions pane not found/);
  assert.match(missing.adapter.getSystemStageError(), /exact-placeholder=0, textareas=1/);
});

test('reuses the exact named native preset, requeries its editor, and names only an empty Untitled preset', async () => {
  const state = setup({ draft: 'chat draft' });
  const opener = state.root.add(new Node('button', { 'aria-label': 'System instructions' }));
  let panel, title, editor, oldEditor, select;
  opener.click = () => {
    panel = state.root.add(new Node('section', { role: 'dialog', 'aria-label': 'System instructions' }));
    panel.add(new Node('h2', {}, 'System instructions'));
    select = panel.add(new Node('select'));
    const untitled = select.add(new Node('option', { value: 'untitled' }, 'Untitled'));
    const named = select.add(new Node('option', { value: 'webcodex' }, 'Helper'));
    untitled.value = 'untitled'; named.value = 'webcodex';
    select.value = untitled.attrs.value;
    title = panel.add(new Node('input', { placeholder: 'Title' })); title.value = 'Untitled';
    oldEditor = panel.add(new Textarea(''));
    oldEditor.attrs.placeholder = 'Optional tone and style instructions for the model';
    select.dispatchEvent = event => {
      select.events.push(event);
      if (select.value === named.attrs.value) {
        oldEditor.isConnected = false;
        panel.children = panel.children.filter(child => child !== oldEditor);
        editor = panel.add(new Textarea(''));
        editor.attrs.placeholder = 'Optional tone and style instructions for the model';
      }
    };
    void untitled;
  };
  assert.equal(await state.adapter.stageSystemInstructions('managed instructions'), true);
  assert.equal(select.value, 'webcodex');
  assert.equal(title.value, 'Helper');
  assert.equal(editor.value, '<!-- HELPER BEGIN -->\nmanaged instructions\n<!-- HELPER END -->');
  assert.equal(oldEditor.value, '', 'writes only to editor after preset selection');
  assert.equal(state.editor.value, 'chat draft');
});

test('fails safely on duplicate named presets and never chooses among duplicate Untitled selectors', async () => {
  const duplicate = setup();
  const opener = duplicate.root.add(new Node('button', { 'aria-label': 'System instructions' }));
  opener.click = () => {
    const panel = duplicate.root.add(new Node('section', { role: 'dialog', 'aria-label': 'System instructions' }));
    panel.add(new Node('h2', {}, 'System instructions'));
    const select = panel.add(new Node('select'));
    select.add(new Node('option', { value: 'one' }, 'Helper'));
    select.add(new Node('option', { value: 'two' }, 'Helper'));
    const editor = panel.add(new Textarea('')); editor.attrs.placeholder = 'Optional tone and style instructions for the model';
  };
  assert.equal(await duplicate.adapter.stageSystemInstructions('managed'), false);
  assert.match(duplicate.adapter.getSystemStageError(), /preset Helper not unique/);

  const untitled = setup();
  const untitledOpener = untitled.root.add(new Node('button', { 'aria-label': 'System instructions' }));
  untitledOpener.click = () => {
    const panel = untitled.root.add(new Node('section', { role: 'dialog', 'aria-label': 'System instructions' }));
    panel.add(new Node('h2', {}, 'System instructions'));
    for (let i = 0; i < 4; i++) panel.add(new Node('button', { role: 'button' }, 'Untitled'));
    const title = panel.add(new Node('input', { placeholder: 'Title' })); title.value = 'User title';
    const editor = panel.add(new Textarea('User text')); editor.attrs.placeholder = 'Optional tone and style instructions for the model';
  };
  assert.equal(await untitled.adapter.stageSystemInstructions('managed'), false);
  assert.match(untitled.adapter.getSystemStageError(), /preset selector ambiguous/);
  assert.equal(untitled.root.querySelectorAll('button').filter(button => button.attrs.role === 'button' && button.clicked).length, 0);
});

test('installs missing WebCodex instructions in a saved personal preset and leaves matching instructions untouched',async()=>{
  const state=setup({draft:'keep my draft'});
  const panel=state.root.add(new Node('section',{role:'dialog','aria-label':'System instructions'}));
  panel.add(new Node('h2',{},'System instructions'));
  const select=panel.add(new Node('select'));
  const custom=select.add(new Node('option',{value:'personal'},'Personal rules'));custom.value='personal';select.value='personal';
  const title=panel.add(new Node('input',{placeholder:'Title'}));title.value='Personal rules';
  const editor=panel.add(new Textarea('Keep personal instructions.'));editor.attrs.placeholder='Optional tone and style instructions for the model';
  assert.equal(await state.adapter.stageSystemInstructions('Helper tools'),true);
  assert.equal(editor.value,'Keep personal instructions.\n<!-- HELPER BEGIN -->\nHelper tools\n<!-- HELPER END -->');
  assert.equal(title.value,'Personal rules');assert.equal(select.value,'personal');assert.equal(state.editor.value,'keep my draft');
  const eventCount=editor.events.length;
  assert.equal(await state.adapter.stageSystemInstructions('Helper tools'),true);
  assert.equal(editor.events.length,eventCount,'an installed matching block is not rewritten');
});

test('selects an existing WebCodex preset when a saved chat has another named combobox preset selected',async()=>{
  const state=setup({draft:'preserve this draft'});
  const panel=state.root.add(new Node('section',{role:'dialog','aria-label':'System instructions'}));
  panel.add(new Node('h2',{},'System instructions'));
  const selector=panel.add(new Node('button',{role:'combobox'},'Personal rules'));
  const previous=panel.add(new Textarea('Personal preset contents'));previous.attrs.placeholder='Optional tone and style instructions for the model';
  let selectedEditor;
  selector.click=()=>{
    const named=state.root.add(new Node('div',{role:'option'},'Helper'));
    named.click=()=>{named.remove();previous.remove();selector._text='Helper';selectedEditor=panel.add(new Textarea(''));selectedEditor.attrs.placeholder='Optional tone and style instructions for the model';};
  };
  assert.equal(await state.adapter.stageSystemInstructions('Helper tools'),true);
  assert.equal(selector._text,'Helper');
  assert.equal(previous.value,'Personal preset contents');
  assert.match(selectedEditor.value,/Helper tools/);
  assert.equal(state.editor.value,'preserve this draft');
});

test('creates the named preset from the explicit Create new instruction control without choosing Untitled entries', async () => {
  const state = setup();
  state.document.defaultView.KeyboardEvent = class { constructor(type, init) { this.type = type; Object.assign(this, init); } };
  const opener = state.root.add(new Node('button', { 'aria-label': 'System instructions' }));
  let title, editor, create, closed = false;
  opener.click = () => {
    const panel = state.root.add(new Node('section', { role: 'dialog', 'aria-label': 'System instructions' }));
    panel.add(new Node('h2', {}, 'System instructions'));
    create = panel.add(new Node('button', { role: 'combobox' }, '+ Create new instruction'));
    create.dispatchEvent = event => { create.events.push(event); if (event.key === 'Escape') closed = true; };
    title = panel.add(new Node('input', { placeholder: 'Title' })); title.value = 'Untitled';
    editor = panel.add(new Textarea('')); editor.attrs.placeholder = 'Optional tone and style instructions for the model';
    for (let i = 0; i < 4; i++) state.root.add(new Node('div', { role: 'menuitem' }, 'Untitled'));
  };
  assert.equal(await state.adapter.stageSystemInstructions('managed instructions'), true);
  assert.equal(closed, true, 'dismisses the menu after finding no named entry');
  assert.equal(title.value, 'Helper');
  assert.equal(editor.value, '<!-- HELPER BEGIN -->\nmanaged instructions\n<!-- HELPER END -->');
  assert.equal(state.root.querySelectorAll('[role="menuitem"]').filter(item => item.clicked).length, 0);
});

test('uses the nested native editor, explicit turn classes, and exact delivery markers', () => {
  const { adapter, editor } = setup();
  const state = adapter.inspect();
  assert.equal(state.supported, true);
  assert.equal(state.editorEmpty, true);
  assert.equal(state.latestModel.text, 'answer');
  assert.equal(adapter.hasDelivery('op-1'), true);
  assert.equal(adapter.hasDelivery('answer'), false);
  assert.equal(adapter.hasDelivery('op'), false);
  assert.equal(adapter.stage('hello'), true);
  assert.equal(editor.value, 'hello');
  assert.equal(editor.events[0].type, 'input');
});
test('preserves an existing user draft and rejects ambiguous editors or active generation', () => {
  const existing = setup({ draft: 'keep this' });
  assert.equal(existing.adapter.stage('replacement'), false);
  assert.equal(existing.editor.value, 'keep this');
  const ambiguous = setup({ extraEditors: 1 }).adapter.inspect();
  assert.equal(ambiguous.supported, false);
  const active = setup({ generating: true });
  assert.equal(active.adapter.inspect().generating, true);
  assert.equal(active.adapter.stage('later'), false);
});
test('stages the initial blank-chat draft while Run is unavailable without submitting it', () => {
  const blank = setup({ empty: true, withoutRun: true });
  blank.location.pathname = '/prompts/new_chat';
  const state = blank.adapter.inspect();
  assert.equal(state.generating, true);
  assert.equal(state.controlsPending, true);
  assert.equal(state.latestModel, null);
  assert.equal(blank.adapter.stage('initial turn'), true);
  assert.equal(blank.editor.value, 'initial turn');
  assert.equal(blank.editor.events[0].type, 'input');
  assert.equal(blank.run.clicked, undefined, 'staging never clicks Run');
});
test('does not stage when Run is unavailable during active generation or an existing model chat', () => {
  const active = setup({ empty: true, withoutRun: true, generating: true, spinnerClass: true });
  assert.equal(active.adapter.stage('blocked during generation'), false);
  const existing = setup({ withoutRun: true });
  assert.ok(existing.adapter.inspect().latestModel);
  assert.equal(existing.adapter.stage('blocked in existing chat'), false);
});
test('rejects unsupported roles and accepts Russian submit label', () => {
  assert.equal(setup({ extraRole: 'Thoughts' }).adapter.inspect().supported, false);
  assert.equal(setup({ empty: true, russian: true }).adapter.inspect().supported, true);
});
test('recognizes duplicate Run aria and text labels with keyboard shortcut suffix', () => {
  assert.equal(setup({ runText: 'Run Ctrl+Enter' }).adapter.inspect().supported, true);
});

test('recognizes the observed Run icon text without aria and ignores rerun controls', async () => {
  const state = setup({ runText: 'Run\nkeyboard_return' });
  delete state.run.attrs['aria-label'];
  state.root.add(new Node('button', { 'aria-label': 'Rerun this turn' }, ''));
  assert.equal(state.adapter.inspect().supported, true);
  assert.equal(await state.adapter.submit('test prompt'), true);
  assert.equal(state.run.clicked, true);
  const duplicate = setup({ runText: 'Run\nkeyboard_return' });
  duplicate.root.add(new Node('button', {}, 'Run\nkeyboard_return'));
  assert.equal(duplicate.adapter.inspect().supported, false);
});

test('ignores decorative Run icons and waits through missing generation controls', async () => {
  const state = setup({ runText: 'Run' });
  delete state.run.attrs['aria-label'];
  state.run.add(new Node('span', { 'aria-hidden': 'true', class: 'material-symbols-outlined' }, 'keyboard_return'));
  assert.equal(state.adapter.inspect().supported, true);
  assert.equal(state.adapter.inspect().generating, false);
  state.run.hidden = true;
  assert.equal(state.adapter.inspect().supported, true);
  assert.equal(state.adapter.inspect().generating, true);
  assert.equal(await state.adapter.submit('must not send during transition'), false);
  state.run.hidden = false;
  assert.equal(state.adapter.inspect().generating, false);
  assert.equal(await state.adapter.submit('ready'), true);
});
test('CSS stoppable spinner and Stop-only generation remain observable but cannot stage', () => {
  const active = setup({ generating: true, spinnerClass: true, withoutRun: true });
  assert.equal(active.adapter.inspect().supported, true);
  assert.equal(active.adapter.inspect().generating, true);
  assert.equal(active.adapter.stage('blocked while generating'), false);
});
test('normalizes a sole complete legacy envelope with or without prose', () => {
  const valid = setup();
  valid.chunk.children = []; valid.chunk._text = '';
  const codeBlock = new Node('ms-code-block'), pre = codeBlock.add(new Node('pre'));
  pre.add(new Node('code', { class: 'language-json' }, '{"v":1,"turn":"legacy-turn","name":"read","arguments":{}}'));
  valid.chunk.add(codeBlock);
  assert.equal(valid.adapter.inspect().latestModel.text, '```helper-call\n{"v":1,"turn":"legacy-turn","name":"read","arguments":{}}\n```');

  const prose = setup(); prose.chunk.children = []; prose.chunk._text = 'Please run this: ';
  const proseBlock = new Node('ms-code-block'), prosePre = proseBlock.add(new Node('pre'));
  prosePre.add(new Node('code', { class: 'language-json' }, '{"v":1,"turn":"legacy-turn","name":"read","arguments":{}}'));
  prose.chunk.add(proseBlock);
  assert.match(prose.adapter.inspect().latestModel.text, /^```helper-call/);
  assert.equal(prose.chunk._text,'Please run this: ','the visible explanation is preserved in the original DOM');
});
test('reconstructs the current three-field call envelope', () => {
  const state = setup(); state.chunk.children = []; state.chunk._text = '';
  state.chunk.add(new Node('ms-code-block')).add(new Node('pre')).add(new Node('code', {}, '{"v":1,"name":"read","arguments":{}}'));
  assert.equal(state.adapter.inspect().latestModel.text, '```helper-call\n{"v":1,"name":"read","arguments":{}}\n```');
});
test('reconstructs a current call alongside prose, including across chunks', () => {
  const state = setup(); state.chunk.children = []; state.chunk._text = 'I will read that now.';
  state.chunk.add(new Node('ms-code-block', { 'data-test-language': 'helper-call' })).add(new Node('pre')).add(new Node('code', {}, '{"v":1,"name":"read","arguments":{}}'));
  assert.equal(state.adapter.inspect().latestModel.text, '```helper-call\n{"v":1,"name":"read","arguments":{}}\n```');

  const multi = setup(); multi.chunk._text = 'I will read that now.';
  multi.model.children[0].add(new Node('ms-text-chunk', {}, 'One more note.'))
    .add(new Node('ms-code-block', { 'data-test-language': 'helper-call' })).add(new Node('pre')).add(new Node('code', {}, '{"v":1,"name":"read","arguments":{}}'));
  assert.equal(multi.adapter.inspect().latestModel.text, '```helper-call\n{"v":1,"name":"read","arguments":{}}\n```');
});
test('does not promote ordinary JSON without the explicit v1 tool envelope in prose', () => {
  const state = setup(); state.chunk.children = []; state.chunk._text = 'Example payload:';
  state.chunk.add(new Node('ms-code-block')).add(new Node('pre')).add(new Node('code', {}, '{"name":"read","arguments":{}}'));
  assert.doesNotMatch(state.adapter.inspect().latestModel.text, /^```helper-call/);
});

test('reconstructs the screenshot read_file call from the visible code language header after prose', () => {
  const state = setup(); state.chunk.children = []; state.chunk._text = 'I will verify if the file exists at the specified path.';
  const thought = state.model.children[0].add(new Node('ms-thought-accordion', {}, 'Expand to view model thoughts'));
  thought.add(new Node('ms-code-block')).add(new Node('pre')).add(new Node('code', {}, '{"v":1,"name":"hidden","arguments":{}}'));
  const block = state.chunk.add(new Node('ms-code-block'));
  const header = block.add(new Node('div', { class: 'code-block-header' }));
  header.add(new Node('mat-icon', {}, 'code'));
  header.add(new Node('span', {}, 'Webcodex-call'));
  for (const label of ['download', 'content_copy', 'expand_less']) header.add(new Node('button', {}, label));
  const call = { v: 1, name: 'read_file', arguments: { limit: 5, offset: 1, path: 'W:\\projects\\example-game\\index.html' } };
  block.add(new Node('pre')).add(new Node('code', {}, JSON.stringify(call)));
  const extracted = state.adapter.inspect().latestModel.text;
  const { parseCall } = require('./protocol.js');
  const tools = [{ name: 'read_file', inputSchema: { type: 'object', properties: { path: { type: 'string' }, limit: { type: 'integer' }, offset: { type: 'integer' } }, required: ['path'], additionalProperties: false } }];
  assert.deepEqual(parseCall(extracted, null, tools), { name: call.name, arguments: call.arguments });
  assert.doesNotMatch(extracted, /Expand to view|hidden|content_copy|download/);
});

test('ordinary JSON does not become a tool call because a language label or button mentions the marker', () => {
  for (const label of ['json', 'helper-call example', 'not-helper-call']) {
    const state = setup(); state.chunk.children = []; state.chunk._text = 'Example payload:';
    const block = state.chunk.add(new Node('ms-code-block'));
    block.add(new Node('span', {}, label));
    block.add(new Node('button', {}, 'helper-call'));
    block.add(new Node('pre')).add(new Node('code', {}, '{"example":"helper-call","path":"notes.txt"}'));
    assert.doesNotMatch(state.adapter.inspect().latestModel.text, /```helper-call/);
  }
});

test('recognizes the screenshot list_allowed_roots envelope across code renderers and unrelated language labels',()=>{
  const {parseCall}=require('./protocol.js');
  const payload='{"v":1,"name":"list_allowed_roots","arguments":{}}';
  const tools=[{name:'list_allowed_roots',inputSchema:{type:'object',properties:{},additionalProperties:false}}];
  for(const renderer of ['pre','code','highlighted']){
    const state=setup();state.chunk.children=[];state.chunk._text='Сначала проверю доступные пути и рабочее окружение.';
    const block=state.chunk.add(new Node('ms-code-block'));
    block.add(new Node('mat-icon',{},'code'));block.add(new Node('span',{},'v'));
    block.add(new Node('button',{},'content_copy'));
    const body=block.add(new Node(renderer==='highlighted'?'div':renderer,{},payload));
    Object.defineProperty(body,'innerText',{get:()=>payload.replace('list_allowed_roots','list\n_allowed_roots')});
    const extracted=state.adapter.inspect().latestModel.text;
    assert.deepEqual(parseCall(extracted,null,tools),{name:'list_allowed_roots',arguments:{}},renderer);
  }
});

test('rejects non-v1 envelopes, extra fields and multiple JSON calls instead of normalizing them',()=>{
  for(const payload of ['{"v":2,"name":"read","arguments":{}}','{"v":1,"name":"read","arguments":{},"extra":true}','{"v":1,"name":"read","arguments":[]}','{"v":1,"name":"read","arguments":{}} {"v":1,"name":"read","arguments":{}}']){
    const state=setup();state.chunk.children=[];state.chunk._text='Explanation.';
    state.chunk.add(new Node('ms-code-block')).add(new Node('pre',{},payload));
    assert.doesNotMatch(state.adapter.inspect().latestModel.text,/```helper-call/);
  }
});

test('language-independent v1 recognition still rejects unselected tools and injected arguments',()=>{
  const {parseCall}=require('./protocol.js');
  const tools=[{name:'list_allowed_roots',inputSchema:{type:'object',properties:{},additionalProperties:false}}];
  for(const call of [{v:1,name:'unselected_tool',arguments:{}},{v:1,name:'list_allowed_roots',arguments:{request_key:'not-model-controlled'}}]){
    const state=setup();state.chunk.children=[];state.chunk._text='Проверю окружение.';
    state.chunk.add(new Node('ms-code-block')).add(new Node('pre',{},JSON.stringify(call)));
    assert.throws(()=>parseCall(state.adapter.inspect().latestModel.text,null,tools));
  }
});

test('finds a single call outside paragraph chunks or inside nested chunks without double counting',()=>{
  const {parseCall}=require('./protocol.js');
  const tools=[{name:'list_allowed_roots',inputSchema:{type:'object',properties:{},additionalProperties:false}}];
  for(const layout of ['sibling','nested']){
    const state=setup();state.chunk.children=[];state.chunk._text='Сначала проверю окружение.';
    const container=layout==='sibling'?state.model.children[0]:state.chunk.add(new Node('ms-text-chunk'));
    const block=container.add(new Node('ms-code-block'));
    block.add(new Node('span',{},'v'));block.add(new Node('pre',{},'{"v":1,"name":"list_allowed_roots","arguments":{}}'));
    assert.deepEqual(parseCall(state.adapter.inspect().latestModel.text,null,tools),{name:'list_allowed_roots',arguments:{}},layout);
  }
});
test('rejects duplicate call envelopes and accepts a single valid legacy call alongside prose', () => {
  const duplicate = setup(); duplicate.chunk.children = []; duplicate.chunk._text = '';
  for (let i = 0; i < 2; i++) duplicate.chunk.add(new Node('ms-code-block')).add(new Node('pre')).add(new Node('code', {}, '{"v":1,"name":"read","arguments":{}}'));
  assert.doesNotMatch(duplicate.adapter.inspect().latestModel.text, /^```helper-call/);

  const legacy = setup(); legacy.chunk.children = []; legacy.chunk._text = 'Please run this: ';
  legacy.chunk.add(new Node('ms-code-block')).add(new Node('pre')).add(new Node('code', {}, '{"v":1,"turn":"legacy-turn","name":"read","arguments":{}}'));
  assert.match(legacy.adapter.inspect().latestModel.text, /^```helper-call/);
});
test('inspect exposes the latest user message without turn headers or other role text', () => {
  const state = setup();
  state.user._text = '';
  state.user.children = [];
  state.user.add(new Node('div', {}, 'User\nNov 11, 2026, 10:30 AM'));
  const content = state.user.add(new Node('div', { 'data-turn-role': 'User' }));
  content.add(new Node('ms-prompt-chunk', {}, 'distinct tool result'));
  const oldUser = new Node('ms-chat-turn');
  oldUser.add(new Node('div', { 'data-turn-role': 'User' }, 'old result'));
  state.root.children.splice(state.root.children.indexOf(state.user), 0, oldUser);
  const latest = state.adapter.inspect().latestUser;
  assert.equal(latest.element, state.user);
  assert.equal(latest.text, 'distinct tool result');
  assert.equal(state.adapter.inspect().modelAfterUser, true);
  state.root.children.splice(state.root.children.indexOf(state.user), 1);
  state.root.add(state.user);
  assert.equal(state.adapter.inspect().modelAfterUser, false);
  state.root.children.splice(state.root.children.indexOf(state.model), 1);
  state.root.add(state.model);
  assert.equal(state.adapter.inspect().modelAfterUser, true);
});
test('reads nested data roles and extracts final text chunks without role headers', () => {
  const state = setup({ nestedRoles: false });
  const header = new Node('div', {}, 'Model');
  state.model.children.unshift(header); header.parent = state.model;
  assert.equal(state.adapter.inspect().supported, true);
  assert.equal(state.adapter.inspect().latestModel.text, 'answer');

  const conflict = setup();
  conflict.model.add(new Node('div', { 'data-turn-role': 'User' }, 'contradiction'));
  assert.equal(conflict.adapter.inspect().supported, false);
});
test('normalizes a later call without modifying the original prose and rendered layout', () => {
  const state = setup();
  state.model.style.setProperty('display', 'flex', 'important');
  state.chunk._text = 'Before call';
  const next = state.model.children[0].add(new Node('ms-text-chunk', {}, ''));
  const block = next.add(new Node('ms-code-block'));
  block.add(new Node('button', {}, 'Copy'));
  block.add(new Node('pre')).add(new Node('code', {}, '{"v":1,"turn":"legacy-turn","name":"read","arguments":{}}'));
  assert.equal(state.adapter.inspect().latestModel.text, '```helper-call\n{"v":1,"turn":"legacy-turn","name":"read","arguments":{}}\n```');
  assert.equal(state.chunk._text,'Before call');
});
test('accepts a sole exact call JSON block without a language class and ignores its copy button', () => {
  const state = setup(); state.chunk.children = []; state.chunk._text = '';
  const block = state.chunk.add(new Node('ms-code-block'));
  block.add(new Node('button', {}, 'Copy'));
  block.add(new Node('pre')).add(new Node('code', {}, '{"v":1,"turn":"legacy-turn","name":"read","arguments":{}}'));
  assert.equal(state.adapter.inspect().latestModel.text, '```helper-call\n{"v":1,"turn":"legacy-turn","name":"read","arguments":{}}\n```');
});
test('removes thought content from detached clones and accepts only the exact AI Studio host', () => {
  const { adapter, chunk } = setup();
  const thought = chunk.add(new Node('ms-thought-chunk', {}, '```helper-call\n{"v":1}\n``` hidden thought'));
  chunk._text = 'answer';
  assert.doesNotMatch(adapter.inspect().latestModel.text, /hidden thought|helper-call/);
  thought.isConnected = false;
  assert.equal(setup({ hostname: 'preview.aistudio.google.com' }).adapter.inspect().supported, false);
  assert.equal(setup({ hostname: 'aistudio.google.com' }).adapter.inspect().supported, true);
});
test('submits only after staging and does not claim delivery until the marker appears', async () => {
  const { adapter, run } = setup();
  assert.equal(await adapter.submit('sent'), true);
  assert.equal(run.clicked, true);
  assert.equal(adapter.hasDelivery('sent'), false);
});

test('confirms only an exact result envelope in the submitted user turn', () => {
  const payload = '{"type":"helper-result","operation_id":"op-1","result":42}';
  const turnContent = setup();
  turnContent.user.attrs['data-turn-role'] = 'User';
  turnContent.user.children = [];
  turnContent.user._text = 'User\nNov 11, 2026, 10:30 AM';
  turnContent.user.add(new Node('div', { class: 'turn-content' }, payload));
  assert.equal(turnContent.adapter.hasDelivery('op-1'), true);

  const cmark = setup();
  cmark.user.attrs['data-turn-role'] = 'User';
  cmark.user.children = [];
  cmark.user._text = 'User\nNov 11, 2026, 10:30 AM';
  cmark.user.add(new Node('ms-cmark-node', {}, payload));
  assert.equal(cmark.adapter.hasDelivery('op-1'), true);

  const header = setup();
  header.user.attrs['data-turn-role'] = 'User';
  header.user.children = [];
  header.user._text = `User\nNov 11, 2026, 10:30 AM\n${payload}`;
  assert.equal(header.adapter.hasDelivery('op-1'), true);
  assert.equal(header.adapter.hasDelivery('other'), false);

  for (const invalid of [
    '{"type":"other","operation_id":"op-1"}',
    'partial {"type":"helper-result","operation_id":"op-1"',
    'Here is the result: {"type":"helper-result","operation_id":"op-1"}',
  ]) {
    const state = setup();
    state.user.attrs['data-turn-role'] = 'User';
    state.user.children = [];
    state.user._text = invalid;
    assert.equal(state.adapter.hasDelivery('op-1'), false);
  }

  const modelOnly = setup();
  modelOnly.user.attrs['data-turn-role'] = 'User';
  modelOnly.user.children = [];
  modelOnly.user._text = 'User\nNov 11, 2026, 10:30 AM';
  modelOnly.model.children[0].add(new Node('ms-text-chunk', {}, payload));
  assert.equal(modelOnly.adapter.hasDelivery('op-1'), false);
  assert.equal(setup().adapter.hasDelivery('op-1'), true, 'legacy exact marker remains supported');
});

test('proves legacy Markdown-rendered delivery against the exact expected envelope', () => {
  const payload = JSON.stringify({
    type: 'helper-result', v: 1, operation_id: 'op-legacy', name: 'browser',
    result: { content: [{ type: 'text', text: JSON.stringify({ path: 'C:\\Users\\alice\\file.txt', quote: 'say "hello"' }) }] },
  });
  const rendered = payload.replace(/\\([!"#$%&'()*+,\-./:;<=>?@\[\\\]^_`{|}~])/g, '$1');
  assert.throws(() => JSON.parse(rendered));

  const state = setup();
  state.user.attrs['data-turn-role'] = 'User';
  state.user.children = [];
  state.user.add(new Node('div', { class: 'turn-content' }, `User 12:35\u202fAM\n\n${rendered}`));
  assert.equal(state.adapter.hasDelivery('op-legacy', payload), true);
  assert.equal(state.adapter.hasDelivery('op-other', payload), false);

  const changed = setup();
  changed.user.attrs['data-turn-role'] = 'User';
  changed.user.children = [];
  changed.user.add(new Node('ms-text-chunk', {}, rendered.replace('alice', 'mallory')));
  assert.equal(changed.adapter.hasDelivery('op-legacy', payload), false);
  assert.equal(changed.adapter.hasDelivery('op-legacy'), false, 'malformed Markdown output is not accepted without the expected envelope');
});

test('exposes the latest user result operation id from wrapped text and nested result nodes only', () => {
  const payload = '{"type":"helper-result","operation_id":"op-latest"}';
  const header = setup();
  header.user.attrs['data-turn-role'] = 'User';
  header.user.children = [];
  header.user.textContent = `User\nNov 11, 2026, 10:30 AM\n${payload}`;
  assert.equal(header.adapter.inspect().latestUser.operationId, 'op-latest');
  assert.equal(header.adapter.hasDelivery(header.adapter.inspect().latestUser.operationId), true);

  const nested = setup();
  nested.user.children = [];
  const nestedRole = nested.user.add(new Node('div', { class: 'chat-turn-container user' }));
  const content = nestedRole.add(new Node('div', { class: 'turn-content' }));
  content.add(new Node('ms-cmark-node', {}, payload));
  assert.equal(nested.adapter.inspect().latestUser.operationId, 'op-latest');
  assert.equal(nested.adapter.hasDelivery(nested.adapter.inspect().latestUser.operationId), true);

  for (const invalid of [
    '{"type":"other","operation_id":"op-latest"}',
    '{"type":"helper-result"}',
    '[{"type":"helper-result","operation_id":"op-latest"}]',
  ]) {
    const state = setup();
    state.user.attrs['data-turn-role'] = 'User';
    state.user.children = [];
    state.user.textContent = invalid;
    assert.equal(state.adapter.inspect().latestUser.operationId, '');
    assert.equal(state.adapter.hasDelivery('op-latest'), false);
  }
});

test('cleanup hides only requested current turns and exact delivery turns, then restores them', () => {
  const state = setup();
  state.model.style.setProperty('display', 'flex', 'important');
  const unrelated = state.root.add(new Node('ms-chat-turn', {}, 'old unrelated history'));
  unrelated.add(new Node('div', { class: 'chat-turn-container user' }, '[WebCodex operation old-op]'));
  const stale = new Node('ms-chat-turn');
  assert.deepEqual(state.adapter.deliveryTurns('op-1'), [state.user]);
  state.adapter.cleanup({ turns: [state.model, stale], operationIds: ['op-1'] });
  assert.equal(state.model.hidden, true);
  assert.equal(state.model.style.getPropertyValue('display'), 'none');
  assert.equal(state.model.style.getPropertyPriority('display'), 'important');
  assert.equal(state.user.hidden, true);
  assert.equal(state.model.getAttribute('data-webcodex-hidden'), 'true');
  assert.equal(unrelated.hidden, false);
  assert.equal(state.adapter.hasDelivery('op-1'), true);
  assert.equal(state.adapter.inspect().latestModel.element, state.model);
  state.adapter.restoreCleanup();
  assert.equal(state.model.hidden, false);
  assert.equal(state.model.style.getPropertyValue('display'), 'flex');
  assert.equal(state.model.style.getPropertyPriority('display'), 'important');
  assert.equal(state.user.hidden, false);
  assert.equal(state.model.getAttribute('data-webcodex-hidden'), null);
  assert.equal(state.adapter.hasDelivery('op-1'), true);

  state.model.hidden = true;
  state.adapter.cleanup({ turns: [state.model], operationIds: [] });
  state.adapter.restoreCleanup();
  assert.equal(state.model.hidden, true, 'pre-existing hidden state is preserved');
});

test('context annotation counts hidden turns and stays out of model text and repeated counts', () => {
  const state = setup();
  state.adapter.cleanup({ turns: [], operationIds: ['op-1'] });
  const expected = Math.ceil((state.user.textContent.length + state.model.textContent.length) / 4);
  assert.equal(state.adapter.annotateContext(), expected);
  const notes = state.model.querySelectorAll('[data-webcodex-context]');
  assert.equal(notes.length, 1);
  assert.equal(notes[0].textContent, `Текст сообщений: ≈ ${expected} токенов (оценка; содержимое вложений не учтено)`);
  assert.equal(state.adapter.inspect().latestModel.text, 'answer');
  assert.equal(state.adapter.annotateContext(), expected);
  assert.equal(state.model.querySelectorAll('[data-webcodex-context]').length, 1);
});

test('recognizes an empty editor with disabled Run and only clicks after input enables it', async () => {
  const state = setup({ empty: true });
  state.run.disabled = true;
  assert.equal(state.adapter.inspect().supported, true);
  state.editor.dispatchEvent = () => { state.run.disabled = false; };
  assert.equal(await state.adapter.submit('instructions'), true);
  assert.equal(state.run.clicked, true);
  for (const aria of [false, true]) {
    const blocked = setup({ empty: true });
    if (aria) blocked.run.attrs['aria-disabled'] = 'true'; else blocked.run.disabled = true;
    assert.equal(await blocked.adapter.submit('draft'), false);
    assert.equal(blocked.editor.value, 'draft');
    assert.equal(blocked.run.clicked, undefined);
  }
});

test('waits for delayed Run activation, retries an owned unchanged draft, and prevents duplicate clicks', async () => {
  const delayed = setup({ empty: true });
  delayed.run.disabled = true;
  const pending = delayed.adapter.submit('delayed');
  await new Promise(resolve => setTimeout(resolve, 80));
  delayed.run.disabled = false;
  assert.equal(await pending, true);
  assert.equal(delayed.run.clicked, true);

  const retry = setup({ empty: true });
  retry.run.disabled = true;
  retry.document.defaultView.setTimeout = callback => queueMicrotask(callback);
  assert.equal(await retry.adapter.submit('owned'), false);
  retry.run.disabled = false;
  assert.equal(await retry.adapter.submit('owned'), true);
  assert.equal(retry.run.clicked, true);
  assert.equal(await retry.adapter.submit('owned'), false);
});

test('aborts if the staged draft changes or the caller disarms while waiting', async () => {
  const edited = setup({ empty: true });
  edited.run.disabled = true;
  const editedSubmit = edited.adapter.submit('draft');
  await new Promise(resolve => setTimeout(resolve, 10));
  edited.editor.value = 'user edit';
  assert.equal(await editedSubmit, false);

  const disarmed = setup({ empty: true });
  disarmed.run.disabled = true;
  let armed = true;
  const movedSubmit = disarmed.adapter.submit('draft', () => armed);
  await new Promise(resolve => setTimeout(resolve, 10));
  armed = false;
  assert.equal(await movedSubmit, false);
  assert.equal(disarmed.run.clicked, undefined);
});

test('waits through transient Run removal and rejects competing or stale submits', async () => {
  const state = setup({ empty: true });
  assert.equal(state.adapter.stage('one draft'), true);
  state.run.hidden = true;
  const pending = state.adapter.submit('one draft');
  assert.equal(await state.adapter.submit('another draft'), false);
  await new Promise(resolve => setTimeout(resolve, 80));
  state.run.hidden = false;
  assert.equal(await pending, true);

  const stale = setup({ empty: true });
  assert.equal(await stale.adapter.submit('never stage', () => false), false);
  assert.equal(stale.editor.value, '');
});
