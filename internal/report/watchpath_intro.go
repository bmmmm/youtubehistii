// SPDX-License-Identifier: GPL-3.0-or-later

package report

// introJS draws the strip of entry cards at the top of the overview: one per
// view, each holding a real miniature of the real data and a small motion that
// says what that view does. It exists because the best views of this page used
// to sit behind a single word in the corner, which is the same as not having
// them. See the shell in watchpath_page.go for the helpers it may use and the
// two rules it must obey.
const introJS = `<script>
// The miniatures share one frame. Small enough that four fit across a phone,
// big enough that a shape is still a shape.
const MW = 160, MH = 74;

// mini opens a preview canvas. It is a picture of the view behind the card,
// so it carries the card's own name and nothing else for a screen reader —
// the heading and the sentence under it do the describing.
function mini(title) {
  const s = svg("svg", {
    class: "mini", viewBox: "0 0 " + MW + " " + MH,
    role: "img", "aria-hidden": "true", focusable: "false",
  });
  const t = svg("title");
  t.textContent = title;
  s.appendChild(t);
  return s;
}

// card wraps a miniature into a real anchor. Not a div with a click handler:
// these are the page's main navigation, and navigation has to be reachable
// with a keyboard and openable in a new tab like any other link.
function card(hash, heading, line, art) {
  const a = $("a", "way");
  a.href = hash;
  a.appendChild(art);
  a.appendChild($("h3", null, heading));
  a.appendChild($("p", null, line));
  return a;
}

// ---- the miniatures -----------------------------------------------------

// The topic tree, packed by the same code the full view uses — so the card is
// literally a smaller version of what the click leads to, not an impression
// of it. The three largest areas breathe in turn: that is the drawing saying
// a circle can be opened.
function miniTopics() {
  const s = mini("the topic tree");
  const top = (D.clusters || []).slice(0, 14);
  if (!top.length) return s;
  const cs = top.map(n => ({ r: Math.sqrt(Math.max(1, n[T_VIEWS])), name: n[T_NAME] }));
  const R = packSiblings(cs) || 1;
  const k = (MH / 2 - 3) / R;
  cs.forEach((c, i) => {
    const el = svg("circle", {
      cx: (MW / 2 + c.x * k).toFixed(2), cy: (MH / 2 + c.y * k).toFixed(2),
      r: Math.max(1, c.r * k).toFixed(2),
      fill: areaColor(D.areas.indexOf(c.name)), "fill-opacity": 0.5,
    });
    if (i < 3) {
      el.setAttribute("class", "grow");
      el.style.animationDelay = (i * 2) + "s";
    }
    s.appendChild(el);
  });
  return s;
}

// One day on its hours, with the second lane if that day has one. The sweep
// is a clock hand: it says the axis is time, which a row of bars alone does
// not.
function miniDay(di) {
  const s = mini("a day on a 24-hour axis");
  const day = D.days[di];
  const rows = [];
  for (let si = day[DY_TO]; si >= day[DY_FROM]; si--) {
    const vs = sessionViews(si);
    for (let i = vs.length - 1; i >= 0; i--) rows.push(vs[i]);
  }
  if (!rows.length) return s;
  const d0 = at(rows[0][R_TS]);
  d0.setHours(0, 0, 0, 0);
  const dayStart = d0.getTime() / 1000 - D.t0;
  const L = 6, W = MW - 12;
  // The axis grows past 24 h exactly as the full view's does. Clamping instead
  // would pile every video started after midnight onto the right edge and
  // invent a spike that no evening had.
  let last = 24;
  for (const r of rows) last = Math.max(last, (r[R_TS] - dayStart) / 3600);
  const span = Math.max(24, Math.ceil(last));
  const xOf = ts => L + Math.max(0, (ts - dayStart) / 3600 / span) * W;

  s.appendChild(svg("line", { x1: L, y1: MH - 12, x2: L + W, y2: MH - 12, stroke: "var(--grid)" }));
  for (let h = 0; h <= span; h += 6) {
    const x = L + (h / span) * W;
    s.appendChild(svg("line", { x1: x, y1: 10, x2: x, y2: MH - 12, stroke: "var(--grid)" }));
  }
  for (const r of rows) {
    const ov = isOverlap(r);
    s.appendChild(svg("rect", {
      x: xOf(r[R_TS]).toFixed(2), y: ov ? 40 : 20, width: 2, height: 14, rx: 1,
      fill: areaColor(r[R_AREA]), "fill-opacity": ov ? 0.45 : 0.85,
    }));
  }
  s.appendChild(svg("line", {
    class: "sweep", x1: L, y1: 10, x2: L, y2: MH - 12,
    stroke: "var(--bar)", "stroke-width": 1.5,
  }));
  return s;
}

// A sitting as the path it was. Height is the video's length, so the line
// really is a profile of that evening and not a decorative zigzag; the stroke
// travels because the view behind this card is about order.
function miniSitting(si) {
  const s = mini("one sitting as a path");
  const vs = sessionViews(si).slice(-8).reverse(); // oldest first: time forwards
  if (!vs.length) return s;
  const yOf = r => MH - 16 - Math.min(1, Math.max(0, r[R_DUR]) / 1800) * (MH - 34);
  const step = (MW - 24) / Math.max(1, vs.length - 1);
  const pts = vs.map((r, i) => [12 + i * step, yOf(r)]);

  // The path is drawn once and stays; a brighter segment then travels along
  // it. The line has to be readable at every moment of the loop, so the
  // motion sits ON it rather than being it.
  const d = pts.map(p => p[0].toFixed(1) + "," + p[1].toFixed(1)).join(" ");
  s.appendChild(svg("polyline", {
    fill: "none", stroke: "var(--line)", "stroke-width": 1.6, points: d,
  }));
  s.appendChild(svg("polyline", {
    class: "draw", fill: "none", stroke: "var(--bar)", "stroke-width": 1.8, points: d,
  }));
  vs.forEach((r, i) => {
    s.appendChild(svg("circle", {
      cx: pts[i][0].toFixed(1), cy: pts[i][1].toFixed(1), r: isOverlap(r) ? 2 : 3.4,
      fill: areaColor(r[R_AREA]), "fill-opacity": isOverlap(r) ? 0.5 : 0.9,
    }));
  });
  return s;
}

// The list, as the list looks: a stack of rows drifting slowly, because that
// is the one thing 35000 rows do.
function miniList() {
  const s = mini("every view as one list");
  const rows = [];
  for (let i = 0; i < D.rows.length && rows.length < 7; i++) {
    if (D.rows[i][0] !== 0) rows.push(D.rows[i]);
  }
  const g = svg("g", { class: "rise" });
  rows.forEach((r, i) => {
    const y = 4 + i * 11;
    g.appendChild(svg("circle", { cx: 10, cy: y + 4, r: 2.6, fill: areaColor(r[R_AREA]) }));
    g.appendChild(svg("rect", {
      x: 17, y: y, width: 40 + Math.min(1, Math.max(0, r[R_DUR]) / 1200) * (MW - 70),
      height: 7, rx: 2, fill: "var(--line)",
    }));
  });
  s.appendChild(g);
  return s;
}

// The drift in miniature: the topic mix of the FIRST video of a sitting
// against the mix from the seventh on, as two stacked bars. Two columns are
// enough to show that they differ, which is the card's whole claim.
function miniDrift() {
  const s = mini("what you start with, and what you end up on");
  const A = D.areas.length;
  const early = new Array(A).fill(0), late = new Array(A).fill(0);
  for (let si = 0; si < D.sess.length; si++) {
    const vs = sessionViews(si);
    let pos = 0;
    for (let i = vs.length - 1; i >= 0; i--) {
      if (isOverlap(vs[i])) continue;
      pos++;
      const into = pos === 1 ? early : pos >= 7 ? late : null;
      if (into) into[vs[i][R_AREA]]++;
    }
  }
  const col = (counts, x, w) => {
    const total = counts.reduce((a, b) => a + b, 0);
    if (!total) return;
    const order = counts.map((v, ai) => [v, ai]).filter(([v]) => v > 0).sort((a, b) => b[0] - a[0]);
    let y = 8;
    for (const [v, ai] of order) {
      const h = (v / total) * (MH - 18);
      g.appendChild(svg("rect", { x: x, y: y, width: w, height: h, fill: areaColor(ai) }));
      y += h;
    }
  };
  const g = svg("g", { class: "rise" });
  col(early, 22, 44);
  col(late, 94, 44);
  s.appendChild(g);
  const l1 = svg("text", { x: 44, y: MH - 2, "text-anchor": "middle", "font-size": 8, class: "m" });
  l1.textContent = "1st";
  const l2 = svg("text", { x: 116, y: MH - 2, "text-anchor": "middle", "font-size": 8, class: "m" });
  l2.textContent = "7th+";
  s.appendChild(l1);
  s.appendChild(l2);
  return s;
}

// The rabbit holes as a depth histogram: how many runs were 4 videos long, 5,
// 6, and how far the tail reaches. The card's promise is a RANKING, so the
// miniature shows the distribution the ranking sorts — not one example chain,
// which would say nothing about whether the deepest is unusual.
function miniHoles() {
  const s = mini("how deep the rabbit holes went");
  const cs = D.chains || [];
  if (!cs.length) return s;
  const bins = new Map();
  let max = 1, deepest = 0;
  for (const c of cs) {
    const n = c[C_LEN];
    const v = (bins.get(n) || 0) + 1;
    bins.set(n, v);
    max = Math.max(max, v);
    deepest = Math.max(deepest, n);
  }
  const lens = [...bins.keys()].sort((a, b) => a - b);
  const w = Math.max(2, Math.min(9, (MW - 8) / Math.max(1, lens.length) - 1));
  const g = svg("g", { class: "rise" });
  lens.forEach((n, i) => {
    // sqrt, like the calendar: one enormous first bar would flatten the tail
    // into nothing, and the tail is the half worth looking at.
    const h = Math.max(2, Math.sqrt(bins.get(n) / max) * (MH - 12));
    g.appendChild(svg("rect", {
      x: 4 + i * (w + 1), y: MH - 4 - h, width: w, height: h, rx: 1.5,
      fill: "var(--bar)", "fill-opacity": 0.35 + 0.65 * (n / deepest),
    }));
  });
  s.appendChild(g);
  return s;
}

// The months of the report, as bars — the same aggregate the report draws in
// full, at the size of a sparkline. The outline travels because the view
// behind this card spans years rather than a moment; it traces the tops of
// the bars, so it repeats the drawing instead of claiming anything new.
function miniReport() {
  const s = mini("the export summed up");
  const ms = (D.report && D.report.months) || [];
  if (!ms.length) return s;

  const tot = ms.map(monthTotal);
  let max = 1;
  for (const n of tot) max = Math.max(max, n);
  const L = 5, W = MW - 10, TOP = 8, BASE = MH - 6;
  const step = W / ms.length;
  const bw = Math.max(1, Math.min(7, step - 1));
  const yOf = n => BASE - (n / max) * (BASE - TOP);

  const pts = [];
  ms.forEach((m, i) => {
    const x = L + i * step;
    let bottom = BASE;
    m[RM_MODES].forEach((v, mi) => {
      if (!v) return;
      const h = Math.max(0.6, (v / max) * (BASE - TOP));
      bottom -= h;
      s.appendChild(svg("rect", {
        x: x.toFixed(2), y: bottom.toFixed(2), width: bw.toFixed(2), height: h.toFixed(2),
        fill: "var(--" + (D.modes[mi] || "unclear") + ")", "fill-opacity": 0.75,
      }));
    });
    pts.push((x + bw / 2).toFixed(1) + "," + yOf(tot[i]).toFixed(1));
  });
  if (pts.length > 1) {
    s.appendChild(svg("polyline", {
      class: "draw", fill: "none", stroke: "var(--bar)", "stroke-width": 1.6,
      points: pts.join(" "),
    }));
  }
  return s;
}

// ---- the strip ----------------------------------------------------------

// introCards builds one card per view that is NOT already on the overview.
// A card is only offered when the path really holds that thing: a card that
// leads to "not on this timeline" would be worse than no card.
function introCards(st) {
  const ways = $("div", "ways");

  // First, because it is the widest cut: everything at once, as numbers. It
  // used to be a separate file nobody arrived at this page from.
  if (D.report) {
    const R = D.report;
    ways.appendChild(card("#/report", "the report",
      R.views.toLocaleString() + " views summed up — topics, months, channels" +
      (R.hasSubs ? " and subscriptions" : "") + ", with every hour an upper bound.",
      miniReport()));
  }
  if ((D.clusters || []).length) {
    // Only the two levels that can be counted without lying: the leaf count is
    // area/subject/channel triples, not distinct channels, so it is left out
    // rather than dressed up as one.
    let subjects = 0;
    for (const a of D.clusters) subjects += (a.length > T_KIDS ? a[T_KIDS] : []).length;
    ways.appendChild(card("#/topics", "the topic tree",
      D.clusters.length + " areas holding " + subjects.toLocaleString() +
      " subjects, down to the channels. Click a circle to go finer.",
      miniTopics()));
  }
  if (st.busiestDay >= 0) {
    const day = D.days[st.busiestDay];
    ways.appendChild(card("#/day/" + dayDate(day[DY_ED]), "a day",
      "Every video of one day on its hours. The busiest was " + dayLabel(day[DY_ED]) +
      " with " + day[DY_VIEWS] + ".",
      miniDay(st.busiestDay)));
  }
  if (st.longestSess >= 0) {
    ways.appendChild(card("#/session/" + st.longestSess, "a sitting",
      "The path through one sitting, video by video. The longest ran " +
      st.longestSessN + " of them.",
      miniSitting(st.longestSess)));
  }
  // The reverse reading. Placed after the zoom cards because it only makes
  // sense once you have seen what it is reading backwards.
  ways.appendChild(card("#/algo", "the algorithm, backwards",
    "What held you and what you clicked away, how the topics drift from the " +
    "first video of a sitting to the seventh, and the introductions that stuck.",
    miniDrift()));
  // Only with chains on the payload: a card leading to "there is none" would
  // be worse than no card.
  if ((D.chains || []).length) {
    const deepest = st.deepestChain >= 0 ? D.chains[st.deepestChain][C_LEN] : 0;
    ways.appendChild(card("#/holes", "the rabbit holes",
      D.chains.length.toLocaleString() + " runs of " + {{.RabbitLen}} + "+ videos on one area, " +
      "ranked — the deepest went " + deepest + " deep. Each says what pulled you in.",
      miniHoles()));
  }
  ways.appendChild(card("#/list", "all views",
    D.views.toLocaleString() + " views on one timeline, newest first.",
    miniList()));

  return ways;
}
</script>
`
