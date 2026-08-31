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

// HOLE_CAP is printed next to the list rather than hidden. Same rule the
// transition ring follows: a drawing that silently drops rows claims to be
// the whole list and is not.
const HOLE_CAP = 300;

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
  const shown = sorted.slice(0, HOLE_CAP);
  const top = Math.max(1, sort.id === "date" ? 1 : sort.of(shown[0]));

  const panel = $("div", "panel");
  for (const f of shown) {
    const c = D.chains[f.ci];
    const k = chainsOf(c[C_SESS]).indexOf(f.ci);
    const hash = "#/session/" + c[C_SESS] + "/chain/" + k;
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
    panel.appendChild(line);
    // The second line is the reverse-algorithm sentence: which topic the
    // chain was fallen into from, and what finally let the reader out.
    const why = [];
    if (f.door) why.push("entered from " + f.door);
    why.push(f.chans === 1 ? "one channel" : f.chans + " channels");
    if (f.exit) why.push("ended by " + EDGE_TEXT[f.exit]);
    panel.appendChild($("p", "rwhy", why.join(" · ")));
  }
  if (sorted.length > shown.length) {
    panel.appendChild($("p", "muted",
      "The " + HOLE_CAP + " deepest are drawn; " +
      (sorted.length - shown.length).toLocaleString() + " more are not."));
  }
  root.appendChild(panel);
}
</script>
`
