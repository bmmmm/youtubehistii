// SPDX-License-Identifier: GPL-3.0-or-later

package report

// The watch path page carries every view of one export, so it is assembled
// from parts rather than written as one string: this file holds the shell —
// markup, style, shared drawing helpers, the router and the flat list — and
// five drawing files hold the rest. watchpath_overview.go has the calendar and
// the transition graph, watchpath_detail.go the day and the sitting,
// watchpath_cluster.go the topic tree, watchpath_reportview.go the aggregate
// report, watchpath_intro.go the entry cards. One file with all of it would be
// unreadable, and the seams here are the seams the page itself has.
//
// Two rules bind every part, including the drawing files:
//
//   - No backticks and no "{{" outside a real template action. The whole page
//     is one Go raw string literal that html/template parses.
//   - Video titles and channel names go through textContent or esc(), never
//     into innerHTML raw. They are YouTube's data, not ours.
var watchPathTpl = pageHead + pageCSS + pageBody + coreJS +
	overviewJS + detailJS + clusterJS + reportJS + rankJS + rankDaysJS + algoJS + introJS + pageTail

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

/* The ways in. A page whose best views sit behind a word in the top corner
   has hidden them, so the first thing under the heading is one card per view,
   each carrying a REAL drawing of the real data — a made-up example would be
   a strange thing to put first on a page this careful about what it claims.
   The motion is the label: it shows what the view DOES, which a screenshot
   cannot. */
.ways { display: grid; grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr));
  gap: .6rem; margin: 1.1rem 0 1.3rem; }
.way { display: block; text-decoration: none; color: inherit;
  background: var(--card); border: 1px solid transparent; border-radius: .5rem;
  padding: .5rem .6rem .6rem; overflow: hidden; }
.way:hover, .way:focus-visible { background: var(--card2); border-color: var(--line); }
.way h3 { margin: .4rem 0 .1rem; font-size: .85rem; font-weight: 600; }
.way p { margin: 0; font-size: .72rem; color: var(--muted); line-height: 1.3; }
.mini { display: block; width: 100%; height: 4.6rem; }
.mini circle, .mini rect, .mini path, .mini line, .mini g { transform-box: fill-box;
  transform-origin: center; }

/* Four motions, each saying what the view behind the card is: a circle opening
   up, a day being swept, a path being walked, a list running on. The report
   card borrows the travelling stroke for its months. Slow and low-contrast on
   purpose — this is a hint, not a carousel. */
@keyframes wayGrow { 0%, 100% { transform: scale(1); opacity: .5; }
  50% { transform: scale(1.22); opacity: 1; } }
@keyframes waySweep { from { transform: translateX(0); } to { transform: translateX(152px); } }
/* A short bright segment travelling along a line that is always there. An
   earlier version animated the whole stroke in and out, which left the card
   showing nothing but loose dots for half of every loop — a blink, not a path. */
@keyframes wayDraw { from { stroke-dashoffset: 0; } to { stroke-dashoffset: -226; } }
@keyframes wayRise { from { transform: translateY(3px); } to { transform: translateY(-13px); } }
.mini .grow { animation: wayGrow 6s ease-in-out infinite; }
.mini .sweep { animation: waySweep 7s linear infinite; }
.mini .draw { stroke-dasharray: 26 200; animation: wayDraw 6.5s linear infinite; }
.mini .rise { animation: wayRise 9s ease-in-out infinite alternate; }

/* Motion is a preference, not a decoration budget. Everything above still
   reads as a still picture with the path drawn in full. */
@media (prefers-reduced-motion: reduce) {
  .mini .grow, .mini .sweep, .mini .draw, .mini .rise { animation: none; }
  .mini .grow { opacity: 1; }
  /* Both of these only carry motion: the sweep is a clock hand with nothing
     to point at when it stands still, and the travelling segment sits on top
     of a line that is drawn anyway. Neither loses information by going. */
  .mini .sweep, .mini .draw { visibility: hidden; }
}

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
/* The topic on a card is an address, so it is drawn as one. Dotted and not
   solid: the whole second line is muted supporting text, and a solid
   underline in there reads as emphasis on the one word it is under. */
a.area { text-decoration: underline dotted; text-underline-offset: 2px; }
a.area:hover { text-decoration-style: solid; }
a.area:focus-visible { outline: 2px solid var(--bar); outline-offset: 1px;
  border-radius: .15rem; }
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

/* The report's topic rows. A row with subjects under it is a real button, so
   the toggle answers the keyboard and announces its state without any code;
   one with none is a plain row that happens to line up with it. */
.rt { margin-top: .7rem; }
.rrow { display: flex; align-items: center; gap: .5rem; width: 100%;
  padding: .3rem .4rem; border: 0; border-radius: .3rem; background: none;
  color: inherit; font: inherit; text-align: left; }
button.rrow { cursor: pointer; }
button.rrow:hover, button.rrow:focus-visible { background: var(--card); }
.rcar { width: .8rem; flex-shrink: 0; font-size: .65rem; color: var(--muted); }
.rname { flex: 1; min-width: 4rem; font-size: .88rem;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.rn { flex-shrink: 0; font-size: .72rem; color: var(--muted); }
.rnum { flex-shrink: 0; width: 4.8rem; text-align: right; white-space: nowrap;
  font-variant-numeric: tabular-nums; font-size: .76rem; color: var(--muted); }
.rbar { flex-shrink: 0; width: 7rem; height: .5rem; border-radius: .25rem; background: var(--line); }
.rfill { height: 100%; border-radius: .25rem; background: var(--bar); }
.rsubs { margin: 0 0 .4rem 1.3rem; border-left: 1px solid var(--line); }
.rsubs[hidden] { display: none; }
.rlink { color: var(--bar); }
a.rlink { text-decoration: underline; text-underline-offset: 2px; }
a.rlink:focus-visible { outline: 2px solid var(--bar); outline-offset: 2px;
  border-radius: .15rem; }
.runcl { margin: .5rem 0 0; padding-left: 1.1rem; font-size: .85rem; }

/* A row that leads somewhere has to say so BEFORE it is touched — the same
   rule the drawn controls follow further down, and the reason neither a
   cursor change nor a hover tint counts on its own: both are invisible to
   anyone who is not already pointing at the row. So a linked row carries an
   arrow that is on screen at rest.
   The arrow is a separate element rather than the row itself becoming a link
   in every case, because a row with subjects under it is already a button:
   opening it and travelling to it are two different actions, and interactive
   content may not nest inside a button anyway. */
.rline { display: flex; align-items: center; gap: .2rem; }
.rline > .rrow { flex: 1; min-width: 0; }
a.rrow { text-decoration: none; }
a.rrow:hover, a.rrow:focus-visible { background: var(--card); }
.rgo { flex-shrink: 0; color: var(--bar); font-size: .8rem; text-decoration: none;
  padding: 0 .2rem; }
a.rgo:hover, a.rgo:focus-visible { text-decoration: underline; }
/* On a phone the bar is the first thing that has to go: the number next to it
   says the same, and a 3 rem bar says nothing. */
@media (max-width: 30rem) { .rbar { display: none; } .rnum { width: 4rem; } }

/* Elements the views make clickable get the affordance from one place. */
.hit { cursor: pointer; }
.hit:hover { opacity: .75; }
/* A drawn control has to look like one BEFORE it is touched, and a cursor
   change is only visible to someone already pointing at it. The ring's nodes
   therefore grow a halo on hover and on focus. It is drawn as a stroke, not an
   outline: outline on an SVG shape is still unreliable across browsers, and a
   box around a circle is the wrong shape anyway. Half-opacity on purpose — the
   picked node's ring is set as an attribute and stays solid, and that
   difference is what keeps "under the pointer" apart from "picked". The rule
   names circle so it cannot reach the arcs' invisible hit paths, whose whole
   job is to stay unpainted. */
circle.hit:hover:not([aria-pressed="true"]),
circle.hit:focus-visible:not([aria-pressed="true"]) {
  stroke: var(--fg); stroke-width: 2; stroke-opacity: .55;
}
/* A chain bracket says it leads somewhere with a chevron above the bar, so
   it reads as a control on a touchscreen and in a screenshot too, where a
   cursor says nothing. The focused one is wider and fully opaque — the same
   "picked beats hovered" rule the ring follows. */
g.chain { cursor: pointer; }
g.chain > rect { opacity: .8; }
g.chain:hover > rect, g.chain:focus-visible > rect { opacity: 1; }
g.chain.on > rect { opacity: 1; }
g.chain:focus-visible { outline: 2px solid var(--bar); outline-offset: 2px; }
.chainpanel h2 { margin-bottom: .1rem; }
/* The neighbour row: three steps on one line, the ones that lead nowhere
   greyed rather than removed, so the row does not shift as you walk it. */
.near { display: flex; flex-wrap: wrap; gap: .45rem; align-items: baseline;
        font-size: .85rem; margin: .35rem 0 .6rem; }
.near a { color: var(--bar); text-decoration: none; border-bottom: 1px solid transparent; }
.near a:hover, .near a:focus-visible { border-bottom-color: currentColor; }
/* The ranking views' sort bar. The active key is text, not a link to the
   page you are already on — and it stays bold so the row reads as a state,
   not as a list of equal choices. */
.sortbar { display: flex; flex-wrap: wrap; gap: .55rem; align-items: baseline;
           font-size: .85rem; margin: .3rem 0 .2rem; }
.sortbar a { color: var(--bar); text-decoration: none; border-bottom: 1px solid transparent; }
.sortbar a:hover, .sortbar a:focus-visible { border-bottom-color: currentColor; }
.sortbar .on { font-weight: 600; }
/* The second line of a ranked row: what led in, what let out. Indented under
   the row it explains rather than beside it, so the columns above stay a
   table. */
.rwhy { margin: -.15rem 0 .45rem .4rem; font-size: .76rem; color: var(--muted); }
/* A ranked row is two lines of fixed total height, which is what lets the
   whole ranking be virtualised instead of capped at its first 300. */
.rank { padding: .1rem 0; }
.rank .rwhy { margin: 0 0 0 .4rem; }
/* Small multiples: one sparkline per area, each against its OWN peak. A
   shared scale would flatten every small area into a flat line, and the
   question these ask is the shape over time, not the size. */
.smalls { display: grid; grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr));
          gap: .6rem; margin: .6rem 0; }
.small { background: var(--card); border-radius: .4rem; padding: .45rem .55rem; }
.small .sk { display: flex; align-items: center; gap: .35rem; font-size: .78rem;
             white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.small .sk .dot { width: .5rem; height: .5rem; border-radius: 50%; flex-shrink: 0; }
.small .sn { font-size: .68rem; color: var(--muted); }
/* The report's month columns are hit by an invisible zone drawn OVER the
   stacked bars, so the shared opacity fade has nothing to fade. It tints
   instead — faint enough that the bars keep their colours through it, which
   a solid fill would take away. */
rect.mhit:hover { fill: var(--fg); fill-opacity: .09; }

/* The graph's picker and its clear button. The picker is one focus stop that
   holds the whole selection; the button only exists while something is
   picked, because "clear" with nothing to clear is furniture. */
.pick { display: flex; flex-wrap: wrap; align-items: baseline; gap: .4rem;
  font-size: .8rem; color: var(--muted); margin: .2rem 0 0; }
.pick select { font: inherit; color: var(--fg); background: var(--card);
  border: 1px solid var(--line); border-radius: .3rem; padding: .1rem .3rem;
  max-width: 100%; }
.clear { margin-left: .4rem; font: inherit; font-size: .78rem; color: var(--bar);
  background: none; border: 1px solid var(--line); border-radius: .3rem;
  padding: .02rem .45rem; cursor: pointer; }
.clear:hover, .clear:focus-visible { background: var(--card); }
.clear[hidden] { display: none; }

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
  <span><b>chain</b> {{.RabbitLen}}+ videos of one area, under {{.RabbitGap}} min apart —
    each one has its own page, ranked at #/holes</span>
  <span><b>night</b> started between {{.NightFrom}}:00 and {{.NightTo}}:00, local time</span>
  <span><b>held you</b> the share watched through, counted only over videos whose
    length is known — the rest are left out rather than counted as zero</span>
  <span><b>most unusual</b> a day's strongest percentile rank across views, chain
    depth, night share and spread; never a blend, so no invented weight sits
    next to a measured number</span>
</div>
</details>

<main id="view"></main>
<div id="tip" hidden></div>
`

// coreJS is everything every view shares: the payload, the small format
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
const pathNoteEl = document.querySelector("details.note");

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

// topicHash is the address of an area or of a subject inside it — the one
// place on the page that builds a link into the tree, so the report, the cards
// and the tree itself cannot drift apart on how a name becomes a URL. Names
// are URI-encoded because a subject may hold a slash. An area with no name is
// nowhere in the tree, and gets no link rather than a link into the root.
function topicHash(area, sub) {
  if (!area) return null;
  return "#/topics/" + encodeURIComponent(area) +
    (sub ? "/" + encodeURIComponent(sub) : "");
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
const DY_ED = 0, DY_VIEWS = 1, DY_AREA = 2, DY_FROM = 3, DY_TO = 4,
      DY_DUR = 5, DY_CHAINV = 6, DY_CHAINMAX = 7, DY_NIGHT = 8, DY_AREAN = 9,
      DY_THROUGH = 10, DY_EDGED = 11, DY_NEWCH = 12, DY_PEAK = 13, DY_AXIS = 14;
// The same order Go's peakAxes has: the axis travels as its index.
const PEAK_AXES = ["views", "chain", "night", "areas"];
const C_SESS = 0, C_FROM = 1, C_TO = 2, C_LEN = 3, C_AREA = 4, C_SPAN = 5, C_DUR = 6;

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

// chainsOf lists the chains of one sitting as GLOBAL indices into D.chains,
// oldest first — the order the session view draws them in, which is what
// makes the k in "#/session/<i>/chain/<k>" the one a reader counts from the
// top. Go already ships them that way; this only groups them.
const chainsBySess = new Map();
if (D.chains) D.chains.forEach((c, i) => {
  const list = chainsBySess.get(c[C_SESS]);
  if (list) list.push(i); else chainsBySess.set(c[C_SESS], [i]);
});
const chainsOf = si => chainsBySess.get(si) || [];

// chainRows returns the rows a chain covers, oldest first, so a caller reads
// it in the direction it was watched.
const chainRows = ci => {
  const c = D.chains[ci];
  return D.rows.slice(c[C_FROM], c[C_TO] + 1).reverse();
};

// chainDoor is the main-lane view the chain was entered FROM: the row just
// past its old end, still inside the same sitting. Null when the chain
// opened the sitting — then nothing on the timeline led into it.
function chainDoor(ci) {
  const c = D.chains[ci];
  const s = D.sess[c[C_SESS]];
  const last = s[S_ROW] + s[S_N]; // the sitting's oldest row
  for (let r = c[C_TO] + 1; r <= last; r++) {
    if (!isOverlap(D.rows[r])) return D.rows[r];
  }
  return null;
}

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
  // The topic a view carries is an address, so it travels as a link. Inside
  // the card, never the card itself: a card is one video, and a video has no
  // view of its own to lead to — while its topic has two, the tree and the
  // list cut down to it. The hash is built from encodeURIComponent output and
  // a fixed prefix, so it cannot carry a quote out of the attribute; the name
  // beside it still goes through esc(), which is rule two of this page.
  const tHash = topicHash(areaName(r[R_AREA]), D.subs[r[R_SUB]]);
  const tText = esc(areaName(r[R_AREA])) +
    (D.subs[r[R_SUB]] ? " / " + esc(D.subs[r[R_SUB]]) : "");
  bits.push(tHash
    ? '<a class="area" href="' + tHash + '">' + tText + "</a>"
    : '<span class="area">' + tText + "</span>");
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

// chainName is what a rabbit hole is called wherever it is listed. The
// label from the model, if this run had one, otherwise the plain facts —
// the page has to read the same with an LLM and without one, so the name is
// decoration and never structure.
const holeLabel = new Map();
if (D.holeLabels) for (const [ci, label] of D.holeLabels) holeLabel.set(ci, label);

function chainName(ci) {
  const c = D.chains[ci];
  return holeLabel.get(ci) || ("chain of " + c[C_LEN] + " · " + areaName(c[C_AREA]));
}

// chainFacts is the sentence that reads a chain backwards: how deep it went,
// how long it held, how many channels fed it, which door led in and how it
// ended. Everything but the door is a walk over the chain's own rows —
// nothing of this rides on the payload.
function chainFacts(ci) {
  const c = D.chains[ci];
  const rows = chainRows(ci);
  const chans = new Set();
  let held = 0, edged = 0, exit = "";
  for (const r of rows) {
    if (isOverlap(r)) continue;
    if (D.chans[r[R_CHAN]]) chans.add(r[R_CHAN]);
    const e = D.edges[r[R_EDGE]];
    if (e) { edged++; if (e === "through") held++; }
    exit = e || exit;
  }
  const door = chainDoor(ci);
  return {
    len: c[C_LEN], area: areaName(c[C_AREA]), span: c[C_SPAN], durS: c[C_DUR],
    chans: chans.size, held: held, edged: edged, exit: exit,
    door: door ? areaName(door[R_AREA]) : "",
  };
}

// chainLine is chainFacts as one line of prose: the reverse-algorithm
// sentence — where you fell in, and what let you back out.
function chainLine(ci) {
  const f = chainFacts(ci);
  const bits = [f.len + " videos", f.area];
  if (f.span > 0) bits.push("over " + dur(f.span));
  bits.push(f.chans === 1 ? "one channel" : f.chans + " channels");
  const tail = [];
  if (f.door) tail.push("entered from " + f.door);
  if (f.exit) tail.push("ended by " + EDGE_TEXT[f.exit]);
  return bits.join(" · ") + (tail.length ? " — " + tail.join(", ") : "");
}

function chainTip(ci) {
  return "<b>" + esc(chainName(ci)) + '</b><span class="m">' +
    esc(chainLine(ci)) + "</span>";
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

// NO_SUBJECT is what Go calls the empty subject bucket in the topic tree
// (NoSubject in watchpath.go). The same view carries the empty string on the
// timeline, so a link out of the tree has to fold the two names into one
// index — the tree and the list would otherwise disagree about a bucket that
// is one bucket.
const NO_SUBJECT = "(no subject)";

// listRows turns an area and a subject name into the row indices the list
// draws, and it is the only filter this view has: the address IS the state,
// so there is nothing to reset and nothing to get out of step with the hash.
//
// The session headers come along, so a filtered list is still a timeline and
// not a heap of cards — with the count of what actually matched under each,
// because a header describes the WHOLE sitting and would otherwise promise
// rows that are not there.
//
// The unfiltered case builds the identity index rather than taking a second
// path through the drawing below: one array of row numbers is cheap next to
// the payload it indexes, and two ways of finding row i is how the two drift.
function listRows(area, sub) {
  const out = { idx: [], hits: null };
  if (!area) {
    for (let i = 0; i < D.rows.length; i++) out.idx.push(i);
    return out;
  }
  out.hits = new Map();
  const ai = D.areas.indexOf(area);
  if (ai < 0) return out;
  let si = -1;
  if (sub) {
    si = D.subs.indexOf(sub);
    if (si < 0 && sub === NO_SUBJECT) si = 0;
    if (si < 0) return out;
  }
  let head = -1, n = 0;
  for (let i = 0; i < D.rows.length; i++) {
    const r = D.rows[i];
    if (r[0] === 0) { head = i; n = 0; continue; }
    if (r[R_AREA] !== ai) continue;
    if (si >= 0 && r[R_SUB] !== si) continue;
    if (!n && head >= 0) out.idx.push(head);
    out.idx.push(i);
    out.hits.set(head, ++n);
  }
  return out;
}

// virtualRows is the one virtual list on this page. Only the rows near the
// viewport exist in the DOM; a spacer of count*rowH gives the scrollbar the
// right length, and each row is placed absolutely at i*rowH.
//
// Extracted from the timeline so the two rankings can drop their caps: a
// list of the top 300 of 1068 rabbit holes is a list that stops answering
// exactly where the tail gets interesting, and a second copy of this loop
// is how the two would drift.
//
// key names the scroll position to remember. Coming back to a list from a
// row is a return, not a fresh visit — and an offset measured in one list
// means nothing in another, which is why it is keyed rather than shared.
const listScroll = new Map();

function virtualRows(root, count, rowH, rowEl, key) {
  const path = $("div"); path.id = "path";
  const spacer = $("div"); spacer.id = "spacer";
  path.appendChild(spacer);
  root.appendChild(path);
  spacer.style.height = (count * rowH) + "px";
  path.style.setProperty("--rh", rowH + "px");

  // Rows kept above and below the viewport. Generous on purpose: a scroll
  // event lands after the frame it belongs to, so without a buffer a fast
  // scroll shows empty space until the next draw catches up.
  const OVERSCAN = 14;
  let first = -1, last = -1;
  const live = new Map();

  function draw() {
    const top = scrollY - path.offsetTop;
    const from = Math.max(0, Math.floor(top / rowH) - OVERSCAN);
    const to = Math.min(count - 1, Math.ceil((top + innerHeight) / rowH) + OVERSCAN);
    if (from === first && to === last) return;
    for (const [i, el] of live) {
      if (i < from || i > to) { el.remove(); live.delete(i); }
    }
    for (let i = from; i <= to; i++) {
      if (live.has(i)) continue;
      const el = rowEl(i);
      el.classList.add("row");
      el.style.top = (i * rowH) + "px";
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
    listScroll.set(key, scrollY);
    if (queued) return;
    queued = true;
    requestAnimationFrame(() => { queued = false; draw(); });
  }
  addEventListener("scroll", schedule, { passive: true });
  addEventListener("resize", schedule);
  scrollTo(0, listScroll.get(key) || 0);
  draw();

  return () => {
    removeEventListener("scroll", schedule);
    removeEventListener("resize", schedule);
  };
}

function renderList(root, area, sub) {
  const sel = listRows(area, sub);
  const label = sub ? area + " / " + sub : area;
  // The key is the filter itself, so every cut keeps its own place in the
  // list and the whole list keeps the one it had.
  const key = sel.hits ? area + "\n" + (sub || "") : "";

  if (sel.hits) {
    root.appendChild($("p", "muted", sel.idx.length
      ? "Only the views of " + label + ", newest first, in the sittings they " +
        "were watched in. A sitting line still describes the whole sitting; the " +
        "count after it says how many of its videos are listed here."
      : "No view on this timeline carries " + label + "."));
  }

  function rowEl(i) {
    const ri = sel.idx[i];
    const r = D.rows[ri];
    const el = $("div");
    if (r[0] === 0) {
      const wrap = $("div", "sess");
      const when = $("span", "when", stamp(r[R_TS]));
      wrap.appendChild(hit(when, "#/session/" + rowToSession.get(ri), "open this sitting"));
      // A session row and a D.sess entry carry span, count and gap at the
      // same offsets, which is what lets one formatter serve both.
      const n = sel.hits ? sel.hits.get(ri) : 0;
      wrap.appendChild($("span", "gap", sessionLine(r) +
        (n ? " · " + n + " of them listed here" : "")));
      el.appendChild(wrap);
      return el;
    }
    if (isOverlap(r)) el.classList.add("lane2");
    el.appendChild(viewCard(r));
    return el;
  }

  return virtualRows(root, sel.idx.length, RH, rowEl, key);
}

// rowToSession maps a session header row back to its index in D.sess, which
// is what a click on the list has to hand to the router.
const rowToSession = new Map();
if (D.sess) D.sess.forEach((s, i) => rowToSession.set(s[S_ROW], i));

// ---- the router --------------------------------------------------------

// Navigation is location.hash and nothing else: the back button then walks
// the same path backwards for free, and a view is a link that can be shared.
let teardown = null;

function crumbs(trail, here) {
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
  // The topic tree and the flat list are siblings of the calendar zoom, not
  // steps inside it: both hold every view, cut a different way. They sit on
  // the far side of the bar, and the one you are already in drops out rather
  // than repeating what the trail just said.
  const sides = [
    { id: "topics", text: "topics", hash: "#/topics" },
    { id: "list", text: "all views (" + D.views.toLocaleString() + ")", hash: "#/list" },
  ];
  // The report is a sibling too — the same export summed instead of walked.
  // Only when the numbers actually travelled: a link to a view that is not on
  // the payload is a promise the page cannot keep.
  if (D.report) sides.splice(1, 0, { id: "report", text: "report", hash: "#/report" });
  for (const s of sides) {
    if (s.id === here) continue;
    const a = $("a", "alt", s.text);
    a.href = s.hash;
    crumbsEl.appendChild(a);
  }
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

  // The note above describes how a timeline is read: sittings, gaps, the
  // overlap legend. The report does none of that reading — it sums — and it
  // carries its own caveat about the hours being an upper bound. Two notes
  // opening with "Takeout logs when a video was STARTED" on one screen say it
  // once too often, so the timeline note stands down there.
  pathNoteEl.hidden = parts[0] === "report";

  if (parts[0] === "topics") {
    // The focus travels as names, not indices: an index would move whenever
    // the data is re-read, and a link into the tree should still land next
    // month. Names are URI-encoded because a subject may hold a slash.
    const area = parts[1] ? decodeURIComponent(parts[1]) : "";
    const sub = parts[2] ? decodeURIComponent(parts[2]) : "";
    const trail = [{ text: "overview", hash: "/" },
                   { text: "topics", hash: area ? "/topics" : null }];
    if (area) trail.push({ text: area, hash: sub ? "/topics/" + encodeURIComponent(area) : null });
    if (sub) trail.push({ text: sub });
    crumbs(trail, "topics");
    scrollTo(0, 0);
    teardown = renderClusters(viewEl, area, sub) || null;
    return;
  }
  if (parts[0] === "report") {
    crumbs([{ text: "overview", hash: "/" }, { text: "report" }], "report");
    scrollTo(0, 0);
    if (!D.report) { notFound(viewEl, "the report"); return; }
    teardown = renderReport(viewEl) || null;
    return;
  }
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
    // A chain is addressed by its ordinal INSIDE the sitting, counted from
    // the top of the diagram — an index into D.chains would be shorter but
    // would move whenever the export is re-read, and this link should still
    // land next month.
    const chains = chainsOf(si);
    let focusK = -1;
    if (parts[2] === "chain" && parts[3] !== undefined) {
      focusK = parseInt(parts[3], 10);
      if (!(focusK >= 0 && focusK < chains.length)) {
        crumbs([{ text: "overview", hash: "/" }, { text: date, hash: "/day/" + date },
                { text: clock(s[S_TS]) + " sitting", hash: "/session/" + si },
                { text: "chain " + parts[3] }]);
        notFound(viewEl, "that chain");
        return;
      }
    }
    const trail = [{ text: "overview", hash: "/" }, { text: date, hash: "/day/" + date }];
    if (focusK >= 0) {
      trail.push({ text: clock(s[S_TS]) + " sitting", hash: "/session/" + si });
      trail.push({ text: chainName(chains[focusK]) });
    } else {
      trail.push({ text: clock(s[S_TS]) + " sitting" });
    }
    crumbs(trail);
    scrollTo(0, 0);
    teardown = renderSession(viewEl, si, focusK) || null;
    return;
  }
  if (parts[0] === "holes") {
    // Sort key and area filter both live in the hash: re-sorting is a step
    // the back button can undo, and a sorted list is a link worth sharing.
    const sortId = parts[1] || "depth";
    const area = parts[2] ? decodeURIComponent(parts[2]) : "";
    crumbs([{ text: "overview", hash: "/" }, { text: "rabbit holes" }]);
    scrollTo(0, 0);
    if (!D.chains || !D.chains.length) { notFound(viewEl, "a rabbit hole"); return; }
    teardown = renderHoles(viewEl, sortId, area) || null;
    return;
  }
  if (parts[0] === "algo") {
    crumbs([{ text: "overview", hash: "/" }, { text: "the algorithm" }]);
    scrollTo(0, 0);
    teardown = renderAlgo(viewEl) || null;
    return;
  }
  if (parts[0] === "days") {
    crumbs([{ text: "overview", hash: "/" }, { text: "the days" }]);
    scrollTo(0, 0);
    if (!D.days || !D.days.length) { notFound(viewEl, "a day"); return; }
    teardown = renderDays(viewEl, parts[1] || "peak") || null;
    return;
  }
  if (parts[0] === "list") {
    // The list takes the same address the topic tree does, one level down:
    // #/list/<area>[/<subject>] is that topic's views on the timeline. Names
    // again, not indices, for the reason given above — and the filter lives
    // nowhere but in this hash, so the back button undoes it like any other
    // step and nothing on screen has to hold it.
    const area = parts[1] ? decodeURIComponent(parts[1]) : "";
    const sub = parts[2] ? decodeURIComponent(parts[2]) : "";
    if (area) {
      const trail = [{ text: "overview", hash: "/" },
                     { text: "topics", hash: "/topics" },
                     { text: area, hash: "/topics/" + encodeURIComponent(area) }];
      if (sub) {
        trail.push({ text: sub, hash: "/topics/" + encodeURIComponent(area) +
          "/" + encodeURIComponent(sub) });
      }
      trail.push({ text: "its views" });
      // No "here" for a filtered list: the unfiltered one then stays in the
      // bar on the right, which is the way back OUT of the filter and saves
      // it a control of its own.
      crumbs(trail);
      teardown = renderList(viewEl, area, sub) || null;
      return;
    }
    crumbs([{ text: "overview", hash: "/" }, { text: "all views" }], "list");
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
