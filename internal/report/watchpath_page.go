// SPDX-License-Identifier: GPL-3.0-or-later

package report

// The watch path page carries four views of one timeline, so it is assembled
// from parts rather than written as one string: this file holds the shell —
// markup, style, shared drawing helpers and the router — and the two view
// files hold the drawing. A single file with all four would be unreadable,
// and the seams here are the same seams the page has.
//
// Two rules bind every part, including the view files:
//
//   - No backticks and no "{{" outside a real template action. The whole page
//     is one Go raw string literal that html/template parses.
//   - Video titles and channel names go through textContent or esc(), never
//     into innerHTML raw. They are YouTube's data, not ours.
var watchPathTpl = pageHead + pageCSS + pageBody + coreJS + overviewJS + detailJS + pageTail

const pageHead = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>youtubehistii watch path</title>
`

const pageCSS = `<style>
:root {
  --bg: #ffffff; --fg: #1a1a1a; --muted: #666; --line: #e2e2e2;
  --bar: #4a7dbd; --card: #f6f6f6; --card2: #eeeeee;
  --consume: #d98a3d; --learn: #4c9e6b; --mixed: #8a6fb8; --unclear: #9a9a9a;
  --area-sat: 55%; --area-lum: 42%;
  --grid: #ececec; --tipbg: #ffffff;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #16181c; --fg: #e6e6e6; --muted: #9a9a9a; --line: #33363c;
    --bar: #6b9fd8; --card: #1f2228; --card2: #24272e;
    --consume: #e0a468; --learn: #6dbb8a; --mixed: #a68fd0; --unclear: #7a7a7a;
    --area-sat: 50%; --area-lum: 62%;
    --grid: #23262b; --tipbg: #23262b;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0 auto; padding: 1.5rem 1rem 4rem; max-width: 62rem;
  background: var(--bg); color: var(--fg);
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  line-height: 1.5;
}
h1 { font-size: 1.5rem; margin: 0 0 .2rem; }
h2 { font-size: 1rem; margin: 0 0 .1rem; }
.muted { color: var(--muted); font-size: .85rem; }
.note { background: var(--card); border-left: 3px solid var(--bar); padding: .6rem .9rem;
  border-radius: .3rem; font-size: .82rem; margin: 1rem 0; }
.note summary { cursor: pointer; font-weight: 600; }
.note[open] summary { margin-bottom: .4rem; }
.legend { display: flex; flex-wrap: wrap; gap: .8rem; font-size: .78rem; color: var(--muted); margin: .6rem 0 0; }
.legend b { font-weight: 600; color: var(--fg); }

/* Breadcrumb — the only navigation. Every step is a real hash link so the
   browser's back button walks the same path backwards. */
nav#crumbs { font-size: .85rem; margin: .6rem 0 1rem; display: flex; flex-wrap: wrap;
  align-items: baseline; gap: .35rem; }
nav#crumbs a { color: var(--bar); text-decoration: none; }
nav#crumbs a:hover { text-decoration: underline; }
nav#crumbs .sep { color: var(--muted); }
nav#crumbs .here { font-weight: 600; }
nav#crumbs .spacer { flex: 1; }
nav#crumbs .alt { color: var(--muted); }

/* Panels group one drawing with its heading; the inner wrapper scrolls
   sideways on its own so the page body never does. */
.panel { border-top: 1px solid var(--line); padding-top: .9rem; margin-top: 1.6rem; }
.panel > .muted { margin: 0 0 .6rem; }
.chart { overflow-x: auto; overflow-y: hidden; }
.chart svg { display: block; }

.tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(9.5rem, 1fr));
  gap: .5rem; margin: 1rem 0; }
.tile { background: var(--card); border-radius: .4rem; padding: .5rem .7rem; }
.tile .k { display: block; font-size: .72rem; color: var(--muted); text-transform: lowercase; }
.tile .v { display: block; font-size: 1.15rem; font-variant-numeric: tabular-nums; }
/* The sub-line wraps instead of being clipped: it carries the caveat that
   keeps the number honest ("if every video had run to its end"), and half of
   that sentence is worse than two lines. Grid rows stretch, so the tiles in a
   row stay the same height anyway. */
.tile .s { display: block; font-size: .72rem; color: var(--muted); line-height: 1.3; }
a.tile { color: inherit; text-decoration: none; }
a.tile:hover { background: var(--card2); outline: 1px solid var(--line); }

/* One tooltip element for every view — a cell, an arc and a bar all want the
   same thing, and one node that moves beats hundreds of titles. */
#tip { position: fixed; z-index: 9; pointer-events: none; max-width: 22rem;
  background: var(--tipbg); border: 1px solid var(--line); border-radius: .3rem;
  padding: .3rem .5rem; font-size: .76rem; line-height: 1.35;
  box-shadow: 0 2px 8px rgba(0,0,0,.18); }
#tip[hidden] { display: none; }
#tip b { display: block; font-weight: 600; }
#tip .m { color: var(--muted); }

/* Cards are shared: the virtual list positions them absolutely, the session
   view lets them flow. */
.card { background: var(--card); border-radius: .4rem; padding: .3rem .6rem;
  border-left: 3px solid transparent; overflow: hidden; }
.card.rabbit { border-left-color: var(--area); background: var(--card2); }
.card.overlap { background: none; border: 1px dashed var(--line); }
.l1 { display: flex; gap: .5rem; align-items: baseline; }
.dot { width: .55rem; height: .55rem; border-radius: 50%; background: var(--area); flex-shrink: 0; }
.title { flex: 1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-size: .88rem; }
.clock { font-variant-numeric: tabular-nums; font-size: .75rem; color: var(--muted); flex-shrink: 0; }
.l2 { font-size: .72rem; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.area { color: var(--area); }
.mode { display: inline-block; padding: 0 .4rem; border-radius: .6rem; font-size: .68rem; color: #fff; }
.mode.consume { background: var(--consume); } .mode.learn { background: var(--learn); }
.mode.mixed { background: var(--mixed); } .mode.unclear { background: var(--unclear); }
.edge { color: var(--muted); }
.edge.skipped { color: var(--consume); }
.ov { border: 1px solid var(--line); border-radius: .6rem; padding: 0 .35rem; font-size: .68rem; }

/* The virtual list: one spacer carries the full height, only the visible
   rows exist in the DOM. Every row is exactly --rh tall. */
#path { position: relative; border-left: 2px solid var(--line); margin-top: 1rem; }
#spacer { width: 1px; }
.row { position: absolute; left: 0; right: 0; height: var(--rh); padding: .25rem 0 .25rem .9rem; }
.row > .card { height: 100%; }
.row.lane2 { padding-left: 3rem; }
.sess { height: 100%; display: flex; align-items: center; gap: .6rem;
  border-top: 1px solid var(--line); font-size: .8rem; }
.sess .when { font-weight: 600; }
.sess .gap { color: var(--muted); }

/* The session view stacks the same cards without the absolute positioning —
   one sitting is short enough that nothing needs virtualising. */
.stack { display: flex; flex-direction: column; gap: .3rem; margin-top: .6rem; }
.stack .lane2 { margin-left: 2rem; }

/* Elements the views make clickable get the affordance from one place. */
.hit { cursor: pointer; }
.hit:hover { opacity: .75; }
svg text { fill: var(--fg); font-family: inherit; }
svg text.m { fill: var(--muted); }
</style>
</head>
`

const pageBody = `<body>
<h1>watch path</h1>
<p class="muted" id="head"></p>
<nav id="crumbs"></nav>

<details class="note">
<summary>Takeout logs when a video was STARTED — nothing else.</summary>
No end, no watch time, no device. Everything on this page is derived from the
gap to the next start, and every gap has two readings: the video was abandoned,
or it kept playing while something else was started. The labels mark what the
data suggests; none of them is a fact. A sitting counts on the day it BEGAN,
so an evening that runs past midnight stays on that evening.
<div class="legend">
  <span><b>session</b> a new one starts after {{.SessionGap}} min of silence</span>
  <span><b>watched through</b> the gap was at least the video's length</span>
  <span><b>most of it</b> over half</span>
  <span><b>moved on</b> under half</span>
  <span><b>overlap suspected</b> started while a video of {{.LongVideo}} min or more
    from another area was still running — parallel, or simply abandoned; the export cannot tell</span>
  <span><b>chain</b> {{.RabbitLen}}+ videos of one area, under {{.RabbitGap}} min apart</span>
</div>
</details>

<main id="view"></main>
<div id="tip" hidden></div>
`

// coreJS is everything the four views share: the payload, the small format
// helpers, the card the list and the session view both draw, and the router.
const coreJS = `<script>
// Assigned in a plain script element on purpose: html/template only treats
// this as JavaScript here, and json.Marshal's default escaping of < > &
// means no title can close the element.
const D = {{.Data}};
const RH = {{.RowHeight}};

const viewEl = document.getElementById("view");
const crumbsEl = document.getElementById("crumbs");
const tipEl = document.getElementById("tip");

// ---- element helpers -------------------------------------------------

function $(tag, cls, txt) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (txt != null) e.textContent = txt;
  return e;
}

const SVGNS = "http://www.w3.org/2000/svg";
function svg(tag, attrs) {
  const e = document.createElementNS(SVGNS, tag);
  for (const k in attrs) {
    if (attrs[k] !== null && attrs[k] !== undefined) e.setAttribute(k, attrs[k]);
  }
  return e;
}

// Titles and channel names go through textContent; only the small labels are
// built as markup, so this escape covers exactly those interpolations.
function esc(s) {
  return String(s).replace(/[&<>"]/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}

// ---- colour and naming ------------------------------------------------

function areaColor(i) {
  if (i < 0 || i >= D.areaHues.length) return "var(--unclear)";
  return "hsl(" + D.areaHues[i] + " var(--area-sat) var(--area-lum))";
}
function areaName(i) {
  if (i < 0 || i >= D.areas.length) return "unclear";
  return D.areas[i] || "unclear";
}

// ---- time formatting ---------------------------------------------------

const pad = n => String(n).padStart(2, "0");
const at = ts => new Date((D.t0 + ts) * 1000);
const clock = ts => { const d = at(ts); return pad(d.getHours()) + ":" + pad(d.getMinutes()); };
const stamp = ts => at(ts).toLocaleDateString(undefined,
  { weekday: "short", year: "numeric", month: "short", day: "numeric" }) + ", " + clock(ts);

// Durations read as a human would say them, not as zero-padded clock time:
// "6 h 20 min" for a gap between sessions, "4:12" for a video length.
const dur = s => {
  if (s <= 0) return "";
  const d = Math.floor(s / 86400), h = Math.floor(s % 86400 / 3600), m = Math.round(s % 3600 / 60);
  if (d) return h ? d + " d " + h + " h" : d + " d";
  if (h) return m ? h + " h " + m + " min" : h + " h";
  return m + " min";
};
const clip = s => (s <= 0 ? "" : Math.floor(s / 60) + ":" + pad(s % 60));

// Days are grid coordinates, not moments: Go turned each local calendar date
// into a day number once, so every read here is UTC and no timezone can shift
// a cell into the neighbouring week.
const dayAt = ed => new Date(ed * 86400000);
const dayDate = ed => dayAt(ed).toISOString().slice(0, 10);
const dayLabel = ed => dayAt(ed).toLocaleDateString(undefined,
  { weekday: "short", year: "numeric", month: "short", day: "numeric", timeZone: "UTC" });
const dayWeekday = ed => (((ed + 4) % 7) + 7) % 7;   // 1970-01-01 was a Thursday
const epochOf = s => Math.floor(Date.parse(s + "T00:00:00Z") / 86400000);

const EDGE_TEXT = { through: "watched through", most: "most of it", skipped: "moved on" };

// ---- lookups over the payload -----------------------------------------

// Row layout, kept in one place so no view has to remember the offsets.
const R_TS = 1, R_DUR = 2, R_AREA = 3, R_SUB = 4, R_CHAN = 5,
      R_MODE = 6, R_EDGE = 7, R_GAP = 8, R_FLAGS = 9, R_TITLE = 10;
const S_ROW = 0, S_TS = 1, S_SPAN = 2, S_N = 3, S_GAP = 4, S_DAY = 5;
const DY_ED = 0, DY_VIEWS = 1, DY_AREA = 2, DY_FROM = 3, DY_TO = 4;

const isOverlap = r => (r[R_FLAGS] & 1) !== 0;
const isRabbit = r => (r[R_FLAGS] & 2) !== 0;

// sessionViews returns the views of one sitting, newest first — the slice of
// rows that follows its header row. Go guarantees that layout.
function sessionViews(si) {
  const s = D.sess[si];
  return D.rows.slice(s[S_ROW] + 1, s[S_ROW] + 1 + s[S_N]);
}

const dayByDate = new Map();
if (D.days) D.days.forEach((d, i) => dayByDate.set(dayDate(d[DY_ED]), i));

// ---- the shared card ---------------------------------------------------

// viewCard draws one video the way both the list and the session view want
// it. It sets --area on itself, so a caller can lean on that variable too.
function viewCard(r) {
  const overlap = isOverlap(r);
  const card = $("div", "card" + (isRabbit(r) ? " rabbit" : "") + (overlap ? " overlap" : ""));
  card.style.setProperty("--area", areaColor(r[R_AREA]));

  const l1 = $("div", "l1");
  l1.appendChild($("span", "dot"));
  const t = $("span", "title", r[R_TITLE] || "(no title)");
  l1.appendChild(t);
  l1.appendChild($("span", "clock", clock(r[R_TS])));

  const l2 = $("div", "l2");
  const bits = [];
  bits.push('<span class="area">' + esc(areaName(r[R_AREA])) +
    (D.subs[r[R_SUB]] ? " / " + esc(D.subs[r[R_SUB]]) : "") + "</span>");
  if (D.chans[r[R_CHAN]]) bits.push(esc(D.chans[r[R_CHAN]]));
  if (r[R_DUR] > 0) bits.push(clip(r[R_DUR]));
  bits.push('<span class="mode ' + D.modes[r[R_MODE]] + '">' + D.modes[r[R_MODE]] + "</span>");
  if (overlap) bits.push('<span class="ov">overlap suspected</span>');
  const edge = D.edges[r[R_EDGE]];
  if (edge) bits.push('<span class="edge ' + edge + '">&#8595; ' + EDGE_TEXT[edge] +
    (r[R_GAP] > 0 ? " after " + dur(r[R_GAP]) : "") + "</span>");
  l2.innerHTML = bits.join(" &middot; ");

  card.appendChild(l1);
  card.appendChild(l2);
  return card;
}

// sessionLine is the one-line summary a sitting gets wherever it is listed.
function sessionLine(s) {
  const n = s[S_N];
  return n + (n === 1 ? " video" : " videos") +
    (s[S_SPAN] > 0 ? " over " + dur(s[S_SPAN]) : "") +
    (s[S_GAP] > 0 ? " · " + dur(s[S_GAP]) + " after the previous session" : "");
}

// ---- the tooltip -------------------------------------------------------

// One node that moves, rather than a title attribute per cell: the heatmap
// alone has thousands of cells, and the browser's own tooltip arrives too
// late to be part of reading a chart.
const tip = {
  show(ev, html) {
    tipEl.innerHTML = html;
    tipEl.hidden = false;
    const r = tipEl.getBoundingClientRect();
    let x = ev.clientX + 12, y = ev.clientY + 14;
    if (x + r.width > innerWidth - 8) x = ev.clientX - r.width - 12;
    if (y + r.height > innerHeight - 8) y = ev.clientY - r.height - 14;
    tipEl.style.left = Math.max(4, x) + "px";
    tipEl.style.top = Math.max(4, y) + "px";
  },
  hide() { tipEl.hidden = true; },
};
// A view can be torn down mid-hover; nothing else clears the tooltip then.
addEventListener("scroll", () => tip.hide(), { passive: true });

// hover wires an element to the tooltip. html is a function so the text is
// only built for the cell actually under the pointer.
function hover(el, html) {
  el.addEventListener("mouseenter", ev => tip.show(ev, html()));
  el.addEventListener("mousemove", ev => tip.show(ev, html()));
  el.addEventListener("mouseleave", () => tip.hide());
}

// hit makes an HTML element navigate like a link, for the places where the
// element that carries the destination is not an anchor.
function hit(el, hash, label) {
  el.classList.add("hit");
  el.setAttribute("tabindex", "0");
  el.setAttribute("role", "link");
  if (label) el.setAttribute("aria-label", label);
  el.addEventListener("click", () => go(hash));
  el.addEventListener("keydown", ev => {
    if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); go(hash); }
  });
  return el;
}

// clickTo is hit() without the keyboard, and it is what every drawn shape
// uses. Each chart is one role="img" with a title, so a screen reader reads
// the picture and never descends into it — a tabindex on a cell inside it
// would hand the keyboard a focus stop that nothing announces. Worse, the
// calendar has one shape per day and a busy day has one per video, so those
// stops would number in the thousands and bury the real controls. The drawing
// is a picture you click; the keyboard path is the HTML around it — the
// breadcrumb, the tiles, and the sitting list under every chart.
function clickTo(el, hash) {
  el.classList.add("hit");
  el.addEventListener("click", () => go(hash));
  return el;
}

function go(hash) { location.hash = hash; }
</script>
`

// pageTail closes the page with the list view and the router. It comes last
// because the router names every view function, and the view files are
// spliced in between.
const pageTail = `<script>
// ---- level 4: every view as one list ----------------------------------

// The list keeps its own scroll offset, because coming back to it from a
// session is a return, not a fresh visit.
let listScroll = 0;

function renderList(root) {
  const path = $("div"); path.id = "path";
  const spacer = $("div"); spacer.id = "spacer";
  path.appendChild(spacer);
  root.appendChild(path);
  spacer.style.height = (D.rows.length * RH) + "px";
  path.style.setProperty("--rh", RH + "px");

  function rowEl(r, i) {
    const el = $("div", "row");
    el.style.top = (i * RH) + "px";
    if (r[0] === 0) {
      const wrap = $("div", "sess");
      const when = $("span", "when", stamp(r[R_TS]));
      wrap.appendChild(hit(when, "#/session/" + rowToSession.get(i), "open this sitting"));
      // A session row and a D.sess entry carry span, count and gap at the
      // same offsets, which is what lets one formatter serve both.
      wrap.appendChild($("span", "gap", sessionLine(r)));
      el.appendChild(wrap);
      return el;
    }
    if (isOverlap(r)) el.classList.add("lane2");
    el.appendChild(viewCard(r));
    return el;
  }

  // Rows kept above and below the viewport. Generous on purpose: a scroll
  // event lands after the frame it belongs to, so without a buffer a fast
  // scroll shows empty space until the next draw catches up.
  const OVERSCAN = 14;
  let first = -1, last = -1;
  const live = new Map();

  function draw() {
    const top = scrollY - path.offsetTop;
    const from = Math.max(0, Math.floor(top / RH) - OVERSCAN);
    const to = Math.min(D.rows.length - 1, Math.ceil((top + innerHeight) / RH) + OVERSCAN);
    if (from === first && to === last) return;
    for (const [i, el] of live) {
      if (i < from || i > to) { el.remove(); live.delete(i); }
    }
    for (let i = from; i <= to; i++) {
      if (live.has(i)) continue;
      const el = rowEl(D.rows[i], i);
      path.appendChild(el);
      live.set(i, el);
    }
    first = from; last = to;
  }

  // Draw inside the animation frame rather than straight out of the event:
  // the scroll handler runs after the browser has already painted, so drawing
  // there means one frame of empty space on every fast scroll.
  let queued = false;
  function schedule() {
    listScroll = scrollY;
    if (queued) return;
    queued = true;
    requestAnimationFrame(() => { queued = false; draw(); });
  }
  addEventListener("scroll", schedule, { passive: true });
  addEventListener("resize", schedule);
  scrollTo(0, listScroll);
  draw();

  return () => {
    removeEventListener("scroll", schedule);
    removeEventListener("resize", schedule);
  };
}

// rowToSession maps a session header row back to its index in D.sess, which
// is what a click on the list has to hand to the router.
const rowToSession = new Map();
if (D.sess) D.sess.forEach((s, i) => rowToSession.set(s[S_ROW], i));

// ---- the router --------------------------------------------------------

// Navigation is location.hash and nothing else: the back button then walks
// the same path backwards for free, and a view is a link that can be shared.
let teardown = null;

function crumbs(trail) {
  crumbsEl.textContent = "";
  trail.forEach((c, i) => {
    if (i) crumbsEl.appendChild($("span", "sep", "›"));
    if (c.hash) {
      const a = $("a", null, c.text);
      a.href = "#" + c.hash;
      crumbsEl.appendChild(a);
    } else {
      crumbsEl.appendChild($("span", "here", c.text));
    }
  });
  crumbsEl.appendChild($("span", "spacer"));
  // The flat list is a sibling of the whole zoom, not a step inside it, so it
  // sits on the far side of the bar rather than in the trail.
  const onList = trail.length === 1 && !trail[0].hash && trail[0].text === "all views";
  const alt = $("a", "alt", onList ? "overview" : "all views (" + D.views.toLocaleString() + ")");
  alt.href = onList ? "#/" : "#/list";
  crumbsEl.appendChild(alt);
}

function notFound(root, what) {
  root.appendChild($("p", "muted", what + " is not on this timeline."));
}

function route() {
  if (teardown) { teardown(); teardown = null; }
  tip.hide();
  viewEl.textContent = "";
  const h = (location.hash || "").replace(/^#/, "") || "/";
  const parts = h.split("/").filter(s => s.length > 0);

  if (parts[0] === "day" && parts[1]) {
    const di = dayByDate.has(parts[1]) ? dayByDate.get(parts[1]) : -1;
    crumbs([{ text: "overview", hash: "/" }, { text: parts[1] }]);
    if (di < 0) { notFound(viewEl, parts[1]); return; }
    scrollTo(0, 0);
    teardown = renderDay(viewEl, di) || null;
    return;
  }
  if (parts[0] === "session" && parts[1]) {
    const si = parseInt(parts[1], 10);
    if (!(si >= 0 && D.sess && si < D.sess.length)) {
      crumbs([{ text: "overview", hash: "/" }, { text: "sitting " + parts[1] }]);
      notFound(viewEl, "that sitting");
      return;
    }
    const s = D.sess[si];
    const date = dayDate(D.days[s[S_DAY]][DY_ED]);
    crumbs([{ text: "overview", hash: "/" }, { text: date, hash: "/day/" + date },
            { text: clock(s[S_TS]) + " sitting" }]);
    scrollTo(0, 0);
    teardown = renderSession(viewEl, si) || null;
    return;
  }
  if (parts[0] === "list") {
    crumbs([{ text: "all views" }]);
    teardown = renderList(viewEl) || null;
    return;
  }
  crumbs([{ text: "overview" }]);
  scrollTo(0, 0);
  teardown = renderOverview(viewEl) || null;
}

document.getElementById("head").textContent =
  "generated {{.Generated}} · {{date .P.From}} … {{date .P.To}} · " +
  D.views.toLocaleString() + " views in " + D.sessions.toLocaleString() + " sessions" +
  (D.dropped ? " · " + D.dropped.toLocaleString() + " without a timestamp left off" : "");

addEventListener("hashchange", route);
if (!D.rows.length) {
  viewEl.appendChild($("p", "muted", "Nothing to show — run \"classify\" first."));
} else {
  route();
}
</script>
</body>
</html>
`
