// SPDX-License-Identifier: GPL-3.0-or-later

package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"html/template"
	"time"
)

// Row heights, shared between Go and the page: every row is the same height
// so the virtual list can place row i at i*rowHeight without measuring
// anything. Variable heights would mean prefix sums and a binary search for
// the first visible row — real code for a gain nobody asked for. The price is
// single-line titles, which is what the ellipsis is for.
const rowHeightPx = 56

// Row type tags, first element of every serialized row.
const (
	rowSession = 0
	rowView    = 1
)

// pathData is what the page gets: lookup tables plus rows of numbers. Titles
// dominate the payload, everything else is an index — that keeps 35k views
// near 4 MB instead of the 7 MB an array of objects would cost.
type pathData struct {
	T0       int64      `json:"t0"` // unix seconds of the oldest view
	Chans    []string   `json:"chans"`
	Areas    []string   `json:"areas"`
	AreaHues []int      `json:"areaHues"` // parallel to Areas
	Subs     []string   `json:"subs"`
	Modes    []string   `json:"modes"`
	Edges    []string   `json:"edges"`
	Rows     [][]any    `json:"rows"`
	Sessions int        `json:"sessions"`
	Views    int        `json:"views"`
	Dropped  int        `json:"dropped"`
	Span     [2]float64 `json:"span"` // unix seconds, oldest and newest
}

// intern hands out stable indices for repeated strings.
type intern struct {
	idx  map[string]int
	list []string
}

func newIntern(seed ...string) *intern {
	in := &intern{idx: map[string]int{}}
	for _, s := range seed {
		in.get(s)
	}
	return in
}

func (in *intern) get(s string) int {
	if i, ok := in.idx[s]; ok {
		return i
	}
	i := len(in.list)
	in.idx[s] = i
	in.list = append(in.list, s)
	return i
}

// areaHue derives a stable colour from the area name. Hashing beats a hand
// written palette here: the area list is YouTube's and can grow, and a name
// always lands on the same hue without anyone maintaining a mapping. The
// saturation and lightness stay in CSS so light and dark can differ.
func areaHue(area string) int {
	h := fnv.New32a()
	h.Write([]byte(area))
	return int(h.Sum32() % 360)
}

// buildPathData flattens the path into rows. The row list is built in Go on
// purpose: the page then owns nothing but scroll positions, and every rule
// that decides what a row MEANS stays in tested code.
func buildPathData(p *Path) *pathData {
	d := &pathData{
		Sessions: len(p.Sessions),
		Views:    p.Views,
		Dropped:  p.Dropped,
	}
	if !p.From.IsZero() {
		d.T0 = p.From.Unix()
		d.Span = [2]float64{float64(p.From.Unix()), float64(p.To.Unix())}
	}
	chans := newIntern()
	areas := newIntern()
	subs := newIntern("") // index 0 is "no sub"
	modes := newIntern(ModeOrder...)
	edges := newIntern("", EdgeThrough, EdgeMost, EdgeSkipped)

	for _, s := range p.Sessions {
		// A session row carries what the separator has to say: when it
		// started, how long it ran, how many videos, and the silence before it.
		d.Rows = append(d.Rows, []any{
			rowSession,
			s.Start.Unix() - d.T0,
			int(s.End.Sub(s.Start).Seconds()),
			len(s.Views),
			int(s.GapBefore.Seconds()),
		})
		for _, v := range s.Views {
			flags := 0
			if v.Overlap {
				flags |= 1
			}
			if v.Rabbit {
				flags |= 2
			}
			d.Rows = append(d.Rows, []any{
				rowView,
				v.WatchedAt.Unix() - d.T0,
				v.DurationS,
				areas.get(v.Area),
				subs.get(v.Sub),
				chans.get(v.Channel),
				modes.get(v.Mode),
				edges.get(v.Edge),
				v.GapS,
				flags,
				v.Title,
			})
		}
	}
	d.Chans, d.Areas, d.Subs, d.Modes, d.Edges = chans.list, areas.list, subs.list, modes.list, edges.list
	for _, a := range d.Areas {
		d.AreaHues = append(d.AreaHues, areaHue(a))
	}
	return d
}

// RenderWatchPath produces the self-contained timeline page — same rules as
// RenderHTML: system fonts, no external assets, light/dark by preference.
func RenderWatchPath(p *Path, generated time.Time) ([]byte, error) {
	data := buildPathData(p)
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	// json.Marshal escapes <, > and & by default, so a video title can never
	// close the script element it rides in. That escaping is what makes
	// template.JS safe here; do not switch the encoder to SetEscapeHTML(false).
	tpl, err := template.New("watchpath").Funcs(template.FuncMap{
		"date": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.Format("2006-01-02")
		},
	}).Parse(watchPathTpl)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	err = tpl.Execute(&buf, map[string]any{
		"Data":       template.JS(raw),
		"P":          p,
		"RowHeight":  rowHeightPx,
		"Generated":  generated.Format("2006-01-02 15:04"),
		"SessionGap": fmt.Sprintf("%.0f", sessionGap.Minutes()),
		"LongVideo":  fmt.Sprintf("%d", longVideoS/60),
		"RabbitLen":  fmt.Sprintf("%d", rabbitMinLen),
		"RabbitGap":  fmt.Sprintf("%.0f", rabbitMaxGap.Minutes()),
	})
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

const watchPathTpl = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>youtubehistii watch path</title>
<style>
:root {
  --bg: #ffffff; --fg: #1a1a1a; --muted: #666; --line: #e2e2e2;
  --bar: #4a7dbd; --card: #f6f6f6; --card2: #eeeeee;
  --consume: #d98a3d; --learn: #4c9e6b; --mixed: #8a6fb8; --unclear: #9a9a9a;
  --area-sat: 55%; --area-lum: 42%;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #16181c; --fg: #e6e6e6; --muted: #9a9a9a; --line: #33363c;
    --bar: #6b9fd8; --card: #1f2228; --card2: #24272e;
    --consume: #e0a468; --learn: #6dbb8a; --mixed: #a68fd0; --unclear: #7a7a7a;
    --area-sat: 50%; --area-lum: 62%;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0 auto; padding: 2rem 1rem 4rem; max-width: 62rem;
  background: var(--bg); color: var(--fg);
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  line-height: 1.5;
}
h1 { font-size: 1.6rem; margin-bottom: .2rem; }
.muted { color: var(--muted); font-size: .85rem; }
.note { background: var(--card); border-left: 3px solid var(--bar); padding: .6rem .9rem;
  border-radius: .3rem; font-size: .85rem; margin: 1rem 0; }
.legend { display: flex; flex-wrap: wrap; gap: .8rem; font-size: .78rem; color: var(--muted); margin: .8rem 0; }
.legend b { font-weight: 600; color: var(--fg); }

/* The virtual list: one spacer carries the full height, only the visible
   rows exist in the DOM. Every row is exactly --rh tall. */
#path { position: relative; border-left: 2px solid var(--line); margin-top: 1.5rem; }
#spacer { width: 1px; }
.row { position: absolute; left: 0; right: 0; height: var(--rh); padding: .25rem 0 .25rem .9rem; }
.card { height: 100%; background: var(--card); border-radius: .4rem; padding: .3rem .6rem;
  border-left: 3px solid transparent; overflow: hidden; }
.card.rabbit { border-left-color: var(--area); background: var(--card2); }
.row.lane2 { padding-left: 3rem; }
.row.lane2 .card { background: none; border: 1px dashed var(--line); }
.l1 { display: flex; gap: .5rem; align-items: baseline; }
.dot { width: .55rem; height: .55rem; border-radius: 50%; background: var(--area); flex-shrink: 0; }
.title { flex: 1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-size: .88rem; }
.clock { font-variant-numeric: tabular-nums; font-size: .75rem; color: var(--muted); flex-shrink: 0; }
.l2 { font-size: .72rem; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.area { color: var(--area); }
.mode { display: inline-block; padding: 0 .4rem; border-radius: .6rem; font-size: .68rem; color: #fff; }
.mode.consume { background: var(--consume); } .mode.learn { background: var(--learn); }
.mode.mixed { background: var(--mixed); } .mode.unclear { background: var(--unclear); }
.sess { height: 100%; display: flex; align-items: center; gap: .6rem;
  border-top: 1px solid var(--line); font-size: .8rem; }
.sess .when { font-weight: 600; }
.sess .gap { color: var(--muted); }
.edge { color: var(--muted); }
.edge.skipped { color: var(--consume); }
.ov { border: 1px solid var(--line); border-radius: .6rem; padding: 0 .35rem; font-size: .68rem; }
</style>
</head>
<body>
<h1>watch path</h1>
<p class="muted">generated {{.Generated}} · {{date .P.From}} … {{date .P.To}} ·
{{.P.Views}} views in {{len .P.Sessions}} sessions · newest first</p>

<p class="note"><b>Takeout logs when a video was STARTED — nothing else.</b>
No end, no watch time, no device. Everything on this page is derived from the
gap to the next start, and every gap has two readings: the video was abandoned,
or it kept playing while something else was started. The labels below mark
what the data suggests; none of them is a fact.
{{if .P.Dropped}} {{.P.Dropped}} views carry no timestamp and are not on this
timeline.{{end}}</p>

<div class="legend">
  <span><b>session</b> a new one starts after {{.SessionGap}} min of silence</span>
  <span><b>watched through</b> the gap was at least the video's length</span>
  <span><b>most of it</b> over half</span>
  <span><b>moved on</b> under half</span>
  <span><b>overlap suspected</b> started while a video of {{.LongVideo}} min or more
    from another area was still running — parallel, or simply abandoned; the export cannot tell</span>
  <span><b>coloured edge</b> {{.RabbitLen}}+ videos of one area, under {{.RabbitGap}} min apart</span>
</div>

<div id="path"><div id="spacer"></div></div>
<p class="muted" id="empty" hidden>Nothing to show — run "classify" first.</p>

<script>
// Assigned in a plain script element on purpose: html/template only treats
// this as JavaScript here, and json.Marshal's default escaping of < > &
// means no title can close the element.
const D = {{.Data}};
const RH = {{.RowHeight}};
const path = document.getElementById("path");
const spacer = document.getElementById("spacer");

if (!D.rows.length) {
  document.getElementById("empty").hidden = false;
}
spacer.style.height = (D.rows.length * RH) + "px";
path.style.setProperty("--rh", RH + "px");

const pad = n => String(n).padStart(2, "0");
const clock = ts => {
  const d = new Date((D.t0 + ts) * 1000);
  return pad(d.getHours()) + ":" + pad(d.getMinutes());
};
const stamp = ts => {
  const d = new Date((D.t0 + ts) * 1000);
  return d.toLocaleDateString(undefined, { weekday: "short", year: "numeric", month: "short", day: "numeric" })
    + ", " + clock(ts);
};
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

const EDGE_TEXT = { through: "watched through", most: "most of it", skipped: "moved on" };

function renderRow(r, i) {
  const el = document.createElement("div");
  el.className = "row";
  el.style.top = (i * RH) + "px";
  if (r[0] === 0) {
    const [, ts, span, n, gapBefore] = r;
    el.innerHTML = '<div class="sess"><span class="when"></span>' +
      '<span class="gap"></span></div>';
    el.querySelector(".when").textContent = stamp(ts);
    el.querySelector(".gap").textContent =
      n + (n === 1 ? " video" : " videos") + (span > 0 ? " over " + dur(span) : "") +
      (gapBefore > 0 ? " · " + dur(gapBefore) + " after the previous session" : "");
    return el;
  }
  const [, ts, duration, ai, si, ci, mi, ei, gap, flags, title] = r;
  const overlap = (flags & 1) !== 0, rabbit = (flags & 2) !== 0;
  el.style.setProperty("--area", "hsl(" + D.areaHues[ai] + " var(--area-sat) var(--area-lum))");
  if (overlap) el.classList.add("lane2");

  const card = document.createElement("div");
  card.className = "card" + (rabbit ? " rabbit" : "");

  const l1 = document.createElement("div");
  l1.className = "l1";
  l1.innerHTML = '<span class="dot"></span><span class="title"></span><span class="clock"></span>';
  l1.querySelector(".title").textContent = title || "(no title)";
  l1.querySelector(".clock").textContent = clock(ts);

  const l2 = document.createElement("div");
  l2.className = "l2";
  const bits = [];
  bits.push('<span class="area">' + esc(D.areas[ai] || "unclear") +
    (D.subs[si] ? " / " + esc(D.subs[si]) : "") + "</span>");
  if (D.chans[ci]) bits.push(esc(D.chans[ci]));
  if (duration > 0) bits.push(clip(duration));
  bits.push('<span class="mode ' + D.modes[mi] + '">' + D.modes[mi] + "</span>");
  if (overlap) bits.push('<span class="ov">overlap suspected</span>');
  const edge = D.edges[ei];
  if (edge) bits.push('<span class="edge ' + edge + '">↓ ' + EDGE_TEXT[edge] +
    (gap > 0 ? " after " + dur(gap) : "") + "</span>");
  l2.innerHTML = bits.join(" · ");

  card.appendChild(l1);
  card.appendChild(l2);
  el.appendChild(card);
  return el;
}

// Titles and channel names go through textContent; only the small labels are
// built as markup, so this escape covers exactly those two interpolations.
function esc(s) {
  return s.replace(/[&<>"]/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}

// Rows kept above and below the viewport. Generous on purpose: a scroll
// event lands after the frame it belongs to, so without a buffer a fast
// scroll shows empty space until the next draw catches up.
const OVERSCAN = 14;
let first = -1, last = -1;
const live = new Map();

function draw() {
  const top = window.scrollY - path.offsetTop;
  const from = Math.max(0, Math.floor(top / RH) - OVERSCAN);
  const to = Math.min(D.rows.length - 1, Math.ceil((top + window.innerHeight) / RH) + OVERSCAN);
  if (from === first && to === last) return;
  for (const [i, el] of live) {
    if (i < from || i > to) { el.remove(); live.delete(i); }
  }
  for (let i = from; i <= to; i++) {
    if (live.has(i)) continue;
    const el = renderRow(D.rows[i], i);
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
  if (queued) return;
  queued = true;
  requestAnimationFrame(() => { queued = false; draw(); });
}

addEventListener("scroll", schedule, { passive: true });
addEventListener("resize", schedule);
draw();
</script>
</body>
</html>
`
