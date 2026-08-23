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
	plain := Render(st, false)
	if strings.Contains(plain, "factorio") {
		t.Error("creator tags must stay hidden unless asked for — they carry names")
	}
	for _, want := range []string{"Gaming", "category coverage", "tag coverage", "not fetched yet"} {
		if !strings.Contains(plain, want) {
			t.Errorf("summary misses %q in:\n%s", want, plain)
		}
	}
	if withTags := Render(st, true); !strings.Contains(withTags, "factorio") {
		t.Errorf("-tags must show them:\n%s", withTags)
	}
}

func TestRenderEmptyCache(t *testing.T) {
	txt := Render(Aggregate([]takeout.View{{VideoID: "a"}}, map[string]enrich.Meta{}, 5), true)
	if !strings.Contains(txt, "run \"enrich\" first") {
		t.Errorf("an empty cache should say what to do:\n%s", txt)
	}
}
