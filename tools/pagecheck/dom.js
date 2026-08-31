// A DOM small enough to run the page's own JS, big enough that the router,
// the SVG builders and the views all work. Nothing is stubbed that a check
// depends on: elements really nest, classes really attach, and a click
// really sets location.hash and re-runs the router.
class El {
  constructor(tag, ns) {
    this.tagName = tag; this.ns = ns || null;
    this.children = []; this.attrs = {}; this.style = {};
    this.classList = new Set(); this._text = ""; this.parentNode = null;
    this.listeners = {}; this.hidden = false; this.dataset = {};
    this.style.setProperty = (k, v) => { this.style[k] = v; };
  }
  get className() { return [...this.classList].join(" "); }
  set className(v) { this.classList = new Set(String(v).split(/\s+/).filter(Boolean)); }
  appendChild(c) {
    if (c.tagName === "#fragment") { for (const k of c.children) this.appendChild(k); c.children = []; return c; }
    c.parentNode = this; this.children.push(c); return c;
  }
  removeChild(c) { this.children = this.children.filter(x => x !== c); return c; }
  insertBefore(c, ref) {
    c.parentNode = this;
    const i = ref ? this.children.indexOf(ref) : -1;
    if (i < 0) this.children.push(c); else this.children.splice(i, 0, c);
    return c;
  }
  get firstChild() { return this.children[0] || null; }
  get lastChild() { return this.children[this.children.length - 1] || null; }
  remove() { if (this.parentNode) this.parentNode.removeChild(this); }
  setAttribute(k, v) { this.attrs[k] = String(v); if (k === "class") this.className = v; }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(t, fn) { (this.listeners[t] = this.listeners[t] || []).push(fn); }
  removeEventListener(t, fn) {
    if (this.listeners[t]) this.listeners[t] = this.listeners[t].filter(f => f !== fn);
  }
  dispatchEvent(ev) {
    for (const fn of this.listeners[ev.type] || []) fn(ev);
    if (this.parentNode && ev.bubbles) this.parentNode.dispatchEvent(ev);
    return true;
  }
  click() { this.dispatchEvent({ type: "click", bubbles: true, preventDefault() {} }); }
  get textContent() {
    if (this.children.length) return this.children.map(c => c.textContent).join("");
    return this._text;
  }
  set textContent(v) { this.children = []; this._text = String(v); }
  get href() { return this.attrs.href || ""; }
  set href(v) { this.attrs.href = String(v); }
  get value() { return this.attrs.value || ""; }
  set value(v) { this.attrs.value = String(v); }
  get offsetTop() { return 0; }
  getBoundingClientRect() { return { top: 0, bottom: 10, left: 0, right: 10, width: 10, height: 10 }; }
  // Selectors used by the page are simple: "#id", ".cls", "tag", "tag.cls",
  // and descendant chains of those.
  querySelectorAll(sel) {
    const out = [];
    const parts = sel.split(",").map(s => s.trim());
    const walk = el => {
      for (const c of el.children) {
        if (parts.some(p => matches(c, p))) out.push(c);
        walk(c);
      }
    };
    walk(this);
    return out;
  }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
}
function simple(el, part) {
  const m = part.match(/^([a-zA-Z]*)((?:\.[-\w]+)*)$/);
  if (!m) return false;
  if (m[1] && el.tagName !== m[1]) return false;
  for (const c of m[2].split(".").filter(Boolean)) if (!el.classList.has(c)) return false;
  return true;
}
// Descendant combinators are honoured: "p.sortbar a" must not match every
// anchor on the page, which is exactly the false green a last-part-only
// matcher produces.
function matches(el, sel) {
  const parts = sel.trim().split(/\s+/);
  if (!simple(el, parts[parts.length - 1])) return false;
  let node = el.parentNode;
  for (let i = parts.length - 2; i >= 0; i--) {
    while (node && !simple(node, parts[i])) node = node.parentNode;
    if (!node) return false;
    node = node.parentNode;
  }
  return true;
}
module.exports = { El, matches };
