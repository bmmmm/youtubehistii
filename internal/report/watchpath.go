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

	Days     []DayAgg     // OLDEST first — calendar order
	Trans    []Transition // by N desc, then From, then To — deterministic
	Clusters []Cluster    // the topic tree, most-watched area first
	Stats    PathStats
}

// Cluster is one node of the topic tree: an area, a subject inside it, or a
// channel inside that subject. Three levels, always — a view classified to
// the bare area still gets a subject node, because a tree with ragged depth
// would draw two different things at the same size and call them the same.
// NoSubject and NoChannel are what that node is called.
//
// Unlike the day's dominant area, this counts overlap views too. The calendar
// asks what a day was ABOUT, where background must not vote; this asks what
// was watched, and a track that ran under a documentary was watched.
type Cluster struct {
	Name      string
	Views     int
	DurationS int // upper bound: the full length of every video below
	Children  []Cluster
}

// The buckets for what the export leaves open. They are named rather than
// dropped: leaving them out would make every circle above them a little
// wrong, which is the one thing a proportional drawing may not be.
const (
	NoSubject = "(no subject)"
	NoChannel = "(no channel)"
)

// DayAgg is one calendar day of the path: the sittings that started on it.
//
// A sitting that runs past midnight counts entirely on the day it began. The
// sitting is the unit everything else is built on, and cutting one in half at
// 00:00 would put a single evening on two rows of the heatmap.
type DayAgg struct {
	Date     string  // "2006-01-02", local date of the session starts
	EpochDay int     // days since 1970-01-01 — the heatmap grid coordinate
	Views    int     // views of all sessions that started this day
	Hours    float64 // upper bound, sum of full video lengths
	Area     string  // dominant MAIN-LANE area; ties broken by name (dominant())
	SessFrom int     // inclusive index range into Path.Sessions (newest first),
	SessTo   int     // so Sessions[SessFrom] is the NEWEST sitting of that day
}

// Transition counts one area following another on the main lane, inside one
// sitting. Self-loops are kept: they are a chain staying on one topic.
type Transition struct {
	From, To string
	N        int
}

// PathStats are the headline numbers of the overview.
type PathStats struct {
	Views, Sessions, Dropped  int
	OverlapViews, RabbitViews int
	HoursUpper                float64
	LongestSession            int // index into Sessions, -1 when there is none
	LongestSessionViews       int
	LongestSessionSpan        time.Duration
	DeepestRabbit             int // index into Sessions holding the longest chain, -1
	DeepestRabbitLen          int // that chain's length in views
	BusiestDay                int // index into Days, -1 when there is none
	BusiestDayViews           int
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
			// Takeout serialises every timestamp as UTC, but a watch path is
			// read in wall-clock time: a video started at 01:20 belongs to
			// that night, not to the previous UTC day. Converting once here
			// is what makes the calendar day, the session clock and the hour
			// axis on the page all mean the same thing — the page renders in
			// the reader's local time, and this is the only place Go has to
			// agree with it. The instant is untouched, so every gap, every
			// session cut and every t0-relative offset stays as it was.
			WatchedAt: r.WatchedAt.Local(), DurationS: r.DurationS,
		})
	}
	if len(views) == 0 {
		p.Stats = buildStats(p) // an empty path still owes the page its -1 indices
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
	p.Days = buildDays(sessions)
	p.Trans = buildTransitions(sessions)
	p.Clusters = buildClusters(sessions)
	p.Stats = buildStats(p)
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

// buildDays folds the sittings into calendar days, oldest first. Sessions
// arrive newest first and are chronologically ordered, so every day owns one
// contiguous run of them and a single backwards pass finds it.
func buildDays(sessions []Session) []DayAgg {
	var days []DayAgg
	var mainAreas []map[string]int
	for i := len(sessions) - 1; i >= 0; i-- {
		s := sessions[i]
		date := s.Start.Format("2006-01-02")
		if len(days) == 0 || days[len(days)-1].Date != date {
			y, m, d := s.Start.Date()
			days = append(days, DayAgg{
				Date:     date,
				EpochDay: int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400),
				SessTo:   i, // the oldest sitting of the day, reached first
			})
			mainAreas = append(mainAreas, map[string]int{})
		}
		day, counts := &days[len(days)-1], mainAreas[len(mainAreas)-1]
		day.SessFrom = i // slides on towards the day's newest sitting
		day.Views += len(s.Views)
		for _, v := range s.Views {
			day.Hours += float64(v.DurationS) / 3600
			if !v.Overlap {
				counts[v.Area]++ // background must not decide what a day was about
			}
		}
	}
	for i := range days {
		days[i].Area = dominant(mainAreas[i])
	}
	return days
}

// buildTransitions counts what followed what on the main lane. A jump over a
// night is not a transition, so the walk never leaves a sitting; overlap views
// are skipped rather than breaking the chain, the same treatment they get in
// markRabbitHoles.
func buildTransitions(sessions []Session) []Transition {
	counts := map[[2]string]int{}
	for _, s := range sessions {
		prev, have := "", false
		// Views are stored newest first; "followed by" is a statement about
		// the order they were watched in.
		for i := len(s.Views) - 1; i >= 0; i-- {
			v := s.Views[i]
			if v.Overlap {
				continue
			}
			if have {
				counts[[2]string{prev, v.Area}]++
			}
			prev, have = v.Area, true
		}
	}
	out := make([]Transition, 0, len(counts))
	for k, n := range counts {
		out = append(out, Transition{From: k[0], To: k[1], N: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

// buildClusters folds every view into the area / subject / channel tree the
// cluster view packs into circles.
//
// The counts roll up rather than being recomputed per level, so a parent can
// never disagree with the sum of its children — in a drawing where area IS the
// number, that disagreement would be a visible lie.
func buildClusters(sessions []Session) []Cluster {
	type node struct {
		views int
		durS  int
		kids  map[string]*node
	}
	newNode := func() *node { return &node{kids: map[string]*node{}} }
	child := func(n *node, name string) *node {
		k := n.kids[name]
		if k == nil {
			k = newNode()
			n.kids[name] = k
		}
		return k
	}
	root := newNode()
	for _, s := range sessions {
		for _, v := range s.Views {
			sub, ch := v.Sub, v.Channel
			if sub == "" {
				sub = NoSubject
			}
			if ch == "" {
				ch = NoChannel
			}
			area := child(root, v.Area)
			subject := child(area, sub)
			for _, n := range []*node{area, subject, child(subject, ch)} {
				n.views++
				n.durS += v.DurationS
			}
		}
	}

	// Depth-first, most-watched first at every level with the name as the
	// tie-break, so two equally watched subjects do not swap between runs.
	var collect func(n *node) []Cluster
	collect = func(n *node) []Cluster {
		out := make([]Cluster, 0, len(n.kids))
		for name, k := range n.kids {
			out = append(out, Cluster{
				Name: name, Views: k.views, DurationS: k.durS,
				Children: collect(k),
			})
		}
		sortByViews(out, func(c Cluster) (int, string) { return c.Views, c.Name })
		return out
	}
	return collect(root)
}

// buildStats reduces the path to its headline numbers. It needs Days already
// filled, because BusiestDay indexes them.
//
// All three superlatives rank by VIEWS. For the sitting that is a decision:
// its span is the distance between the first and the last START, not watch
// time, so two videos half an hour apart would otherwise outrank an hour of
// clicking. The span is reported alongside, never used to rank.
func buildStats(p *Path) PathStats {
	st := PathStats{
		Views: p.Views, Sessions: len(p.Sessions), Dropped: p.Dropped,
		LongestSession: -1, DeepestRabbit: -1, BusiestDay: -1,
	}
	for si, s := range p.Sessions {
		run := 0
		for _, v := range s.Views {
			st.HoursUpper += float64(v.DurationS) / 3600
			if v.Overlap {
				st.OverlapViews++
				continue // set aside, so it neither counts nor cuts a chain
			}
			if !v.Rabbit {
				run = 0
				continue
			}
			st.RabbitViews++
			run++
			if run > st.DeepestRabbitLen {
				st.DeepestRabbitLen, st.DeepestRabbit = run, si
			}
		}
		// Strict >, so a tie keeps the newer sitting — Sessions is newest
		// first, and the reader means the recent one.
		if n := len(s.Views); n > st.LongestSessionViews {
			st.LongestSessionViews, st.LongestSession = n, si
			st.LongestSessionSpan = s.End.Sub(s.Start)
		}
	}
	// Days run oldest first, so >= is what keeps the newer day on a tie —
	// same rule as above, stated the other way round.
	for di, d := range p.Days {
		if d.Views >= st.BusiestDayViews {
			st.BusiestDayViews, st.BusiestDay = d.Views, di
		}
	}
	return st
}
