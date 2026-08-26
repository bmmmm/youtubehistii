// SPDX-License-Identifier: GPL-3.0-or-later

// Package taxonomy derives a data-driven two-level taxonomy from the
// classified corpus and projects verdict topics through it on read.
//
// The verdicts stay untouched: they are the raw signal, and what is broken
// is only what gets built from them afterwards — the area comes from
// YouTube's uploader-picked category and scatters one subject across many
// areas, and the free sub level grows an unbounded tail. So the fix is a
// PROJECTION, applied the same way sub_aliases already are: folded on read,
// never part of the classify fingerprint, invalidating nothing.
package taxonomy

import (
	"sort"
	"strings"

	"github.com/bmmmm/youtubehistii/internal/rules"
)

// View is one watch event as collection sees it: the already-normalized
// topic halves plus the metadata that gives a label its meaning.
type View struct {
	VideoID string
	Area    string
	Sub     string
	Channel string
	Title   string
	Tags    []string
}

// Label is one observed area/sub pair with its aggregated signal. The
// channel and tag counts stay complete — the embedding uses only the top of
// each, but the channel-entropy metric needs them all.
type Label struct {
	Area, Sub string
	Views     int            // watch events
	Videos    int            // unique videos
	Channels  map[string]int // channel -> views
	Tags      map[string]int // creator tag -> unique videos carrying it
	Titles    []string       // up to two sample titles, most-watched first
}

// Topic renders the label back into the canonical verdict spelling.
func (l Label) Topic() string {
	if l.Sub == "" {
		return l.Area
	}
	return l.Area + "/" + l.Sub
}

// EmbedText renders the text a label is embedded as. The slug alone is
// ambiguous ("dev"), so the strongest channels, the creator tags and two
// sample titles give the vector its meaning — and because the tags and
// titles keep their original language, a multilingual model can lay
// "chess" and "schach" into the same region of the space.
func (l Label) EmbedText() string {
	parts := []string{strings.NewReplacer("/", " ", "-", " ").Replace(l.Topic())}
	if ch := topKeys(l.Channels, 3); len(ch) > 0 {
		parts = append(parts, "channels: "+strings.Join(ch, ", "))
	}
	if tg := topKeys(l.Tags, 8); len(tg) > 0 {
		parts = append(parts, "tags: "+strings.Join(tg, ", "))
	}
	if len(l.Titles) > 0 {
		parts = append(parts, "titles: "+strings.Join(l.Titles, " | "))
	}
	return strings.Join(parts, "\n")
}

// topKeys returns the n highest-counted keys, count desc with the name as
// tie-break — the same determinism rule collectSubSeeds follows: anything
// that feeds a prompt or a vector must not reshuffle between runs.
func topKeys(m map[string]int, n int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys[:min(len(keys), n)]
}

// Collect aggregates watch events into labels, one per observed area/sub
// pair. "unclear" is skipped: it means "cannot tell", and clustering it into
// a subject would pretend otherwise. Purely local — no network, no model.
func Collect(views []View) []Label {
	type agg struct {
		label      *Label
		titleViews map[string]int
		seenVideo  map[string]bool
	}
	byTopic := map[string]*agg{}
	for _, v := range views {
		if v.Area == "" || v.Area == "unclear" {
			continue
		}
		key := v.Area + "/" + v.Sub
		a := byTopic[key]
		if a == nil {
			a = &agg{
				label:      &Label{Area: v.Area, Sub: v.Sub, Channels: map[string]int{}, Tags: map[string]int{}},
				titleViews: map[string]int{},
				seenVideo:  map[string]bool{},
			}
			byTopic[key] = a
		}
		a.label.Views++
		if v.Channel != "" {
			a.label.Channels[v.Channel]++
		}
		if v.Title != "" {
			a.titleViews[v.Title]++
		}
		// Deleted videos carry no ID; their title stands in so they still
		// count as distinct videos rather than collapsing into one.
		id := v.VideoID
		if id == "" {
			id = "t:" + v.Title
		}
		if !a.seenVideo[id] {
			a.seenVideo[id] = true
			a.label.Videos++
			for _, t := range v.Tags {
				a.label.Tags[t]++
			}
		}
	}
	labels := make([]Label, 0, len(byTopic))
	for _, a := range byTopic {
		a.label.Titles = topKeys(a.titleViews, 2)
		labels = append(labels, *a.label)
	}
	sort.Slice(labels, func(i, j int) bool {
		if labels[i].Views != labels[j].Views {
			return labels[i].Views > labels[j].Views
		}
		return labels[i].Topic() < labels[j].Topic()
	})
	return labels
}

// Taxonomy is the projection "old area/sub" -> "new top/subject". Fold is
// the one point where it is applied.
type Taxonomy struct {
	Map map[string]string `yaml:"map"`
}

// Fold projects one canonical verdict topic into the new taxonomy. A topic
// the projection has never seen (classified after the taxonomy was built)
// keeps its sub and only has its area folded — and if even the area is
// unknown, the topic passes through unchanged, "unclear" included.
func (t Taxonomy) Fold(topic string) string {
	if n, ok := t.Map[topic]; ok {
		return n
	}
	area, sub := rules.SplitTopic(topic)
	if n, ok := t.Map[area]; ok {
		top, _ := rules.SplitTopic(n)
		if sub == "" {
			return n
		}
		return top + "/" + sub
	}
	return topic
}

// Build renders subject clusters into the projection map. A subject named
// like its top level maps to the bare top — the same "sub repeating its
// area says nothing" rule NormalizeTopic applies.
func Build(subjects []Cluster) Taxonomy {
	m := map[string]string{}
	for _, c := range subjects {
		target := c.Parent + "/" + c.Name
		if c.Name == c.Parent || c.Name == "" {
			target = c.Parent
		}
		for _, l := range c.Members {
			m[l.Topic()] = target
		}
	}
	return Taxonomy{Map: m}
}
