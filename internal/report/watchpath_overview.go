// SPDX-License-Identifier: GPL-3.0-or-later

package report

// overviewJS draws level 1: the calendar heatmap over the whole span, the
// transition graph between areas, and the headline numbers. See the shell in
// watchpath_page.go for the helpers it may use and the two rules it must obey.
const overviewJS = `<script>
// ---- level 1: the overview ---------------------------------------------

// A day is a 10 px square with a 2 px gutter, so a week is 12 px wide and a
// whole year needs under 700 px — wide enough to aim at with a mouse, narrow
// enough that the page itself never has to scroll sideways.
const CELL = 10, GAP = 2, STEP = CELL + GAP;
const CAL_X = 36, CAL_TOP = 18, BLOCK_H = CAL_TOP + 7 * STEP + 12;

// Past this many arcs the graph stops being readable and starts being
// texture. The cap is printed next to the drawing rather than hidden, so the
// picture never claims to be the whole list.
const ARC_MAX = 60;

// Ring geometry of the transition graph. Fixed numbers, scaled by the
// viewBox: the layout has to be the same on every screen and every reload,
// which is also why nothing here is force-directed.
const G_W = 900, G_H = 520, G_CX = G_W / 2, G_CY = G_H / 2, G_RING = 178;

function renderOverview(root) {
  const st = D.stats || {};
  const days = D.days || [];

  // The ways in come first, before the numbers: the other views used to be
  // reachable only through a word in the corner, and a view nobody finds is a
  // view that does not exist.
  root.appendChild(introCards(st));
  root.appendChild(statTiles(st, days));
  root.appendChild($("p", "muted",
    "Below, the same views cut by time: a calendar of every day, and what followed what between the areas. A cell opens its day, a day lists the sittings that began on it, and a sitting shows every video in the order it was started."));

  if (!days.length) {
    root.appendChild($("p", "muted", "No day on this timeline carries a sitting."));
    return;
  }

  // The calendar and the graph are one instrument: picking an area in the
  // graph is a question about the calendar, so the graph is handed the
  // calendar and the selection is the only state between them.
  const cal = buildCalendar(days);
  root.appendChild(cal.panel);
  root.appendChild(buildGraph(cal));
}

// ---- the numbers -------------------------------------------------------

function tile(k, v, s, href) {
  const el = $(href ? "a" : "div", "tile");
  if (href) el.href = href;
  el.appendChild($("span", "k", k));
  el.appendChild($("span", "v", v));
  if (s) el.appendChild($("span", "s", s));
  return el;
}

// sessDate names the calendar day a sitting belongs to by going through
// D.days, so the day rule — a sitting counts where it began — holds here too
// instead of being re-derived from a timestamp.
function sessDate(si) {
  const s = D.sess && D.sess[si];
  if (!s) return "";
  const d = D.days && D.days[s[S_DAY]];
  return d ? dayDate(d[DY_ED]) : "";
}

function statTiles(st, days) {
  const t = $("div", "tiles");
  const n = st.views || 0;

  // The views tile is the way into the topic tree: the calendar answers when,
  // the tree answers what, and both start from the same number.
  t.appendChild(tile("views", n.toLocaleString(),
    days.length.toLocaleString() + " days carried a sitting · open the topic tree",
    "#/topics"));
  t.appendChild(tile("sittings", (st.sessions || 0).toLocaleString(),
    st.sessions ? (n / st.sessions).toFixed(1) + " videos each" : ""));
  // Never "hours watched": this is the sum of full video lengths, and the
  // export cannot say that a single one of them ran to its end.
  t.appendChild(tile("hours at most", Math.round(st.hoursUpper || 0).toLocaleString(),
    "if every video had run to its end"));

  // An index of -1 means the path has no such thing; the tile then says so
  // rather than linking into nowhere.
  const ls = st.longestSess;
  t.appendChild(ls >= 0
    ? tile("longest sitting", st.longestSessN + " videos",
      sessDate(ls) + (st.longestSessS > 0 ? " · " + dur(st.longestSessS) : ""),
      "#/session/" + ls)
    : tile("longest sitting", "—", ""));

  // The deepest chain names a CHAIN now, not just the sitting that held it,
  // so the tile opens that exact run — and says how many others there are,
  // because "deepest" only means something against the rest of them.
  const dc = st.deepestChain;
  if (dc >= 0) {
    const c = D.chains[dc];
    const k = chainsOf(c[C_SESS]).indexOf(dc);
    t.appendChild(tile("deepest chain", c[C_LEN] + " videos",
      sessDate(c[C_SESS]) + " · one of " + D.chains.length.toLocaleString() + " rabbit holes",
      "#/session/" + c[C_SESS] + "/chain/" + k));
  } else {
    t.appendChild(tile("deepest chain", "—", ""));
  }

  t.appendChild(tile("overlap suspected",
    n ? Math.round(100 * (st.overlapViews || 0) / n) + "%" : "—",
    "suspected, not measured"));

  const bd = st.busiestDay;
  const bday = bd >= 0 ? days[bd] : null;
  t.appendChild(bday
    ? tile("busiest day", st.busiestDayN + " views", dayLabel(bday[DY_ED]),
      "#/day/" + dayDate(bday[DY_ED]))
    : tile("busiest day", "—", ""));

  return t;
}

// ---- the calendar ------------------------------------------------------

// buildCalendar draws one block per year of the span. Every grid coordinate
// comes out of the epoch day, never out of a Date read in local time: the day
// numbers were fixed in Go from the local calendar date, and re-deriving a
// weekday from a local Date would slide cells into the neighbouring week for
// anyone east or west of the machine that generated the page.
function buildCalendar(days) {
  const panel = $("div", "panel");
  panel.appendChild($("h2", null, "the calendar"));
  const rule = $("p", "muted",
    "One cell is one day. The hue is the area most of that day's main-lane views belonged to; what the opacity means is yours to pick below. A sitting counts on the day it BEGAN, so an evening that runs past midnight stays on that evening.");
  const filterNote = $("span");
  rule.appendChild(filterNote);
  panel.appendChild(rule);

  // The strongest day of the WHOLE span sets the scale, so the year blocks
  // stay comparable with each other instead of each one being its own record.
  // Keyed to the INDEX, not to the day: a cell has to be able to say which
  // entry of D.days it is, because that is the coordinate a transition's day
  // set is expressed in.
  let maxV = 1;
  const byDay = new Map();
  days.forEach((d, i) => {
    byDay.set(d[DY_ED], i);
    if (d[DY_VIEWS] > maxV) maxV = d[DY_VIEWS];
  });

  const y0 = +dayDate(days[0][DY_ED]).slice(0, 4);
  const y1 = +dayDate(days[days.length - 1][DY_ED]).slice(0, 4);

  const s = svg("svg", { role: "img" });
  const ttl = svg("title");
  ttl.textContent = "calendar of watched days, " + y0 + " to " + y1;
  s.appendChild(ttl);

  const cells = [];
  let width = 0;

  for (let y = y0; y <= y1; y++) {
    const top = (y - y0) * BLOCK_H;
    const yearStart = epochOf(y + "-01-01");
    const yearEnd = epochOf((y + 1) + "-01-01") - 1;
    // Column 0 is the week the year starts in, Monday first.
    const aligned = yearStart - ((dayWeekday(yearStart) + 6) % 7);

    const yl = svg("text", { x: 0, y: top + 11, "font-size": 11, "font-weight": 600 });
    yl.textContent = y;
    s.appendChild(yl);

    const wd = ["Mon", "", "Wed", "", "Fri", "", ""];
    for (let r = 0; r < 7; r++) {
      if (!wd[r]) continue;
      const t = svg("text", { class: "m", x: 0, y: top + CAL_TOP + r * STEP + 9, "font-size": 9 });
      t.textContent = wd[r];
      s.appendChild(t);
    }

    for (let m = 0; m < 12; m++) {
      const ed = epochOf(y + "-" + pad(m + 1) + "-01");
      const t = svg("text", {
        class: "m", x: CAL_X + Math.floor((ed - aligned) / 7) * STEP,
        y: top + 11, "font-size": 9,
      });
      t.textContent = dayAt(ed).toLocaleDateString(undefined, { month: "short", timeZone: "UTC" });
      s.appendChild(t);
    }

    // Every day of the year gets a cell, including the years with no sitting
    // at all: a calendar that skips its empty stretches lies about the axis.
    for (let ed = yearStart; ed <= yearEnd; ed++) {
      const x = CAL_X + Math.floor((ed - aligned) / 7) * STEP;
      const cy = top + CAL_TOP + ((dayWeekday(ed) + 6) % 7) * STEP;
      if (x + CELL > width) width = x + CELL;

      const rect = svg("rect", { x: x, y: cy, width: CELL, height: CELL, rx: 2 });
      const di = byDay.get(ed);
      if (di === undefined) {
        // Nothing was watched, so there is nothing to hover and nowhere to go.
        rect.setAttribute("fill", "var(--grid)");
        s.appendChild(rect);
        continue;
      }
      const d = days[di];

      const area = d[DY_AREA], views = d[DY_VIEWS];
      const sess = d[DY_TO] - d[DY_FROM] + 1;
      const fill = areaColor(area);
      // sqrt, because a linear ramp would leave nine out of ten days as a
      // faint smudge under the handful of marathon days.
      const op = (0.18 + 0.82 * Math.sqrt(views / maxV)).toFixed(3);
      rect.setAttribute("fill", fill);
      rect.setAttribute("fill-opacity", op);
      hover(rect, () => "<b>" + dayLabel(ed) + "</b><span class='m'>" +
        views + (views === 1 ? " view" : " views") + " · " + esc(areaName(area)) +
        " · " + sess + (sess === 1 ? " sitting" : " sittings") + "</span>");
      clickTo(rect, "#/day/" + dayDate(ed));
      cells.push({ el: rect, di: di, area: area, fill: fill, op: op });
      s.appendChild(rect);
    }
  }

  const w = width + 8, h = (y1 - y0 + 1) * BLOCK_H;
  s.setAttribute("width", w);
  s.setAttribute("height", h);
  s.setAttribute("viewBox", "0 0 " + w + " " + h);

  const chart = $("div", "chart");
  chart.appendChild(s);
  panel.appendChild(chart);

  // filter(null) restores every cell. One entry point rather than two, because
  // the graph has ONE selection and hands it over whole: an area keeps the days
  // that area dominated, a pair arrives carrying the set of day indices that
  // transition actually happened on. Greying out rather than hiding keeps the
  // grid intact, so the answer stays readable as a shape in the year.
  function filter(sel) {
    const byArea = !!sel && sel.area != null;
    for (const c of cells) {
      const on = !sel || (byArea ? c.area === sel.area : sel.days.has(c.di));
      c.el.setAttribute("fill", on ? c.fill : "var(--grid)");
      c.el.setAttribute("fill-opacity", on ? c.op : 1);
    }
    filterNote.textContent = !sel ? "" : byArea
      ? " Right now only the days " + areaName(sel.area) + " dominated keep their colour."
      : " Right now only the " + sel.days.size.toLocaleString() +
        (sel.days.size === 1 ? " day " : " days ") + sel.label +
        " happened on keep their colour.";
  }

  // The lens changes what the OPACITY means, never the hue: the colour of a
  // day is what it was about, and that answer does not depend on the
  // question being asked. Views is the one the page opens with, because it
  // is the only one that needs no explaining.
  //
  // It is local state, not a hash step — the same call the transition
  // graph's selection makes. Switching lens is looking at the same picture
  // differently, not going somewhere; a back button full of lens changes
  // would bury the step that actually moved.
  const LENSES = [
    { id: "views", text: "views", of: d => d[DY_VIEWS],
      note: " Opacity is that day's views against the busiest day." },
    { id: "chain", text: "time in a chain", of: d => (d[DY_VIEWS] ? d[DY_CHAINV] / d[DY_VIEWS] : 0),
      note: " Opacity is the share of the day spent inside a rabbit hole." },
    { id: "night", text: "night share", of: d => (d[DY_VIEWS] ? d[DY_NIGHT] / d[DY_VIEWS] : 0),
      note: " Opacity is the share of the day started between " + {{.NightFrom}} + ":00 and " + {{.NightTo}} + ":00." },
    { id: "held", text: "watched through", of: d => (d[DY_EDGED] ? d[DY_THROUGH] / d[DY_EDGED] : 0),
      note: " Opacity is the share of the day watched through to the end — days with no known length stay faint." },
    { id: "new", text: "new channels", of: d => d[DY_NEWCH],
      note: " Opacity is how many channels were seen for the first time that day." },
    { id: "peak", text: "how unusual", of: d => d[DY_PEAK] / 1000,
      note: " Opacity is the day's strongest rank across views, chain depth, night share and spread." },
  ];
  const lensNote = $("span");
  rule.appendChild(lensNote);

  function applyLens(id) {
    const lens = LENSES.find(l => l.id === id) || LENSES[0];
    let top = 0;
    for (const c of cells) top = Math.max(top, lens.of(days[c.di]));
    for (const c of cells) {
      const v = top > 0 ? lens.of(days[c.di]) / top : 0;
      // sqrt for the same reason the views lens needs it: a linear ramp
      // leaves nine days out of ten a faint smudge under the few extremes.
      c.op = (0.18 + 0.82 * Math.sqrt(Math.max(0, v))).toFixed(3);
      c.el.setAttribute("fill-opacity", c.op);
    }
    lensNote.textContent = lens.note;
  }

  const picker = $("label", "pick");
  picker.appendChild($("span", null, "shade by "));
  const sel = $("select");
  for (const l of LENSES) {
    const o = $("option", null, l.text);
    o.value = l.id;
    sel.appendChild(o);
  }
  sel.addEventListener("change", () => applyLens(sel.value));
  picker.appendChild(sel);
  panel.insertBefore(picker, chart);
  applyLens("views");

  return { panel: panel, filter: filter, cells: cells.length };
}

// ---- the transition graph ----------------------------------------------

// daysOf answers "on which days did THIS transition actually happen" without a
// single byte more on the payload: the sittings, their rows and the day each
// sitting began on are all already there, so the answer is a walk, not a
// shipment. 238 pairs would otherwise have cost a day list each.
//
// The walk has to be buildTransitions' walk exactly — the graph prints Go's
// count and the calendar shows this walk's days, and two numbers about the same
// pair may not come from two different rules:
//
//   - it never leaves a sitting: a jump over a night is not a transition;
//   - it reads each sitting BACKWARDS, because rows are stored newest first and
//     "followed by" is a statement about the order they were watched in;
//   - an overlap view is stepped over with continue, so the previous area stays
//     the previous area and the chain does not break;
//   - a self-loop counts — it is the chain staying on its topic.
//
// One pass builds every pair at once, on the first pick and never again: 41k
// rows is 6 ms, which is nothing once and far too much on every hover.
let pairDays = null;

function daysOf(from, to) {
  if (!pairDays) {
    pairDays = new Map();
    const sess = D.sess || [], rows = D.rows || [];
    for (const s of sess) {
      const di = s[S_DAY], first = s[S_ROW] + 1;
      let prev = -1, have = false;
      for (let i = first + s[S_N] - 1; i >= first; i--) {
        const r = rows[i];
        if (isOverlap(r)) continue;
        if (have) {
          const k = prev + "|" + r[R_AREA];
          let set = pairDays.get(k);
          if (!set) { set = new Set(); pairDays.set(k, set); }
          set.add(di);
        }
        prev = r[R_AREA];
        have = true;
      }
    }
  }
  return pairDays.get(from + "|" + to) || new Set();
}

function buildGraph(cal) {
  const panel = $("div", "panel");
  panel.appendChild($("h2", null, "what followed what"));

  const trans = D.trans || [];
  const av = D.areaViews || [];
  const shown = trans.slice(0, ARC_MAX);
  panel.appendChild($("p", "muted",
    "An arc says a video of one area was started right after a video of the other, inside the same sitting — a jump over a night is not a transition, and overlap views are stepped over. Thickness is how often it happened; a loop is the chain staying on its topic. " +
    (trans.length > shown.length
      ? "Showing the " + shown.length + " strongest of " + trans.length + " transitions."
      : "All " + trans.length + " transitions are drawn.")));
  // The drawing has answered a click since the day it was drawn, and nothing
  // said so — no outline, no hover, not a word — so on screen it read as a
  // picture. An interaction nobody is told about is an interaction nobody has.
  panel.appendChild($("p", "muted",
    "The drawing is a control. Click an area to keep its arcs and the days it dominated; click an arc to keep that one transition and the days it actually happened on. The calendar above follows the pick, and the list under the ring is the same choice for the keyboard."));

  // Only areas with a main-lane view can carry a transition, so the ring is
  // exactly those. Sorted by size, ties by name: the ring has to look the
  // same on every reload.
  const idx = [];
  for (let i = 0; i < av.length; i++) if (av[i] > 0) idx.push(i);
  idx.sort((a, b) => (av[b] - av[a]) || (areaName(a) < areaName(b) ? -1 : 1));

  // A group, not an image: the calendar and the day axis are pictures with a
  // mouse shortcut, but the ring is the only place a topic can be picked, and
  // an image role would hide those seventeen buttons from anything that is not
  // a pointer. Seventeen focus stops are a list; the calendar's two thousand
  // would have been a wall, which is why only this chart carries them.
  //
  // The arcs are on the far side of that same line. Sixty of them is not a
  // list any more, and sixty invisible hit paths as focus stops would bury the
  // seventeen real ones plus every control after the chart. So an arc is a
  // click and a hover, and the keyboard reaches a pair through the select
  // under the drawing — one stop for all sixty, announced by name, and with
  // type-ahead a pair is found faster there than by tabbing anyway.
  const s = svg("svg", {
    role: "group", "aria-label": "pick an area or a transition to trace it through the calendar",
    width: "100%", height: G_H,
    viewBox: "0 0 " + G_W + " " + G_H,
  });
  const ttl = svg("title");
  ttl.textContent = "which area followed which, as arcs between " + idx.length + " areas";
  s.appendChild(ttl);

  if (!idx.length || !shown.length) {
    panel.appendChild($("p", "muted", "No two main-lane views followed each other."));
    return panel;
  }

  const vMax = av[idx[0]], vSum = av.reduce((a, b) => a + b, 0) || 1;
  const pos = new Map();
  idx.forEach((a, k) => {
    const ang = -Math.PI / 2 + 2 * Math.PI * k / idx.length;
    pos.set(a, {
      a: ang, x: G_CX + G_RING * Math.cos(ang), y: G_CY + G_RING * Math.sin(ang),
      r: 4 + 18 * Math.sqrt(av[a] / vMax),
    });
  });

  let nMin = Infinity, nMax = 0;
  for (const t of shown) {
    if (t[2] < nMin) nMin = t[2];
    if (t[2] > nMax) nMax = t[2];
  }
  // Square root, not linear. The counts here span three orders of magnitude
  // (one pair runs into the thousands, the tail sits in the tens), and on a
  // linear scale that turns every arc but the widest into the same hairline —
  // a picture that only shows its own maximum. The root keeps the order
  // intact and gives the middle of the range a thickness you can compare.
  const widthOf = n => (nMax > nMin
    ? 1 + 9 * Math.sqrt((n - nMin) / (nMax - nMin))
    : 3);

  const gArcs = svg("g"), gHits = svg("g"), gNodes = svg("g");
  const arcs = [], arcBy = new Map();
  // One selection for the whole chart — an area or a pair, never both — and
  // the arc the pointer happens to be over, which is a highlight and not a
  // choice. Declared here because the handlers built below close over them.
  let sel = null, hot = null;

  for (const t of shown) {
    const p = pos.get(t[0]), q = pos.get(t[1]);
    if (!p || !q) continue;
    let d;
    if (t[0] === t[1]) {
      // A self-loop lives outside its node, where it cannot be mistaken for
      // the node's own outline.
      const L = p.r + 26;
      const p1 = p.a - 0.55, p2 = p.a + 0.55, c1 = p.a - 1.35, c2 = p.a + 1.35;
      d = "M" + xy(p.x + p.r * Math.cos(p1), p.y + p.r * Math.sin(p1)) +
        "C" + xy(p.x + L * Math.cos(c1), p.y + L * Math.sin(c1)) +
        " " + xy(p.x + L * Math.cos(c2), p.y + L * Math.sin(c2)) +
        " " + xy(p.x + p.r * Math.cos(p2), p.y + p.r * Math.sin(p2));
    } else {
      // Bending towards the middle keeps two arcs between the same pair of
      // nodes apart and stops the ring from turning into a ball of chords.
      const mx = (p.x + q.x) / 2, my = (p.y + q.y) / 2;
      d = "M" + xy(p.x, p.y) +
        "Q" + xy(G_CX + (mx - G_CX) * 0.35, G_CY + (my - G_CY) * 0.35) +
        " " + xy(q.x, q.y);
    }
    const el = svg("path", {
      d: d, fill: "none", stroke: areaColor(t[0]),
      "stroke-width": widthOf(t[2]).toFixed(2),
      "stroke-opacity": 0.45, "stroke-linecap": "round",
    });
    gArcs.appendChild(el);

    // A one-pixel stroke is not something a pointer can hit, so the tooltip
    // and the click hang on an invisible fat copy underneath the nodes.
    const grab = svg("path", {
      d: d, fill: "none", stroke: "var(--fg)", "stroke-opacity": 0,
      "stroke-width": Math.max(7, widthOf(t[2])),
    });
    grab.classList.add("hit");
    hover(grab, () => "<b>" + esc(areaName(t[0])) + " &#8594; " + esc(areaName(t[1])) +
      "</b><span class='m'>" + t[2].toLocaleString() +
      (t[2] === 1 ? " time" : " times") + " &middot; click for its days</span>");
    const rec = { el: el, from: t[0], to: t[1], n: t[2] };
    // A hairline under the pointer is easy to lose among sixty others, so the
    // arc the tooltip is talking about is the one that lights up.
    grab.addEventListener("mouseenter", () => { hot = rec; paintArcs(); });
    grab.addEventListener("mouseleave", () => {
      if (hot === rec) { hot = null; paintArcs(); }
    });
    // stopPropagation, or the click would travel on to the background handler
    // that clears the selection and undo itself.
    grab.addEventListener("click", ev => { ev.stopPropagation(); pick(pairSel(t[0], t[1])); });
    gHits.appendChild(grab);
    arcs.push(rec);
    arcBy.set(t[0] + "|" + t[1], rec);
  }

  const nodes = new Map();
  for (const a of idx) {
    const p = pos.get(a);
    const c = svg("circle", {
      cx: p.x.toFixed(1), cy: p.y.toFixed(1), r: p.r.toFixed(1),
      fill: areaColor(a), stroke: "var(--bg)", "stroke-width": 1,
      tabindex: 0, role: "button", "aria-pressed": "false",
      "aria-label": "trace " + areaName(a),
    });
    c.classList.add("hit");
    hover(c, () => "<b>" + esc(areaName(a)) + "</b><span class='m'>" +
      av[a].toLocaleString() + " views · " +
      (100 * av[a] / vSum).toFixed(1) + "% of the main lane · click to trace it</span>");
    c.addEventListener("click", ev => { ev.stopPropagation(); pick({ area: a }); });
    c.addEventListener("keydown", ev => {
      if (ev.key === "Enter" || ev.key === " ") {
        ev.preventDefault(); ev.stopPropagation(); pick({ area: a });
      }
    });
    gNodes.appendChild(c);
    nodes.set(a, c);

    // The label sits outside the ring; near the top and the bottom it is
    // centred instead of pushed sideways, where neighbouring labels would
    // otherwise run into each other.
    const co = Math.cos(p.a), si = Math.sin(p.a);
    const lx = G_CX + (G_RING + p.r + 9) * co, ly = G_CY + (G_RING + p.r + 9) * si;
    let anchor = "middle", dy = si < 0 ? "-0.35em" : "0.95em";
    if (co > 0.3) { anchor = "start"; dy = "0.32em"; }
    else if (co < -0.3) { anchor = "end"; dy = "0.32em"; }
    const t = svg("text", {
      x: lx.toFixed(1), y: ly.toFixed(1), dy: dy,
      "text-anchor": anchor, "font-size": 11,
    });
    t.textContent = areaName(a);
    gNodes.appendChild(t);
  }

  s.appendChild(gArcs);
  s.appendChild(gHits);
  s.appendChild(gNodes);
  panel.appendChild(s);

  // The selection changes the panel ABOVE this one, so it also has to be
  // readable as a sentence — colour is not a state anyone can read back, and
  // nobody should have to guess what the calendar is now showing.
  const state = $("p", "muted");
  const stateText = $("span");
  const clearEl = $("button", "clear", "show everything");
  clearEl.type = "button";
  clearEl.hidden = true;
  clearEl.addEventListener("click", () => { sel = null; apply(); });
  state.appendChild(stateText);
  state.appendChild(clearEl);
  panel.appendChild(state);

  // The same choice as a real control: one focus stop that holds the whole
  // selection, and the only way to a pair that is not a pointer. It doubles as
  // the list the drawing cannot be — sixty arcs are a shape, not a legend.
  const picker = $("label", "pick");
  picker.appendChild($("span", null, "or pick from the list:"));
  const pickEl = $("select");
  function opt(value, text) {
    const o = $("option", null, text);
    o.value = value;
    return o;
  }
  pickEl.appendChild(opt("", "nothing picked — every day is showing"));
  const gArea = $("optgroup");
  gArea.label = "areas";
  for (const a of idx) {
    gArea.appendChild(opt("a:" + a,
      areaName(a) + " · " + av[a].toLocaleString() + " views"));
  }
  pickEl.appendChild(gArea);
  const gPair = $("optgroup");
  gPair.label = "transitions";
  for (const arc of arcs) {
    gPair.appendChild(opt("p:" + arc.from + ":" + arc.to,
      areaName(arc.from) + " → " + areaName(arc.to) + " · " + arc.n.toLocaleString() +
      (arc.n === 1 ? " time" : " times")));
  }
  pickEl.appendChild(gPair);
  pickEl.addEventListener("change", () => {
    const v = pickEl.value.split(":");
    sel = v[0] === "a" ? { area: +v[1] } : v[0] === "p" ? pairSel(+v[1], +v[2]) : null;
    apply();
  });
  picker.appendChild(pickEl);
  panel.appendChild(picker);

  // pairSel is where the browser-side walk is paid for: the count is Go's, off
  // the arc, and only the day set is derived here.
  function pairSel(from, to) {
    const arc = arcBy.get(from + "|" + to);
    if (!arc) return null;
    return {
      from: from, to: to, n: arc.n, days: daysOf(from, to),
      label: areaName(from) + " → " + areaName(to),
    };
  }
  // Two selections are the same one when they name the same thing; the objects
  // are rebuilt on every pick, so identity would never match and clicking the
  // picked node again would never let go.
  const same = (a, b) => !!a && !!b &&
    a.area === b.area && a.from === b.from && a.to === b.to;
  function pick(next) { sel = same(sel, next) ? null : next; apply(); }

  function paintArcs() {
    for (const arc of arcs) {
      const on = sel === null ? true
        : sel.days ? (arc.from === sel.from && arc.to === sel.to)
        : (arc.from === sel.area || arc.to === sel.area);
      arc.el.setAttribute("stroke-opacity", arc === hot ? 0.95 : (on ? 0.45 : 0.08));
    }
  }

  function apply() {
    paintArcs();
    for (const [a, el] of nodes) {
      // A picked pair lights its two ends too, at half strength: the ring is
      // where the areas are named, and an arc with no lit ends is a stripe.
      const picked = sel !== null && sel.area === a;
      const end = sel !== null && !!sel.days && (sel.from === a || sel.to === a);
      el.setAttribute("stroke", picked || end ? "var(--fg)" : "var(--bg)");
      el.setAttribute("stroke-width", picked ? 3 : (end ? 2 : 1));
      el.setAttribute("stroke-opacity", end && !picked ? 0.5 : 1);
      el.setAttribute("aria-pressed", picked ? "true" : "false");
    }
    cal.filter(sel);
    pickEl.value = sel === null ? ""
      : sel.days ? "p:" + sel.from + ":" + sel.to : "a:" + sel.area;
    clearEl.hidden = sel === null;
    stateText.textContent = sel === null
      ? "Nothing picked — the calendar above is showing every day. Click an area, or an arc, to cut it down."
      : sel.days
        ? "Picked: " + sel.label + ", " + sel.n.toLocaleString() +
          (sel.n === 1 ? " time" : " times") + ", on " + sel.days.size.toLocaleString() +
          (sel.days.size === 1 ? " day" : " days") +
          " — the calendar above is showing those days only."
        : "Picked: " + areaName(sel.area) +
          " — its arcs, and the days it dominated in the calendar above.";
  }
  s.addEventListener("click", () => { if (sel !== null) { sel = null; apply(); } });
  apply();

  return panel;
}

// xy rounds a point into a path string; two decimals of a pixel are noise
// nobody can see but every arc would carry them.
const xy = (x, y) => x.toFixed(1) + " " + y.toFixed(1) + " ";
</script>
`
