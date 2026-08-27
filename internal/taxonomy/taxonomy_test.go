// SPDX-License-Identifier: GPL-3.0-or-later

package taxonomy

import (
	"math"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// lab builds a minimal label for clustering fixtures.
func lab(area, sub string, views int) Label {
	return Label{Area: area, Sub: sub, Views: views, Videos: views,
		Channels: map[string]int{}, Tags: map[string]int{}}
}

// labCh is lab with a channel, for the prompts that quote channels.
func labCh(area, sub string, views int, channel string) Label {
	l := lab(area, sub, views)
	l.Channels[channel] = views
	return l
}

func TestCollectAggregatesPerTopic(t *testing.T) {
	views := []View{
		{VideoID: "v1", Area: "music", Sub: "jazz", Channel: "ch1", Title: "t1", Tags: []string{"jazz", "piano"}},
		{VideoID: "v1", Area: "music", Sub: "jazz", Channel: "ch1", Title: "t1", Tags: []string{"jazz", "piano"}},
		{VideoID: "v2", Area: "music", Sub: "jazz", Channel: "ch2", Title: "t2", Tags: []string{"jazz"}},
		{VideoID: "v3", Area: "sports", Sub: "chess", Channel: "ch3", Title: "t3"},
		{VideoID: "v4", Area: "unclear", Sub: "", Channel: "ch4", Title: "t4"},
		{VideoID: "", Area: "music", Sub: "jazz", Channel: "ch1", Title: "deleted one"},
	}
	labels := Collect(views)
	if len(labels) != 2 {
		t.Fatalf("got %d labels, want 2 (unclear skipped): %+v", len(labels), labels)
	}
	jazz := labels[0] // most views first
	if jazz.Topic() != "music/jazz" || jazz.Views != 4 || jazz.Videos != 3 {
		t.Errorf("jazz = %s views %d videos %d, want music/jazz 4 views 3 videos", jazz.Topic(), jazz.Views, jazz.Videos)
	}
	// The repeated watch of v1 counts its tags once: tag counts are per video.
	if jazz.Tags["jazz"] != 2 || jazz.Tags["piano"] != 1 {
		t.Errorf("tags = %v, want jazz:2 piano:1", jazz.Tags)
	}
	if jazz.Channels["ch1"] != 3 {
		t.Errorf("channels = %v, want ch1:3 (views, not videos)", jazz.Channels)
	}
}

func TestEmbedTextCarriesContext(t *testing.T) {
	l := Label{Area: "music", Sub: "free-jazz", Views: 5,
		Channels: map[string]int{"chan a": 3, "chan b": 1},
		Tags:     map[string]int{"jazz": 2},
		Titles:   []string{"song x"}}
	got := l.EmbedText()
	for _, want := range []string{"music free jazz", "chan a", "jazz", "song x"} {
		if !strings.Contains(got, want) {
			t.Errorf("EmbedText misses %q:\n%s", want, got)
		}
	}
}

// The hand-checked fixture: three groups in three corners of a 3-d space —
// illustrations, not anyone's history. jazz sits in music and entertainment
// (the spread this package exists to repair), chess has three language
// spellings, arduino stands alone.
func fixture() ([]Label, [][]float32) {
	labels := []Label{
		lab("music", "jazz", 100),
		lab("entertainment", "jazz", 20),
		lab("sports", "chess", 50),
		lab("sports", "schach", 30),
		lab("entertainment", "ajedrez", 10),
		lab("science-technology", "arduino", 40),
	}
	vecs := [][]float32{
		{1, 0.05, 0},
		{1, 0, 0.05},
		{0, 1, 0.05},
		{0.05, 1, 0},
		{0, 1, 0},
		{0, 0.05, 1},
	}
	return labels, vecs
}

func TestClusterLabelsHandCheckedFixture(t *testing.T) {
	labels, vecs := fixture()
	cs := ClusterLabels(labels, vecs, 0.3)
	if len(cs) != 3 {
		t.Fatalf("got %d clusters, want 3: %+v", len(cs), names(cs))
	}
	// Views-sorted: jazz (120) first, chess (90) second, arduino (40) last.
	if got := topics(cs[0]); !reflect.DeepEqual(got, []string{"music/jazz", "entertainment/jazz"}) {
		t.Errorf("cluster 0 = %v", got)
	}
	if got := topics(cs[1]); !reflect.DeepEqual(got, []string{"sports/chess", "sports/schach", "entertainment/ajedrez"}) {
		t.Errorf("cluster 1 = %v", got)
	}
	if cs[0].Name != "jazz" || cs[1].Name != "chess" {
		t.Errorf("fallback names = %q, %q", cs[0].Name, cs[1].Name)
	}
	if cs[0].Views != 120 || cs[0].Videos != 120 {
		t.Errorf("cluster 0 views %d videos %d, want 120/120", cs[0].Views, cs[0].Videos)
	}
}

func TestClusterThresholdAtTheEdge(t *testing.T) {
	// cos([1,0],[1,1]) = 1/sqrt(2): distance 0.2929 exactly.
	labels := []Label{lab("a", "x", 1), lab("a", "y", 1)}
	vecs := [][]float32{{1, 0}, {1, 1}}
	edge := 1 - 1/math.Sqrt2
	if cs := ClusterLabels(labels, vecs, edge+0.01); len(cs) != 1 {
		t.Errorf("just above the distance: %d clusters, want 1", len(cs))
	}
	if cs := ClusterLabels(labels, vecs, edge-0.01); len(cs) != 2 {
		t.Errorf("just below the distance: %d clusters, want 2", len(cs))
	}
}

func TestClusteringIsDeterministic(t *testing.T) {
	// A few dozen synthetic labels on a ring — no randomness, so two runs
	// must produce byte-identical assignments.
	var labels []Label
	var vecs [][]float32
	for i := 0; i < 40; i++ {
		labels = append(labels, lab("a", slug(i), 1+i%7))
		angle := float64(i) * 0.37
		vecs = append(vecs, []float32{float32(math.Cos(angle)), float32(math.Sin(angle)), float32(i % 3)})
	}
	a := ClusterLabels(labels, vecs, 0.2)
	b := ClusterLabels(labels, vecs, 0.2)
	if !reflect.DeepEqual(allTopics(a), allTopics(b)) {
		t.Error("two clustering runs disagree")
	}
}

func slug(i int) string { return string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) }

// TestAgglomerateIsIndependentOfCoreCount pins the promise the parallel
// distance loops make: the grouping is decided by the input alone, never by
// how many workers happened to run it. TestClusteringIsDeterministic covers
// repeatability at 40 labels, which is below parMinRows and therefore never
// leaves the serial path — this one deliberately sits above it and compares
// one core against many.
func TestAgglomerateIsIndependentOfCoreCount(t *testing.T) {
	// Above parMinRows, or both runs take the serial path and the test proves
	// nothing about the change it is here to guard.
	const n = parMinRows + 64
	vecs := make([][]float32, n)
	weights := make([]int, n)
	for i := range vecs {
		// Sixteen loose groups on a ring, every point nudged off its centre:
		// the fixture has to actually merge, because the row update is the
		// second parallel loop and an all-singleton run would never reach it.
		angle := float64(i%16)*(2*math.Pi/16) + float64(i/16)*0.004
		v := make([]float32, 64)
		v[0] = float32(math.Cos(angle))
		v[1] = float32(math.Sin(angle))
		v[2+i%62] = float32(0.01 * float64(i%7))
		vecs[i] = v
		weights[i] = 1 + i%5
	}

	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)
	serial := agglomerate(vecs, weights, 0.15)
	runtime.GOMAXPROCS(8)
	parallel := agglomerate(vecs, weights, 0.15)

	if len(serial) == n {
		t.Fatal("fixture produced no merges — the parallel row update never ran, so this test guards nothing")
	}
	if !reflect.DeepEqual(serial, parallel) {
		t.Errorf("grouping depends on core count: %d groups on one core, %d on eight",
			len(serial), len(parallel))
	}
}

func TestFoldSmallFoldsIntoNearest(t *testing.T) {
	labels := []Label{lab("a", "big-x", 50), lab("a", "big-y", 50), lab("a", "tiny", 1)}
	vecs := [][]float32{{1, 0}, {0, 1}, {0.9, 0.1}}
	cs := ClusterLabels(labels, vecs, 0.001) // three singleton clusters (tiny sits at d≈0.006 from big-x)
	if len(cs) != 3 {
		t.Fatalf("setup: %d clusters", len(cs))
	}
	folded := FoldSmall(cs, 2, nil)
	if len(folded) != 2 {
		t.Fatalf("got %d clusters, want 2", len(folded))
	}
	// tiny is closest to big-x.
	for _, c := range folded {
		if c.Name == "big-x" && len(c.Members) != 2 {
			t.Errorf("tiny did not fold into big-x: %v", topics(c))
		}
	}
	// keep exempts it.
	kept := FoldSmall(cs, 2, map[string]bool{"tiny": true})
	if len(kept) != 3 {
		t.Errorf("keep ignored: %d clusters, want 3", len(kept))
	}
}

// The shape that made a stray a top level of one subject and three views:
// a subject sitting far enough from everything else forms a coarse group of
// its own, and nothing used to stop that group from becoming a section.
func TestFoldSmallGroupsAbsorbsAStraySection(t *testing.T) {
	// assignParents runs after naming, so the subjects carry their names —
	// that is what the keep set matches on.
	mk := func(name string, l Label, v []float32) Cluster {
		c := newCluster([]Label{l}, [][]float32{v})
		c.Name = name
		return c
	}
	cs := []Cluster{
		mk("x-big", lab("area-x", "x-big", 200), []float32{1, 0, 0}),
		mk("x-mid", lab("area-x", "x-mid", 120), []float32{0.98, 0.2, 0}),
		mk("y-big", lab("area-y", "y-big", 300), []float32{0, 1, 0}),
		mk("x-stray", lab("area-x", "x-stray", 3), []float32{0.7, 0.05, 0.7}),
	}
	groups := [][]int{{0, 1}, {2}, {3}}

	folded := FoldSmallGroups(cs, groups, 25, nil)
	if len(folded) != 2 {
		t.Fatalf("got %d groups, want 2 — the stray should not stay a section: %v", len(folded), folded)
	}
	// It lands with its own family, not with the far one: that is the whole
	// point of folding by centroid rather than dropping it.
	var host []int
	for _, g := range folded {
		for _, i := range g {
			if cs[i].Name == "x-stray" {
				host = g
			}
		}
	}
	names := map[string]bool{}
	for _, i := range host {
		names[cs[i].Name] = true
	}
	if !names["x-big"] || !names["x-mid"] || names["y-big"] {
		t.Errorf("the stray landed in the wrong section: %v", names)
	}
	// The subjects themselves are untouched — only the grouping moved.
	if len(cs[3].Members) != 1 || cs[3].Name != "x-stray" {
		t.Errorf("folding rewrote the subject: %+v", cs[3])
	}

	// A keep name protects its group.
	kept := FoldSmallGroups(cs, groups, 25, map[string]bool{"x-stray": true})
	if len(kept) != 3 {
		t.Errorf("keep ignored: %d groups, want 3", len(kept))
	}
	// And when nothing clears the bar, everything stays.
	if all := FoldSmallGroups(cs, groups, 100000, nil); len(all) != 3 {
		t.Errorf("a tiny corpus is not a tail: %d groups, want 3", len(all))
	}
}

func TestGroupPromptCarriesEveryCluster(t *testing.T) {
	// Three subjects of one coarse group, each with several labels and its
	// own channels — the shape that named a 31-subject music top level after
	// its biggest part.
	mk := func(a, b Label, va, vb []float32) Cluster {
		return newCluster([]Label{a, b}, [][]float32{va, vb})
	}
	cs := []Cluster{
		mk(labCh("area-a", "a1", 100, "a1chan"), labCh("area-a", "a2", 40, "a2chan"), []float32{1, 0, 0}, []float32{1, 0.1, 0}),
		mk(labCh("area-a", "b1", 50, "b1chan"), labCh("area-a", "b2", 20, "b2chan"), []float32{0, 1, 0}, []float32{0.1, 1, 0}),
		mk(labCh("area-a", "c1", 30, "c1chan"), labCh("area-a", "c2", 10, "c2chan"), []float32{0, 0, 1}, []float32{0, 0.1, 1}),
	}

	// An empty cluster in the group is skipped, not carried as a member.
	p := GroupPrompt(append(cs, Cluster{}), []int{0, 1, 2, 3})
	if got, want := topics(p), []string{"area-a/a1", "area-a/b1", "area-a/c1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("prompt members = %v, want the strongest label of each cluster %v", got, want)
	}
	if p.Views != 180 {
		t.Errorf("prompt views = %d, want 180 (100+50+30)", p.Views)
	}
	// The channels a top-level prompt shows must come from the whole group;
	// before, all five were the biggest subject's.
	if got, want := p.TopChannels(5), []string{"a1chan", "b1chan", "c1chan"}; !reflect.DeepEqual(got, want) {
		t.Errorf("TopChannels = %v, want one channel from each cluster %v", got, want)
	}

	// A group of one IS that cluster: cutting it down to its strongest label
	// would take context away from the namer instead of adding any.
	if solo := GroupPrompt(cs, []int{1}); !reflect.DeepEqual(topics(solo), topics(cs[1])) {
		t.Errorf("solo group = %v, want the cluster itself %v", topics(solo), topics(cs[1]))
	}
}

func TestRefineSplitsWhereCoherenceTears(t *testing.T) {
	// Two far-apart pairs forced into one cluster by a huge threshold.
	labels := []Label{lab("a", "x1", 10), lab("a", "x2", 10), lab("a", "y1", 10), lab("a", "y2", 10)}
	vecs := [][]float32{{1, 0.02, 0}, {1, 0, 0.02}, {0.02, 1, 0}, {0, 1, 0.02}}
	cs := ClusterLabels(labels, vecs, 0.99) // the pair centroids sit at d≈0.98
	if len(cs) != 1 {
		t.Fatalf("setup: %d clusters, want 1 overwide cluster", len(cs))
	}
	out, changes := Refine(cs, Control{}, RefineOpts{SplitAt: 0.1, MaxRadius: 0.2, MergeBelow: 0.05, MinVideos: 1})
	if len(out) != 2 {
		t.Fatalf("got %d clusters after refine, want 2: %v", len(out), names(out))
	}
	if len(changes) == 0 || changes[0].Op != "split" {
		t.Errorf("changes = %v, want a split first", changes)
	}
	for _, c := range out {
		if c.Name != "" {
			t.Errorf("split pieces must come back unnamed, got %q", c.Name)
		}
	}
}

func TestRefineSkipsBlockedNames(t *testing.T) {
	// The same over-wide cluster as above: it tears without the block, and
	// stays whole once its name carries one — because the last split of it
	// was undone by the same-name merge, so repeating it is a loop.
	labels := []Label{lab("a", "x1", 10), lab("a", "x2", 10), lab("a", "y1", 10), lab("a", "y2", 10)}
	vecs := [][]float32{{1, 0.02, 0}, {1, 0, 0.02}, {0.02, 1, 0}, {0, 1, 0.02}}
	cs := ClusterLabels(labels, vecs, 0.99)
	if len(cs) != 1 {
		t.Fatalf("setup: %d clusters, want 1 overwide cluster", len(cs))
	}
	opts := RefineOpts{SplitAt: 0.1, MaxRadius: 0.2, MergeBelow: 0.05, MinVideos: 1}
	if out, _ := Refine(cs, Control{}, opts); len(out) != 2 {
		t.Fatalf("unblocked: %d clusters, want the radius trigger to split it", len(out))
	}

	opts.NoSplit = map[string]bool{cs[0].Name: true}
	out, changes := Refine(cs, Control{}, opts)
	if len(out) != 1 {
		t.Errorf("blocked %q still split into %d: %v", cs[0].Name, len(out), names(out))
	}
	for _, c := range changes {
		if c.Op == "split" {
			t.Errorf("blocked name produced a split change: %v", c)
		}
	}

	// The block stands down the automatic trigger only — a split a human
	// asked for through the control file still happens.
	if out, _ := Refine(cs, Control{Split: []string{cs[0].Name}}, opts); len(out) != 2 {
		t.Errorf("control split ignored the wish: %d clusters, want 2", len(out))
	}
}

func TestRefineMergesBelowThreshold(t *testing.T) {
	labels := []Label{lab("a", "x", 30), lab("a", "y", 10)}
	vecs := [][]float32{{1, 0.01}, {1, 0}}
	cs := ClusterLabels(labels, vecs, 0.000001)
	if len(cs) != 2 {
		t.Fatalf("setup: %d clusters", len(cs))
	}
	out, changes := Refine(cs, Control{}, RefineOpts{SplitAt: 0.001, MaxRadius: 0.9, MergeBelow: 0.01, MinVideos: 1})
	if len(out) != 1 || out[0].Name != "x" {
		t.Fatalf("got %v, want one cluster named after the stronger side", names(out))
	}
	if len(changes) != 1 || changes[0].Op != "merge" || changes[0].From != "y" || changes[0].To != "x" {
		t.Errorf("changes = %v", changes)
	}
}

func TestRefineHonorsControl(t *testing.T) {
	labels := []Label{lab("a", "x", 30), lab("a", "y", 10), lab("b", "z", 20)}
	vecs := [][]float32{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	cs := ClusterLabels(labels, vecs, 0.001)
	ctl := Control{
		Merge: [][]string{{"y", "z"}},
		Pin:   map[string]string{"a/x": "y"},
	}
	out, changes := Refine(cs, ctl, RefineOpts{SplitAt: 0.001, MaxRadius: 0.9, MergeBelow: 0.0001, MinVideos: 1})
	if len(out) != 1 || out[0].Name != "y" {
		t.Fatalf("got %v, want everything pinned and merged into y", names(out))
	}
	ops := map[string]bool{}
	for _, c := range changes {
		ops[c.Op] = true
	}
	if !ops["pin"] || !ops["merge"] {
		t.Errorf("changes = %v, want a pin and a merge", changes)
	}
}

func TestFoldProjectsAndPassesThrough(t *testing.T) {
	tax := Taxonomy{Map: map[string]string{
		"entertainment/jazz": "music/jazz",
		"music/jazz":         "music/jazz",
		"music":              "music",
	}}
	cases := map[string]string{
		"entertainment/jazz":  "music/jazz", // the projection itself
		"music/brand-new-sub": "music/brand-new-sub",
		"unclear":             "unclear",     // never mapped
		"gaming/rust":         "gaming/rust", // unknown area passes through
	}
	for in, want := range cases {
		if got := tax.Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
	// The area fallback replaces only the top level and keeps the sub.
	tax.Map["entertainment"] = "shows/late-night"
	if got := tax.Fold("entertainment/some-new-thing"); got != "shows/some-new-thing" {
		t.Errorf("area fallback = %q, want shows/some-new-thing", got)
	}
}

func TestBuildAndWriteRoundtrip(t *testing.T) {
	labels, vecs := fixture()
	cs := ClusterLabels(labels, vecs, 0.3)
	for i := range cs {
		cs[i].Parent = "top-" + cs[i].Name
	}
	cs[2].Name = cs[2].Parent // a subject named like its top collapses to the bare top

	built := Build(cs)
	if got := built.Map["entertainment/jazz"]; got != "top-jazz/jazz" {
		t.Errorf("Build maps to %q", got)
	}

	path := filepath.Join(t.TempDir(), "taxonomy.yaml")
	if err := WriteFile(path, cs, []string{"test header"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Map, built.Map) {
		t.Errorf("roundtrip drifted:\nwrote %v\nread  %v", built.Map, loaded.Map)
	}
	if got := loaded.Fold("science-technology/arduino"); got != cs[2].Parent {
		t.Errorf("bare-top subject folds to %q, want %q", got, cs[2].Parent)
	}
}

func TestMeasureSpreadAndTail(t *testing.T) {
	labels, _ := fixture()
	base := MeasureLabels(labels, 25)
	if base.Spread != 1 { // jazz under music AND entertainment
		t.Errorf("baseline spread = %d, want 1", base.Spread)
	}
	if base.Subjects != 6 || base.Tops != 4 {
		t.Errorf("baseline subjects/tops = %d/%d", base.Subjects, base.Tops)
	}
	if want := 2.0 / 6.0; math.Abs(base.TailShare-want) > 1e-9 {
		t.Errorf("baseline tail = %f, want %f", base.TailShare, want)
	}

	labels2, vecs := fixture()
	cs := ClusterLabels(labels2, vecs, 0.3)
	for i := range cs {
		cs[i].Parent = "p"
	}
	m := Measure(cs, 25)
	if m.Spread != 0 || m.Subjects != 3 || m.Tops != 1 {
		t.Errorf("clustered metrics = %+v", m)
	}
}

func TestLoadControlMissingFileIsEmpty(t *testing.T) {
	c, err := LoadControl(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil || c.Stop || len(c.Pin) != 0 {
		t.Errorf("missing control file: %+v, %v", c, err)
	}
}

func names(cs []Cluster) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Name
	}
	return out
}

func topics(c Cluster) []string {
	out := make([]string, len(c.Members))
	for i, l := range c.Members {
		out[i] = l.Topic()
	}
	return out
}

func allTopics(cs []Cluster) [][]string {
	out := make([][]string, len(cs))
	for i, c := range cs {
		out[i] = topics(c)
	}
	return out
}
