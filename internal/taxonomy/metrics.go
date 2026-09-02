// SPDX-License-Identifier: GPL-3.0-or-later

package taxonomy

import "sort"

// Metrics are the four numbers every refinement round is judged by, plus the
// counts that make them readable. A round that moves none of them is done.
//
// What is NOT here, and why: a measure for the subject that is complete and
// wrong. Every retry selector picks by DEFECT — no sub, no mode, an unclear
// topic — and a subject that has an area, a sub and a mode is finished for
// all of them even when the sub is a channel name that has since grown over
// unrelated channels. "Wrong" is not a missing field.
//
// The obvious candidate was distinct channels per subject, weighted against
// size. Calibrated on the real corpus (3,274 subjects) before any of it was
// built, it does not separate:
//
//   - channels/videos puts the one documented case that still exists at 1.00,
//     which is the MEDIAN of every subject. Exactly no signal.
//   - the raw channel count puts it at rank 97 of 3,274 — top 3 %, but the 96
//     above it run to 861 channels, and 55 subjects that provably are not
//     named after any of their channels carry a median of 22 each. Legitimate
//     breadth and the defect sit on the same axis.
//   - the structural variant (the sub IS one of its own channels AND it has
//     spread) selects a small, actionable set — 4 subjects, 391 views, at 5+
//     channels — and MISSES the surviving documented case, which is not named
//     after a channel at all. Validated against zero positives.
//
// The second documented case is no longer in the corpus; re-classification
// dissolved it. So the measure was designed for a case that is gone and fails
// on the one that is left. Recorded, not carried forward as a task: a detector
// nobody can calibrate is a hypothesis with a threshold on it.
//
// ChanMean and ChanMax below are the OPPOSITE direction — subjects per
// channel, not channels per subject. They do not answer this and were checked.
type Metrics struct {
	Subjects  int
	Tops      int
	Spread    int     // subject names appearing under more than one top level
	TailShare float64 // share of subjects with <= tailN unique videos
	ChanMean  float64 // mean distinct subjects per channel
	ChanMax   int     // most subjects any single channel scatters across
	Coherence float64 // views-weighted mean subject radius; too big = mixed
}

// Measure computes the metrics over named subjects with parents assigned.
func Measure(subjects []Cluster, tailN int) Metrics {
	m := Metrics{Subjects: len(subjects)}
	parents := map[string]map[string]bool{}
	tops := map[string]bool{}
	chanSubjects := map[string]map[string]bool{}
	tail := 0
	var radSum, wSum float64
	for _, c := range subjects {
		tops[c.Parent] = true
		if parents[c.Name] == nil {
			parents[c.Name] = map[string]bool{}
		}
		parents[c.Name][c.Parent] = true
		if c.Videos <= tailN {
			tail++
		}
		radSum += float64(c.Views) * c.Radius
		wSum += float64(c.Views)
		for _, l := range c.Members {
			for ch := range l.Channels {
				if chanSubjects[ch] == nil {
					chanSubjects[ch] = map[string]bool{}
				}
				chanSubjects[ch][c.Name] = true
			}
		}
	}
	m.Tops = len(tops)
	for _, ps := range parents {
		if len(ps) > 1 {
			m.Spread++
		}
	}
	if len(subjects) > 0 {
		m.TailShare = float64(tail) / float64(len(subjects))
	}
	if wSum > 0 {
		m.Coherence = radSum / wSum
	}
	m.ChanMean, m.ChanMax = channelStats(chanSubjects)
	return m
}

// MeasureLabels is the before picture: every label its own subject, the sub
// name as the subject name, the area as the top level. This is the baseline
// the run prints next to every round.
func MeasureLabels(labels []Label, tailN int) Metrics {
	m := Metrics{Subjects: len(labels)}
	areas := map[string]bool{}
	subAreas := map[string]map[string]bool{}
	chanSubjects := map[string]map[string]bool{}
	tail := 0
	for _, l := range labels {
		areas[l.Area] = true
		if l.Sub != "" {
			if subAreas[l.Sub] == nil {
				subAreas[l.Sub] = map[string]bool{}
			}
			subAreas[l.Sub][l.Area] = true
		}
		if l.Videos <= tailN {
			tail++
		}
		for ch := range l.Channels {
			if chanSubjects[ch] == nil {
				chanSubjects[ch] = map[string]bool{}
			}
			chanSubjects[ch][l.Topic()] = true
		}
	}
	m.Tops = len(areas)
	for _, as := range subAreas {
		if len(as) > 1 {
			m.Spread++
		}
	}
	if len(labels) > 0 {
		m.TailShare = float64(tail) / float64(len(labels))
	}
	m.ChanMean, m.ChanMax = channelStats(chanSubjects)
	return m
}

func channelStats(chanSubjects map[string]map[string]bool) (mean float64, maxN int) {
	if len(chanSubjects) == 0 {
		return 0, 0
	}
	counts := make([]int, 0, len(chanSubjects))
	sum := 0
	for _, subs := range chanSubjects {
		counts = append(counts, len(subs))
		sum += len(subs)
	}
	sort.Ints(counts)
	return float64(sum) / float64(len(counts)), counts[len(counts)-1]
}
