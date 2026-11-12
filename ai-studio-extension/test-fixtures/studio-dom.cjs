const createAdapter = require('../studio-adapter.js');

class Node {
  constructor(tag, attrs = {}, content = '') { this.tag = tag; this.attrs = attrs; this._text = content; this.children = []; this.disabled = false; this.readOnly = false; this.isConnected = true; this.hidden = false; this.events = []; this.style = { values: {}, priorities: {}, getPropertyValue(name) { return this.values[name] || ''; }, getPropertyPriority(name) { return this.priorities[name] || ''; }, setProperty(name, value, priority = '') { this.values[name] = String(value); this.priorities[name] = priority; }, removeProperty(name) { delete this.values[name]; delete this.priorities[name]; } }; }
  get textContent() { return this._text + this.children.map(child => child.textContent).join(''); }
  set textContent(value) { this._text = String(value); this.children = []; }
  get innerText() { return this.textContent; }
  set innerText(value) { this._text = String(value); }
  getAttribute(name) { return this.attrs[name] ?? null; }
  setAttribute(name, value) { this.attrs[name] = String(value); }
  removeAttribute(name) { delete this.attrs[name]; }
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
  appendChild(child) { return this.add(child); }
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
  const document = { documentElement: new Node('html'), body: new Node('body'), createElement(tag) { return new Node(tag); }, defaultView: { InputEvent: class { constructor(type, init) { this.type = type; Object.assign(this, init); } } }, querySelectorAll(selector) { return root.querySelectorAll(selector); } };
  const location = { hostname, pathname: '/prompts/p1', search: '?x=2' };
  return { adapter: createAdapter(document, location), location, editor, wrapper, run, user, model, chunk, root, document };
}

module.exports = { Node, Textarea, setup };
