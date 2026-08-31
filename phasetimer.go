// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"strings"
	"time"
)

// phaseTimer records how long each stage took. The probe can only estimate
// the two server-side costs; the clustering that sits between them never
// appeared in any estimate, so the run itself has to say where its time went.
type phaseTimer struct {
	last  time.Time
	spans []phaseSpan
}

type phaseSpan struct {
	Phase string `json:"phase"`
	MS    int64  `json:"ms"`
}

func newPhaseTimer() *phaseTimer { return &phaseTimer{last: time.Now()} }

// mark closes the span that started at the previous mark. Stages are
// sequential, so one call per boundary is the whole bookkeeping.
func (t *phaseTimer) mark(phase string) {
	now := time.Now()
	t.spans = append(t.spans, phaseSpan{Phase: phase, MS: now.Sub(t.last).Milliseconds()})
	t.last = now
}

// line renders the spans for the terminal, skipping anything under 50 ms —
// the point is which stage dominates, not a full accounting.
func (t *phaseTimer) line() string {
	var parts []string
	var total int64
	for _, s := range t.spans {
		total += s.MS
		if s.MS >= 50 {
			parts = append(parts, fmt.Sprintf("%s %.1fs", s.Phase, float64(s.MS)/1000))
		}
	}
	parts = append(parts, fmt.Sprintf("total %.1fs", float64(total)/1000))
	return strings.Join(parts, " | ")
}
