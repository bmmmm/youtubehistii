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
	Chains   []Chain      // sittings newest first, chains inside one OLDEST first
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

	// What makes one day different from the next. All main-lane only, for
	// the same reason the dominant area is: background must not vote on what
	// a day WAS.
	ChainViews int // views inside a rabbit hole
	ChainMax   int // the deepest chain that started this day, in views
	NightViews int // started inside the night window (see nightFromHour)
	AreaN      int // distinct areas — a day on one subject reads differently
	ThroughN   int // views the following gap covered in full
	EdgedN     int // views that carry ANY edge — the honest denominator
	NewChans   int // channels seen for the first time ever on this day

	// Peak is the day's STRONGEST percentile rank across the four axes
	// below, 0..1000, and PeakAxis names the axis that produced it. Not a
	// weighted score: weights would be invented numbers sitting next to
	// measured ones, and the reader could not tell them apart. "A day is
	// interesting if it was extreme in SOME way" needs no weights, and the
	// row can then say "top 0.4 % by chain depth" — a claim about the
	// distribution, which is checkable, rather than a verdict.
	Peak     int
	PeakAxis string // "views", "chain", "night" or "areas"
}

// The night window. Not "after midnight": the 23:00 hour is already the one
// where a sitting stops being an evening and becomes a night, and cutting at
// 00:00 would file the first half of every long night under the day before.
const (
	nightFromHour = 23
	nightToHour   = 5
)

// peakAxes are the four axes a day can be extreme on, in the order Peak
// breaks ties: views first because it is the axis a reader already knows.
var peakAxes = []struct {
	name string
	of   func(DayAgg) float64
}{
	{"views", func(d DayAgg) float64 { return float64(d.Views) }},
	{"chain", func(d DayAgg) float64 { return float64(d.ChainMax) }},
	{"night", func(d DayAgg) float64 {
		if d.Views == 0 {
			return 0
		}
		return float64(d.NightViews) / float64(d.Views)
	}},
	{"areas", func(d DayAgg) float64 { return float64(d.AreaN) }},
}

// Chain is one rabbit hole with an identity: the run of same-area main-lane
// views markRabbitHoles flagged, as an object the page can rank, address and
// link to. The flags stay — a card still needs to know it is inside one —
// but "which chain, how deep, where did it start" is a question about the
// run, and until it had a name nothing could ask it.
//
// First and Last index Session.Views in DISPLAY order (newest first), so
// First is the chain's NEWEST video and First <= Last.
type Chain struct {
	Session     int // index into Path.Sessions
	First, Last int // inclusive range in Sessions[Session].Views
	Len         int // main-lane views in the run; overlap views inside it do not count
	Area        string
	Span        time.Duration // first start to last start
	DurationS   int           // upper bound, the full length of every video in the run
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
	DeepestChain              int // index into Chains, -1 when there is none
	DeepestChainLen           int // that chain's length in main-lane views
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
	// One entry per session, in build (chronological) order: the chain runs
	// markRabbitHoles found, still as forward indices into that session.
	var runsPerSession [][][2]int
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
		runsPerSession = append(runsPerSession, markRabbitHoles(block))
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
	// The chains follow the same two reversals, in the same place, so the
	// off-by-one lives here and nowhere else: a forward run [a..b] of a
	// block of n views is [n-1-b .. n-1-a] once the block is newest-first,
	// and session fi becomes len-1-fi. The runs of one session keep their
	// build order, which is chronological — and chronological is the order
	// the session view draws them in, so a chain's ordinal is what a reader
	// counts from the top.
	for si := range sessions {
		fi := len(sessions) - 1 - si // the same sitting before the reversal
		vs := sessions[si].Views
		n := len(vs)
		for _, run := range runsPerSession[fi] {
			first, last := n-1-run[1], n-1-run[0]
			c := Chain{Session: si, First: first, Last: last, Area: vs[first].Area}
			for _, v := range vs[first : last+1] {
				c.DurationS += v.DurationS
				if !v.Overlap {
					c.Len++
				}
			}
			c.Span = vs[first].WatchedAt.Sub(vs[last].WatchedAt)
			p.Chains = append(p.Chains, c)
		}
	}
	p.Days = buildDays(sessions)
	markDayChains(p) // after both: a day needs the chains, the chains a session
	markDayPeaks(p.Days)
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
// breaking the chain — background music does not end a chain of videos on
// one subject, it just is not part of it.
//
// It RETURNS the runs it marked, as inclusive index pairs into vs, so the
// chain objects and the per-view flags come out of one walk. Deriving the
// chains again from the flags afterwards would be a second copy of this
// rule, and the two would disagree exactly where it matters: two chains of
// DIFFERENT areas that touch look like one long run to anything that only
// reads Rabbit (which is what the old deepest-chain counter did).
func markRabbitHoles(vs []PathView) [][2]int {
	main := make([]int, 0, len(vs))
	for i := range vs {
		if !vs[i].Overlap {
			main = append(main, i)
		}
	}
	var runs [][2]int
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
			// The bounds span the whole run INCLUDING the overlap views it
			// stepped over: they sit between its videos on screen, so a
			// bracket that skipped them would not close around what it names.
			runs = append(runs, [2]int{main[runStart], main[k-1]})
		}
		runStart = k
	}
	return runs
}

// buildDays folds the sittings into calendar days, oldest first. Sessions
// arrive newest first and are chronologically ordered, so every day owns one
// contiguous run of them and a single backwards pass finds it.
func buildDays(sessions []Session) []DayAgg {
	var days []DayAgg
	var mainAreas []map[string]int
	firstSeen := map[string]bool{} // channels already met, in time order
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
			// First contact counts wherever it happened, background
			// included: a channel first met as a track under a documentary
			// was still met, and skipping those left 992 of 7382 channels
			// counted as "new" on some LATER day, which is the one thing the
			// number must not say. Sessions arrive newest first and this loop
			// runs them backwards, so it walks the corpus in time order and
			// the first sighting here is the first sighting there was.
			if v.Channel != "" && !firstSeen[v.Channel] {
				firstSeen[v.Channel] = true
				day.NewChans++
			}
			if v.Overlap {
				continue // background decides nothing else about a day
			}
			counts[v.Area]++
			if v.Rabbit {
				day.ChainViews++
			}
			if h := v.WatchedAt.Hour(); h >= nightFromHour || h < nightToHour {
				day.NightViews++
			}
			if v.Edge != "" {
				day.EdgedN++
				if v.Edge == EdgeThrough {
					day.ThroughN++
				}
			}
		}
	}
	for i := range days {
		days[i].Area = dominant(mainAreas[i])
		days[i].AreaN = len(mainAreas[i])
	}
	return days
}

// markDayChains fills DayAgg.ChainMax: the deepest chain that STARTED on
// that day. A chain belongs to the sitting it is in, and a sitting belongs
// to the day it began — so a chain running past midnight counts on the day
// the sitting did, which is the rule the views follow too.
func markDayChains(p *Path) {
	sessDay := make([]int, len(p.Sessions))
	for di, day := range p.Days {
		for si := day.SessFrom; si <= day.SessTo; si++ {
			sessDay[si] = di
		}
	}
	for _, c := range p.Chains {
		di := sessDay[c.Session]
		if c.Len > p.Days[di].ChainMax {
			p.Days[di].ChainMax = c.Len
		}
	}
}

// markDayPeaks fills Peak and PeakAxis: the day's best percentile rank over
// peakAxes. The rank counts days scoring STRICTLY less, so the best day of n
// reaches (n-1)/n·1000 — never quite 1000, because a day does not beat
// itself — while the most common value of a flat axis stays low. That is
// what makes "top 0.4 % by chain depth" a statement about the distribution,
// checkable, rather than a verdict about the day.
//
// Ties inside an axis share a rank; between axes the order in peakAxes
// decides. Deterministic either way — nothing here reads a map's order.
func markDayPeaks(days []DayAgg) {
	if len(days) == 0 {
		return
	}
	for _, ax := range peakAxes {
		vals := make([]float64, len(days))
		for i, d := range days {
			vals[i] = ax.of(d)
		}
		sorted := append([]float64(nil), vals...)
		sort.Float64s(sorted)
		for i := range days {
			below := sort.SearchFloat64s(sorted, vals[i]) // start of this value's run
			rank := below * 1000 / len(days)
			if rank > days[i].Peak {
				days[i].Peak, days[i].PeakAxis = rank, ax.name
			}
		}
	}
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
		LongestSession: -1, DeepestChain: -1, BusiestDay: -1,
	}
	for si, s := range p.Sessions {
		for _, v := range s.Views {
			st.HoursUpper += float64(v.DurationS) / 3600
			if v.Overlap {
				st.OverlapViews++
				continue // set aside, so it neither counts nor cuts a chain
			}
			if v.Rabbit {
				st.RabbitViews++
			}
		}
		// Strict >, so a tie keeps the newer sitting — Sessions is newest
		// first, and the reader means the recent one.
		if n := len(s.Views); n > st.LongestSessionViews {
			st.LongestSessionViews, st.LongestSession = n, si
			st.LongestSessionSpan = s.End.Sub(s.Start)
		}
	}
	// The deepest chain is an argmax over the chain objects, not a run
	// counter walking the flags: a counter cannot see where one chain ends
	// and the next begins, so two touching chains of different areas used to
	// add up into a "deepest" run that no single chain ever was. Chains run
	// newest sitting first, so strict > keeps the newer one on a tie — the
	// same rule the sitting above follows.
	for ci, c := range p.Chains {
		if c.Len > st.DeepestChainLen {
			st.DeepestChainLen, st.DeepestChain = c.Len, ci
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
