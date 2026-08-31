// SPDX-License-Identifier: GPL-3.0-or-later

package report

// clusterJS draws the topic tree as nested circles: areas holding subjects
// holding channels, each circle's AREA proportional to its views. Clicking a
// circle makes it the root, which is what turns the drawing finer. See the
// shell in watchpath_page.go for the helpers it may use and the two rules it
// must obey.
const clusterJS = `<script>
// ---- level 2, cut by topic: the tree as nested circles ------------------

// Node layout in the payload — positional, like every other row on this page.
// A leaf simply has no fourth element.
const T_NAME = 0, T_VIEWS = 1, T_DUR = 2, T_KIDS = 3;
const tKids = n => (n.length > T_KIDS ? n[T_KIDS] : null);

// The frame is square because a packing is: a wide box would waste its two
// corners and shrink every circle inside to pay for them.
const PACK_W = 880, PACK_H = 880, PACK_PAD = 6;

// A circle under this radius is not opened. Its children would land under
// ~3 px, which is texture rather than information — and the count of what
// stayed shut goes into the reading rule instead of vanishing.
const OPEN_R = 14;

// Under this radius a label survives as two or three characters, which names
// nothing. The tooltip carries the full name at every size.
const LABEL_R = 22;

// The list under the drawing is the exact path. At 2700 subjects it would stop
// being a list, so it shows the largest and says what it left out.
const LIST_MAX = 60;

const KIND = ["area", "subject", "channel"];
const plural = (w, n) => (n === 1 ? w : w + "s");

// Every duration in this tree is a sum of FULL video lengths — an upper bound,
// never watch time. The wording carries that wherever the number appears.
const atMost = s => "at most " +
  (s >= 36000 ? Math.round(s / 3600).toLocaleString() : (s / 3600).toFixed(1)) + " h of video";

// A share nobody can see as an angle still has to be readable as a number, so
// anything under a tenth of a percent says so rather than rounding to zero.
const share = (a, b) => {
  if (!(b > 0)) return "0%";
  const p = 100 * a / b;
  return (p >= 0.1 ? p.toFixed(1) : "<0.1") + "%";
};

// ---- circle packing -----------------------------------------------------

// The front-chain method (Wang et al., the one d3-hierarchy uses): the first
// three circles start a ring, every further circle is laid tangent to the pair
// at the front of that ring, and when it collides the ring is closed over the
// blocking circle and the placement retried. It is written out here because
// the page may not load a library, and the only property we lean on is that it
// is deterministic — same input, same picture, every reload.
let packBrake = 0; // groups the step budget had to rescue; reset per render

// place lays c tangent to both a and b, on the side away from the ring.
function packPlace(a, b, c) {
  const dx = b.x - a.x, dy = b.y - a.y, d2 = dx * dx + dy * dy;
  if (!d2) { c.x = a.x + c.r; c.y = a.y; return; }
  let a2 = a.r + c.r; a2 *= a2;
  let b2 = b.r + c.r; b2 *= b2;
  if (a2 > b2) {
    const x = (d2 + b2 - a2) / (2 * d2);
    const y = Math.sqrt(Math.max(0, b2 / d2 - x * x));
    c.x = b.x - x * dx - y * dy;
    c.y = b.y - x * dy + y * dx;
  } else {
    const x = (d2 + a2 - b2) / (2 * d2);
    const y = Math.sqrt(Math.max(0, a2 / d2 - x * x));
    c.x = a.x + x * dx - y * dy;
    c.y = a.y + x * dy + y * dx;
  }
}

// The epsilon is what keeps a tangency from reading as a collision and sending
// the algorithm round again forever.
function packHits(a, b) {
  const dr = a.r + b.r - 1e-6, dx = b.x - a.x, dy = b.y - a.y;
  return dr > 0 && dr * dr > dx * dx + dy * dy;
}

// score ranks a pair on the ring by how close its shared tangent sits to the
// centre; the front always advances at the closest pair, which is what keeps
// the result round instead of growing a tail.
function packScore(node) {
  const a = node.c, b = node.next.c, ab = a.r + b.r;
  const dx = (a.x * b.r + b.x * a.r) / ab, dy = (a.y * b.r + b.y * a.r) / ab;
  return dx * dx + dy * dy;
}

// packRing is the deterministic rescue when the budget below runs out:
// everything still unplaced goes on rings OUTSIDE what was packed, each circle
// stepped by its own angular width. All circles of one ring sit at the same
// distance from the centre, so the step is provably wide enough and the ring
// cannot overlap itself. It is a placement, not a packing — the reading rule
// says as much whenever it happens.
function packRing(cs, from) {
  let R = 0, rmax = 0;
  for (let i = 0; i < from; i++) R = Math.max(R, Math.hypot(cs[i].x, cs[i].y) + cs[i].r);
  for (let i = from; i < cs.length; i++) rmax = Math.max(rmax, cs[i].r);
  let rr = Math.max(R + rmax, 1e-6), ang = 0;
  for (let i = from; i < cs.length; i++) {
    const half = Math.asin(Math.min(1, cs[i].r / rr)) + 0.004;
    if (ang > 0 && ang + 2 * half > 2 * Math.PI) { rr += 2 * rmax; ang = 0; }
    cs[i].x = rr * Math.cos(ang + half);
    cs[i].y = rr * Math.sin(ang + half);
    ang += 2 * half;
  }
}

// packEnclose centres the group and returns a circle around it. The MINIMAL
// enclosing circle (Welzl) would be a page of code for a couple of percent of
// radius; centring the bounding box and taking the farthest edge is three
// lines and is guaranteed to contain every child, which is the only property
// the drawing actually needs.
function packEnclose(cs) {
  let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
  for (const c of cs) {
    if (c.x - c.r < x0) x0 = c.x - c.r;
    if (c.y - c.r < y0) y0 = c.y - c.r;
    if (c.x + c.r > x1) x1 = c.x + c.r;
    if (c.y + c.r > y1) y1 = c.y + c.r;
  }
  const mx = (x0 + x1) / 2, my = (y0 + y1) / 2;
  let r = 0;
  for (const c of cs) {
    c.x -= mx; c.y -= my;
    const d = Math.hypot(c.x, c.y) + c.r;
    if (d > r) r = d;
  }
  return r;
}

function packSiblings(cs) {
  const n = cs.length;
  if (!n) return 0;
  cs[0].x = 0; cs[0].y = 0;
  if (n > 1) { cs[0].x = -cs[1].r; cs[1].x = cs[0].r; cs[1].y = 0; }
  if (n > 2) {
    packPlace(cs[1], cs[0], cs[2]);
    let a = { c: cs[0] }, b = { c: cs[1] }, c = { c: cs[2] };
    a.next = c.prev = b; b.next = a.prev = c; c.next = b.prev = a;

    // The hard loop brake. Every collision retries the same circle against a
    // shorter ring, so on sane input this terminates — but a degenerate one
    // (a radius that is zero, or NaN out of a bad number) can keep re-entering
    // the retry, and a hung tab is worse than an imperfect picture.
    const budget = 8 * n + 64;
    let steps = 0, i = 3;
    pack: for (; i < n; ++i) {
      if (++steps > budget) break;
      packPlace(a.c, b.c, cs[i]);
      c = { c: cs[i] };
      let j = b.next, k = a.prev, sj = b.c.r, sk = a.c.r;
      do {
        if (sj <= sk) {
          if (packHits(j.c, c.c)) { b = j; a.next = b; b.prev = a; --i; continue pack; }
          sj += j.c.r; j = j.next;
        } else {
          if (packHits(k.c, c.c)) { a = k; a.next = b; b.prev = a; --i; continue pack; }
          sk += k.c.r; k = k.prev;
        }
      } while (j !== k.next);

      c.prev = a; c.next = b; a.next = b.prev = c;
      // The new circle joins the ring, then the front moves to whichever pair
      // now sits closest to the centre.
      b = c;
      let aa = packScore(a), ca;
      while ((c = c.next) !== b) { if ((ca = packScore(c)) < aa) { a = c; aa = ca; } }
      b = a.next;
    }
    if (i < n) { packBrake++; packRing(cs, i); }
  }
  return packEnclose(cs);
}

// clusterLayout sizes the tree bottom up. Radii come from the LEAVES —
// sqrt(views), so a leaf's area is its views — and every parent is exactly as
// big as packing its children made it. That is the only honest direction: a
// parent forced to sqrt(its own views) would be smaller than its contents and
// would have to clip them.
function clusterLayout(node) {
  const kids = tKids(node);
  const out = {
    name: node[T_NAME], views: node[T_VIEWS], dur: node[T_DUR],
    x: 0, y: 0, r: 0, kids: null,
  };
  if (!kids || !kids.length) {
    out.r = Math.sqrt(Math.max(node[T_VIEWS], 1));
    return out;
  }
  out.kids = kids.map(clusterLayout);
  out.r = packSiblings(out.kids);
  return out;
}

// ---- the view -----------------------------------------------------------

// Absolute depth 1 is an area, 2 a subject: those two are addresses. A channel
// is where the tree ends, so its circle is a shape and nothing else. The URL
// itself is topicHash's, in the shell — every link into this tree is built in
// one place, or two of them eventually encode a name differently.
const clusterHash = p => (p.length >= 1 && p.length <= 2
  ? topicHash(p[0], p[1] || "") : null);

function renderClusters(root, area, sub) {
  const tree = D.clusters || [];
  let allV = 0, allD = 0;
  for (const n of tree) { allV += n[T_VIEWS]; allD += n[T_DUR]; }

  // The focus travels as names, so it is looked up on every render — and a
  // name that has left the tree has to say so rather than throw.
  let node = ["every topic", allV, allD, tree], path = [];
  if (area) {
    const an = tree.find(n => n[T_NAME] === area);
    if (!an) { root.appendChild($("p", "muted", area + " is not in this tree.")); return; }
    node = an; path = [area];
    if (sub) {
      const sn = (tKids(an) || []).find(n => n[T_NAME] === sub);
      if (!sn) { root.appendChild($("p", "muted", sub + " is not in this tree.")); return; }
      node = sn; path = [area, sub];
    }
  }
  const kids = tKids(node) || [];
  const kind = KIND[Math.min(path.length, 2)];
  const rootName = node[T_NAME] || "unclear";

  root.appendChild($("h2", null, rootName));
  root.appendChild($("p", "muted",
    node[T_VIEWS].toLocaleString() + plural(" view", node[T_VIEWS]) + " · " +
    share(node[T_VIEWS], allV) + " of all " + allV.toLocaleString() + " · " +
    atMost(node[T_DUR]) + " · " +
    kids.length.toLocaleString() + " " + plural(kind, kids.length) + " inside"));

  // The tree answers WHAT was watched; the list answers WHICH. Only a focused
  // node has something to hand over — the root of the tree is the whole list
  // and already sits in the bar above. The count is the node's own, and it is
  // the same number the list will draw: both count every view under the name,
  // overlap included, off the same rows.
  if (path.length) {
    const p = $("p", "muted");
    const a = $("a", "rlink",
      "list the " + node[T_VIEWS].toLocaleString() + plural(" view", node[T_VIEWS]) +
      " behind " + rootName + " →");
    a.href = "#/list/" + path.map(encodeURIComponent).join("/");
    p.appendChild(a);
    root.appendChild(p);
  }

  if (!kids.length) {
    root.appendChild($("p", "muted", "Nothing sits below this one."));
    return;
  }

  // ---- the packing
  packBrake = 0;
  const lay = clusterLayout(node);
  const scale = (Math.min(PACK_W, PACK_H) / 2 - PACK_PAD) / (lay.r || 1);
  const CX = PACK_W / 2, CY = PACK_H / 2;

  const s = svg("svg", {
    role: "img", width: "100%", viewBox: "0 0 " + PACK_W + " " + PACK_H,
  });
  const ttl = svg("title");
  ttl.textContent = rootName + " as nested circles: " + kids.length + " " +
    plural(kind, kids.length) + ", each circle sized by its views";
  s.appendChild(ttl);

  // One group per level, so a deeper circle always paints over the one holding
  // it — and no circle is an ANCESTOR of another in the DOM, which is what
  // keeps a click on a small circle from also firing on its container.
  const layers = [svg("g"), svg("g"), svg("g"), svg("g")];
  const labels = svg("g");
  let drawn = 0, shut = 0;

  function addLabel(name, cx, cy, rr, open) {
    const fs = Math.max(9, Math.min(15, rr / 3.2));
    const room = Math.floor(1.7 * rr / (0.55 * fs));
    if (room < 3) return;
    const txt = name.length > room ? name.slice(0, room - 1) + "…" : name;
    const t = svg("text", {
      x: cx.toFixed(1),
      y: (open ? cy - rr + fs * 1.2 : cy + fs * 0.35).toFixed(1),
      "text-anchor": "middle", "font-size": fs.toFixed(1), "pointer-events": "none",
    });
    if (open) {
      // An opened circle is covered by its own children, so its name only
      // stays readable where it crosses them if it carries a halo.
      t.setAttribute("font-weight", "600");
      t.setAttribute("stroke", "var(--bg)");
      t.setAttribute("stroke-width", "3");
      t.setAttribute("stroke-linejoin", "round");
      t.setAttribute("paint-order", "stroke");
    }
    t.textContent = txt;
    labels.appendChild(t);
  }

  function drawKids(parent, ax, ay, names) {
    for (const k of parent.kids) {
      const p = names.concat(k.name);
      const depth = p.length - path.length;          // 1 = a direct child
      const kx = ax + k.x * scale, ky = ay + k.y * scale, rr = k.r * scale;
      const deep = !!(k.kids && k.kids.length);
      const open = deep && rr >= OPEN_R;
      if (deep && !open) shut++;
      const name = k.name || "unclear";
      const c = svg("circle", {
        cx: kx.toFixed(2), cy: ky.toFixed(2), r: rr.toFixed(2),
        // The hue is the AREA at every depth, so a circle always says which
        // area it belongs to; only the tone separates the levels.
        fill: areaColor(D.areas.indexOf(p[0])),
        "fill-opacity": (open ? 0.16 + 0.04 * depth : 0.40 + 0.08 * depth).toFixed(2),
        stroke: "var(--bg)", "stroke-width": Math.min(1, rr / 8).toFixed(2),
        "stroke-opacity": 0.65,
      });
      hover(c, () => "<b>" + esc(name) + "</b><span class='m'>" +
        k.views.toLocaleString() + plural(" view", k.views) + " · " +
        share(k.views, parent.views) + " of " + esc(parent.name || "unclear") + " · " +
        atMost(k.dur) + "</span>");
      const h = clusterHash(p);
      if (h) clickTo(c, h);
      layers[Math.min(depth, 3)].appendChild(c);
      drawn++;
      if (open) drawKids(k, kx, ky, p);
      if (rr >= LABEL_R) addLabel(name, kx, ky, rr, open);
    }
  }

  // The root is the frame rather than a topic you can enter, so it is drawn as
  // an outline and carries no link.
  const rc = svg("circle", {
    cx: CX, cy: CY, r: (lay.r * scale).toFixed(2),
    fill: path.length ? areaColor(D.areas.indexOf(path[0])) : "var(--card)",
    "fill-opacity": path.length ? 0.10 : 1,
    stroke: "var(--line)", "stroke-width": 1,
  });
  hover(rc, () => "<b>" + esc(rootName) + "</b><span class='m'>" +
    node[T_VIEWS].toLocaleString() + plural(" view", node[T_VIEWS]) + " · " +
    share(node[T_VIEWS], allV) + " of all views · " + atMost(node[T_DUR]) + "</span>");
  layers[0].appendChild(rc);
  drawn++;
  drawKids(lay, CX, CY, path);

  for (const g of layers) s.appendChild(g);
  s.appendChild(labels);

  const panel = $("div", "panel");
  panel.appendChild($("h2", null, "the packing"));
  panel.appendChild($("p", "muted",
    "Circles nest, and a circle is only as big as packing its children made " +
    "it: a LEAF's area is proportional to its views, a parent's only roughly — " +
    "the gaps between children belong to nobody. Hue is the area and " +
    "everything inside keeps it; a pale circle was opened, a solid one is as " +
    "deep as this drawing goes. Click a circle to make it the root. " +
    (shut ? shut.toLocaleString() + " " + plural("circle", shut) +
      " stayed shut because they are under " + OPEN_R +
      " px in radius here, where their own children would be a smear rather " +
      "than a shape — the list below reaches them without aiming. " : "") +
    (packBrake ? "The packer ran out of its step budget on " + packBrake + " " +
      plural("group", packBrake) + "; what was left went onto a ring outside " +
      "them, so those circles are placed rather than packed. " : "") +
    "Every duration is the full length of the videos below it, never watch time."));
  const chart = $("div", "chart");
  chart.appendChild(s);
  panel.appendChild(chart);
  root.appendChild(panel);

  // ---- the same children as a list
  //
  // This is the keyboard path and the precise one: the drawing is a picture
  // you aim at, and a one-pixel circle is not a target. Rows that lead deeper
  // are real links, so tab and back both work without any code of ours.
  const list = $("div", "panel");
  list.appendChild($("h2", null, plural(kind, kids.length)));
  list.appendChild($("p", "muted", "Most-watched first, ties by name — the " +
    "same order the circles were packed in." +
    (kids.length > LIST_MAX
      ? " Showing the " + LIST_MAX + " largest of " + kids.length.toLocaleString() +
        "; " + (kids.length - LIST_MAX).toLocaleString() + " are not listed."
      : "")));
  const stack = $("div", "stack");
  for (const k of kids.slice(0, LIST_MAX)) {
    const p = path.concat(k[T_NAME]);
    const h = clusterHash(p);
    const row = $(h ? "a" : "div", "tile");
    if (h) row.href = h;
    row.style.setProperty("--area", areaColor(D.areas.indexOf(p[0])));
    const l1 = $("div", "l1");
    l1.appendChild($("span", "dot"));
    l1.appendChild($("span", "title", k[T_NAME] || "unclear"));
    l1.appendChild($("span", "clock", k[T_VIEWS].toLocaleString() + plural(" view", k[T_VIEWS])));
    row.appendChild(l1);
    const inner = tKids(k);
    row.appendChild($("div", "l2",
      share(k[T_VIEWS], node[T_VIEWS]) + " of " + rootName + " · " + atMost(k[T_DUR]) +
      (inner ? " · " + inner.length.toLocaleString() + " " +
        plural(KIND[Math.min(p.length, 2)], inner.length) : "")));
    stack.appendChild(row);
  }
  list.appendChild(stack);
  root.appendChild(list);
}
</script>
`
