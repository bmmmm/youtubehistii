// SPDX-License-Identifier: GPL-3.0-or-later

package report

// rankJS holds the ranking views: the rabbit holes, and later the days. They
// share one row idiom and one rule about where their state lives.
//
// The sort key rides in the HASH, not in a variable: "#/holes/span" is the
// same doctrine the filtered list follows — the address IS the state, so the
// back button undoes a re-sort like any other step, a sorted view can be
// shared, and nothing on screen has to be kept in sync with anything else.
const rankJS = `<script>
// ---- level 5: the rabbit holes, ranked ---------------------------------

// HOLE_SORTS is the whole vocabulary of #/holes. Each key is one honest
// measurement over the chain — no weighted blend, because a blend needs
// invented weights and those look exactly like the measured numbers beside
// them. The reader picks which question is being asked.
const HOLE_SORTS = [
  { id: "depth",  text: "depth",    of: f => f.len,
    note: "how many videos in a row on one area" },
  { id: "span",   text: "span",     of: f => f.span,
    note: "how long the run held, first start to last" },
  { id: "hours",  text: "length",   of: f => f.durS,
    note: "the videos' full length added up — an upper bound" },
  { id: "chans",  text: "channels", of: f => f.chans,
    note: "how many different channels fed the same run" },
  { id: "held",   text: "held you", of: f => (f.edged ? f.held / f.edged : -1),
    note: "the share of the run watched through to the end" },
  { id: "date",   text: "newest",   of: f => 0,
    note: "most recent first — the chains are already in that order" },
];

// RANK_H is the height of one ranked row — two lines, the facts and the
// sentence under them. Fixed, because that is what lets the list be
// virtualised: row i sits at i*RANK_H with nothing to measure.
const RANK_H = {{.RankHeight}};

// holeFacts caches chainFacts per chain: a sort touches every chain, and the
// facts are a walk over its rows. One pass, then sorting is free.
let holeFactCache = null;
function holeFacts() {
  if (!holeFactCache) {
    holeFactCache = D.chains.map((c, i) => {
      const f = chainFacts(i);
      f.ci = i;
      f.ts = D.rows[c[C_FROM]][R_TS];
      return f;
    });
  }
  return holeFactCache;
}

function renderHoles(root, sortId, areaName_) {
  const sort = HOLE_SORTS.find(s => s.id === sortId) || HOLE_SORTS[0];
  const areaIdx = areaName_ ? D.areas.indexOf(areaName_) : -1;

  root.appendChild($("h2", null, "rabbit holes"));
  root.appendChild($("p", "muted",
    "A rabbit hole is " + {{.RabbitLen}} + " or more videos on one area, back to back, " +
    "no more than " + {{.RabbitGap}} + " minutes between starts. Background videos are " +
    "stepped over rather than breaking the run. " + D.chains.length.toLocaleString() +
    " of them are on this timeline; each row says where it was entered from and " +
    "what ended it."));

  // The sort bar: real anchors, so middle-click and the keyboard work with
  // no code, and the active one is text rather than a link to itself.
  const bar = $("p", "sortbar");
  bar.appendChild($("span", "muted", "sort by"));
  for (const s of HOLE_SORTS) {
    if (s.id === sort.id) {
      bar.appendChild($("span", "on", s.text));
      continue;
    }
    const a = $("a", null, s.text);
    a.href = "#/holes/" + s.id + (areaName_ ? "/" + encodeURIComponent(areaName_) : "");
    bar.appendChild(a);
  }
  root.appendChild(bar);
  root.appendChild($("p", "muted", sort.note + "."));

  // The area filter is a select, not a row of links: eleven areas would be
  // eleven more tab stops in front of the list itself.
  const pick = $("p", "pick");
  const sel = $("select");
  const all = $("option", null, "every area");
  all.value = "";
  sel.appendChild(all);
  const seen = new Set(D.chains.map(c => c[C_AREA]));
  [...seen].sort((a, b) => areaName(a).localeCompare(areaName(b))).forEach(ai => {
    const o = $("option", null, areaName(ai));
    o.value = areaName(ai);
    if (ai === areaIdx) o.selected = true;
    sel.appendChild(o);
  });
  sel.addEventListener("change", () => {
    go("#/holes/" + sort.id + (sel.value ? "/" + encodeURIComponent(sel.value) : ""));
  });
  const lab = $("label", null, "area: ");
  lab.appendChild(sel);
  pick.appendChild(lab);
  root.appendChild(pick);

  let rows = holeFacts();
  if (areaIdx >= 0) rows = rows.filter(f => D.chains[f.ci][C_AREA] === areaIdx);
  if (!rows.length) {
    root.appendChild($("p", "muted", "No rabbit hole on this timeline is in that area."));
    return;
  }
  // Chains already run newest first, so "date" needs no comparator at all
  // and every other key keeps that order as its tie-break — the newest of
  // two equal chains is the one the reader means.
  const sorted = rows.slice();
  if (sort.id !== "date") {
    sorted.sort((a, b) => sort.of(b) - sort.of(a) || a.ci - b.ci);
  }
  const top = Math.max(1, sort.id === "date" ? 1 : sort.of(sorted[0]));
  root.appendChild($("p", "muted",
    sorted.length.toLocaleString() + " of them, all of them — scroll."));

  const panel = $("div", "panel");
  root.appendChild(panel);
  // Virtualised, so the whole ranking is here rather than its first 300: the
  // tail of a rabbit-hole list is where the surprising ones are.
  return virtualRows(panel, sorted.length, RANK_H, i => {
    const f = sorted[i];
    const c = D.chains[f.ci];
    const k = chainsOf(c[C_SESS]).indexOf(f.ci);
    const hash = "#/session/" + c[C_SESS] + "/chain/" + k;
    const wrap = $("div", "rank");
    const line = $("div", "rline");
    const a = $("a", "rrow", null);
    a.href = hash;
    a.appendChild($("span", "rname", chainName(f.ci)));
    a.appendChild($("span", "rnum", stamp(f.ts)));
    a.appendChild($("span", "rnum", f.len + " videos"));
    a.appendChild($("span", "rnum", f.span > 0 ? dur(f.span) : ""));
    const box = $("div", "rbar");
    const fill = $("div", "rfill");
    fill.style.width = Math.min(100, Math.max(2, 100 * Math.max(0, sort.of(f)) / top)).toFixed(1) + "%";
    fill.style.background = areaColor(c[C_AREA]);
    box.appendChild(fill);
    a.appendChild(box);
    line.appendChild(a);
    line.appendChild(goArrow(hash, "open " + chainName(f.ci)));
    wrap.appendChild(line);
    // The second line is the reverse-algorithm sentence: which topic the
    // chain was fallen into from, and what finally let the reader out.
    const why = [];
    if (f.door) why.push("entered from " + f.door);
    why.push(f.chans === 1 ? "one channel" : f.chans + " channels");
    if (f.exit) why.push("ended by " + EDGE_TEXT[f.exit]);
    wrap.appendChild($("p", "rwhy", why.join(" · ")));
    return wrap;
  }, "holes\n" + sort.id + "\n" + (areaName_ || ""));
}
</script>
`

// rankDaysJS is the second ranking view. It sits in its own const only so the
// file reads as two views rather than one long one; both are spliced in
// together.
const rankDaysJS = `<script>
// ---- level 5b: the days, ranked ----------------------------------------

// DAY_SORTS mirrors HOLE_SORTS: one honest measurement per key. "peak" is
// the default and the only composite one — and it composes by MAX, not by a
// weighted sum, so no invented number ever sits next to a measured one.
const DAY_SORTS = [
  { id: "peak",   text: "most unusual", of: d => d[DY_PEAK],
    note: "each day's strongest rank across views, chain depth, night share and spread" },
  { id: "views",  text: "views",        of: d => d[DY_VIEWS],
    note: "how many videos were started that day" },
  { id: "chain",  text: "deepest chain", of: d => d[DY_CHAINMAX],
    note: "the longest run on one area that started that day" },
  { id: "night",  text: "night",        of: d => (d[DY_VIEWS] ? d[DY_NIGHT] / d[DY_VIEWS] : 0),
    note: "the share of the day's videos started between " + {{.NightFrom}} + ":00 and " + {{.NightTo}} + ":00" },
  { id: "hours",  text: "length",       of: d => d[DY_DUR],
    note: "the videos' full length added up — an upper bound" },
  { id: "areas",  text: "spread",       of: d => d[DY_AREAN],
    note: "how many different areas the day touched" },
  { id: "held",   text: "held you",     of: d => (d[DY_EDGED] ? d[DY_THROUGH] / d[DY_EDGED] : -1),
    note: "the share of the day watched through to the end" },
  { id: "new",    text: "new channels", of: d => d[DY_NEWCH],
    note: "channels seen for the first time ever that day" },
  { id: "date",   text: "newest",       of: d => d[DY_ED],
    note: "most recent first" },
];

// peakLine says what a day's rank MEANS, in the distribution's own words.
// "top 0.4 % by chain depth" can be checked against the data; a score of 87
// cannot.
function peakLine(d) {
  const axis = PEAK_AXES[d[DY_AXIS]];
  if (!axis || !D.days.length) return "";
  const share = (1000 - d[DY_PEAK]) / 10;
  const by = { views: "views", chain: "chain depth", night: "night share", areas: "spread" }[axis];
  return "top " + (share < 1 ? share.toFixed(1) : Math.round(share)) + "% by " + by;
}

function renderDays(root, sortId) {
  const sort = DAY_SORTS.find(s => s.id === sortId) || DAY_SORTS[0];
  root.appendChild($("h2", null, "the days, ranked"));
  root.appendChild($("p", "muted",
    D.days.length.toLocaleString() + " days carried a sitting. The calendar shows " +
    "them in place; this shows them in order — pick what makes a day worth opening."));

  const bar = $("p", "sortbar");
  bar.appendChild($("span", "muted", "sort by"));
  for (const s of DAY_SORTS) {
    if (s.id === sort.id) { bar.appendChild($("span", "on", s.text)); continue; }
    const a = $("a", null, s.text);
    a.href = "#/days/" + s.id;
    bar.appendChild(a);
  }
  root.appendChild(bar);
  root.appendChild($("p", "muted", sort.note + "."));

  // Days run oldest first on the payload; every key wants the newest of two
  // equal days on top, so the base order is reversed once here.
  const rows = D.days.map((d, i) => [d, i]).reverse();
  if (sort.id !== "date") rows.sort((a, b) => sort.of(b[0]) - sort.of(a[0]) || b[1] - a[1]);
  const top = Math.max(1e-9, sort.of(rows[0][0]));
  root.appendChild($("p", "muted",
    rows.length.toLocaleString() + " of them, all of them — scroll."));

  const panel = $("div", "panel");
  root.appendChild(panel);
  return virtualRows(panel, rows.length, RANK_H, ri => {
    const [d, di] = rows[ri];
    const date = dayDate(d[DY_ED]);
    const wrap = $("div", "rank");
    const line = $("div", "rline");
    const a = $("a", "rrow", null);
    a.href = "#/day/" + date;
    a.appendChild($("span", "rname", dayLabel(d[DY_ED])));
    a.appendChild($("span", "rnum", d[DY_VIEWS].toLocaleString() + " views"));
    a.appendChild($("span", "rnum", d[DY_DUR] > 0 ? upto(d[DY_DUR]) : ""));
    const box = $("div", "rbar");
    const fill = $("div", "rfill");
    fill.style.width = Math.min(100, Math.max(2, 100 * Math.max(0, sort.of(d)) / top)).toFixed(1) + "%";
    if (d[DY_AREA] >= 0) fill.style.background = areaColor(d[DY_AREA]);
    box.appendChild(fill);
    a.appendChild(box);
    line.appendChild(a);
    line.appendChild(goArrow("#/day/" + date, "open " + date));
    wrap.appendChild(line);

    const why = [peakLine(d)];
    if (d[DY_CHAINMAX] > 0) why.push("longest chain " + d[DY_CHAINMAX]);
    if (d[DY_NIGHT] > 0) why.push(Math.round(100 * d[DY_NIGHT] / d[DY_VIEWS]) + "% after " + {{.NightFrom}});
    why.push(d[DY_AREAN] === 1 ? "one area" : d[DY_AREAN] + " areas");
    if (d[DY_NEWCH] > 0) why.push(d[DY_NEWCH] + " new " + (d[DY_NEWCH] === 1 ? "channel" : "channels"));
    wrap.appendChild($("p", "rwhy", why.filter(Boolean).join(" · ")));
    return wrap;
  }, "days\n" + sort.id);
}
</script>
`
