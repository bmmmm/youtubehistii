// SPDX-License-Identifier: GPL-3.0-or-later

package report

// detailJS draws levels 2 and 3: one day on a 24-hour axis, and one sitting
// as the sequence of videos it actually was. See the shell in
// watchpath_page.go for the helpers it may use and the two rules it must obey.
const detailJS = `<script>
// Both views read time FORWARDS, while the payload stores sittings and their
// views newest first. Every walk below therefore reverses once, right at its
// start, so nothing after that has to think about direction.

// SVG has no text-overflow, so every label is trimmed to its box by hand. The
// width is measured on a canvas rather than guessed from a character count:
// caps and emoji are far wider than an average character, and the guess shows.
// Sizes are in user units — the space the viewBox is in — so the trim holds at
// any rendered width.
const measureCtx = document.createElement("canvas").getContext("2d");
let measureFont = "";
function textW(s, size) {
  if (!measureFont) measureFont = getComputedStyle(document.body).fontFamily;
  measureCtx.font = size + "px " + measureFont;
  return measureCtx.measureText(s).width;
}

function fit(s, px, size) {
  s = s || "(no title)";
  if (textW(s, size) <= px) return s;
  let lo = 0, hi = s.length;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (textW(s.slice(0, mid) + "…", size) <= px) lo = mid; else hi = mid - 1;
  }
  return s.slice(0, lo) + "…";
}

// viewTip is shared so a bar on the day axis and a node in the sequence say
// the same thing about the same video. A missing length is stated rather than
// drawn over: the export does not know it, so neither do we.
function viewTip(r) {
  const bits = [];
  bits.push(esc(areaName(r[R_AREA])) + (D.subs[r[R_SUB]] ? " / " + esc(D.subs[r[R_SUB]]) : ""));
  if (D.chans[r[R_CHAN]]) bits.push(esc(D.chans[r[R_CHAN]]));
  bits.push(clock(r[R_TS]));
  bits.push(r[R_DUR] > 0 ? clip(r[R_DUR]) : "length unknown");
  bits.push(esc(D.modes[r[R_MODE]]));
  const edge = D.edges[r[R_EDGE]];
  if (edge) bits.push(EDGE_TEXT[edge] + (r[R_GAP] > 0 ? " after " + dur(r[R_GAP]) : ""));
  if (isOverlap(r)) bits.push("overlap suspected");
  return "<b>" + esc(r[R_TITLE] || "(no title)") + '</b><span class="m">' +
    bits.join(" &middot; ") + "</span>";
}

// dayOrder lists a day's sittings oldest first. D.days holds the range as
// [newest … oldest], because D.sess itself runs newest first.
function dayOrder(day) {
  const out = [];
  for (let si = day[DY_TO]; si >= day[DY_FROM]; si--) out.push(si);
  return out;
}

// sessAreas counts what a sitting was about, main lane only — background must
// not decide the topic, the same rule Go applies to a day.
function sessAreas(si) {
  const c = new Map();
  for (const r of sessionViews(si)) {
    if (!isOverlap(r)) c.set(r[R_AREA], (c.get(r[R_AREA]) || 0) + 1);
  }
  return [...c.entries()].sort((a, b) =>
    b[1] - a[1] || areaName(a[0]).localeCompare(areaName(b[0])));
}

// ---- level 2: one day on a 24-hour axis ---------------------------------

function renderDay(root, di) {
  const day = D.days[di];
  const order = dayOrder(day);

  // Every view of the day in the order it was watched, each remembering the
  // sitting it belongs to — that is what a click on a bar needs.
  const items = [];
  for (const si of order) {
    const vs = sessionViews(si);
    for (let i = vs.length - 1; i >= 0; i--) items.push({ r: vs[i], si: si });
  }

  root.appendChild($("h2", null, dayLabel(day[DY_ED])));
  let hours = 0;
  for (const it of items) hours += Math.max(0, it.r[R_DUR]) / 3600;
  root.appendChild($("p", "muted",
    day[DY_VIEWS] + (day[DY_VIEWS] === 1 ? " view" : " views") +
    " · " + areaName(day[DY_AREA]) +
    " · " + order.length + (order.length === 1 ? " sitting" : " sittings") +
    " · at most " + hours.toFixed(1) + " h of video"));
  if (!items.length) return;

  // ---- the axis
  // Midnight of this day, read off its oldest view: a sitting belongs to the
  // day it began on, so that view can only be on this calendar day.
  const d0 = at(items[0].r[R_TS]);
  d0.setHours(0, 0, 0, 0);
  const dayStart = d0.getTime() / 1000 - D.t0;
  const hourOf = ts => (ts - dayStart) / 3600;

  // The axis grows past 24 rather than clipping: a sitting that runs into the
  // small hours is the thing this view exists to show.
  let end = 24;
  for (const it of items) {
    end = Math.max(end, hourOf(it.r[R_TS]) + Math.max(0, it.r[R_DUR]) / 3600);
  }
  // Rounded up to an even hour so the last gridline is a labelled one — the
  // ticks run every two hours, and an axis ending between them reads as cut off.
  let span = Math.max(24, Math.ceil(end));
  if (span % 2) span++;

  const W = 960, ML = 46, MR = 14, PW = W - ML - MR;
  const xOf = h => ML + (h / span) * PW;

  // A second lane costs a third of the height, so it is only drawn when there
  // is something to put in it.
  const hasOv = items.some(it => isOverlap(it.r));
  const LBL_Y = [18, 32], BR_Y = 44, LANE_Y = 58, LANE_H = 16, LANE2_Y = 82;
  const laneBottom = (hasOv ? LANE2_Y : LANE_Y) + LANE_H;
  const axisY = laneBottom + 12;
  const H = axisY + 22;

  const s = svg("svg", { viewBox: "0 0 " + W + " " + H, width: "100%", role: "img" });
  const ttl = svg("title", {});
  ttl.textContent = dayLabel(day[DY_ED]) + ": " + items.length +
    " videos on an axis of " + span + " hours from midnight";
  s.appendChild(ttl);

  for (let h = 0; h <= span; h += 2) {
    const x = xOf(h);
    s.appendChild(svg("line", { x1: x, y1: 10, x2: x, y2: axisY, stroke: "var(--grid)" }));
    const t = svg("text", { x: x, y: axisY + 14, "text-anchor": "middle", "font-size": 10, class: "m" });
    t.textContent = pad(h);
    s.appendChild(t);
  }
  s.appendChild(svg("line", { x1: ML, y1: axisY, x2: xOf(span), y2: axisY, stroke: "var(--line)" }));

  if (hasOv) {
    for (const l of [[LANE_Y, "main"], [LANE2_Y, "overlap"]]) {
      const t = svg("text", { x: ML - 8, y: l[0] + 12, "text-anchor": "end", "font-size": 10, class: "m" });
      t.textContent = l[1];
      s.appendChild(t);
    }
  }

  for (const it of items) {
    const r = it.r, ov = isOverlap(r), known = r[R_DUR] > 0;
    const x = xOf(hourOf(r[R_TS]));
    // Three pixels minimum, or a three-minute video would not exist on screen.
    const w = known ? Math.max(3, (r[R_DUR] / 3600 / span) * PW) : 3;
    const col = areaColor(r[R_AREA]);
    const dashed = ov || !known;
    const bar = svg("rect", {
      x: x, y: ov ? LANE2_Y : LANE_Y, width: w, height: LANE_H, rx: 2,
      fill: col, "fill-opacity": known ? (ov ? 0.5 : 0.85) : 0.2,
      stroke: dashed ? col : null,
      "stroke-width": dashed ? 1 : null,
      "stroke-dasharray": dashed ? "3 2" : null,
    });
    hover(bar, () => viewTip(r));
    clickTo(bar, "#/session/" + it.si);
    s.appendChild(bar);
  }

  // Sittings as brackets over the lanes. Two label rows, because two sittings
  // an hour apart would otherwise print their labels on top of each other.
  const lvlEnd = [-1e9, -1e9];
  for (const si of order) {
    const sd = D.sess[si];
    const x1 = xOf(hourOf(sd[S_TS]));
    const x2 = Math.max(x1 + 3, xOf(hourOf(sd[S_TS] + sd[S_SPAN])));
    const g = svg("g", {});
    g.appendChild(svg("path", {
      d: "M" + x1 + " " + (BR_Y + 6) + " V" + BR_Y + " H" + x2 + " V" + (BR_Y + 6),
      fill: "none", stroke: "var(--line)", "stroke-width": 1.5,
    }));
    const txt = clock(sd[S_TS]) + " · " + sd[S_N] + (sd[S_N] === 1 ? " video" : " videos");
    let lvl = 0;
    while (lvl < LBL_Y.length - 1 && lvlEnd[lvl] > x1 - 6) lvl++;
    lvlEnd[lvl] = x1 + textW(txt, 10);
    const t = svg("text", { x: x1, y: LBL_Y[lvl], "font-size": 10 });
    t.textContent = txt;
    g.appendChild(t);
    hover(g, () => "<b>" + clock(sd[S_TS]) + " – " + clock(sd[S_TS] + sd[S_SPAN]) +
      '</b><span class="m">' + esc(sessionLine(sd)) + "</span>");
    clickTo(g, "#/session/" + si);
    s.appendChild(g);
  }

  const chart = $("div", "chart");
  chart.appendChild(s);
  const panel = $("div", "panel");
  panel.appendChild($("p", "muted",
    "Bar width is the video's full length, not watch time" +
    (hasOv ? "; the lower lane holds what looks like background." : ".") +
    " A dashed hairline is a video whose length the export never gave us."));
  panel.appendChild(chart);
  root.appendChild(panel);

  // ---- the same sittings as a list, so a day is navigable without aiming
  const list = $("div", "panel");
  list.appendChild($("h2", null, "sittings"));
  list.appendChild($("p", "muted", "Oldest first, like the axis above."));
  const stack = $("div", "stack");
  for (const si of order) {
    const sd = D.sess[si];
    const areas = sessAreas(si);
    const a = $("a", "tile");
    a.href = "#/session/" + si;
    a.style.setProperty("--area", areaColor(areas.length ? areas[0][0] : -1));
    const l1 = $("div", "l1");
    l1.appendChild($("span", "dot"));
    l1.appendChild($("span", "clock", clock(sd[S_TS])));
    l1.appendChild($("span", "title", sessionLine(sd)));
    a.appendChild(l1);
    a.appendChild($("div", "l2", areas.slice(0, 3)
      .map(e => areaName(e[0]) + " " + e[1]).join(" · ") || "no main-lane video"));
    stack.appendChild(a);
  }
  list.appendChild(stack);
  root.appendChild(list);
}

// ---- level 3: one sitting as a sequence ---------------------------------

function renderSession(root, si) {
  const sd = D.sess[si];
  const vs = sessionViews(si).slice().reverse();

  root.appendChild($("h2", null, stamp(sd[S_TS])));
  root.appendChild($("p", "muted", sessionLine(sd)));
  if (!vs.length) return;

  // The path runs top to bottom. Sideways would need a scrollbar at sixty
  // videos; downwards a sitting of any length simply scrolls like a page.
  const W = 960, NODE_X = 76, NODE_W = 424, NH = 30, EDGE_X = NODE_X + 26;
  const OV_X = 520, OV_W = 420, OVH = 26, PAD = 14;
  // Title budgets: the box minus the number column on the left and the clock
  // on the right.
  const TW = NODE_W - 52 - 48, OTW = OV_W - 12 - 48;

  // Positions first, drawing second: an edge needs to know where the node
  // below it will land, and a chain bracket where its whole run ends.
  const pos = [];
  let y = PAD;
  for (const r of vs) {
    const ov = isOverlap(r);
    pos.push({ y: y, ov: ov, h: ov ? OVH : NH });
    y += (ov ? OVH + 10 : NH + 26);
  }
  const last = pos[pos.length - 1];
  const H = last.y + last.h + PAD;

  const s = svg("svg", { viewBox: "0 0 " + W + " " + H, width: "100%", role: "img" });
  const ttl = svg("title", {});
  ttl.textContent = "The sitting of " + stamp(sd[S_TS]) + " as " + vs.length +
    " videos in the order they were started, top to bottom";
  s.appendChild(ttl);

  const defs = svg("defs", {});
  const mk = svg("marker", {
    id: "wp-arrow", viewBox: "0 0 8 8", refX: 7, refY: 4,
    markerWidth: 6, markerHeight: 6, orient: "auto",
  });
  mk.appendChild(svg("path", { d: "M0 0 L8 4 L0 8 z", fill: "var(--muted)" }));
  defs.appendChild(mk);
  s.appendChild(defs);

  // prevMain is both the source of the next edge and the video an overlap
  // branches off: markOverlap keeps the main lane on the last non-overlap
  // view, so that view is the one that was still running.
  let prevMain = -1;
  for (let i = 0; i < vs.length; i++) {
    const r = vs[i], p = pos[i], col = areaColor(r[R_AREA]);

    if (p.ov) {
      if (prevMain >= 0) {
        s.appendChild(svg("path", {
          d: "M" + (NODE_X + NODE_W) + " " + (pos[prevMain].y + NH / 2) +
             " H" + (OV_X - 14) + " V" + (p.y + OVH / 2) + " H" + OV_X,
          fill: "none", stroke: "var(--muted)", "stroke-width": 1,
          "stroke-dasharray": "3 3", "stroke-opacity": 0.6,
        }));
      }
      const g = svg("g", {});
      g.appendChild(svg("rect", {
        x: OV_X, y: p.y, width: OV_W, height: OVH, rx: 6,
        fill: "none", stroke: col, "stroke-width": 1, "stroke-dasharray": "4 3",
      }));
      const t = svg("text", { x: OV_X + 12, y: p.y + OVH / 2 + 4, "font-size": 12 });
      t.textContent = fit(r[R_TITLE], OTW, 12);
      g.appendChild(t);
      const c = svg("text", {
        x: OV_X + OV_W - 12, y: p.y + OVH / 2 + 4,
        "text-anchor": "end", "font-size": 10, class: "m",
      });
      c.textContent = clock(r[R_TS]);
      g.appendChild(c);
      hover(g, () => viewTip(r));
      s.appendChild(g);
      continue;
    }

    if (prevMain >= 0) {
      const from = pos[prevMain].y + NH;
      s.appendChild(svg("line", {
        x1: EDGE_X, y1: from, x2: EDGE_X, y2: p.y - 4,
        stroke: "var(--muted)", "stroke-width": 1, "marker-end": "url(#wp-arrow)",
      }));
      const pr = vs[prevMain];
      const edge = D.edges[pr[R_EDGE]];
      // No length, no verdict — only the silence we can measure. The label
      // sits under its source, because the statement belongs to that video.
      const txt = edge
        ? EDGE_TEXT[edge] + (pr[R_GAP] > 0 ? " after " + dur(pr[R_GAP]) : "")
        : (pr[R_GAP] > 0 ? dur(pr[R_GAP]) + " later" : "");
      if (txt) {
        const t = svg("text", {
          x: EDGE_X + 10, y: from + 16, "font-size": 11,
          class: edge === "skipped" ? null : "m",
          fill: edge === "skipped" ? "var(--consume)" : null,
        });
        t.textContent = txt;
        s.appendChild(t);
      }
    }

    const g = svg("g", {});
    g.appendChild(svg("rect", {
      x: NODE_X, y: p.y, width: NODE_W, height: NH, rx: 6,
      fill: "var(--card)", stroke: col, "stroke-width": 1.5,
    }));
    g.appendChild(svg("circle", { cx: NODE_X + 14, cy: p.y + NH / 2, r: 4, fill: col }));
    const n = svg("text", {
      x: NODE_X + 44, y: p.y + NH / 2 + 4, "text-anchor": "end",
      "font-size": 10, class: "m",
    });
    n.textContent = String(i + 1);
    g.appendChild(n);
    const t = svg("text", { x: NODE_X + 52, y: p.y + NH / 2 + 4, "font-size": 13 });
    t.textContent = fit(r[R_TITLE], TW, 13);
    g.appendChild(t);
    const c = svg("text", {
      x: NODE_X + NODE_W - 12, y: p.y + NH / 2 + 4,
      "text-anchor": "end", "font-size": 10, class: "m",
    });
    c.textContent = clock(r[R_TS]);
    g.appendChild(c);
    hover(g, () => viewTip(r));
    s.appendChild(g);

    prevMain = i;
  }

  // Chains in the left gutter. A run ends where the area changes, which is
  // where markRabbitHoles ended it too — two chains can sit back to back.
  const main = [];
  for (let i = 0; i < vs.length; i++) {
    if (!pos[i].ov) main.push(i);
  }
  let k = 0;
  while (k < main.length) {
    if (!isRabbit(vs[main[k]])) { k++; continue; }
    const area = vs[main[k]][R_AREA];
    let j = k;
    while (j + 1 < main.length && isRabbit(vs[main[j + 1]]) &&
           vs[main[j + 1]][R_AREA] === area) j++;
    const y1 = pos[main[k]].y, y2 = pos[main[j]].y + NH, mid = (y1 + y2) / 2;
    const col = areaColor(area);
    s.appendChild(svg("rect", { x: 26, y: y1, width: 5, height: y2 - y1, rx: 2.5, fill: col }));
    const t = svg("text", {
      x: 18, y: mid, "text-anchor": "middle", "font-size": 10, fill: col,
      transform: "rotate(-90 18 " + mid + ")",
    });
    t.textContent = fit("chain of " + (j - k + 1) + " · " + areaName(area), y2 - y1 - 10, 10);
    s.appendChild(t);
    k = j + 1;
  }

  const chart = $("div", "chart");
  chart.appendChild(s);
  const panel = $("div", "panel");
  panel.appendChild($("p", "muted",
    "Downwards is forwards. A branch to the right started while the video it " +
    "hangs off was still running; a bar on the left is a run on one area."));
  panel.appendChild(chart);
  root.appendChild(panel);

  const cards = $("div", "panel");
  cards.appendChild($("h2", null, "the same sitting as cards"));
  cards.appendChild($("p", "muted",
    "Oldest first, like the diagram — the rest of the page lists newest first."));
  const stack = $("div", "stack");
  for (const r of vs) {
    const wrap = $("div", isOverlap(r) ? "lane2" : null);
    wrap.appendChild(viewCard(r));
    stack.appendChild(wrap);
  }
  cards.appendChild(stack);
  root.appendChild(cards);
}
</script>
`
