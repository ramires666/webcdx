const { test } = require('node:test');
const assert = require('node:assert/strict');
const createAdapter = require('./studio-adapter.js');

class Node {
  constructor(tag, attrs = {}, content = '') { this.tag = tag; this.attrs = attrs; this._text = content; this.children = []; this.disabled = false; this.readOnly = false; this.isConnected = true; this.hidden = false; this.events = []; }
  get textContent() { return this._text + this.children.map(child => child.textContent).join(''); }
  set textContent(value) { this._text = String(value); this.children = []; }
  get innerText() { return this.textContent; }
  set innerText(value) { this._text = String(value); }
  getAttribute(name) { return this.attrs[name] ?? null; }
  getBoundingClientRect() { return { width: 10 }; }
  matches(part) {
    const tag = part.match(/^[\w*-]+/)?.[0] || '';
    const classes = [...part.matchAll(/\.([\w-]+)/g)].map(m => m[1]);
    const attrs = [...part.matchAll(/\[([\w-]+)(?:=["']([^"']*)["'])?\]/g)];
    return (!tag || tag === this.tag || tag === '*') && classes.every(c => (this.attrs.class || '').split(/\s+/).includes(c)) &&
      attrs.every(([, key, val]) => val === undefined ? key in this.attrs : this.attrs[key] === val);
  }
  querySelectorAll(selector) {
    const found = [];
    for (const group of selector.split(',').map(x => x.trim())) {
      const parts = group.split(/\s+/);
      const visit = (node, index) => {
        for (const child of node.children) {
          if (child.matches(parts[index])) {
            if (index === parts.length - 1) found.push(child);
            else visit(child, index + 1);
          }
          visit(child, index);
        }
      };
      visit(this, 0);
    }
    return [...new Set(found)];
  }
  closest(selector) { return selector.split(',').map(s => s.trim()).some(s => this.matches(s)) ? this : null; }
  cloneNode(deep) { const copy = new Node(this.tag, { ...this.attrs }, this._text); copy.isConnected = false; if (deep) copy.children = this.children.map(x => { const child = x.cloneNode(true); child.parent = copy; return child; }); return copy; }
  remove() { if (this.parent) this.parent.children = this.parent.children.filter(x => x !== this); this.removed = true; }
  dispatchEvent(event) { this.events.push(event); }
  click() { this.clicked = true; }
  add(child) { child.parent = this; this.children.push(child); return child; }
}
class Textarea extends Node {
  constructor(value = '') { super('textarea'); this._value = value; }
  get value() { return this._value; }
  set value(value) { this._value = String(value); }
}
function setup({ draft = '', extraEditors = 0, generating = false, spinnerClass = false, withoutRun = false, nestedRoles = true, extraRole = null, empty = false, russian = false, runText = null, hostname = 'aistudio.google.com' } = {}) {
  const wrapper = new Node('ms-prompt-input-wrapper'), autosize = wrapper.add(new Node('ms-autosize-textarea'));
  const editor = autosize.add(new Textarea(draft));
  const moreEditors = Array.from({ length: extraEditors }, () => { const w = new Node('ms-prompt-input-wrapper'); w.add(new Node('ms-prompt-box').add(new Textarea())); return w; });
  const user = new Node('ms-chat-turn', {}, 'operation result');
  const userRole = new Node('div', nestedRoles ? { class: 'chat-turn-container user' } : { 'data-turn-role': 'User' }, '[WebCodex operation op-1]'); user.add(userRole);
  const model = new Node('ms-chat-turn');
  const modelRole = new Node('div', nestedRoles ? { class: 'chat-turn-container model' } : { 'data-turn-role': 'Model' });
  const chunk = modelRole.add(new Node('ms-text-chunk', {}, 'answer')); model.add(modelRole);
  const turns = empty ? [] : [user, model];
  if (extraRole) turns.push(new Node('ms-chat-turn', { 'data-turn-role': extraRole }, 'unknown'));
  const buttonText = runText ?? (russian ? 'Запустить' : 'Run');
  const run = new Node('button', { 'aria-label': buttonText }, buttonText);
  if (generating) run.add(new Node(spinnerClass ? 'div' : 'ms-stoppable-spinner', spinnerClass ? { class: 'stoppable-spinner' } : {}));
  const stop = new Node('button', { 'aria-label': russian ? 'Отмена' : 'Stop' });
  const root = new Node('document'); root.children = [wrapper, ...moreEditors, ...turns, ...(withoutRun ? [] : [run]), ...(generating ? [stop] : [])];
  const document = { documentElement: new Node('html'), body: new Node('body'), defaultView: { InputEvent: class { constructor(type, init) { this.type = type; Object.assign(this, init); } } }, querySelectorAll(selector) { return root.querySelectorAll(selector); } };
  return { adapter: createAdapter(document, { hostname, pathname: '/prompts/p1', search: '?x=2' }), editor, wrapper, run, user, model, chunk, root };
}

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
test('rejects unsupported roles and accepts Russian submit label', () => {
  assert.equal(setup({ extraRole: 'Thoughts' }).adapter.inspect().supported, false);
  assert.equal(setup({ empty: true, russian: true }).adapter.inspect().supported, true);
});
test('recognizes duplicate Run aria and text labels with keyboard shortcut suffix', () => {
  assert.equal(setup({ runText: 'Run Ctrl+Enter' }).adapter.inspect().supported, true);
});
test('CSS stoppable spinner and Stop-only generation remain observable but cannot stage', () => {
  const active = setup({ generating: true, spinnerClass: true, withoutRun: true });
  assert.equal(active.adapter.inspect().supported, true);
  assert.equal(active.adapter.inspect().generating, true);
  assert.equal(active.adapter.stage('blocked while generating'), false);
});
test('only reconstructs a sole exact JSON call block without prose or language suffix', () => {
  const valid = setup();
  valid.chunk.children = []; valid.chunk._text = '';
  const codeBlock = new Node('ms-code-block'), pre = codeBlock.add(new Node('pre'));
  pre.add(new Node('code', { class: 'language-json' }, '{"v":1,"turn":2,"name":"read","arguments":{}}'));
  valid.chunk.add(codeBlock);
  assert.equal(valid.adapter.inspect().latestModel.text, '```webcodex-call\n{"v":1,"turn":2,"name":"read","arguments":{}}\n```');

  const prose = setup(); prose.chunk.children = []; prose.chunk._text = 'Please run this: ';
  const proseBlock = new Node('ms-code-block'), prosePre = proseBlock.add(new Node('pre'));
  prosePre.add(new Node('code', { class: 'language-json' }, '{"v":1,"turn":2,"name":"read","arguments":{}}'));
  prose.chunk.add(proseBlock);
  assert.doesNotMatch(prose.adapter.inspect().latestModel.text, /^```webcodex-call/);
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
test('keeps prose from all final chunks and does not promote a later code block to a call', () => {
  const state = setup();
  state.chunk._text = 'Before call';
  const next = state.model.children[0].add(new Node('ms-text-chunk', {}, ''));
  const block = next.add(new Node('ms-code-block'));
  block.add(new Node('button', {}, 'Copy'));
  block.add(new Node('pre')).add(new Node('code', {}, '{"v":1,"turn":2,"name":"read","arguments":{}}'));
  assert.equal(state.adapter.inspect().latestModel.text, 'Before call\n{"v":1,"turn":2,"name":"read","arguments":{}}');
});
test('accepts a sole exact call JSON block without a language class and ignores its copy button', () => {
  const state = setup(); state.chunk.children = []; state.chunk._text = '';
  const block = state.chunk.add(new Node('ms-code-block'));
  block.add(new Node('button', {}, 'Copy'));
  block.add(new Node('pre')).add(new Node('code', {}, '{"v":1,"turn":2,"name":"read","arguments":{}}'));
  assert.equal(state.adapter.inspect().latestModel.text, '```webcodex-call\n{"v":1,"turn":2,"name":"read","arguments":{}}\n```');
});
test('removes thought content from detached clones and accepts only the exact AI Studio host', () => {
  const { adapter, chunk } = setup();
  const thought = chunk.add(new Node('ms-thought-chunk', {}, '```webcodex-call\n{"v":1}\n``` hidden thought'));
  chunk._text = 'answer';
  assert.doesNotMatch(adapter.inspect().latestModel.text, /hidden thought|webcodex-call/);
  thought.isConnected = false;
  assert.equal(setup({ hostname: 'preview.aistudio.google.com' }).adapter.inspect().supported, false);
  assert.equal(setup({ hostname: 'aistudio.google.com' }).adapter.inspect().supported, true);
});
test('submits only after staging and does not claim delivery until the marker appears', () => {
  const { adapter, run } = setup();
  assert.equal(adapter.submit('sent'), true);
  assert.equal(run.clicked, true);
  assert.equal(adapter.hasDelivery('sent'), false);
});

test('recognizes an empty editor with disabled Run and only clicks after input enables it', () => {
  const state = setup({ empty: true });
  state.run.disabled = true;
  assert.equal(state.adapter.inspect().supported, true);
  state.editor.dispatchEvent = () => { state.run.disabled = false; };
  assert.equal(state.adapter.submit('instructions'), true);
  assert.equal(state.run.clicked, true);
  for (const aria of [false, true]) {
    const blocked = setup({ empty: true });
    if (aria) blocked.run.attrs['aria-disabled'] = 'true'; else blocked.run.disabled = true;
    assert.equal(blocked.adapter.submit('draft'), false);
    assert.equal(blocked.editor.value, 'draft');
    assert.equal(blocked.run.clicked, undefined);
  }
});
