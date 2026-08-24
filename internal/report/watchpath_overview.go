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

  root.appendChild(statTiles(st, days));
  root.appendChild($("p", "muted",
    "Start with a day: a cell in the calendar opens that day, the day lists the sittings that began on it, and a sitting shows every video in the order it was started."));

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

  const dr = st.deepestRabbit;
  t.appendChild(dr >= 0
    ? tile("deepest chain", st.deepestRabbitN + " videos",
      "one area back to back · " + sessDate(dr), "#/session/" + dr)
    : tile("deepest chain", "—", ""));

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
    "One cell is one day. The hue is the area most of that day's main-lane views belonged to, the opacity is that day's views against the busiest day of the whole span. A sitting counts on the day it BEGAN, so an evening that runs past midnight stays on that evening.");
  const filterNote = $("span");
  rule.appendChild(filterNote);
  panel.appendChild(rule);

  // The strongest day of the WHOLE span sets the scale, so the year blocks
  // stay comparable with each other instead of each one being its own record.
  let maxV = 1;
  const byDay = new Map();
  for (const d of days) {
    byDay.set(d[DY_ED], d);
    if (d[DY_VIEWS] > maxV) maxV = d[DY_VIEWS];
  }

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
      const d = byDay.get(ed);
      if (!d) {
        // Nothing was watched, so there is nothing to hover and nowhere to go.
        rect.setAttribute("fill", "var(--grid)");
        s.appendChild(rect);
        continue;
      }

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
      cells.push({ el: rect, area: area, fill: fill, op: op });
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

  // filter(null) restores every cell. Greying out rather than hiding keeps
  // the grid intact, so the answer stays readable as a shape in the year.
  function filter(area) {
    for (const c of cells) {
      const on = area == null || c.area === area;
      c.el.setAttribute("fill", on ? c.fill : "var(--grid)");
      c.el.setAttribute("fill-opacity", on ? c.op : 1);
    }
    filterNote.textContent = area == null ? "" :
      " Right now only the days " + areaName(area) + " dominated keep their colour.";
  }

  return { panel: panel, filter: filter, cells: cells.length };
}

// ---- the transition graph ----------------------------------------------

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
  const s = svg("svg", {
    role: "group", "aria-label": "pick an area to trace it through the calendar",
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
  const arcs = [];

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
    // hangs on an invisible fat copy underneath the nodes.
    const grab = svg("path", {
      d: d, fill: "none", stroke: "var(--fg)", "stroke-opacity": 0,
      "stroke-width": Math.max(7, widthOf(t[2])),
    });
    hover(grab, () => "<b>" + esc(areaName(t[0])) + " &#8594; " + esc(areaName(t[1])) +
      "</b><span class='m'>" + t[2].toLocaleString() +
      (t[2] === 1 ? " time" : " times") + "</span>");
    gHits.appendChild(grab);
    arcs.push({ el: el, from: t[0], to: t[1] });
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
      (100 * av[a] / vSum).toFixed(1) + "% of the main lane</span>");
    c.addEventListener("click", ev => { ev.stopPropagation(); pick(a); });
    c.addEventListener("keydown", ev => {
      if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); ev.stopPropagation(); pick(a); }
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
  // readable as a sentence — nobody should have to guess what is filtered.
  const state = $("p", "muted");
  panel.appendChild(state);

  let sel = null;
  function pick(a) { sel = (sel === a ? null : a); apply(); }
  function apply() {
    for (const arc of arcs) {
      const on = sel === null || arc.from === sel || arc.to === sel;
      arc.el.setAttribute("stroke-opacity", on ? 0.45 : 0.08);
    }
    for (const [a, el] of nodes) {
      el.setAttribute("stroke", a === sel ? "var(--fg)" : "var(--bg)");
      el.setAttribute("stroke-width", a === sel ? 2 : 1);
      el.setAttribute("aria-pressed", a === sel ? "true" : "false");
    }
    cal.filter(sel);
    state.textContent = sel === null
      ? "Nothing picked — click an area to keep only its arcs and only the days it dominated."
      : "Picked: " + areaName(sel) + " — its arcs and its days only. Click it again, or the background, to let the rest back in.";
  }
  s.addEventListener("click", () => { if (sel !== null) pick(sel); });
  apply();

  return panel;
}

// xy rounds a point into a path string; two decimals of a pixel are noise
// nobody can see but every arc would carry them.
const xy = (x, y) => x.toFixed(1) + " " + y.toFixed(1) + " ";
</script>
`
