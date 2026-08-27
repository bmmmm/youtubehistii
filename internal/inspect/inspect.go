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
	Name        string // "" = the video carries no category
	Views       int
	Videos      int
	Tags        []TagCount // most videos first
	SubjectTags []TagCount // the same list minus everything on < minSubjectChannels channels
}

// TagCount counts how many distinct videos carry a tag, not how often it was
// watched: the question is whether a tag identifies a subject, and a
// rewatched video should not make its tags look more common than they are.
//
// Channels is the same tag counted along the other axis — how many distinct
// channels use it — and that is what separates a subject from a brand.
// A creator's own name is frequent AND on one channel; a subject like "rust"
// is frequent across many.
// Counted globally, never per category: a channel tag stays a channel tag
// wherever it shows up.
type TagCount struct {
	Tag      string
	Videos   int
	Channels int
}

// minSubjectChannels is where a tag stops looking like a channel's own name.
// Two channels can share a brand (a main channel and its clips channel, a
// label and its artist), three is the first count that needs a subject to
// explain it.
const minSubjectChannels = 3

// minDonorVideos is how many categorized videos a channel needs before its
// majority category means anything. Below that a single upload decides.
const minDonorVideos = 3

// inheritBounds are the majority-share buckets, widest first — the LAST one
// is the rest bucket, everything the majority does not carry. Aggregation and
// rendering share the list so that "usable" and "the table" cannot drift
// apart. Labels stay ASCII: they are padded to a column width, and a "≥"
// costs three bytes against one column.
var inheritBounds = []struct {
	label string
	num   int // majority share needed, in percent
}{{"100 %", 100}, {">= 90 %", 90}, {">= 70 %", 70}, {"< 70 %", 0}}

// InheritBucket is one row of the inheritability histogram: videos with no
// category of their own, grouped by how uniform their channel's other videos
// are. OverMin repeats the count for donor channels with at least
// minDonorVideos categorized videos — the share and the sample size are two
// separate doubts and the table answers both at once.
type InheritBucket struct {
	Label         string
	Videos        int
	Views         int
	VideosOverMin int
	ViewsOverMin  int
}

// ChannelStats answers the two questions that looked like LLM questions and
// turned out to be counting ones: can a video without a category borrow one
// from its channel, and does any tag name a subject rather than a brand.
type ChannelStats struct {
	NoCategoryVideos int // tombstoned + unfetched + enriched-but-uncategorized
	NoCategoryViews  int
	WithChannel      int // of those: the export names a channel
	WithChannelViews int
	DonorFound       int // of those: that channel has categorized videos elsewhere
	DonorViews       int
	Buckets          []InheritBucket // by majority share, descending

	DistinctTags     int
	SubjectTags      int // tags on >= minSubjectChannels channels
	BrandTags        int // tags on exactly one channel
	VideosSubjectTag int // enriched videos carrying at least one subject tag
	ViewsSubjectTag  int
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
	Channels       ChannelStats
}

// Aggregate counts categories and tags over the enriched videos, weighting
// categories by views (that is what a report row means) and tags by videos.
func Aggregate(views []takeout.View, metas map[string]enrich.Meta, tagsPerCategory int) *Stats {
	st := &Stats{}

	viewsPerVideo := map[string]int{}
	channelOf := map[string]string{}
	for _, v := range views {
		if v.VideoID == "" {
			continue
		}
		st.Views++
		viewsPerVideo[v.VideoID]++
		if channelOf[v.VideoID] == "" {
			channelOf[v.VideoID] = v.ChannelKey()
		}
	}
	st.Videos = len(viewsPerVideo)

	type catBucket struct {
		views, videos int
		tags          map[string]int
	}
	cats := map[string]*catBucket{}
	var tagCounts []int
	// The two channel axes, both filled while walking the videos anyway.
	tagChannels := map[string]map[string]bool{} // tag -> distinct channels
	channelCats := map[string]map[string]int{}  // channel -> category -> videos
	categoryOf := map[string]string{}           // video -> its own category, "" if none

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
		categoryOf[id] = category
		channel := channelOf[id]
		if category != "" {
			st.ViewsWithCat += viewCount
			st.VideosWithCat++
			if channel != "" {
				if channelCats[channel] == nil {
					channelCats[channel] = map[string]int{}
				}
				channelCats[channel][category]++
			}
		}

		b := cats[category]
		if b == nil {
			b = &catBucket{tags: map[string]int{}}
			cats[category] = b
		}
		b.views += viewCount
		b.videos++

		tags := normTags(m.Tags)
		for _, t := range tags {
			b.tags[t]++
			if channel == "" {
				continue
			}
			if tagChannels[t] == nil {
				tagChannels[t] = map[string]bool{}
			}
			tagChannels[t][channel] = true
		}
		if len(tags) > 0 {
			st.VideosWithTags++
		}
		tagCounts = append(tagCounts, len(tags))
	}
	st.Channels = aggregateChannels(viewsPerVideo, metas, channelOf, categoryOf, channelCats, tagChannels)

	if len(tagCounts) > 0 {
		sort.Ints(tagCounts)
		st.MedianTags = tagCounts[len(tagCounts)/2]
	}

	for name, b := range cats {
		agg := CategoryAgg{Name: name, Views: b.views, Videos: b.videos}
		all := make([]TagCount, 0, len(b.tags))
		for tag, n := range b.tags {
			all = append(all, TagCount{Tag: tag, Videos: n, Channels: len(tagChannels[tag])})
		}
		sort.Slice(all, func(i, j int) bool {
			if all[i].Videos != all[j].Videos {
				return all[i].Videos > all[j].Videos
			}
			return all[i].Tag < all[j].Tag
		})
		// Both lists come off the same ranking, but the subject list is
		// filtered BEFORE the cap: the brand names are exactly the frequent
		// ones, so capping first would leave the subject list empty.
		agg.Tags = append(agg.Tags, all[:min(len(all), tagsPerCategory)]...)
		for _, tc := range all {
			if len(agg.SubjectTags) == tagsPerCategory {
				break
			}
			if tc.Channels >= minSubjectChannels {
				agg.SubjectTags = append(agg.SubjectTags, tc)
			}
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

// normTags lowercases, trims and deduplicates one video's creator tags. A
// video that repeats a tag in two spellings still carries it once.
func normTags(tags []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		t := strings.ToLower(strings.TrimSpace(tag))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// aggregateChannels answers both channel questions from what the main walk
// already collected. Videos with no category of their own are the ones the
// report loses today; the buckets say how uniform the channel they came from
// is, which is the whole question behind inheriting a category from it.
func aggregateChannels(viewsPerVideo map[string]int, metas map[string]enrich.Meta,
	channelOf, categoryOf map[string]string, channelCats map[string]map[string]int,
	tagChannels map[string]map[string]bool) ChannelStats {

	var cs ChannelStats

	cs.DistinctTags = len(tagChannels)
	for _, chans := range tagChannels {
		switch {
		case len(chans) >= minSubjectChannels:
			cs.SubjectTags++
		case len(chans) == 1:
			cs.BrandTags++
		}
	}

	cs.Buckets = make([]InheritBucket, len(inheritBounds))
	for i, b := range inheritBounds {
		cs.Buckets[i].Label = b.label
	}

	for id, viewCount := range viewsPerVideo {
		m, enriched := metas[id]
		if enriched && !m.Unavailable {
			for _, t := range normTags(m.Tags) {
				if len(tagChannels[t]) >= minSubjectChannels {
					cs.VideosSubjectTag++
					cs.ViewsSubjectTag += viewCount
					break
				}
			}
		}

		if categoryOf[id] != "" {
			continue
		}
		cs.NoCategoryVideos++
		cs.NoCategoryViews += viewCount
		channel := channelOf[id]
		if channel == "" {
			continue
		}
		cs.WithChannel++
		cs.WithChannelViews += viewCount
		donor := channelCats[channel]
		if len(donor) == 0 {
			continue
		}
		cs.DonorFound++
		cs.DonorViews += viewCount

		// Only the SIZE of the majority is needed, never its name — so a tie
		// between two categories cannot make the result depend on map order.
		top, total := 0, 0
		for _, n := range donor {
			total += n
			if n > top {
				top = n
			}
		}
		for i, b := range inheritBounds {
			if top*100 < total*b.num {
				continue
			}
			cs.Buckets[i].Videos++
			cs.Buckets[i].Views += viewCount
			if total >= minDonorVideos {
				cs.Buckets[i].VideosOverMin++
				cs.Buckets[i].ViewsOverMin += viewCount
			}
			break
		}
	}
	return cs
}

// Render writes the terminal summary. Creator tags are the creators' words,
// not the viewer's, but they routinely contain channel and personal names —
// so the caller decides whether to show them (showTags). showChannels adds
// the two counting answers: can a category be inherited from the channel, and
// does any tag name a subject rather than a brand. Both are aggregates and
// name nobody, so they are independent of showTags.
func Render(st *Stats, showTags, showChannels bool) string {
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

	if showChannels {
		renderChannels(&b, st)
	}

	if !showTags {
		b.WriteString("\n(creator tags omitted — pass -tags to see them)\n")
		if !showChannels {
			b.WriteString("(pass -channels for the channel signal: inheritable categories, subject vs. brand tags)\n")
		}
		return b.String()
	}
	b.WriteString("\ntop creator tags per category — how well a tag names the SUBJECT\n")
	fmt.Fprintf(&b, "decides whether subs can come from tags instead of the LLM (videos/channels;\n"+
		"\"subjects\" are the same tags minus everything on fewer than %d channels):\n", minSubjectChannels)
	for _, c := range st.Categories {
		if len(c.Tags) == 0 {
			continue
		}
		name := c.Name
		if name == "" {
			name = "(no category)"
		}
		fmt.Fprintf(&b, "  %s:\n    %s\n", name, joinTags(c.Tags))
		subjects := joinTags(c.SubjectTags)
		if subjects == "" {
			subjects = "(none — every top tag here is a channel's own name)"
		}
		fmt.Fprintf(&b, "    subjects: %s\n", subjects)
	}
	return b.String()
}

func joinTags(tags []TagCount) string {
	parts := make([]string, 0, len(tags))
	for _, t := range tags {
		parts = append(parts, fmt.Sprintf("%s %d/%dch", t.Tag, t.Videos, t.Channels))
	}
	return strings.Join(parts, " · ")
}

// renderChannels prints the two counting answers. Both are decisions waiting
// on a number, so the funnel and the histogram are shown in full — the point
// is to see WHERE the videos are lost, not just how many survive.
func renderChannels(b *strings.Builder, st *Stats) {
	cs := st.Channels
	b.WriteString("\nchannel signal — what the data answers without the model\n")

	// Counts first, prose after: the labels differ in length and only the
	// numbers are meant to line up.
	b.WriteString("\ninheriting a category from the channel:\n")
	fmt.Fprintf(b, "  %6s %8s\n", "videos", "views")
	funnel := []struct {
		videos, views int
		label         string
	}{
		{cs.NoCategoryVideos, cs.NoCategoryViews,
			fmt.Sprintf("have no category of their own (%.0f%% of all views)", pct(cs.NoCategoryViews, st.Views))},
		{cs.WithChannel, cs.WithChannelViews, "of those, the export names a channel"},
		{cs.DonorFound, cs.DonorViews, "of those, the channel has categorized videos elsewhere"},
	}
	for _, f := range funnel {
		fmt.Fprintf(b, "  %6d %8d  %s\n", f.videos, f.views, f.label)
	}
	if cs.DonorFound == 0 {
		b.WriteString("  nothing to inherit from — the channel axis is empty\n")
		renderTagSpread(b, st)
		return
	}

	fmt.Fprintf(b, "\n  %-10s %8s %8s   %8s %8s  (donor with >= %d categorized videos)\n",
		"majority", "videos", "views", "videos", "views", minDonorVideos)
	usableVideos, usableViews := 0, 0
	for _, bucket := range cs.Buckets {
		fmt.Fprintf(b, "  %-10s %8d %8d   %8d %8d\n",
			bucket.Label, bucket.Videos, bucket.Views, bucket.VideosOverMin, bucket.ViewsOverMin)
		if bucket.Label != inheritBounds[len(inheritBounds)-1].label {
			usableVideos += bucket.VideosOverMin
			usableViews += bucket.ViewsOverMin
		}
	}
	// The one number the decision hangs on, stated as such.
	fmt.Fprintf(b, "\n  inheritable at >= 70%% majority and >= %d donor videos: %d of %d videos (%.0f%%), %d views\n",
		minDonorVideos, usableVideos, cs.NoCategoryVideos, pct(usableVideos, cs.NoCategoryVideos), usableViews)

	renderTagSpread(b, st)
}

func renderTagSpread(b *strings.Builder, st *Stats) {
	cs := st.Channels
	b.WriteString("\ntags: subject or brand?\n")
	fmt.Fprintf(b, "  %d distinct tags: %d on exactly one channel (brand), %d on >= %d channels (subject)\n",
		cs.DistinctTags, cs.BrandTags, cs.SubjectTags, minSubjectChannels)
	fmt.Fprintf(b, "  %d of %d enriched videos carry at least one subject tag (%.0f%%, %d views)\n",
		cs.VideosSubjectTag, st.Enriched, pct(cs.VideosSubjectTag, st.Enriched), cs.ViewsSubjectTag)
	b.WriteString("  a subject tag is a candidate for a sub without the LLM — not proof of one:\n" +
		"  a format word (\"lets play\", \"tutorial\") also travels across channels.\n")
}

func pct(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}
