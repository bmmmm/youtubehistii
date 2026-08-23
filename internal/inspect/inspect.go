// SPDX-License-Identifier: GPL-3.0-or-later

// Package inspect summarizes what the metadata cache actually contains —
// YouTube's own categories and the creator tags — so the taxonomy can be
// decided from the data instead of guessed. It reads only what enrich has
// already fetched and never talks to the network or the LLM.
package inspect

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

// CategoryAgg is one YouTube category with the creator tags seen under it.
type CategoryAgg struct {
	Name   string // "" = the video carries no category
	Views  int
	Videos int
	Tags   []TagCount
}

// TagCount counts how many distinct videos carry a tag, not how often it was
// watched: the question is whether a tag identifies a subject, and a
// rewatched video should not make its tags look more common than they are.
type TagCount struct {
	Tag    string
	Videos int
}

type Stats struct {
	Views          int // watch events with a video id
	Videos         int // distinct videos among them
	Enriched       int // videos with usable metadata
	Tombstoned     int // videos gone from YouTube
	Unfetched      int // enrich has not reached them yet
	ViewsWithCat   int
	VideosWithCat  int
	VideosWithTags int
	MultiCategory  int // videos yt-dlp gave more than one category
	MedianTags     int
	Categories     []CategoryAgg // by views desc
}

// Aggregate counts categories and tags over the enriched videos, weighting
// categories by views (that is what a report row means) and tags by videos.
func Aggregate(views []takeout.View, metas map[string]enrich.Meta, tagsPerCategory int) *Stats {
	st := &Stats{}

	viewsPerVideo := map[string]int{}
	for _, v := range views {
		if v.VideoID == "" {
			continue
		}
		st.Views++
		viewsPerVideo[v.VideoID]++
	}
	st.Videos = len(viewsPerVideo)

	type catBucket struct {
		views, videos int
		tags          map[string]int
	}
	cats := map[string]*catBucket{}
	var tagCounts []int

	for id, viewCount := range viewsPerVideo {
		m, ok := metas[id]
		switch {
		case !ok:
			st.Unfetched++
			continue
		case m.Unavailable:
			st.Tombstoned++
			continue
		}
		st.Enriched++

		// yt-dlp reports categories as a list; the first is the one YouTube
		// shows, and a second is rare enough to be worth counting separately.
		category := ""
		if len(m.Categories) > 0 {
			category = m.Categories[0]
		}
		if len(m.Categories) > 1 {
			st.MultiCategory++
		}
		if category != "" {
			st.ViewsWithCat += viewCount
			st.VideosWithCat++
		}

		b := cats[category]
		if b == nil {
			b = &catBucket{tags: map[string]int{}}
			cats[category] = b
		}
		b.views += viewCount
		b.videos++

		seen := map[string]bool{}
		for _, tag := range m.Tags {
			t := strings.ToLower(strings.TrimSpace(tag))
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			b.tags[t]++
		}
		if len(seen) > 0 {
			st.VideosWithTags++
		}
		tagCounts = append(tagCounts, len(seen))
	}

	if len(tagCounts) > 0 {
		sort.Ints(tagCounts)
		st.MedianTags = tagCounts[len(tagCounts)/2]
	}

	for name, b := range cats {
		agg := CategoryAgg{Name: name, Views: b.views, Videos: b.videos}
		for tag, n := range b.tags {
			agg.Tags = append(agg.Tags, TagCount{Tag: tag, Videos: n})
		}
		sort.Slice(agg.Tags, func(i, j int) bool {
			if agg.Tags[i].Videos != agg.Tags[j].Videos {
				return agg.Tags[i].Videos > agg.Tags[j].Videos
			}
			return agg.Tags[i].Tag < agg.Tags[j].Tag
		})
		if len(agg.Tags) > tagsPerCategory {
			agg.Tags = agg.Tags[:tagsPerCategory]
		}
		st.Categories = append(st.Categories, agg)
	}
	sort.Slice(st.Categories, func(i, j int) bool {
		if st.Categories[i].Views != st.Categories[j].Views {
			return st.Categories[i].Views > st.Categories[j].Views
		}
		return st.Categories[i].Name < st.Categories[j].Name
	})
	return st
}

// Render writes the terminal summary. Creator tags are the creators' words,
// not the viewer's, but they routinely contain channel and personal names —
// so the caller decides whether to show them (showTags).
func Render(st *Stats, showTags bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d views on %d distinct videos\n", st.Views, st.Videos)
	fmt.Fprintf(&b, "metadata: %d enriched, %d tombstoned, %d not fetched yet\n",
		st.Enriched, st.Tombstoned, st.Unfetched)
	if st.Enriched == 0 {
		b.WriteString("\nnothing enriched yet — run \"enrich\" first\n")
		return b.String()
	}
	// Two different questions: how good YouTube's own data is where we have
	// it, and how much of the actual watching it can carry today (the latter
	// counts tombstoned and not-yet-fetched videos against it).
	fmt.Fprintf(&b, "category coverage: %.0f%% of enriched videos have one; they account for %.0f%% of all views\n",
		pct(st.VideosWithCat, st.Enriched), pct(st.ViewsWithCat, st.Views))
	fmt.Fprintf(&b, "tag coverage:      %.0f%% of enriched videos carry creator tags (median %d tags)\n",
		pct(st.VideosWithTags, st.Enriched), st.MedianTags)
	if st.MultiCategory > 0 {
		fmt.Fprintf(&b, "note: %d videos list more than one category — only the first is counted\n", st.MultiCategory)
	}

	maxViews := 1
	if len(st.Categories) > 0 {
		maxViews = st.Categories[0].Views
	}
	b.WriteString("\nyoutube categories (by views):\n")
	for _, c := range st.Categories {
		name := c.Name
		if name == "" {
			name = "(no category)"
		}
		bar := strings.Repeat("█", 1+c.Views*24/maxViews)
		fmt.Fprintf(&b, "  %-22s %6d views %6d videos  %s\n", name, c.Views, c.Videos, bar)
	}

	if !showTags {
		b.WriteString("\n(creator tags omitted — pass -tags to see them)\n")
		return b.String()
	}
	b.WriteString("\ntop creator tags per category — how well a tag names the SUBJECT\n")
	b.WriteString("decides whether subs can come from tags instead of the LLM:\n")
	for _, c := range st.Categories {
		if len(c.Tags) == 0 {
			continue
		}
		name := c.Name
		if name == "" {
			name = "(no category)"
		}
		parts := make([]string, 0, len(c.Tags))
		for _, t := range c.Tags {
			parts = append(parts, fmt.Sprintf("%s %d", t.Tag, t.Videos))
		}
		fmt.Fprintf(&b, "  %s:\n    %s\n", name, strings.Join(parts, " · "))
	}
	return b.String()
}

func pct(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}
