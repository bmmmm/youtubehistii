// SPDX-License-Identifier: GPL-3.0-or-later

package taxonomy

import "sort"

// Metrics are the four numbers every refinement round is judged by, plus the
// counts that make them readable. A round that moves none of them is done.
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
