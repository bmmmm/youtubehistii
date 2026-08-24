// SPDX-License-Identifier: GPL-3.0-or-later

package report

import (
	"sort"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/rules"
)

// The watch path answers the question the aggregate report cannot: not WHAT
// was watched but HOW — in which sittings, in which order, how deep.
//
// Everything below is derived from one weak signal. Takeout logs the START of
// a playback and nothing else: no end, no device, no watch time. So the only
// thing we ever know about a video is when the NEXT one started, and that gap
// has two readings that the export cannot tell apart — the video was
// abandoned, or it kept playing while something else was started elsewhere.
// The rules below therefore mark and never assert, and where the data says
// nothing (no duration) they stay silent instead of guessing.

const (
	// sessionGap splits the stream into sittings. Half an hour is long enough
	// to cover a pause for coffee and short enough that the evening and the
	// next morning do not merge into one block.
	sessionGap = 30 * time.Minute

	// skippedRatio is where "moved on early" starts: a gap under half the
	// video's length. Between that and the full length the viewer saw most of
	// it, which is a different statement and gets its own label.
	skippedRatio = 0.5

	// longVideoS is what counts as a "long-video session" for the overlap
	// suspicion. Twenty minutes is where a video stops being something you
	// click through and becomes something that runs.
	longVideoS = 20 * 60

	// rabbitMinLen / rabbitMaxGap describe a rabbit hole: a run of videos on
	// one area, watched back to back. Four is the first length that cannot be
	// a coincidence of two related clicks, and a quarter of an hour between
	// starts keeps the chain a sitting rather than an evening.
	rabbitMinLen = 4
	rabbitMaxGap = 15 * time.Minute
)

// Edge labels: what the gap to the next view says about this one. The empty
// string means the export gives no answer — no duration (tombstoned or never
// enriched), or nothing followed.
const (
	EdgeThrough = "through" // the gap was at least as long as the video
	EdgeMost    = "most"    // over half of it, under all of it
	EdgeSkipped = "skipped" // under half — moved on early
)

// PathView is one watch event on the timeline.
type PathView struct {
	Title, Channel  string
	Area, Sub, Mode string
	WatchedAt       time.Time
	DurationS       int
	GapS            int    // to the next view in time; 0 at the end of a session
	Edge            string // see the Edge* constants; "" = the data does not say
	Overlap         bool   // suspected of having run alongside, not after
	Rabbit          bool   // part of a same-area chain
}

// Session is a run of views with no gap longer than sessionGap.
type Session struct {
	Start, End time.Time
	Views      []PathView    // newest first, the order the page shows
	GapBefore  time.Duration // to the previous, older session; 0 for the oldest
}

// Path is the whole timeline, newest session first.
type Path struct {
	Sessions []Session
	Views    int // views placed on the timeline
	Dropped  int // views without a timestamp — no place on a time axis
	From, To time.Time
}

// BuildPath derives sessions, edges, overlap suspicion and rabbit holes from
// the classified views.
//
// Everything is computed forwards in time, because that is how the statements
// are defined ("the NEXT view started N minutes later"), and only reversed at
// the very end for display. Doing it the other way round would put the
// off-by-one into the interesting part instead of into one final loop.
func BuildPath(rows []classify.Verdict) *Path {
	p := &Path{}
	views := make([]PathView, 0, len(rows))
	for _, r := range rows {
		if r.WatchedAt.IsZero() {
			p.Dropped++
			continue
		}
		area, sub := rules.SplitTopic(r.Topic)
		mode := r.Mode
		if mode == "" {
			mode = "unclear"
		}
		views = append(views, PathView{
			Title: r.Title, Channel: r.Channel,
			Area: area, Sub: sub, Mode: mode,
			WatchedAt: r.WatchedAt, DurationS: r.DurationS,
		})
	}
	if len(views) == 0 {
		return p
	}
	sort.Slice(views, func(i, j int) bool { return views[i].WatchedAt.Before(views[j].WatchedAt) })
	p.Views = len(views)
	p.From, p.To = views[0].WatchedAt, views[len(views)-1].WatchedAt

	// Cut into sessions, then derive within each — a gap that ends a sitting
	// is not a statement about the video before it.
	start := 0
	var sessions []Session
	for i := 1; i <= len(views); i++ {
		gap := time.Duration(0)
		if i < len(views) {
			gap = views[i].WatchedAt.Sub(views[i-1].WatchedAt)
			if gap <= sessionGap {
				continue
			}
		}
		block := views[start:i]
		markEdges(block)
		markOverlap(block)
		markRabbitHoles(block)
		s := Session{Start: block[0].WatchedAt, End: block[len(block)-1].WatchedAt, Views: block}
		if len(sessions) > 0 {
			s.GapBefore = block[0].WatchedAt.Sub(sessions[len(sessions)-1].End)
		}
		sessions = append(sessions, s)
		start = i
	}

	// Newest first, both levels.
	for i, j := 0, len(sessions)-1; i < j; i, j = i+1, j-1 {
		sessions[i], sessions[j] = sessions[j], sessions[i]
	}
	for si := range sessions {
		vs := sessions[si].Views
		for i, j := 0, len(vs)-1; i < j; i, j = i+1, j-1 {
			vs[i], vs[j] = vs[j], vs[i]
		}
	}
	p.Sessions = sessions
	return p
}

// markEdges fills GapS and Edge for every view but the last of the session.
func markEdges(vs []PathView) {
	for i := 0; i < len(vs)-1; i++ {
		gap := int(vs[i+1].WatchedAt.Sub(vs[i].WatchedAt).Seconds())
		vs[i].GapS = gap
		if vs[i].DurationS <= 0 {
			continue // no length known: the gap says nothing, so neither do we
		}
		switch {
		case gap >= vs[i].DurationS:
			vs[i].Edge = EdgeThrough
		case float64(gap) < skippedRatio*float64(vs[i].DurationS):
			vs[i].Edge = EdgeSkipped
		default:
			vs[i].Edge = EdgeMost
		}
	}
}

// markOverlap flags views that started while a long video was still running
// and belong to a different area — the interleaved-stream case (music entries
// inside a documentary). It deliberately does NOT flag a fast run through
// short videos: there, "abandoned" is the nearer reading, and calling that an
// overlap would turn a guess into a claim.
func markOverlap(vs []PathView) {
	running := -1 // index of the last main-lane view still playing
	for i := range vs {
		if running >= 0 {
			r := &vs[running]
			end := r.WatchedAt.Add(time.Duration(r.DurationS) * time.Second)
			if vs[i].WatchedAt.Before(end) && r.DurationS >= longVideoS && vs[i].Area != r.Area {
				vs[i].Overlap = true
				continue // the long video keeps the main lane
			}
		}
		running = i
	}
}

// markRabbitHoles marks runs of rabbitMinLen or more main-lane views on one
// area with short gaps between them. Overlap views are skipped rather than
// breaking the chain — background music does not end a chain of cycling
// videos, it just is not part of it.
func markRabbitHoles(vs []PathView) {
	main := make([]int, 0, len(vs))
	for i := range vs {
		if !vs[i].Overlap {
			main = append(main, i)
		}
	}
	runStart := 0
	for k := 1; k <= len(main); k++ {
		if k < len(main) {
			prev, cur := &vs[main[k-1]], &vs[main[k]]
			if cur.Area == prev.Area && cur.WatchedAt.Sub(prev.WatchedAt) <= rabbitMaxGap {
				continue
			}
		}
		if k-runStart >= rabbitMinLen {
			for _, idx := range main[runStart:k] {
				vs[idx].Rabbit = true
			}
		}
		runStart = k
	}
}
