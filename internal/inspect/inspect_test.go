// SPDX-License-Identifier: GPL-3.0-or-later

package inspect

import (
	"strings"
	"testing"

	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

func sample() ([]takeout.View, map[string]enrich.Meta) {
	views := []takeout.View{
		{VideoID: "a"}, {VideoID: "a"}, {VideoID: "a"}, // rewatched three times
		{VideoID: "b"},
		{VideoID: "c"},
		{VideoID: "gone"},
		{VideoID: "pending"},
		{VideoID: ""}, // deleted/private: no id, counted nowhere
	}
	metas := map[string]enrich.Meta{
		"a":    {ID: "a", Categories: []string{"Gaming"}, Tags: []string{"Factorio", "factorio", "lets play"}},
		"b":    {ID: "b", Categories: []string{"Gaming", "Entertainment"}, Tags: []string{"lets play"}},
		"c":    {ID: "c"}, // enriched but YouTube has no category for it
		"gone": {ID: "gone", Unavailable: true},
		// "pending" is deliberately absent: enrich has not reached it yet.
	}
	return views, metas
}

func TestAggregate(t *testing.T) {
	views, metas := sample()
	st := Aggregate(views, metas, 15)

	if st.Views != 7 || st.Videos != 5 {
		t.Errorf("views=%d videos=%d, want 7 and 5 (the id-less view counts nowhere)", st.Views, st.Videos)
	}
	if st.Enriched != 3 || st.Tombstoned != 1 || st.Unfetched != 1 {
		t.Errorf("enriched=%d tombstoned=%d unfetched=%d", st.Enriched, st.Tombstoned, st.Unfetched)
	}
	if st.MultiCategory != 1 {
		t.Errorf("multiCategory=%d, want 1", st.MultiCategory)
	}
	if st.VideosWithCat != 2 || st.VideosWithTags != 2 {
		t.Errorf("videosWithCat=%d videosWithTags=%d", st.VideosWithCat, st.VideosWithTags)
	}

	// Categories are weighted by VIEWS: "a" alone brings three.
	top := st.Categories[0]
	if top.Name != "Gaming" || top.Views != 4 || top.Videos != 2 {
		t.Fatalf("top category = %+v, want Gaming with 4 views / 2 videos", top)
	}
	// Tags are weighted by VIDEOS and deduplicated case-insensitively, so a
	// rewatch cannot inflate them and "Factorio"/"factorio" is one tag.
	want := map[string]int{"lets play": 2, "factorio": 1}
	if len(top.Tags) != len(want) {
		t.Fatalf("tags = %+v, want %v", top.Tags, want)
	}
	for _, tc := range top.Tags {
		if want[tc.Tag] != tc.Videos {
			t.Errorf("tag %q = %d videos, want %d", tc.Tag, tc.Videos, want[tc.Tag])
		}
	}

	// A video without a category still shows up, under its own bucket.
	var uncategorized bool
	for _, c := range st.Categories {
		if c.Name == "" {
			uncategorized = c.Videos == 1
		}
	}
	if !uncategorized {
		t.Errorf("the uncategorized video needs its own row: %+v", st.Categories)
	}
}

func TestAggregateTagLimit(t *testing.T) {
	views := []takeout.View{{VideoID: "a"}}
	metas := map[string]enrich.Meta{"a": {ID: "a", Categories: []string{"Gaming"},
		Tags: []string{"t1", "t2", "t3", "t4", "t5"}}}
	if got := Aggregate(views, metas, 2).Categories[0].Tags; len(got) != 2 {
		t.Errorf("tags = %d, want them capped at 2", len(got))
	}
}

func TestRenderOmitsTagsByDefault(t *testing.T) {
	views, metas := sample()
	st := Aggregate(views, metas, 15)
	plain := Render(st, false, false)
	if strings.Contains(plain, "factorio") {
		t.Error("creator tags must stay hidden unless asked for — they carry names")
	}
	for _, want := range []string{"Gaming", "category coverage", "tag coverage", "not fetched yet"} {
		if !strings.Contains(plain, want) {
			t.Errorf("summary misses %q in:\n%s", want, plain)
		}
	}
	if withTags := Render(st, true, false); !strings.Contains(withTags, "factorio") {
		t.Errorf("-tags must show them:\n%s", withTags)
	}
}

func TestRenderEmptyCache(t *testing.T) {
	txt := Render(Aggregate([]takeout.View{{VideoID: "a"}}, map[string]enrich.Meta{}, 5), true, true)
	if !strings.Contains(txt, "run \"enrich\" first") {
		t.Errorf("an empty cache should say what to do:\n%s", txt)
	}
}

func chanURL(id string) string { return "https://www.youtube.com/channel/" + id }

// channelSample covers every case the inheritance funnel has to separate: a
// channel that agrees with itself, one that does not, one too small to count,
// one with nothing to give, and a video with no channel at all.
func channelSample() ([]takeout.View, map[string]enrich.Meta) {
	views := []takeout.View{
		{VideoID: "u1", ChannelURL: chanURL("UCuni")},
		{VideoID: "u2", ChannelURL: chanURL("UCuni")},
		{VideoID: "u3", ChannelURL: chanURL("UCuni")},
		{VideoID: "ugone", ChannelURL: chanURL("UCuni")}, // watched twice
		{VideoID: "ugone", ChannelURL: chanURL("UCuni")},
		{VideoID: "m1", ChannelURL: chanURL("UCmix")},
		{VideoID: "m2", ChannelURL: chanURL("UCmix")},
		{VideoID: "m3", ChannelURL: chanURL("UCmix")},
		{VideoID: "m4", ChannelURL: chanURL("UCmix")},
		{VideoID: "mgone", ChannelURL: chanURL("UCmix")},
		{VideoID: "s1", ChannelURL: chanURL("UCsmall")},
		{VideoID: "sgone", ChannelURL: chanURL("UCsmall")},
		{VideoID: "ogone", ChannelURL: chanURL("UCorphan")},
		{VideoID: "nochan"},
	}
	// "subject" rides three channels, "brand" only one — the whole difference.
	metas := map[string]enrich.Meta{
		"u1":    {ID: "u1", Categories: []string{"Gaming"}, Tags: []string{"subject", "brand"}},
		"u2":    {ID: "u2", Categories: []string{"Gaming"}, Tags: []string{"brand"}},
		"u3":    {ID: "u3", Categories: []string{"Gaming"}},
		"m1":    {ID: "m1", Categories: []string{"Gaming"}, Tags: []string{"subject"}},
		"m2":    {ID: "m2", Categories: []string{"Gaming"}},
		"m3":    {ID: "m3", Categories: []string{"Music"}},
		"m4":    {ID: "m4", Categories: []string{"Music"}},
		"s1":    {ID: "s1", Categories: []string{"Gaming"}, Tags: []string{"subject"}},
		"ugone": {ID: "ugone", Unavailable: true},
		"mgone": {ID: "mgone", Unavailable: true},
		"sgone": {ID: "sgone", Unavailable: true},
		"ogone": {ID: "ogone", Unavailable: true},
		// "nochan" is unfetched, and its export row names no channel either.
	}
	return views, metas
}

func TestChannelInheritanceFunnel(t *testing.T) {
	views, metas := channelSample()
	cs := Aggregate(views, metas, 15).Channels

	if cs.NoCategoryVideos != 5 || cs.NoCategoryViews != 6 {
		t.Errorf("no-category = %d videos / %d views, want 5 and 6 (ugone was watched twice)",
			cs.NoCategoryVideos, cs.NoCategoryViews)
	}
	if cs.WithChannel != 4 {
		t.Errorf("withChannel = %d, want 4 — the one without a channel drops out here", cs.WithChannel)
	}
	if cs.DonorFound != 3 {
		t.Errorf("donorFound = %d, want 3 — UCorphan has nothing categorized to give", cs.DonorFound)
	}

	want := map[string]InheritBucket{
		// ugone (UCuni, 3/3 Gaming) and sgone (UCsmall, 1/1) are both at
		// 100 %, but only UCuni clears the sample-size bar.
		"100 %":   {Videos: 2, Views: 3, VideosOverMin: 1, ViewsOverMin: 2},
		">= 90 %": {},
		">= 70 %": {},
		// UCmix is 2 Gaming / 2 Music — a 50 % majority decides nothing.
		"< 70 %": {Videos: 1, Views: 1, VideosOverMin: 1, ViewsOverMin: 1},
	}
	if len(cs.Buckets) != len(want) {
		t.Fatalf("buckets = %+v, want %d of them", cs.Buckets, len(want))
	}
	for _, got := range cs.Buckets {
		w, ok := want[got.Label]
		if !ok {
			t.Errorf("unexpected bucket %q", got.Label)
			continue
		}
		w.Label = got.Label
		if got != w {
			t.Errorf("bucket %q = %+v, want %+v", got.Label, got, w)
		}
	}
}

func TestTagSpreadSeparatesSubjectFromBrand(t *testing.T) {
	views, metas := channelSample()
	st := Aggregate(views, metas, 15)
	cs := st.Channels

	if cs.DistinctTags != 2 || cs.SubjectTags != 1 || cs.BrandTags != 1 {
		t.Errorf("tags = %d distinct / %d subject / %d brand, want 2/1/1",
			cs.DistinctTags, cs.SubjectTags, cs.BrandTags)
	}
	// u1, m1, s1 carry "subject"; u2 carries only the brand tag.
	if cs.VideosSubjectTag != 3 || cs.ViewsSubjectTag != 3 {
		t.Errorf("videos with a subject tag = %d (%d views), want 3 and 3",
			cs.VideosSubjectTag, cs.ViewsSubjectTag)
	}

	var gaming CategoryAgg
	for _, c := range st.Categories {
		if c.Name == "Gaming" {
			gaming = c
		}
	}
	// "brand" is the more frequent tag, so a subject list filtered after the
	// ranking cap would be the wrong one — it must survive the cap.
	if len(gaming.SubjectTags) != 1 || gaming.SubjectTags[0].Tag != "subject" {
		t.Errorf("gaming subject tags = %+v, want just \"subject\"", gaming.SubjectTags)
	}
	if gaming.SubjectTags[0].Channels != 3 {
		t.Errorf("\"subject\" spans %d channels, want 3", gaming.SubjectTags[0].Channels)
	}
}

func TestRenderChannelsIsOptOut(t *testing.T) {
	views, metas := channelSample()
	st := Aggregate(views, metas, 15)
	// Matched on a line only the block itself has — the opt-in hint mentions
	// the block by name, so its heading is not enough to tell them apart.
	if plain := Render(st, false, false); strings.Contains(plain, "inheriting a category") {
		t.Errorf("the channel block needs -channels:\n%s", plain)
	}
	txt := Render(st, false, true)
	for _, want := range []string{"channel signal", "inheriting a category", "inheritable at", "subject or brand"} {
		if !strings.Contains(txt, want) {
			t.Errorf("channel block misses %q in:\n%s", want, txt)
		}
	}
	// Aggregates only: -channels must not leak tag names on its own.
	if strings.Contains(txt, "subject 3") || strings.Contains(txt, "brand 2") {
		t.Errorf("-channels must stay free of tag names without -tags:\n%s", txt)
	}
}
