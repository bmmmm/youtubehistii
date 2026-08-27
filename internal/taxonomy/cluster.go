// SPDX-License-Identifier: GPL-3.0-or-later

package taxonomy

import (
	"runtime"
	"sort"
	"sync"
)

// Cluster is one group of labels: a subject after the fine pass; the coarse
// pass then groups subjects into top levels via Coarse.
type Cluster struct {
	Name     string // subject slug, "" until named
	Parent   string // top-level slug, "" until the coarse pass assigns one
	Members  []Label
	Views    int
	Videos   int
	Radius   float64 // views-weighted mean cosine distance member -> centroid
	Centroid []float32

	vecs [][]float32 // member vectors, parallel to Members — kept for splits
}

// parMinRows is the row count below which spreading the work costs more than
// it saves. A row is len(vecs) cosine calls over 1024 dimensions, so the work
// per goroutine shrinks with n while the goroutine costs a fixed ~1 µs —
// and SplitCluster runs this same agglomeration on clusters of a dozen
// members, dozens of times per refinement round. Measured on the real corpus,
// see the parallel note on agglomerate.
const parMinRows = 256

// parRows runs body(i) for every i in [0,n), spread over the available cores
// once n is worth it. Rows are handed out interleaved rather than in blocks:
// the distance matrix is triangular, so row i holds exactly i cells and
// contiguous chunks would leave the first worker idle while the last one does
// most of the work.
//
// body must write only to slots no other index touches — then the result is
// bit-identical to the serial loop, because no float is ever summed in a
// racing order. That is what keeps agglomerate's determinism promise.
func parRows(n int, body func(i int)) {
	w := runtime.GOMAXPROCS(0)
	if n < parMinRows || w < 2 {
		for i := 0; i < n; i++ {
			body(i)
		}
		return
	}
	if w > n {
		w = n
	}
	var wg sync.WaitGroup
	for t := 0; t < w; t++ {
		wg.Add(1)
		go func(start int) {
			defer wg.Done()
			for i := start; i < n; i += w {
				body(i)
			}
		}(t)
	}
	wg.Wait()
}

// agglomerate merges the closest pair of groups until the smallest centroid
// distance (1 - cosine) exceeds threshold, weight-averaging centroids.
// Deterministic: same input order, same float math, ties break on the lowest
// index pair. Returns groups of input indices, each sorted ascending.
//
// The two cosine loops — the initial matrix and the row one merge recomputes —
// run in parallel. They are 98.7% of this function's time on the real corpus
// (2679 labels: 5.90 s matrix, 5.43 s row updates, 0.15 s for everything
// else), and they are memory-bound rather than arithmetic-bound: dropping the
// redundant renormalization inside Cosine, which is 3.7x cheaper measured on
// its own (1616 ns -> 432 ns), moved this function by 2.9% — the loop waits on
// 6.87 million vector pairs of 4 KB each, not on the multiplications. Spread
// over cores instead, the same arithmetic runs 6.2x faster: 11.51 s -> 1.87 s,
// with a grouping identical member for member.
func agglomerate(vecs [][]float32, weights []int, threshold float64) [][]int {
	n := len(vecs)
	if n == 0 {
		return nil
	}
	groups := make([][]int, n)
	cents := make([][]float32, n)
	wsum := make([]int, n)
	alive := make([]bool, n)
	for i := range vecs {
		groups[i] = []int{i}
		cents[i] = vecs[i]
		wsum[i] = max(weights[i], 1)
		alive[i] = true
	}
	// Full distance matrix plus a nearest-neighbor index per group: the min
	// scan is O(n) per merge and a merge recomputes one row — O(n²·d) overall,
	// which holds comfortably for the few thousand labels a corpus produces.
	//
	// The lower triangle is computed in parallel and mirrored afterwards, so
	// each worker writes only into its own row while the cosines are running.
	dist := make([][]float64, n)
	for i := range dist {
		dist[i] = make([]float64, n)
	}
	parRows(n, func(i int) {
		for j := 0; j < i; j++ {
			dist[i][j] = 1 - Cosine(cents[i], cents[j])
		}
	})
	for i := 0; i < n; i++ {
		for j := 0; j < i; j++ {
			dist[j][i] = dist[i][j]
		}
	}
	nn := make([]int, n)
	rescan := func(i int) {
		nn[i] = -1
		best := 0.0
		for j := 0; j < n; j++ {
			if j == i || !alive[j] {
				continue
			}
			if nn[i] == -1 || dist[i][j] < best {
				nn[i], best = j, dist[i][j]
			}
		}
	}
	for i := 0; i < n; i++ {
		rescan(i)
	}

	for {
		// The closest pair overall, ties on the lowest (i, j).
		bi := -1
		best := 0.0
		for i := 0; i < n; i++ {
			if !alive[i] || nn[i] == -1 {
				continue
			}
			if bi == -1 || dist[i][nn[i]] < best {
				bi, best = i, dist[i][nn[i]]
			}
		}
		if bi == -1 || best > threshold {
			break
		}
		bj := nn[bi]
		if bj < bi {
			bi, bj = bj, bi
		}
		// Merge bj into bi: weighted centroid, one recomputed matrix row.
		groups[bi] = append(groups[bi], groups[bj]...)
		cents[bi] = Centroid([][]float32{cents[bi], cents[bj]}, []int{wsum[bi], wsum[bj]})
		wsum[bi] += wsum[bj]
		alive[bj] = false
		// One cell per k, each written by exactly one worker: dist[bi][k] sits
		// in the merged row and dist[k][bi] in k's own, and no other k touches
		// either. The merged centroid is read-only for the whole sweep.
		row := cents[bi]
		merged := bi
		parRows(n, func(k int) {
			if !alive[k] || k == merged {
				return
			}
			d := 1 - Cosine(row, cents[k])
			dist[merged][k], dist[k][merged] = d, d
		})
		rescan(bi)
		for k := 0; k < n; k++ {
			if !alive[k] || k == bi {
				continue
			}
			if nn[k] == bi || nn[k] == bj || dist[k][bi] < dist[k][nn[k]] {
				rescan(k)
			}
		}
	}

	var out [][]int
	for i := 0; i < n; i++ {
		if alive[i] {
			sort.Ints(groups[i])
			out = append(out, groups[i])
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a][0] < out[b][0] })
	return out
}

// newCluster assembles one cluster from labels and their vectors, computing
// weight, centroid and radius. Members sort by views desc, topic asc.
func newCluster(labels []Label, vecs [][]float32) Cluster {
	idx := make([]int, len(labels))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool {
		la, lb := labels[idx[a]], labels[idx[b]]
		if la.Views != lb.Views {
			return la.Views > lb.Views
		}
		return la.Topic() < lb.Topic()
	})
	c := Cluster{}
	weights := make([]int, len(idx))
	for n, i := range idx {
		c.Members = append(c.Members, labels[i])
		c.vecs = append(c.vecs, vecs[i])
		c.Views += labels[i].Views
		c.Videos += labels[i].Videos
		weights[n] = labels[i].Views
	}
	c.Centroid = Centroid(c.vecs, weights)
	var dsum, wtot float64
	for n := range c.Members {
		w := float64(max(c.Members[n].Views, 1))
		dsum += w * (1 - Cosine(c.vecs[n], c.Centroid))
		wtot += w
	}
	if wtot > 0 {
		c.Radius = dsum / wtot
	}
	return c
}

// ClusterLabels groups labels into subjects: agglomerative over cosine
// distance, views-weighted, stopping at the fine threshold. Every cluster
// starts out named by its strongest member (FallbackName), so the control
// file can address clusters even before an LLM has named anything.
func ClusterLabels(labels []Label, vecs [][]float32, threshold float64) []Cluster {
	var out []Cluster
	for _, g := range agglomerate(vecs, viewWeights(labels), threshold) {
		ls := make([]Label, len(g))
		vs := make([][]float32, len(g))
		for n, i := range g {
			ls[n], vs[n] = labels[i], vecs[i]
		}
		c := newCluster(ls, vs)
		c.Name = FallbackName(c)
		out = append(out, c)
	}
	sortClusters(out)
	return out
}

func viewWeights(labels []Label) []int {
	w := make([]int, len(labels))
	for i, l := range labels {
		w[i] = l.Views
	}
	return w
}

func sortClusters(cs []Cluster) {
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Views != cs[j].Views {
			return cs[i].Views > cs[j].Views
		}
		return cs[i].Members[0].Topic() < cs[j].Members[0].Topic()
	})
}

// TopChannels returns the cluster's strongest channels across all members —
// the context a naming prompt gets alongside the member labels.
func (c Cluster) TopChannels(n int) []string {
	sum := map[string]int{}
	for _, l := range c.Members {
		for ch, v := range l.Channels {
			sum[ch] += v
		}
	}
	return topKeys(sum, n)
}

// FallbackName names a cluster without a model: the sub of its strongest
// member, or that member's area when no member carries a sub.
func FallbackName(c Cluster) string {
	for _, l := range c.Members {
		if l.Sub != "" {
			return l.Sub
		}
	}
	return c.Members[0].Area
}

// FoldSmall folds every cluster with fewer than minVideos unique videos into
// the nearest (by centroid) cluster that clears the bar. Keep names subjects
// exempt from folding. When nothing clears the bar, everything stays — a
// tiny corpus is not a tail.
func FoldSmall(cs []Cluster, minVideos int, keep map[string]bool) []Cluster {
	var big, small []Cluster
	for _, c := range cs {
		if c.Videos >= minVideos || keep[c.Name] {
			big = append(big, c)
		} else {
			small = append(small, c)
		}
	}
	if len(big) == 0 {
		return cs
	}
	for _, s := range small {
		bi, best := 0, -2.0
		for i := range big {
			if sim := Cosine(s.Centroid, big[i].Centroid); sim > best {
				bi, best = i, sim
			}
		}
		big[bi] = MergeClusters(big[bi], s)
	}
	sortClusters(big)
	return big
}

// MergeClusters joins two clusters; the views-stronger side keeps the name.
func MergeClusters(a, b Cluster) Cluster {
	name := a.Name
	if b.Views > a.Views {
		name = b.Name
	}
	c := newCluster(append(append([]Label{}, a.Members...), b.Members...),
		append(append([][]float32{}, a.vecs...), b.vecs...))
	c.Name = name
	c.Parent = a.Parent
	if b.Views > a.Views {
		c.Parent = b.Parent
	}
	return c
}

// SplitCluster re-clusters one cluster's members at a tighter threshold.
// Returns nil when the members do not separate — the caller keeps the
// original. The pieces come back unnamed; naming is the caller's round.
func SplitCluster(c Cluster, threshold float64) []Cluster {
	if len(c.Members) < 2 {
		return nil
	}
	groups := agglomerate(c.vecs, viewWeights(c.Members), threshold)
	if len(groups) < 2 {
		return nil
	}
	var out []Cluster
	for _, g := range groups {
		ls := make([]Label, len(g))
		vs := make([][]float32, len(g))
		for n, i := range g {
			ls[n], vs[n] = c.Members[i], c.vecs[i]
		}
		nc := newCluster(ls, vs)
		nc.Parent = c.Parent
		out = append(out, nc)
	}
	sortClusters(out)
	return out
}

// Coarse groups subject clusters into top levels: the same agglomeration
// over the subjects' centroids, views-weighted, at the looser threshold.
// Returns groups of subject indices.
func Coarse(subjects []Cluster, threshold float64) [][]int {
	vecs := make([][]float32, len(subjects))
	weights := make([]int, len(subjects))
	for i, c := range subjects {
		vecs[i] = c.Centroid
		weights[i] = c.Views
	}
	return agglomerate(vecs, weights, threshold)
}

// FoldSmallGroups moves the subjects of every coarse group under the bar into
// the nearest group that clears it. Subjects have had this bar since the
// beginning (FoldSmall, -min-videos); top levels had none, so a subject whose
// centroid sat far enough from everything else became its own top level no
// matter how small it was — one real run had a top level of a single subject
// and three views, whose own label already named the section it belonged in.
//
// The subjects themselves are untouched: only the grouping changes, so a
// folded stray keeps its own name and simply sits under a real section. Keep
// names protect their whole group, and when nothing clears the bar everything
// stays — a tiny corpus is not a tail.
func FoldSmallGroups(subjects []Cluster, groups [][]int, minVideos int, keep map[string]bool) [][]int {
	center := func(g []int) []float32 {
		vecs := make([][]float32, 0, len(g))
		weights := make([]int, 0, len(g))
		for _, i := range g {
			vecs = append(vecs, subjects[i].Centroid)
			weights = append(weights, subjects[i].Views)
		}
		return Centroid(vecs, weights)
	}
	clears := func(g []int) bool {
		videos := 0
		for _, i := range g {
			if keep[subjects[i].Name] {
				return true
			}
			videos += subjects[i].Videos
		}
		return videos >= minVideos
	}

	// The kept groups are copied: this appends to them and sorts them, and
	// doing that to the caller's slices would edit the grouping it passed in.
	var big, small [][]int
	for _, g := range groups {
		if clears(g) {
			big = append(big, append([]int(nil), g...))
		} else {
			small = append(small, g)
		}
	}
	if len(big) == 0 {
		return groups
	}

	centers := make([][]float32, len(big))
	for i, g := range big {
		centers[i] = center(g)
	}
	for _, s := range small {
		sc := center(s)
		bi, best := 0, -2.0
		for i := range big {
			if sim := Cosine(sc, centers[i]); sim > best {
				bi, best = i, sim
			}
		}
		big[bi] = append(big[bi], s...)
		centers[bi] = center(big[bi])
	}
	for _, g := range big {
		sort.Ints(g)
	}
	return big
}

// GroupPrompt builds a stand-in cluster from the strongest label of every
// cluster in the group, so the namer sees the whole group instead of its
// biggest part.
//
// One label per cluster, not the group's strongest labels overall: a namer
// reads only the first handful of members, and the biggest subject would
// otherwise fill that prompt on its own — which is exactly how a top level
// covering 31 subjects ended up named after one of them.
func GroupPrompt(cs []Cluster, group []int) Cluster {
	// A group of one IS that cluster: cutting it down to a single label
	// would take context away from the namer instead of adding any.
	if len(group) == 1 {
		return cs[group[0]]
	}
	var labels []Label
	var vecs [][]float32
	for _, i := range group {
		if len(cs[i].Members) == 0 {
			continue // nothing to say — an empty cluster names nothing
		}
		// newCluster sorts members views-desc, so Members[0] is this
		// cluster's strongest label.
		labels = append(labels, cs[i].Members[0])
		vecs = append(vecs, cs[i].vecs[0])
	}
	if len(labels) == 0 {
		return Cluster{}
	}
	return newCluster(labels, vecs)
}

// MergeSameNames folds clusters that ended up with the same name into one.
// Two clusters the namer cannot tell apart ARE the same subject — and unique
// names are what guarantees a subject sits under exactly one top level.
func MergeSameNames(cs []Cluster) []Cluster {
	byName := map[string]int{}
	var out []Cluster
	for _, c := range cs {
		if i, ok := byName[c.Name]; ok && c.Name != "" {
			out[i] = MergeClusters(out[i], c)
			continue
		}
		byName[c.Name] = len(out)
		out = append(out, c)
	}
	sortClusters(out)
	return out
}
