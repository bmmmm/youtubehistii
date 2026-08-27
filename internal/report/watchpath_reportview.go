// SPDX-License-Identifier: GPL-3.0-or-later

package report

// reportJS draws the aggregate report: the whole export read as numbers rather
// than as a timeline. It used to be its own static page, which meant whoever
// opened it never learned that a topic tree, a calendar and a day view existed
// at all — so it moved in here as a view like any other. See the shell in
// watchpath_page.go for the helpers it may use and the two rules it must obey.
//
// It draws the hierarchy ONCE, as a table that opens: the same tree is already
// drawn as circles at #/topics, and two drawings of one hierarchy on one page
// only ask the reader which of them to believe. The table links there instead.
const reportJS = `<script>
// ---- level 1, cut by aggregate: the report -----------------------------

// Row layouts of the report block — positional, like every other row here.
const RT_NAME = 0, RT_MODE = 1, RT_VIEWS = 2, RT_DUR = 3, RT_SUBS = 4;
const RM_MONTH = 0, RM_MODES = 1;
const RC_CHAN = 0, RC_TOPIC = 1, RC_VIEWS = 2, RC_DUR = 3, RC_SUBBED = 4;
// A subscription row carries the same first four fields as a channel row and
// is read with the same constants; only the fifth is its own.
const RS_LAST = 4;

const rtKids = n => (n.length > RT_SUBS ? n[RT_SUBS] : null);
const verb = (n, one, many) => (n === 1 ? one : many);

// A Go slice with nothing in it marshals to null, not to [] — so every list
// of the report block is read through this.
const rlist = v => v || [];

// Every hour figure here is a sum of full video lengths. The column says "≤"
// and the note above says why; this is the compact form for a table cell.
const upto = s => "≤ " + (s >= 36000 ? Math.round(s / 3600).toLocaleString() : (s / 3600).toFixed(1)) + " h";

function renderReport(root) {
  const R = D.report;

  // Not a <details>: every hour on this view is an upper bound, and a caveat
  // that has to be opened is a caveat that will not be read.
  const caveat = [];
  if (R.noID) {
    caveat.push(R.noID.toLocaleString() + plural(" view", R.noID) +
      verb(R.noID, " has", " have") + " no video link at all (deleted or private)");
  }
  if (R.gone) {
    caveat.push(R.gone.toLocaleString() + plural(" view", R.gone) +
      verb(R.gone, " points", " point") + " at a video that is gone from YouTube");
  }
  root.appendChild($("p", "note",
    "Takeout records that a video was started, never how much of it was watched. " +
    "Every hour below therefore assumes the video ran to its end — an upper bound, " +
    "which is what the ≤ in front of it means. View counts are exact." +
    (caveat.length ? " Of the " + R.views.toLocaleString() + " views counted here, " +
      caveat.join(", and ") + "." : "")));

  root.appendChild(reportTiles(R));
  root.appendChild(reportTopics(R));
  root.appendChild(reportMonths(R));
  root.appendChild(reportChannels(R));
  if (R.hasSubs) root.appendChild(reportSubs(R));
  if (rlist(R.unclear).length) root.appendChild(reportUnclear(R));
}

// ---- the headline numbers ----------------------------------------------

function reportTiles(R) {
  const t = $("div", "tiles");

  // The timeline drops views the export never dated; this count does not, so
  // the difference is named rather than left as two numbers that disagree.
  const undated = R.views - D.views;
  t.appendChild(tile("views", R.views.toLocaleString(),
    R.unique.toLocaleString() + " distinct videos" +
    (undated > 0 ? " · " + undated.toLocaleString() + " of them undated, on no timeline" : "")));

  t.appendChild(tile("hours at most", Math.round(R.durS / 3600).toLocaleString(),
    "if every video had run to its end"));

  const topics = rlist(R.topics);
  let subjects = 0;
  for (const tp of topics) subjects += (rtKids(tp) || []).length;
  t.appendChild(tile("topics", topics.length.toLocaleString(),
    subjects.toLocaleString() + plural(" subject", subjects) + " · see them as a circle tree",
    "#/topics"));

  const src = R.sources;
  t.appendChild(tile("still open", src[3].toLocaleString(),
    "the rest was placed by a rule (" + src[0].toLocaleString() + "), the llm (" +
    src[1].toLocaleString() + ") or the youtube category (" + src[2].toLocaleString() + ")"));

  return t;
}

// ---- the topics, as a table that opens ---------------------------------

function modeChip(i) {
  const m = D.modes[i] || "unclear";
  return $("span", "mode " + m, m);
}

// The bar takes the hue of the AREA it belongs to, so a topic is the same
// colour here, in the calendar and in the circle tree. A name the timeline
// never saw falls back to the unclear grey rather than inventing a colour.
function reportBar(area, views, of) {
  const box = $("div", "rbar");
  const fill = $("div", "rfill");
  fill.style.width = Math.min(100, Math.max(1, 100 * views / Math.max(1, of))).toFixed(1) + "%";
  fill.style.background = areaColor(D.areas.indexOf(area));
  box.appendChild(fill);
  return box;
}

function reportRow(el, cls, name, mode, views, dur, area, of) {
  const row = $(el, cls);
  row.appendChild($("span", "rname", name || "unclear"));
  row.appendChild(modeChip(mode));
  row.appendChild($("span", "rnum", views.toLocaleString()));
  row.appendChild($("span", "rnum", upto(dur)));
  row.appendChild(reportBar(area, views, of));
  return row;
}

function reportTopics(R) {
  const panel = $("div", "panel");
  panel.appendChild($("h2", null, "topics"));
  const rule = $("p", "muted",
    "One row per area, most-watched first, with the mode most of its videos were " +
    "classified as. The bar is the area against the largest one; open a row and the " +
    "bars underneath are its subjects against the area itself. ");
  const link = $("span", "rlink", "The same hierarchy drawn as circles →");
  hit(link, "#/topics", "open the topic tree");
  rule.appendChild(link);
  panel.appendChild(rule);

  const topics = rlist(R.topics);
  if (!topics.length) {
    panel.appendChild($("p", "muted", "No view carries a topic."));
    return panel;
  }

  // Most-watched first is guaranteed by Go, so the first row is the scale.
  const max = topics[0][RT_VIEWS];
  const table = $("div", "rt");
  for (const tp of topics) {
    const name = tp[RT_NAME], kids = rtKids(tp);
    if (!kids) {
      const row = reportRow("div", "rrow", name, tp[RT_MODE], tp[RT_VIEWS], tp[RT_DUR], name, max);
      row.insertBefore($("span", "rcar"), row.firstChild);
      table.appendChild(row);
      continue;
    }

    // A real button, not a div with a click handler: the toggle is a control,
    // and a control has to answer the keyboard and announce its state.
    const row = reportRow("button", "rrow", name, tp[RT_MODE], tp[RT_VIEWS], tp[RT_DUR], name, max);
    row.type = "button";
    const car = $("span", "rcar", "▸");
    row.insertBefore(car, row.firstChild);
    row.insertBefore($("span", "rn", kids.length.toLocaleString() + plural(" subject", kids.length)),
      row.children[2]);
    row.setAttribute("aria-expanded", "false");

    const box = $("div", "rsubs");
    box.hidden = true;
    for (const k of kids) {
      box.appendChild(reportRow("div", "rrow rsub", k[RT_NAME], k[RT_MODE], k[RT_VIEWS],
        k[RT_DUR], name, tp[RT_VIEWS]));
    }
    row.addEventListener("click", () => {
      const open = box.hidden;
      box.hidden = !open;
      car.textContent = open ? "▾" : "▸";
      row.setAttribute("aria-expanded", open ? "true" : "false");
    });

    table.appendChild(row);
    table.appendChild(box);
  }
  panel.appendChild(table);
  return panel;
}

// ---- the months --------------------------------------------------------

function modeLegend() {
  const l = $("div", "legend");
  for (let i = 0; i < 4 && i < D.modes.length; i++) {
    const s = $("span");
    s.appendChild(modeChip(i));
    l.appendChild(s);
  }
  return l;
}

// A month is 14 px of bar and 4 px of air, so a decade still fits a screen and
// a single month is still a shape. The chart scrolls inside its own box.
const RB_W = 14, RB_GAP = 4, RB_H = 132, RB_PAD = 26;

function monthTotal(m) {
  let n = 0;
  for (const v of m[RM_MODES]) n += v;
  return n;
}

function reportMonths(R) {
  const panel = $("div", "panel");
  panel.appendChild($("h2", null, "month by month"));
  panel.appendChild($("p", "muted",
    "One bar per calendar month, stacked by mode, scaled against the busiest month. " +
    "Months in between with nothing watched are drawn as gaps rather than skipped — " +
    "a row of bars that leaves its empty stretches out lies about the axis."));
  panel.appendChild(modeLegend());

  const ms = rlist(R.months);
  if (!ms.length) {
    panel.appendChild($("p", "muted", "No view carries a date."));
    return panel;
  }

  // Every month between the first and the last, so the axis is time and not a
  // list of the months that happened to have views.
  const seen = new Map();
  let maxV = 1;
  for (const m of ms) {
    seen.set(m[RM_MONTH], m);
    maxV = Math.max(maxV, monthTotal(m));
  }
  const cols = [];
  const first = ms[0][RM_MONTH], last = ms[ms.length - 1][RM_MONTH];
  let y = +first.slice(0, 4), mo = +first.slice(5, 7);
  for (;;) {
    const key = y + "-" + pad(mo);
    cols.push({ key: key, m: seen.get(key) || null, year: y, mo: mo });
    if (key === last) break;
    if (++mo > 12) { mo = 1; y++; }
    if (cols.length > 2400) break; // a fixture with a broken date may not hang the page
  }

  const w = RB_PAD + cols.length * (RB_W + RB_GAP) + RB_PAD;
  const h = RB_H + 34;
  const s = svg("svg", { role: "img", width: w, height: h, viewBox: "0 0 " + w + " " + h });
  const ttl = svg("title");
  ttl.textContent = "views per month, " + first + " to " + last;
  s.appendChild(ttl);
  s.appendChild(svg("line", {
    x1: RB_PAD - 4, y1: RB_H, x2: w - RB_PAD + 4, y2: RB_H, stroke: "var(--grid)",
  }));

  cols.forEach((c, i) => {
    const x = RB_PAD + i * (RB_W + RB_GAP);
    // January carries the year; nothing else is labelled, because a label per
    // month at this width is a grey band, not a legend. The first column is
    // named in full whatever month it is, so a span shorter than a year is
    // not left with an axis nobody can read.
    if (c.mo === 1 || i === 0) {
      s.appendChild(svg("line", { x1: x - 2, y1: 8, x2: x - 2, y2: RB_H, stroke: "var(--grid)" }));
      const t = svg("text", { class: "m", x: x, y: RB_H + 14, "font-size": 10 });
      t.textContent = c.mo === 1 ? c.year : c.key;
      s.appendChild(t);
    }
    if (!c.m) return;

    const total = monthTotal(c.m);
    let bottom = RB_H;
    c.m[RM_MODES].forEach((v, mi) => {
      if (!v) return;
      const bh = Math.max(1, (v / maxV) * (RB_H - 12));
      bottom -= bh;
      s.appendChild(svg("rect", {
        x: x, y: bottom.toFixed(2), width: RB_W, height: bh.toFixed(2),
        fill: "var(--" + (D.modes[mi] || "unclear") + ")",
      }));
    });
    const zone = svg("rect", { x: x, y: 8, width: RB_W, height: RB_H - 8, fill: "transparent" });
    hover(zone, () => {
      const bits = [];
      c.m[RM_MODES].forEach((v, mi) => {
        if (v) bits.push(esc(D.modes[mi] || "unclear") + " " + v.toLocaleString());
      });
      return "<b>" + c.key + "</b><span class='m'>" + total.toLocaleString() +
        plural(" view", total) + " · " + bits.join(" · ") + "</span>";
    });
    s.appendChild(zone);
  });

  const chart = $("div", "chart");
  chart.appendChild(s);
  panel.appendChild(chart);
  return panel;
}

// ---- channels and subscriptions ----------------------------------------

// Both lists are the card stack the topic tree uses for its children: same
// row, same two lines, so a name reads the same wherever it appears.
function nameRow(name, right, second, area) {
  const row = $("div", "tile");
  row.style.setProperty("--area", areaColor(D.areas.indexOf(area)));
  const l1 = $("div", "l1");
  l1.appendChild($("span", "dot"));
  l1.appendChild($("span", "title", name || "(no channel)"));
  l1.appendChild($("span", "clock", right));
  row.appendChild(l1);
  row.appendChild($("div", "l2", second));
  return row;
}

// The area of a topic string: the report ships the full "area/subject", and
// the hue belongs to the level the rest of the page colours by.
const topicArea = t => String(t || "").split("/")[0];

function reportChannels(R) {
  const cs = rlist(R.chans);
  const panel = $("div", "panel");
  panel.appendChild($("h2", null, "channels"));
  panel.appendChild($("p", "muted",
    "The " + cs.length + " most-watched, with the topic most of their videos landed in" +
    (R.hasSubs ? ", and whether the export says you subscribe to them" : "") + "."));
  const stack = $("div", "stack");
  for (const c of cs) {
    const bits = [c[RC_TOPIC] || "unclear", upto(c[RC_DUR])];
    if (R.hasSubs) bits.push(c[RC_SUBBED] ? "subscribed" : "not subscribed");
    stack.appendChild(nameRow(D.chans[c[RC_CHAN]],
      c[RC_VIEWS].toLocaleString() + plural(" view", c[RC_VIEWS]),
      bits.join(" · "), topicArea(c[RC_TOPIC])));
  }
  panel.appendChild(stack);
  return panel;
}

function reportSubs(R) {
  const subs = rlist(R.subs);
  const panel = $("div", "panel");
  panel.appendChild($("h2", null, "subscriptions"));
  panel.appendChild($("p", "muted",
    "What the subscription export lists, matched against what was actually watched. " +
    "A subscription with no views is not dormant by proof — it is only absent from THIS export."));

  const t = $("div", "tiles");
  t.appendChild(tile("subscriptions", subs.length.toLocaleString(),
    (subs.length - R.deadSubs).toLocaleString() + " turned up at least once"));
  t.appendChild(tile("never watched", R.deadSubs.toLocaleString(), "not in this export, at least"));
  t.appendChild(tile("of the views", share(R.subViews, R.views), "are on a subscribed channel"));
  t.appendChild(tile("of the hours", share(R.subDurS, R.durS), "same, by upper-bound length"));
  panel.appendChild(t);

  const stack = $("div", "stack");
  for (const s of subs) {
    const last = s[RS_LAST] >= 0 ? "last watched " + dayDate(s[RS_LAST]) : "never watched in this export";
    stack.appendChild(nameRow(D.chans[s[RC_CHAN]],
      s[RC_VIEWS] ? s[RC_VIEWS].toLocaleString() + plural(" view", s[RC_VIEWS]) : "—",
      (s[RC_TOPIC] ? s[RC_TOPIC] + " · " + upto(s[RC_DUR]) + " · " : "") + last,
      topicArea(s[RC_TOPIC])));
  }
  panel.appendChild(stack);
  return panel;
}

function reportUnclear(R) {
  const panel = $("div", "panel");
  panel.appendChild($("h2", null, "unclear"));
  panel.appendChild($("p", "muted",
    "The channels carrying the most views nothing could place. Each one is a rule " +
    "waiting to be written: add a channel_any rule for it in config/rules.yaml and " +
    "run classify again."));
  const ul = $("ul", "runcl");
  for (const i of rlist(R.unclear)) ul.appendChild($("li", null, D.chans[i]));
  panel.appendChild(ul);
  return panel;
}
</script>
`
