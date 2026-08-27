// SPDX-License-Identifier: GPL-3.0-or-later

package taxonomy

import (
	"fmt"
	"sort"
	"strings"
)

// RefineOpts bounds one automatic repair round.
type RefineOpts struct {
	SplitAt    float64 // threshold splits re-cluster at (tighter than fine)
	MaxRadius  float64 // subjects wider than this split automatically
	MergeBelow float64 // subject pairs closer than this merge automatically
	MinVideos  int     // small-tail bar, re-applied after splits
	// NoSplit names subjects the automatic radius trigger must leave alone:
	// a split whose pieces the namer gave one name, so that the same-name
	// merge put the very same cluster back, had no effect — and repeating it
	// every round is the loop, not a repair. The caller fills this in.
	NoSplit map[string]bool
}

// Change is one refinement, worded for the terminal and the run log.
type Change struct {
	Op    string `json:"op"` // "pin" | "merge" | "split"
	From  string `json:"from"`
	To    string `json:"to"`
	Views int    `json:"views"`
}

func (c Change) String() string {
	return fmt.Sprintf("%s %s (%d) → %s", c.Op, c.From, c.Views, c.To)
}

// Refine runs one repair round: control wishes first (they win over the
// automatic rules), then splits where coherence tears, then merges where two
// subjects lie closer than the threshold, then the small tail folds again.
// Split pieces come back unnamed — naming them is the caller's next step.
func Refine(subjects []Cluster, ctl Control, opts RefineOpts) ([]Cluster, []Change) {
	var changes []Change
	cs := append([]Cluster{}, subjects...)

	for _, old := range sortedKeys(ctl.Pin) {
		target := ctl.Pin[old]
		if moved, ok := pinLabel(&cs, old, target); ok {
			changes = append(changes, Change{Op: "pin", From: old, To: target, Views: moved})
		}
	}

	for _, group := range ctl.Merge {
		if len(group) < 2 {
			continue
		}
		if merged, views := mergeByName(&cs, group); merged {
			changes = append(changes, Change{Op: "merge", From: strings.Join(group[1:], "+"), To: group[0], Views: views})
		}
	}

	splitWanted := map[string]bool{}
	for _, name := range ctl.Split {
		splitWanted[name] = true
	}
	var next []Cluster
	for _, c := range cs {
		// A split a human asked for through the control file always happens;
		// the block stands down the automatic radius trigger only.
		split := splitWanted[c.Name] || (!opts.NoSplit[c.Name] && c.Radius > opts.MaxRadius)
		if !split {
			next = append(next, c)
			continue
		}
		pieces := SplitCluster(c, opts.SplitAt)
		if pieces == nil {
			next = append(next, c)
			continue
		}
		changes = append(changes, Change{Op: "split", From: c.Name, To: fmt.Sprintf("%d pieces", len(pieces)), Views: c.Views})
		next = append(next, pieces...)
	}
	cs = next

	// Merge closest pairs until none lies below the bar. Control pins and
	// merges have already happened, so this cannot undo an explicit wish
	// within the round — and the control file is re-read before the next one.
	for {
		bi, bj, d := closestPair(cs)
		if bi == -1 || d >= opts.MergeBelow {
			break
		}
		from, to := cs[bj], cs[bi]
		if from.Views > to.Views {
			from, to = to, from
		}
		changes = append(changes, Change{Op: "merge", From: nameOr(from), To: nameOr(to), Views: from.Views})
		merged := MergeClusters(cs[bi], cs[bj])
		cs[bi] = merged
		cs = append(cs[:bj], cs[bj+1:]...)
	}

	cs = FoldSmall(cs, opts.MinVideos, ctl.KeepSet())
	sortClusters(cs)
	return cs, changes
}

// pinLabel moves the label spelling old into the subject named target,
// creating that subject if no cluster carries the name yet. Reports the
// moved label's views and whether anything moved.
func pinLabel(cs *[]Cluster, old, target string) (int, bool) {
	for ci, c := range *cs {
		for li, l := range c.Members {
			if l.Topic() != old {
				continue
			}
			if c.Name == target {
				return 0, false // already where it belongs
			}
			vec := c.vecs[li]
			rest := removeMember(c, li)
			if rest == nil {
				*cs = append((*cs)[:ci], (*cs)[ci+1:]...)
			} else {
				(*cs)[ci] = *rest
			}
			for ti := range *cs {
				if (*cs)[ti].Name == target {
					(*cs)[ti] = MergeClusters((*cs)[ti], singleton(l, vec, target))
					(*cs)[ti].Name = target
					return l.Views, true
				}
			}
			*cs = append(*cs, singleton(l, vec, target))
			return l.Views, true
		}
	}
	return 0, false
}

func singleton(l Label, vec []float32, name string) Cluster {
	c := newCluster([]Label{l}, [][]float32{vec})
	c.Name = name
	return c
}

// removeMember rebuilds a cluster without member i; nil when it empties.
func removeMember(c Cluster, i int) *Cluster {
	if len(c.Members) == 1 {
		return nil
	}
	ls := append(append([]Label{}, c.Members[:i]...), c.Members[i+1:]...)
	vs := append(append([][]float32{}, c.vecs[:i]...), c.vecs[i+1:]...)
	nc := newCluster(ls, vs)
	nc.Name, nc.Parent = c.Name, c.Parent
	return &nc
}

// mergeByName merges every named cluster of the group into the first name.
func mergeByName(cs *[]Cluster, names []string) (bool, int) {
	find := func(name string) int {
		for i := range *cs {
			if (*cs)[i].Name == name {
				return i
			}
		}
		return -1
	}
	base := find(names[0])
	if base == -1 {
		return false, 0
	}
	merged, views := false, 0
	for _, name := range names[1:] {
		i := find(name)
		if i == -1 {
			continue
		}
		views += (*cs)[i].Views
		m := MergeClusters((*cs)[base], (*cs)[i])
		m.Name = names[0]
		(*cs)[base] = m
		*cs = append((*cs)[:i], (*cs)[i+1:]...)
		if i < base {
			base--
		}
		merged = true
	}
	return merged, views
}

func closestPair(cs []Cluster) (int, int, float64) {
	bi, bj, best := -1, -1, 2.0
	for i := 0; i < len(cs); i++ {
		for j := i + 1; j < len(cs); j++ {
			if d := 1 - Cosine(cs[i].Centroid, cs[j].Centroid); d < best {
				bi, bj, best = i, j, d
			}
		}
	}
	return bi, bj, best
}

func nameOr(c Cluster) string {
	if c.Name != "" {
		return c.Name
	}
	return FallbackName(c)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Stable pin order: the file is a map, the log should not reshuffle.
	sort.Strings(keys)
	return keys
}
