// pagecheck runs the REAL page script from a generated watchpath.html against
// its REAL payload, on a mini DOM, and asserts that the views render, the
// links lead where they say, and the aggregates add up.
//
// It exists because the Go tests structurally cannot do this: they build the
// page as a STRING and never execute a line of its JavaScript. On the day it
// was written it caught two things nothing else would have — a syntax error
// that kills the whole page before its first line (an optional chain used as
// an assignment target), and a real data bug where 992 of 7382 channels were
// counted as "new" on a later day than the one they were first seen on.
//
// Run it after watchpath, from the repo root:
//
//     ./youtubehistii watchpath -taxonomy
//     node tools/pagecheck/pagecheck.js
//
// It prints counts only, never a title or a channel name: the page it reads
// is somebody's watch history.
//
// Two things a browser cannot replace and two it can. Here: every route
// rendered head-to-tail, aggregates recomputed independently, no rAF needed.
// In a browser: real layout, real CSS, and the scroll handler — which is
// exactly what a headless run cannot see, and an MCP tab cannot either
// (a hidden tab never fires requestAnimationFrame).
const fs = require("fs");
const path = require("path");
const { El } = require(path.join(__dirname, "dom.js"));

const pagePath = process.argv[2] || "data/out/watchpath.html";
const html = fs.readFileSync(pagePath, "utf8");
const scripts = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map(m => m[1]);
if (scripts.length < 5) throw new Error("expected the page's script blocks, got " + scripts.length);

const ids = { view: new El("div"), crumbs: new El("div"), tip: new El("div"), head: new El("div") };
const doc = {
  createElement: t => {
    const e = new El(t);
    // The page measures text with a canvas; a monospace-ish estimate is
    // enough for the checks here, which never assert on pixel widths.
    if (t === "canvas") e.getContext = () => ({ font: "", measureText: s => ({ width: s.length * 6 }) });
    return e;
  },
  createElementNS: (ns, t) => new El(t, ns),
  createTextNode: t => { const e = new El("#text"); e.textContent = t; return e; },
  // A fragment behaves like a parentless element whose children are spliced
  // in on append — close enough that appendChild(frag) yields the same tree.
  createDocumentFragment: () => new El("#fragment"),
  getElementById: id => ids[id] || null,
  querySelector: sel => (sel === "details.note" ? new El("details") : null),
  addEventListener() {}, removeEventListener() {},
  documentElement: new El("html"), body: new El("body"),
  visibilityState: "visible",
};
doc.body.appendChild(ids.view);

let hash = "";
const listeners = {};
const win = {
  document: doc,
  location: { get hash() { return hash; }, set hash(v) { hash = v.startsWith("#") ? v : "#" + v; fire("hashchange"); } },
  addEventListener: (t, fn) => { (listeners[t] = listeners[t] || []).push(fn); },
  removeEventListener: (t, fn) => { if (listeners[t]) listeners[t] = listeners[t].filter(f => f !== fn); },
  requestAnimationFrame: fn => { fn(0); return 1; },
  cancelAnimationFrame() {},
  matchMedia: () => ({ matches: false, addEventListener() {}, addListener() {} }),
  scrollTo() {}, scrollY: 0, innerHeight: 900, innerWidth: 1200,
  setTimeout: (fn) => { fn(); return 1; }, clearTimeout() {},
  getComputedStyle: () => ({ getPropertyValue: () => "" }),
  history: { back() {}, pushState() {} },
  console,
};
function fire(t) { for (const fn of listeners[t] || []) fn(); }
win.window = win;
for (const k of ["document", "location", "requestAnimationFrame", "cancelAnimationFrame",
                 "matchMedia", "scrollTo", "innerHeight", "innerWidth", "getComputedStyle",
                 "history", "setTimeout", "clearTimeout", "scrollY", "console"]) {
  Object.defineProperty(globalThis, k, { get: () => win[k], set: v => { win[k] = v; }, configurable: true });
}
globalThis.addEventListener = win.addEventListener;
globalThis.removeEventListener = win.removeEventListener;
globalThis.window = win;

const vm = require("vm");
vm.createContext(globalThis);
for (const src of scripts) vm.runInThisContext(src);
// The page declares its payload as a top-level `const`, which lives in the
// global LEXICAL scope — shared between scripts exactly as in a browser, but
// never a property of globalThis. One more script bridges what the checks
// below need out of it.
vm.runInThisContext("globalThis.__D = D; globalThis.__chainsOf = chainsOf; globalThis.__algoWalk = algoWalk; globalThis.__chainName = chainName;");

// ---- assertions ---------------------------------------------------------
let pass = 0, fail = 0;
const ok = (name, cond, extra) => {
  if (cond) { pass++; return; }
  fail++; console.log("FAIL: " + name + (extra !== undefined ? "  [" + extra + "]" : ""));
};
// skip records a group of checks that did not APPLY to this page, so the
// closing count stops moving in silence: the fixture and a real page report
// different totals, and nothing used to say whether the missing checks did
// not apply or had gone missing. Those two readings need different reactions.
//
// A group, not a per-check number: a hand-kept "6 skipped" goes stale the
// first time somebody adds a check inside the group, and a gate whose own
// bookkeeping rots is the failure this is here to prevent.
const skips = [];
const skip = reason => { skips.push(reason); };
const D = globalThis.__D;
const render = h => { ids.view.textContent = ""; win.location.hash = h; };
const textOf = () => ids.view.textContent;

ok("payload carries chains", Array.isArray(D.chains) && D.chains.length > 0, D.chains && D.chains.length);
ok("deepestChain indexes a chain",
   D.stats.deepestChain >= 0 && D.stats.deepestChain < D.chains.length, D.stats.deepestChain);

// Every chain row must point at real view rows of its own sitting.
let badRange = 0, badArea = 0, badLen = 0;
for (const c of D.chains) {
  const s = D.sess[c[0]];
  const first = s[0] + 1, last = s[0] + s[3];
  if (c[1] < first || c[2] > last || c[1] > c[2]) badRange++;
  let main = 0;
  for (let r = c[1]; r <= c[2]; r++) {
    const row = D.rows[r];
    if (row[0] !== 1) { badRange++; break; }
    if ((row[9] & 1) === 0) { main++; if (row[3] !== c[4]) badArea++; }
  }
  if (main !== c[3]) badLen++;
}
ok("every chain range sits inside its sitting", badRange === 0, badRange);
ok("every main-lane row of a chain carries its area", badArea === 0, badArea);
ok("every chain's length matches its rows", badLen === 0, badLen);

// The six old views still render.
for (const [name, h] of [["overview", "#/"], ["report", "#/report"], ["topics", "#/topics"],
                          ["list", "#/list"]]) {
  render(h);
  ok(name + " renders", textOf().length > 40, textOf().length);
}
const day0 = new Date(D.days[D.days.length - 1][0] * 86400000).toISOString().slice(0, 10);
render("#/day/" + day0);
ok("day renders", textOf().length > 40);
render("#/session/0");
ok("session renders", textOf().length > 40);
ok("session offers its neighbours", ids.view.querySelectorAll("p.near a").length >= 1,
   ids.view.querySelectorAll("p.near a").length);

// The new chain route: focus panel, tiles, and the bracket links.
const dc = D.stats.deepestChain, dcRow = D.chains[dc];
const sameSess = D.chains.map((c, i) => [c, i]).filter(([c]) => c[0] === dcRow[0]);
const k = sameSess.findIndex(([, i]) => i === dc);
render("#/session/" + dcRow[0] + "/chain/" + k);
ok("chain route renders a panel", ids.view.querySelectorAll("div.chainpanel").length === 1,
   ids.view.querySelectorAll("div.chainpanel").length);
ok("chain panel carries tiles", ids.view.querySelectorAll("div.chainpanel div.tile").length >= 3,
   ids.view.querySelectorAll("div.chainpanel div.tile").length);
ok("chain panel names the depth", textOf().includes(String(dcRow[3]) + " videos"));
const brackets = ids.view.querySelectorAll("g.chain");
ok("every chain of the sitting has a bracket", brackets.length === sameSess.length,
   brackets.length + " vs " + sameSess.length);
ok("exactly one bracket is focused",
   ids.view.querySelectorAll("g.chain.on").length === 1,
   ids.view.querySelectorAll("g.chain.on").length);
ok("brackets are keyboard reachable",
   brackets.every(b => b.getAttribute("tabindex") === "0" && b.getAttribute("role") === "link"));
ok("brackets carry an aria-label", brackets.every(b => (b.getAttribute("aria-label") || "").length > 5));

// A bracket click has to land on its own chain route.
render("#/session/" + dcRow[0]);
const target = ids.view.querySelectorAll("g.chain")[k];
target.click();
ok("clicking a bracket opens that chain", hash === "#/session/" + dcRow[0] + "/chain/" + k, hash);

// An out-of-range chain says so instead of throwing.
render("#/session/" + dcRow[0] + "/chain/999");
ok("a chain that does not exist says so", textOf().toLowerCase().includes("not on this timeline"));

// #/holes: renders, sorts, filters, and every row links to a real chain.
render("#/holes");
ok("holes renders", textOf().length > 100);
const rows = ids.view.querySelectorAll("div.rline");
ok("holes lists rows", rows.length > 0, rows.length);
// Virtualised: the spacer is the promise of how many rows there are, and
// the DOM holds only the ones near the viewport.
const holeSpacer = ids.view.querySelector("div");
ok("holes promises every chain, not the first 300", (() => {
  const sp = ids.view.querySelectorAll("div").find(d => d.attrs.id === "spacer" || d.style.height);
  if (!sp || !sp.style.height) return false;
  const rowsPromised = Math.round(parseFloat(sp.style.height) / 62);
  return rowsPromised === D.chains.length;
})(), D.chains.length);
ok("every hole row has an arrow", ids.view.querySelectorAll("a.rgo").length === rows.length);
ok("every hole row explains itself", ids.view.querySelectorAll("p.rwhy").length === rows.length);
const hrefs = ids.view.querySelectorAll("a.rrow").map(a => a.href);
ok("every hole row links to a chain route",
   hrefs.every(h => /^#\/session\/\d+\/chain\/\d+$/.test(h)), hrefs.find(h => !/^#\/session\/\d+\/chain\/\d+$/.test(h)));

// Follow the first row and check the panel really opens there.
const firstHref = hrefs[0];
render(firstHref);
ok("the first hole opens its chain", ids.view.querySelectorAll("div.chainpanel").length === 1);

// Sorting: depth must be non-increasing, span must be non-increasing.
function firstNums(sel) {
  render(sel);
  return ids.view.querySelectorAll("a.rrow").map(a =>
    parseInt((a.querySelectorAll("span.rnum")[1] || { textContent: "0" }).textContent, 10));
}
const depths = firstNums("#/holes/depth");
ok("depth sorts descending", depths.every((v, i) => i === 0 || depths[i - 1] >= v),
   depths.slice(0, 5).join(","));
render("#/holes/span");
ok("a different sort key still renders", ids.view.querySelectorAll("div.rline").length > 0);
const sortLinks = ids.view.querySelectorAll("p.sortbar a");
ok("the sort bar offers the other keys", sortLinks.length === 5, sortLinks.length);
ok("the active key is not a link to itself", !sortLinks.some(a => a.href === "#/holes/span"));

// The area filter narrows the list to that area.
const anArea = D.areas[D.chains[0][4]];
render("#/holes/depth/" + encodeURIComponent(anArea));
const filtered = ids.view.querySelectorAll("a.rrow").length;
const all = (render("#/holes/depth"), ids.view.querySelectorAll("a.rrow").length);
ok("the area filter narrows the list", filtered > 0 && filtered <= all, filtered + " of " + all);

// The overview's tile and card lead to the new places.
render("#/");
const links = ids.view.querySelectorAll("a").map(a => a.href);
ok("overview links to the ranking", links.includes("#/holes"));
ok("overview's deepest-chain tile opens a chain",
   links.some(h => /^#\/session\/\d+\/chain\/\d+$/.test(h)), links.filter(h => h.includes("chain")).length);

// ---- the day explorer ---------------------------------------------------
ok("every day row carries the new fields", D.days.every(d => d.length === 15), D.days[0].length);
const peaked = D.days.filter(d => d[13] > 0).length;
ok("days carry a peak rank", peaked > 0, peaked);
// -1 is a real answer: a day at the bottom of EVERY axis peaked on none of
// them, and the page must render it as "no rank" rather than as axis 0.
ok("peak axes are in range or explicitly none", D.days.every(d => d[14] === -1 || (d[14] >= 0 && d[14] < 4)));
ok("a day with no rank says so", D.days.filter(d => d[14] === -1).every(d => d[13] === 0));
ok("chainMax never exceeds the deepest chain",
   Math.max(...D.days.map(d => d[7])) === Math.max(...D.chains.map(c => c[3])));
// The chain views of a day may not exceed its views.
ok("chain views fit inside the day", D.days.every(d => d[6] <= d[1]));
ok("night views fit inside the day", D.days.every(d => d[8] <= d[1]));
ok("through never exceeds edged", D.days.every(d => d[10] <= d[11]));
// New channels must add up to the number of distinct channels ever watched.
const newSum = D.days.reduce((a, d) => a + d[12], 0);
const distinct = new Set();
for (const r of D.rows) if (r[0] === 1 && D.chans[r[5]]) distinct.add(r[5]);
ok("new channels sum to the distinct channels watched", newSum === distinct.size,
   newSum + " vs " + distinct.size);

render("#/days");
ok("days renders", textOf().length > 100);
const dayRows = ids.view.querySelectorAll("div.rline");
ok("days lists rows", dayRows.length > 0, dayRows.length);
ok("days promises every day, not the first 300", (() => {
  const sp = ids.view.querySelectorAll("div").find(d => d.style.height);
  if (!sp || !sp.style.height) return false;
  return Math.round(parseFloat(sp.style.height) / 62) === D.days.length;
})(), D.days.length);
ok("every day row explains its rank", ids.view.querySelectorAll("p.rwhy").length === dayRows.length);
const dayHrefs = ids.view.querySelectorAll("a.rrow").map(a => a.href);
ok("every day row links to a day route",
   dayHrefs.every(h => /^#\/day\/\d{4}-\d{2}-\d{2}$/.test(h)), dayHrefs.find(h => !/^#\/day\//.test(h)));
// The default sort really is by peak, descending.
render("#/days/views");
const viewNums = ids.view.querySelectorAll("a.rrow").map(a =>
  parseInt((a.querySelectorAll("span.rnum")[0] || {textContent:"0"}).textContent.replace(/[^\d]/g, ""), 10));
ok("views sorts descending", viewNums.every((v, i) => i === 0 || viewNums[i-1] >= v),
   viewNums.slice(0, 4).join(","));
const daySortLinks = ids.view.querySelectorAll("p.sortbar a");
ok("the day sort bar offers the other keys", daySortLinks.length === 8, daySortLinks.length);

// Following a day row must open that day, with its neighbours and tiles.
render(dayHrefs[0]);
ok("the first ranked day opens", textOf().length > 100);
ok("the day offers its neighbours", ids.view.querySelectorAll("p.near a").length >= 1,
   ids.view.querySelectorAll("p.near a").length);
ok("the day shows its measurements", ids.view.querySelectorAll("div.tiles div.tile, div.tiles a.tile").length >= 2,
   ids.view.querySelectorAll("div.tiles div.tile, div.tiles a.tile").length);

// Prev/next must land on a real neighbouring day, and back must return.
const before = hash;
const nextLink = ids.view.querySelectorAll("p.near a").find(a => /#\/day\//.test(a.href));
render(nextLink.href);
ok("stepping to a neighbour opens a day", textOf().length > 100 && hash !== before, hash);

// The calendar lens: switching it must repaint without throwing and without
// changing a single hue.
render("#/");
const cal = ids.view.querySelectorAll("label.pick select");
ok("the calendar offers a lens", cal.length >= 1, cal.length);
const rects = ids.view.querySelectorAll("svg rect");
const huesBefore = rects.map(r => r.getAttribute("fill"));
const lensSel = cal[0];
lensSel.value = "night";
for (const fn of lensSel.listeners.change || []) fn({ type: "change" });
const huesAfter = ids.view.querySelectorAll("svg rect").map(r => r.getAttribute("fill"));
ok("the lens leaves every hue alone", JSON.stringify(huesBefore) === JSON.stringify(huesAfter));
ok("the lens is not a hash step", hash === "#/", hash);

// ---- the algorithm, read backwards --------------------------------------
render("#/algo");
ok("algo renders", textOf().length > 400, textOf().length);
ok("algo names its caveat", textOf().includes("never what was offered"));
const algoPanels = ids.view.querySelectorAll("div.panel");
ok("algo draws four panels", algoPanels.length === 4, algoPanels.length);
ok("algo draws small multiples", ids.view.querySelectorAll("div.smalls div.small").length > 1,
   ids.view.querySelectorAll("div.smalls div.small").length);
const takeovers = ids.view.querySelectorAll("div.rline a.rrow").map(a => a.href);
ok("takeovers link to sittings", takeovers.length > 0 && takeovers.every(h => /^#\/session\/\d+$/.test(h)),
   takeovers.length + " rows");
// The retention scatter's points must lead into the list for their area.
// hit() marks its element with .hit and role=link; the mini DOM's selector
// grammar has no attribute matching, so the class is what is checked here.
const reachable = ids.view.querySelectorAll("g.hit")
  .filter(g => g.getAttribute("role") === "link" && /^#\/list\//.test(""));
const gHits = ids.view.querySelectorAll("g.hit");
ok("retention points are reachable", gHits.length > 0, gHits.length);
ok("retention points carry a role and a label",
   gHits.every(g => g.getAttribute("role") === "link" && (g.getAttribute("aria-label") || "").length > 3));

// Cross-check the page's own walk against the payload, independently: the
// retention numbers the panel draws must match a fresh count over D.rows.
const A2 = globalThis.__algoWalk ? globalThis.__algoWalk() : null;
if (A2) {
  let mainSum = 0;
  A2.retention.forEach(r => { mainSum += r.main; });
  let mainRows = 0;
  for (const r of D.rows) if (r[0] === 1 && (r[9] & 1) === 0) mainRows++;
  ok("the algo walk sees every main-lane view", mainSum === mainRows, mainSum + " vs " + mainRows);
  const posSum = A2.byPos.reduce((a, b) => a + b.reduce((x, y) => x + y, 0), 0);
  ok("every main-lane view lands in a position bucket", posSum === mainRows, posSum + " vs " + mainRows);
  let monthViews = 0;
  for (const m of A2.months.values()) monthViews += m.total;
  ok("every main-lane view lands in a month", monthViews === mainRows, monthViews + " vs " + mainRows);
  let newSum2 = 0;
  for (const m of A2.months.values()) newSum2 += m.newChans;
  ok("the algo walk meets every channel once", newSum2 === distinct.size, newSum2 + " vs " + distinct.size);
}

// The overview must offer the way in.
render("#/");
ok("overview links to the algorithm view",
   ids.view.querySelectorAll("a").map(a => a.href).includes("#/algo"));

// ---- the model's names --------------------------------------------------
if (D.holeLabels) {
  ok("labels are pairs of [chainIdx, label]",
     D.holeLabels.every(r => Array.isArray(r) && r.length === 2 &&
       Number.isInteger(r[0]) && typeof r[1] === "string"));
  ok("every label names a real chain",
     D.holeLabels.every(([ci]) => ci >= 0 && ci < D.chains.length));
  ok("labels are sorted by chain index, so two runs produce the same bytes",
     D.holeLabels.every((r, i) => i === 0 || D.holeLabels[i-1][0] < r[0]));
  ok("labels are short enough for a table cell",
     D.holeLabels.every(([, l]) => l.length > 0 && l.length <= 48),
     D.holeLabels.map(([, l]) => l.length).sort((a,b)=>b-a)[0]);
  // A labelled chain must SHOW its label, and an unlabelled one must fall
  // back — the page has to read the same either way.
  const labelled = D.holeLabels[0][0];
  render("#/holes");
  const names = ids.view.querySelectorAll("a.rrow span.rname").map(e => e.textContent);
  ok("the ranking shows some model names",
     names.some(n => !/^chain of \d+ · /.test(n)), names[0]);
  // The list is virtualised and sorted by depth, so the rows on screen are
  // exactly the labelled ones — the fallback is checked at the source.
  const unlabelledIdx = D.chains.map((c, i) => i).find(i => !D.holeLabels.some(([ci]) => ci === i));
  ok("unlabelled chains keep their plain name",
     unlabelledIdx === undefined || /^chain of \d+ · /.test(globalThis.__chainName(unlabelledIdx)),
     unlabelledIdx === undefined ? "all labelled" : globalThis.__chainName(unlabelledIdx));
  const unlabelled = D.chains.map((c, i) => i).find(i => !D.holeLabels.some(([ci]) => ci === i));
  if (unlabelled !== undefined) {
    const c = D.chains[unlabelled];
    const k = __chainsOf(c[0]).indexOf(unlabelled);
    render("#/session/" + c[0] + "/chain/" + k);
    ok("an unlabelled chain still opens", ids.view.querySelectorAll("div.chainpanel").length === 1);
  }
} else {
  skip("the model's names: this page carries no hole labels");
}

// ---- provenance ---------------------------------------------------------
// The head line has to say which projection the rows were folded through.
// The CSV says it and the terminal says it; the page carried it in the
// payload where no reader could reach it.
if (D.taxonomy && D.taxonomy.startsWith("sha256:")) {
  const digest = D.taxonomy.split(" ")[0];
  ok("the head line names the taxonomy the page ran on",
     ids.head.textContent.includes(digest), ids.head.textContent);
  ok("the head line keeps the short form, not the path and the mtime",
     !ids.head.textContent.includes(D.taxonomy));
} else {
  skip("the head line's provenance: this page was not folded through a taxonomy");
}

let line = (fail ? "FAILED" : "ALL PASS") + ": " + pass + " checks passed, " + fail + " failed";
if (skips.length) line += ", " + skips.length + " group(s) skipped (" + skips.join("; ") + ")";
console.log(line);
process.exit(fail ? 1 : 0);
