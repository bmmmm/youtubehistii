// SPDX-License-Identifier: GPL-3.0-or-later

// Package report aggregates classified views into the numbers the HTML,
// CSV and terminal outputs render. Duration-based figures are upper bounds
// (full video length; Takeout has no per-view watch time).
package report

import (
	"sort"
	"strings"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/rules"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

// TopicAgg is one AREA — the fixed level of the taxonomy. Subs holds the
// free level underneath it, most-watched first; views classified as the bare
// area (no sub) count into Views/Hours but appear in no SubAgg.
type TopicAgg struct {
	Topic string
	Mode  string // dominant mode across views
	Views int
	Hours float64
	Subs  []SubAgg
}

type SubAgg struct {
	Sub   string
	Mode  string
	Views int
	Hours float64
}

type ChannelAgg struct {
	Name       string
	ID         string
	Views      int
	Hours      float64
	TopTopic   string
	Subscribed bool
}

type MonthAgg struct {
	Month     string // "2026-01"
	ModeViews map[string]int
	ModeHours map[string]float64
}

type SubRow struct {
	Title       string
	ChannelID   string
	Views       int
	Hours       float64
	TopTopic    string // "" = never watched
	LastWatched time.Time
}

type Stats struct {
	Views        int
	UniqueVideos int
	HoursUpper   float64
	From, To     time.Time
	NoID         int
	Unavailable  int
	Sources      map[string]int // rule | llm | category | unclassified

	Topics   []TopicAgg
	Months   []MonthAgg
	Channels []ChannelAgg // by views desc

	// Subscription linkage
	Subs         []SubRow
	SubbedViews  int
	SubbedHours  float64
	DeadSubs     int
	HasSubs      bool
	SubbedSet    map[string]bool
	UnclearNames []string // top channels among unclear views, for rule tuning
}

// Modes in render order; "unclear" collects views without a mode.
var ModeOrder = []string{"consume", "learn", "mixed", "unclear"}

func Aggregate(rows []classify.Verdict, subs []takeout.Subscription) *Stats {
	st := &Stats{Sources: map[string]int{}, SubbedSet: map[string]bool{}}
	for _, s := range subs {
		st.SubbedSet[s.ChannelID] = true
	}
	st.HasSubs = len(subs) > 0

	// One bucket per taxonomy level: areas roll up everything, subs keep the
	// free level underneath their area.
	type bucket struct {
		views int
		hours float64
		modes map[string]int
	}
	add := func(m map[string]*bucket, key string, hours float64, mode string) {
		b := m[key]
		if b == nil {
			b = &bucket{modes: map[string]int{}}
			m[key] = b
		}
		b.views++
		b.hours += hours
		b.modes[mode]++
	}
	areas := map[string]*bucket{}
	subTopics := map[string]map[string]*bucket{} // area -> sub -> bucket
	months := map[string]*MonthAgg{}
	type chKey struct{ name, id string }
	channels := map[chKey]*ChannelAgg{}
	chTopics := map[chKey]map[string]int{}
	unclearCh := map[string]int{}
	uniq := map[string]bool{}
	subWatch := map[string]*SubRow{}

	for _, r := range rows {
		st.Views++
		hours := float64(r.DurationS) / 3600
		st.HoursUpper += hours
		if r.VideoID == "" {
			st.NoID++
		} else {
			uniq[r.VideoID] = true
		}
		if r.Unavailable {
			st.Unavailable++
		}
		switch {
		case strings.HasPrefix(r.Source, "rule:"):
			st.Sources["rule"]++
		case strings.HasPrefix(r.Source, "llm:"):
			st.Sources["llm"]++
		case r.Source == "category":
			// Area from the YouTube category, sub and mode still open — a
			// real classification as far as the area goes, so it is neither
			// a rule hit nor unclassified.
			st.Sources["category"]++
		default:
			st.Sources["unclassified"]++
		}
		if !r.WatchedAt.IsZero() {
			// Takeout serialises every timestamp as UTC, but a report is
			// read in wall-clock time — the same basis BuildPath uses for
			// its days. Comparing instants is timezone-blind; only the
			// rendering below cares, so the conversion happens here.
			if st.From.IsZero() || r.WatchedAt.Before(st.From) {
				st.From = r.WatchedAt.Local()
			}
			if r.WatchedAt.After(st.To) {
				st.To = r.WatchedAt.Local()
			}
		}

		mode := r.Mode
		if mode == "" {
			mode = "unclear"
		}
		area, sub := rules.SplitTopic(r.Topic)
		add(areas, area, hours, mode)
		if sub != "" {
			if subTopics[area] == nil {
				subTopics[area] = map[string]*bucket{}
			}
			add(subTopics[area], sub, hours, mode)
		}

		if !r.WatchedAt.IsZero() {
			mk := r.WatchedAt.Local().Format("2006-01")
			m := months[mk]
			if m == nil {
				m = &MonthAgg{Month: mk, ModeViews: map[string]int{}, ModeHours: map[string]float64{}}
				months[mk] = m
			}
			m.ModeViews[mode]++
			m.ModeHours[mode] += hours
		}

		if r.Channel != "" {
			k := chKey{r.Channel, r.ChannelID}
			c := channels[k]
			if c == nil {
				c = &ChannelAgg{Name: r.Channel, ID: r.ChannelID, Subscribed: st.SubbedSet[r.ChannelID]}
				channels[k] = c
				chTopics[k] = map[string]int{}
			}
			c.Views++
			c.Hours += hours
			chTopics[k][r.Topic]++
		}
		if r.Topic == "unclear" && r.Channel != "" {
			unclearCh[r.Channel]++
		}

		if r.ChannelID != "" && st.SubbedSet[r.ChannelID] {
			st.SubbedViews++
			st.SubbedHours += hours
			w := subWatch[r.ChannelID]
			if w == nil {
				w = &SubRow{ChannelID: r.ChannelID}
				subWatch[r.ChannelID] = w
			}
			w.Views++
			w.Hours += hours
			if r.WatchedAt.After(w.LastWatched) {
				w.LastWatched = r.WatchedAt
			}
		}
	}
	st.UniqueVideos = len(uniq)

	for area, b := range areas {
		agg := TopicAgg{Topic: area, Mode: dominant(b.modes), Views: b.views, Hours: b.hours}
		for sub, sb := range subTopics[area] {
			agg.Subs = append(agg.Subs, SubAgg{Sub: sub, Mode: dominant(sb.modes), Views: sb.views, Hours: sb.hours})
		}
		sortByViews(agg.Subs, func(s SubAgg) (int, string) { return s.Views, s.Sub })
		st.Topics = append(st.Topics, agg)
	}
	sortByViews(st.Topics, func(t TopicAgg) (int, string) { return t.Views, t.Topic })

	for mk := range months {
		st.Months = append(st.Months, *months[mk])
	}
	sort.Slice(st.Months, func(i, j int) bool { return st.Months[i].Month < st.Months[j].Month })

	topicByChID := map[string]string{}
	for k, c := range channels {
		c.TopTopic = dominant(chTopics[k])
		if k.id != "" {
			topicByChID[k.id] = c.TopTopic
		}
		st.Channels = append(st.Channels, *c)
	}
	sort.Slice(st.Channels, func(i, j int) bool { return st.Channels[i].Views > st.Channels[j].Views })

	for _, s := range subs {
		row := SubRow{Title: s.Title, ChannelID: s.ChannelID}
		if w := subWatch[s.ChannelID]; w != nil {
			row.Views, row.Hours, row.LastWatched = w.Views, w.Hours, w.LastWatched
			row.TopTopic = topicByChID[s.ChannelID]
		} else {
			st.DeadSubs++
		}
		st.Subs = append(st.Subs, row)
	}
	sort.Slice(st.Subs, func(i, j int) bool { return st.Subs[i].Views > st.Subs[j].Views })

	type nc struct {
		name string
		n    int
	}
	var ncs []nc
	for name, n := range unclearCh {
		ncs = append(ncs, nc{name, n})
	}
	sort.Slice(ncs, func(i, j int) bool { return ncs[i].n > ncs[j].n })
	for i := 0; i < len(ncs) && i < 15; i++ {
		st.UnclearNames = append(st.UnclearNames, ncs[i].name)
	}
	return st
}

// sortByViews orders rows most-watched first with the name as tie-break, so
// two equally watched topics do not swap places between runs.
func sortByViews[T any](rows []T, key func(T) (int, string)) {
	sort.Slice(rows, func(i, j int) bool {
		vi, ni := key(rows[i])
		vj, nj := key(rows[j])
		if vi != vj {
			return vi > vj
		}
		return ni < nj
	})
}

func dominant(counts map[string]int) string {
	best, bestN := "", -1
	for k, n := range counts {
		if n > bestN || (n == bestN && k < best) {
			best, bestN = k, n
		}
	}
	return best
}
