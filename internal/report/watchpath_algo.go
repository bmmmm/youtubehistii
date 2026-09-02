// SPDX-License-Identifier: GPL-3.0-or-later

package report

// algoJS is the view that reads the timeline backwards: not what was
// watched, but what the recommender did with the person watching.
//
// Every panel here is a re-aggregation of facts already on the payload — no
// byte was added for this view. The walks are memoised the way the
// transition graph memoises its day sets: 41k rows take a few milliseconds,
// and once is enough.
//
// One caveat governs the whole view and is printed on it: Takeout records
// what was STARTED, never what was OFFERED. Nothing here can see an
// impression, so every statement is an inference about the watcher's
// response, not a reading of the algorithm's output. The page says that in
// its own words rather than implying otherwise by staying silent.
const algoJS = `<script>
// ---- level 6: the algorithm, read backwards ----------------------------

let algoAgg = null;

// algoWalk is the one pass all four panels read. It walks every sitting in
// the order things were watched (rows are stored newest first, so backwards)
// and collects four things at once, because four walks would cost four times
// as much and could drift apart.
function algoWalk() {
  if (algoAgg) return algoAgg;
  const A = D.areas.length;
  const retention = Array.from({ length: A }, () => ({ through: 0, most: 0, skipped: 0, main: 0 }));
  // Position buckets: 1, 2, 3, 4-6, 7-10, 11+ — the shape of "you chose the
  // first one, the sidebar chose the seventh".
  const POS = [[1, 1], [2, 2], [3, 3], [4, 6], [7, 10], [11, Infinity]];
  const byPos = POS.map(() => new Array(A).fill(0));
  const months = new Map();   // "YYYY-MM" -> {areas: [], total, newChans}
  const first = new Map();    // channel index -> {ts, si, n30, total, last}

  for (let si = D.sess.length - 1; si >= 0; si--) {
    const vs = sessionViews(si);
    let pos = 0;
    for (let i = vs.length - 1; i >= 0; i--) {
      const r = vs[i], ai = r[R_AREA];
      const key = monthKeyOf(r[R_TS]);
      let m = months.get(key);
      if (!m) { m = { areas: new Array(A).fill(0), total: 0, newChans: 0 }; months.set(key, m); }
      const ci = r[R_CHAN];
      if (D.chans[ci] && !first.has(ci)) {
        first.set(ci, { ts: r[R_TS], si: si, n30: 0, total: 0, last: r[R_TS] });
        m.newChans++;
      }
      if (D.chans[ci]) {
        const f = first.get(ci);
        f.total++;
        f.last = Math.max(f.last, r[R_TS]);
        if (r[R_TS] - f.ts <= 30 * 86400) f.n30++;
      }
      if (isOverlap(r)) continue; // background answers no question here
      m.areas[ai]++; m.total++;
      const ret = retention[ai];
      ret.main++;
      const e = D.edges[r[R_EDGE]];
      if (e === "through") ret.through++;
      else if (e === "most") ret.most++;
      else if (e === "skipped") ret.skipped++;
      pos++;
      for (let b = 0; b < POS.length; b++) {
        if (pos >= POS[b][0] && pos <= POS[b][1]) { byPos[b][ai]++; break; }
      }
    }
  }
  algoAgg = { retention: retention, POS: POS, byPos: byPos, months: months, first: first };
  return algoAgg;
}

// monthKeyOf turns a payload timestamp into "YYYY-MM" in LOCAL time — the
// same wall clock every other date on this page is read in.
//
// LOCAL here is the READER'S zone, and Stats.Months in report.go fixed its
// months in the RENDERING machine's zone hours earlier. Open the page
// somewhere else and the two views disagree about the month boundary. The day
// grid is a third rule again: it places a sitting that crosses midnight on the
// day it STARTED, while both month counts place each row on its own month.
//
// Measured on the real corpus (35,144 views, 5,793 sittings) rather than
// guessed at: 121 views (0.34 %) change month between Berlin and UTC, 793
// (2.26 %) between Berlin and Tokyo, and 80 (0.23 %) sit in a month other
// than their sitting's start month. Read where the page was rendered — the
// ordinary case for a tool that renders a file you then open — the first two
// are exactly zero.
//
// So this is a recorded property, not a defect: routing the month through the
// zone-stable day number would rebuild three call sites to move a fraction of
// a percent, and only for a reader who has travelled since the render.
function monthKeyOf(ts) {
  const d = at(ts);
  return d.getFullYear() + "-" + pad(d.getMonth() + 1);
}

const CAVEAT = "Takeout records what was STARTED, never what was offered. " +
  "Nothing here sees a recommendation; it sees what happened next, which is " +
  "the response — so read every panel as an inference, not as a readout.";

// ---- (a) what held you -------------------------------------------------

// algoRetention reads R_EDGE as the reward signal it is: how often a video
// of this area was left running to its end, against how much of the main
// lane that area occupied. The scatter is the point — the bottom right is
// where the recommender kept serving something that kept being clicked away.
function algoRetention(A) {
  const panel = $("div", "panel");
  panel.appendChild($("h2", null, "what held you"));
  panel.appendChild($("p", "muted",
    "Across the bottom: how much of your main lane an area was. Up the side: " +
    "how often one of its videos was still running when the next one started. " +
    "Top right is what the recommender got right; bottom right is what it kept " +
    "serving that you kept clicking away. \"Watched through\" only means the gap " +
    "covered the full length — leaving the room looks the same."));

  const pts = [];
  let mainTotal = 0;
  A.retention.forEach(r => { mainTotal += r.main; });
  A.retention.forEach((r, ai) => {
    const edged = r.through + r.most + r.skipped;
    if (!edged || !r.main) return;
    pts.push({ ai: ai, share: r.main / Math.max(1, mainTotal), held: r.through / edged, n: r.main, edged: edged });
  });
  if (!pts.length) {
    panel.appendChild($("p", "muted", "No video on this timeline has a known length, so nothing can be said about what held you."));
    return panel;
  }

  const W = 720, H = 320, L = 52, B = 40, T = 14, R = 14;
  const maxShare = Math.max(...pts.map(p => p.share));
  const x = v => L + (v / maxShare) * (W - L - R);
  const y = v => H - B - v * (H - B - T);
  const s = svg("svg", { viewBox: "0 0 " + W + " " + H, width: "100%", role: "img" });
  const ttl = svg("title", {});
  ttl.textContent = "share of the main lane against the share watched through, per area";
  s.appendChild(ttl);
  for (let g = 0; g <= 4; g++) {
    const gy = y(g / 4);
    s.appendChild(svg("line", { x1: L, y1: gy, x2: W - R, y2: gy, stroke: "var(--grid)", "stroke-width": 1 }));
    const t = svg("text", { x: L - 8, y: gy + 4, "text-anchor": "end", "font-size": 10, class: "m" });
    t.textContent = (g * 25) + "%";
    s.appendChild(t);
  }
  const xl = svg("text", { x: (L + W - R) / 2, y: H - 8, "text-anchor": "middle", "font-size": 11, class: "m" });
  xl.textContent = "share of the main lane →";
  s.appendChild(xl);

  for (const p of pts) {
    const cx = x(p.share), cy = y(p.held);
    const g = svg("g", {});
    g.appendChild(svg("circle", {
      cx: cx, cy: cy, r: Math.max(4, Math.sqrt(p.n) / 6), fill: areaColor(p.ai), "fill-opacity": 0.75,
    }));
    const t = svg("text", { x: cx + 9, y: cy + 4, "font-size": 10 });
    t.textContent = fit(areaName(p.ai), 120, 10);
    g.appendChild(t);
    hit(g, "#/list/" + encodeURIComponent(areaName(p.ai)), "list the views of " + areaName(p.ai));
    hover(g, () => "<b>" + esc(areaName(p.ai)) + '</b><span class="m">' +
      Math.round(100 * p.share) + "% of the main lane · " +
      Math.round(100 * p.held) + "% watched through, of " + p.edged + " with a known length</span>");
    s.appendChild(g);
  }
  const chart = $("div", "chart");
  chart.appendChild(s);
  panel.appendChild(chart);
  return panel;
}

// ---- (b) the drift -----------------------------------------------------

// algoDrift is the strongest single picture on this page: the topic mix by
// POSITION in a sitting. Video one is a choice — the home page, a search, an
// intention. Video seven is a suggestion accepted. The difference between
// the first column and the last IS the recommender's fingerprint.
function algoDrift(A) {
  const panel = $("div", "panel");
  panel.appendChild($("h2", null, "the drift"));
  panel.appendChild($("p", "muted",
    "Each column is a position in a sitting: the first video you started, the " +
    "second, and so on. You chose the first one. By the seventh, the choosing " +
    "had mostly been done for you — so the way the colours shift from left to " +
    "right is the recommendation, seen from the other side."));

  const labels = ["1st", "2nd", "3rd", "4-6th", "7-10th", "11th+"];
  const W = 720, H = 300, T = 16, B = 34, L = 8;
  const bw = (W - 2 * L) / A.POS.length - 12;
  const s = svg("svg", { viewBox: "0 0 " + W + " " + H, width: "100%", role: "img" });
  const ttl = svg("title", {});
  ttl.textContent = "which areas were watched at which position in a sitting";
  s.appendChild(ttl);

  A.byPos.forEach((counts, bi) => {
    const total = counts.reduce((a, b) => a + b, 0);
    const x = L + bi * ((W - 2 * L) / A.POS.length) + 6;
    const lab = svg("text", { x: x + bw / 2, y: H - 18, "text-anchor": "middle", "font-size": 11, class: "m" });
    lab.textContent = labels[bi];
    s.appendChild(lab);
    const n = svg("text", { x: x + bw / 2, y: H - 5, "text-anchor": "middle", "font-size": 9, class: "m" });
    n.textContent = total.toLocaleString();
    s.appendChild(n);
    if (!total) return;
    // Areas stacked biggest first, so the eye can follow one band across.
    const order = counts.map((v, ai) => [v, ai]).filter(([v]) => v > 0)
      .sort((a, b) => b[0] - a[0] || areaName(a[1]).localeCompare(areaName(b[1])));
    let yy = T;
    for (const [v, ai] of order) {
      const h = (v / total) * (H - T - B);
      const g = svg("g", {});
      g.appendChild(svg("rect", { x: x, y: yy, width: bw, height: h, fill: areaColor(ai) }));
      hover(g, () => "<b>" + esc(areaName(ai)) + '</b><span class="m">' +
        Math.round(100 * v / total) + "% of the " + labels[bi] + " videos · " + v.toLocaleString() + " views</span>");
      hit(g, "#/list/" + encodeURIComponent(areaName(ai)), areaName(ai) + " at position " + labels[bi]);
      s.appendChild(g);
      yy += h;
    }
  });
  const chart = $("div", "chart");
  chart.appendChild(s);
  panel.appendChild(chart);
  return panel;
}

// ---- (c) the shift over the years --------------------------------------

// algoMonths draws one sparkline per area — small multiples, not a stream.
// Eleven bands in one stream leave the middle ones unreadable, and this page
// has chosen legibility over effect everywhere else too (the sqrt ramp in
// the calendar, the printed cap on the ring). Underneath, the one line that
// says whether the recommender was still expanding: new channels per month.
function algoMonths(A) {
  const panel = $("div", "panel");
  panel.appendChild($("h2", null, "the shift"));
  panel.appendChild($("p", "muted",
    "One line per area: its share of that month, over the whole span. Underneath, " +
    "how many channels you met for the first time each month — the rate at which " +
    "you were still being introduced to someone new."));

  const keys = [...A.months.keys()].sort();
  if (keys.length < 2) {
    panel.appendChild($("p", "muted", "One month of history is not a shift yet."));
    return panel;
  }
  const rows = keys.map(k => A.months.get(k));
  // Only the areas that ever mattered: a line that is zero everywhere is a
  // label with nothing under it.
  const totals = new Array(D.areas.length).fill(0);
  rows.forEach(m => m.areas.forEach((v, ai) => { totals[ai] += v; }));
  const shown = totals.map((v, ai) => [v, ai]).filter(([v]) => v > 0)
    .sort((a, b) => b[0] - a[0]).slice(0, 12).map(([, ai]) => ai);

  const grid = $("div", "smalls");
  for (const ai of shown) {
    const cell = $("div", "small");
    const head = $("div", "sk");
    head.appendChild($("span", "dot"));
    head.children[0].style.background = areaColor(ai);
    head.appendChild($("span", null, areaName(ai)));
    cell.appendChild(head);
    const w = 200, h = 44;
    const s = svg("svg", { viewBox: "0 0 " + w + " " + h, width: "100%", role: "img" });
    const ttl = svg("title", {});
    ttl.textContent = areaName(ai) + " as a share of each month";
    s.appendChild(ttl);
    let peak = 0;
    const share = rows.map(m => (m.total ? m.areas[ai] / m.total : 0));
    for (const v of share) peak = Math.max(peak, v);
    // Every sparkline is drawn against its OWN peak, and the peak is
    // printed: a shared scale would flatten every small area into a flat
    // line, and the question here is the SHAPE of each one over time.
    const step = w / Math.max(1, share.length - 1);
    let d = "";
    share.forEach((v, i) => {
      const px = i * step, py = h - 4 - (peak > 0 ? v / peak : 0) * (h - 10);
      d += (i ? "L" : "M") + px.toFixed(1) + " " + py.toFixed(1);
    });
    s.appendChild(svg("path", { d: d, fill: "none", stroke: areaColor(ai), "stroke-width": 1.6 }));
    cell.appendChild(s);
    cell.appendChild($("div", "sn", "peak " + Math.round(100 * peak) + "% of a month"));
    grid.appendChild(cell);
  }
  panel.appendChild(grid);

  // New channels per month, as bars under the whole thing.
  const w = 720, h = 90, PAD = 6;
  const s = svg("svg", { viewBox: "0 0 " + w + " " + h, width: "100%", role: "img" });
  const ttl = svg("title", {});
  ttl.textContent = "channels met for the first time, per month";
  s.appendChild(ttl);
  let maxNew = 1;
  for (const m of rows) maxNew = Math.max(maxNew, m.newChans);
  const bw = Math.max(1.5, (w - 2 * PAD) / rows.length - 1);
  rows.forEach((m, i) => {
    const bh = Math.max(1, (m.newChans / maxNew) * (h - 20));
    const g = svg("g", {});
    g.appendChild(svg("rect", {
      x: PAD + i * ((w - 2 * PAD) / rows.length), y: h - 12 - bh, width: bw, height: bh, rx: 1,
      fill: "var(--bar)", "fill-opacity": 0.75,
    }));
    hover(g, () => "<b>" + keys[i] + '</b><span class="m">' + m.newChans +
      " channels met for the first time · " + m.total.toLocaleString() + " views</span>");
    s.appendChild(g);
  });
  const chart = $("div", "chart");
  chart.appendChild(s);
  panel.appendChild($("p", "muted", "New channels per month, " + keys[0] + " to " + keys[keys.length - 1] +
    " — the peak month brought " + maxNew + "."));
  panel.appendChild(chart);
  return panel;
}

// ---- (d) the takeovers -------------------------------------------------

// algoFirstContacts ranks introductions by what they turned into: met once
// on some day, and then watched how often. The row links to the SITTING of
// the first contact — the click where it started, which is the most personal
// moment this page can point at.
function algoFirstContacts(A) {
  const panel = $("div", "panel");
  panel.appendChild($("h2", null, "the takeovers"));
  panel.appendChild($("p", "muted",
    "Channels ranked by what happened in the thirty days after you first met " +
    "them. An introduction can also have been a search or a link from a friend — " +
    "Takeout does not say which. Each row opens the sitting it began in."));

  const rows = [...A.first.entries()]
    .map(([ci, f]) => ({ ci: ci, ts: f.ts, si: f.si, n30: f.n30, total: f.total, last: f.last }))
    .filter(f => f.n30 > 1)
    .sort((a, b) => b.n30 - a.n30 || b.total - a.total || b.ts - a.ts)
    .slice(0, 30);
  if (!rows.length) {
    panel.appendChild($("p", "muted", "No channel was watched more than once in the month after meeting it."));
    return panel;
  }
  const top = rows[0].n30;
  for (const f of rows) {
    const line = $("div", "rline");
    const a = $("a", "rrow", null);
    a.href = "#/session/" + f.si;
    a.appendChild($("span", "rname", D.chans[f.ci]));
    a.appendChild($("span", "rnum", stamp(f.ts)));
    a.appendChild($("span", "rnum", f.n30 + " in 30 d"));
    a.appendChild($("span", "rnum", f.total.toLocaleString() + " total"));
    const box = $("div", "rbar");
    const fill = $("div", "rfill");
    fill.style.width = Math.min(100, Math.max(2, 100 * f.n30 / top)).toFixed(1) + "%";
    box.appendChild(fill);
    a.appendChild(box);
    line.appendChild(a);
    line.appendChild(goArrow("#/session/" + f.si, "open the sitting it began in"));
    panel.appendChild(line);
  }
  return panel;
}

function renderAlgo(root) {
  const A = algoWalk();
  root.appendChild($("h2", null, "the algorithm, read backwards"));
  root.appendChild($("p", "muted", CAVEAT));
  root.appendChild(algoRetention(A));
  root.appendChild(algoDrift(A));
  root.appendChild(algoMonths(A));
  root.appendChild(algoFirstContacts(A));
}
</script>
`
