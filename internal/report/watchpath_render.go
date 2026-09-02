// SPDX-License-Identifier: GPL-3.0-or-later

package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"html/template"
	"sort"
	"strings"
	"time"
)

// Row heights, shared between Go and the page: every row is the same height
// so the virtual list can place row i at i*rowHeight without measuring
// anything. Variable heights would mean prefix sums and a binary search for
// the first visible row — real code for a gain nobody asked for. The price is
// single-line titles, which is what the ellipsis is for.
const rowHeightPx = 56

// rankRowPx is the same idea for the two ranking views, whose rows carry a
// second line: the facts, then the sentence that explains them.
const rankRowPx = 62

// Row type tags, first element of every serialized row.
const (
	rowSession = 0
	rowView    = 1
)

// pathData is what the page gets: lookup tables plus rows of numbers. Titles
// dominate the payload, everything else is an index — that keeps 35k views
// near 4 MB instead of the 7 MB an array of objects would cost.
type pathData struct {
	T0       int64    `json:"t0"` // unix seconds of the oldest view
	Chans    []string `json:"chans"`
	Areas    []string `json:"areas"`
	AreaHues []int    `json:"areaHues"` // parallel to Areas
	Subs     []string `json:"subs"`
	Modes    []string `json:"modes"`
	Edges    []string `json:"edges"`
	Rows     [][]any  `json:"rows"`
	Sessions int      `json:"sessions"`
	Views    int      `json:"views"`
	Dropped  int      `json:"dropped"`

	// The aggregates the other views run on. They index into rows, days and
	// areas rather than repeating anything: the timeline is the one copy of
	// the data, every other view is a lens on it.
	Sess      [][]any     `json:"sess"` // parallel to Path.Sessions, newest first
	Days      [][]any     `json:"days"` // parallel to Path.Days, oldest first
	Chains    [][]int     `json:"chains"`
	Trans     [][]int     `json:"trans"`
	AreaViews []int       `json:"areaViews"` // parallel to Areas: main-lane views per area
	Clusters  [][]any     `json:"clusters"`  // area / subject / channel, most-watched first
	Stats     *statsData  `json:"stats"`
	Report    *reportData `json:"report,omitempty"` // nil when no Stats were handed in
	// HoleLabels is [[chainIdx, "label"], …] for the chains a model named.
	// Sparse and omitted entirely when no run produced any: the page reads
	// it as decoration, never as structure.
	HoleLabels [][]any `json:"holeLabels,omitempty"`

	// Taxonomy identifies the projection the topics were folded through —
	// the same string the CSV carries in its first line and the terminal
	// prints. Two artefacts of one run that disagree about their topics used
	// to have no way to be told apart.
	Taxonomy string `json:"taxonomy,omitempty"`
}

// clusterNodes turns the topic tree into [name, views, durationS, children],
// with the children left off a leaf. Positional, like every other row on this
// page: the tree has one entry per channel and the key names would outweigh
// the numbers they label.
func clusterNodes(cs []Cluster) [][]any {
	out := make([][]any, 0, len(cs))
	for _, c := range cs {
		n := []any{c.Name, c.Views, c.DurationS}
		if len(c.Children) > 0 {
			n = append(n, clusterNodes(c.Children))
		}
		out = append(out, n)
	}
	return out
}

// statsData is PathStats as the page READS it — not as PathStats is. Only the
// numbers a view actually puts on screen travel: durations in seconds, because
// JSON has no duration, and nothing that already rides on the payload's top
// level. A serialised field nobody reads is a promise the page does not keep,
// so the count of dropped views stays at d.Dropped alone, and the chain total
// stays in PathStats for the terminal to print.
type statsData struct {
	Views        int     `json:"views"`
	Sessions     int     `json:"sessions"`
	OverlapViews int     `json:"overlapViews"`
	HoursUpper   float64 `json:"hoursUpper"`
	LongestSess  int     `json:"longestSess"`
	LongestSessN int     `json:"longestSessN"`
	LongestSessS int     `json:"longestSessS"`
	// One index where there used to be two numbers: the chain row carries
	// its own length and sitting, so shipping them again would be two
	// spellings of one fact.
	DeepestChain int `json:"deepestChain"`
	BusiestDay   int `json:"busiestDay"`
	BusiestDayN  int `json:"busiestDayN"`
}

// reportData is the aggregate report as the report VIEW reads it — the same
// rule statsData follows: only the numbers a view actually puts on screen
// travel, and nothing that already rides at the top level of the payload.
//
// Two conversions happen here rather than on the page. Durations become whole
// seconds, because JSON has no duration and every other duration on this page
// is one; and every channel name becomes an index into the payload's one Chans
// table, because a name a timeline row already carries must not ride twice.
type reportData struct {
	Views   int   `json:"views"`  // ALL classified views, including the undated
	Unique  int   `json:"unique"` // distinct video ids
	DurS    int   `json:"durS"`   // upper bound over every view, in seconds
	Sources []int `json:"sources"`
	NoID    int   `json:"noID"` // views with no video link at all
	Gone    int   `json:"gone"` // views on videos no longer on YouTube

	// Positional rows, the clusterNodes idiom: the key names would outweigh
	// the numbers they label. The comments are the layout, and the page names
	// the same offsets as constants.
	Topics  [][]any `json:"topics"`  // name, mode, views, durS, [subjects]
	Months  [][]any `json:"months"`  // month, views per mode
	Chans   [][]any `json:"chans"`   // channel, top topic, views, durS, subscribed
	Subs    [][]any `json:"subs"`    // channel, top topic, views, durS, last watched
	Unclear []int   `json:"unclear"` // channels carrying the most unclear views

	HasSubs  bool `json:"hasSubs"`
	SubViews int  `json:"subViews"`
	SubDurS  int  `json:"subDurS"`
	DeadSubs int  `json:"deadSubs"`
}

// topChannels is where the channel table stops being a top list and starts
// being a directory. The old HTML report drew the same 25.
const topChannels = 25

// capChannels keeps the channel list to the n most-watched.
func capChannels(cs []ChannelAgg, n int) []ChannelAgg {
	if len(cs) > n {
		return cs[:n]
	}
	return cs
}

// durS turns an upper-bound hour figure back into whole seconds.
func durS(hours float64) int { return int(hours*3600 + 0.5) }

// peakAxisIdx sends the axis name as its position in peakAxes — the page has
// the same list in the same order, so the name itself never has to ride 2374
// times. -1 for a day that peaked on nothing (an empty path).
func peakAxisIdx(name string) int {
	for i, ax := range peakAxes {
		if ax.name == name {
			return i
		}
	}
	return -1
}

// epochDay is the grid coordinate of a wall-clock date — the same number
// DayAgg.EpochDay carries, so the page formats both with one helper.
func epochDay(t time.Time) int {
	y, m, d := t.Date()
	return int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// buildReportData folds the aggregate stats into the payload. It borrows the
// interns of the timeline it travels with, so a channel already named by a
// view row costs nothing here; that is only safe because interning appends —
// an index handed out earlier never moves.
//
// st == nil is the whole "no report" case: the block stays off the payload,
// and the page then offers neither the card nor the route.
func buildReportData(st *Stats, chans, modes *intern) *reportData {
	if st == nil {
		return nil
	}
	r := &reportData{
		Views:  st.Views,
		Unique: st.UniqueVideos,
		DurS:   durS(st.HoursUpper),
		// Fixed order — rule, llm, youtube category, still open — so the four
		// counts cost four numbers instead of four keys.
		Sources:  []int{st.Sources["rule"], st.Sources["llm"], st.Sources["category"], st.Sources["unclassified"]},
		NoID:     st.NoID,
		Gone:     st.Unavailable,
		HasSubs:  st.HasSubs,
		SubViews: st.SubbedViews,
		SubDurS:  durS(st.SubbedHours),
		DeadSubs: st.DeadSubs,
	}
	for _, t := range st.Topics {
		row := []any{t.Topic, modes.get(t.Mode), t.Views, durS(t.Hours)}
		if len(t.Subs) > 0 {
			kids := make([][]any, 0, len(t.Subs))
			for _, s := range t.Subs {
				kids = append(kids, []any{s.Sub, modes.get(s.Mode), s.Views, durS(s.Hours)})
			}
			row = append(row, kids)
		}
		r.Topics = append(r.Topics, row)
	}
	// The per-month counts are parallel to ModeOrder, which newIntern seeded
	// the Modes table with — so column i is D.modes[i] on the page, and the
	// month rows carry no mode names of their own.
	for _, m := range st.Months {
		views := make([]int, len(ModeOrder))
		for i, mode := range ModeOrder {
			views[i] = m.ModeViews[mode]
		}
		r.Months = append(r.Months, []any{m.Month, views})
	}
	for _, c := range capChannels(st.Channels, topChannels) {
		r.Chans = append(r.Chans, []any{chans.get(c.Name), c.TopTopic, c.Views, durS(c.Hours), c.Subscribed})
	}
	for _, s := range st.Subs {
		// -1 is "never watched in this export"; the page says so in words
		// rather than printing a date nothing happened on.
		last := -1
		if !s.LastWatched.IsZero() {
			last = epochDay(s.LastWatched.Local())
		}
		r.Subs = append(r.Subs, []any{chans.get(s.Title), s.TopTopic, s.Views, durS(s.Hours), last})
	}
	for _, name := range st.UnclearNames {
		r.Unclear = append(r.Unclear, chans.get(name))
	}
	return r
}

// intern hands out stable indices for repeated strings.
type intern struct {
	idx  map[string]int
	list []string
}

func newIntern(seed ...string) *intern {
	in := &intern{idx: map[string]int{}}
	for _, s := range seed {
		in.get(s)
	}
	return in
}

func (in *intern) get(s string) int {
	if i, ok := in.idx[s]; ok {
		return i
	}
	i := len(in.list)
	in.idx[s] = i
	in.list = append(in.list, s)
	return i
}

// areaHue derives a stable colour from the area name. Hashing beats a hand
// written palette here: the area list is YouTube's and can grow, and a name
// always lands on the same hue without anyone maintaining a mapping. The
// saturation and lightness stay in CSS so light and dark can differ.
func areaHue(area string) int {
	h := fnv.New32a()
	h.Write([]byte(area))
	return int(h.Sum32() % 360)
}

// buildPathData flattens the path into rows. The row list is built in Go on
// purpose: the page then owns nothing but scroll positions, and every rule
// that decides what a row MEANS stays in tested code.
func buildPathData(p *Path, st *Stats) *pathData {
	d := &pathData{
		Sessions: len(p.Sessions),
		Views:    p.Views,
		Dropped:  p.Dropped,
	}
	if !p.From.IsZero() {
		d.T0 = p.From.Unix()
	}
	chans := newIntern()
	areas := newIntern()
	subs := newIntern("") // index 0 is "no sub"
	modes := newIntern(ModeOrder...)
	edges := newIntern("", EdgeThrough, EdgeMost, EdgeSkipped)

	// Which day each sitting belongs to, read off the day ranges so the two
	// directions can never disagree.
	sessDay := make([]int, len(p.Sessions))
	for di, day := range p.Days {
		for si := day.SessFrom; si <= day.SessTo; si++ {
			sessDay[si] = di
		}
	}

	d.Sess = make([][]any, 0, len(p.Sessions))
	sessRow := make([]int, len(p.Sessions)) // where each sitting's block starts
	for si, s := range p.Sessions {
		rowIdx := len(d.Rows)
		sessRow[si] = rowIdx
		// A session row carries what the separator has to say: when it
		// started, how long it ran, how many videos, and the silence before it.
		d.Rows = append(d.Rows, []any{
			rowSession,
			s.Start.Unix() - d.T0,
			int(s.End.Sub(s.Start).Seconds()),
			len(s.Views),
			int(s.GapBefore.Seconds()),
		})
		// Everything else points at this row instead of copying it: the views
		// of the sitting are rows[rowIdx+1 … rowIdx+nViews].
		d.Sess = append(d.Sess, []any{
			rowIdx,
			s.Start.Unix() - d.T0,
			int(s.End.Sub(s.Start).Seconds()),
			len(s.Views),
			int(s.GapBefore.Seconds()),
			sessDay[si],
		})
		for _, v := range s.Views {
			flags := 0
			if v.Overlap {
				flags |= 1
			}
			if v.Rabbit {
				flags |= 2
			}
			d.Rows = append(d.Rows, []any{
				rowView,
				v.WatchedAt.Unix() - d.T0,
				v.DurationS,
				areas.get(v.Area),
				subs.get(v.Sub),
				chans.get(v.Channel),
				modes.get(v.Mode),
				edges.get(v.Edge),
				v.GapS,
				flags,
				v.Title,
			})
		}
	}
	// The chains ship as row RANGES, not as copies: everything else about a
	// rabbit hole — its channels, the video it was entered from, the edge it
	// ended on — is a walk over rows[from…to] the page can do in a
	// millisecond, and a walk costs no bytes.
	d.Chains = make([][]int, 0, len(p.Chains))
	for _, c := range p.Chains {
		base := sessRow[c.Session] + 1 // +1: the sitting's own row comes first
		d.Chains = append(d.Chains, []int{
			c.Session, base + c.First, base + c.Last, c.Len,
			areas.get(c.Area), int(c.Span.Seconds()), c.DurationS,
		})
	}
	// From here on nothing new may be interned: the area indices are already
	// baked into the rows above, and the aggregates only look them up.
	d.AreaViews = make([]int, len(areas.list))
	for _, s := range p.Sessions {
		for _, v := range s.Views {
			if !v.Overlap {
				d.AreaViews[areas.get(v.Area)]++
			}
		}
	}
	d.Days = make([][]any, 0, len(p.Days))
	for _, day := range p.Days {
		areaIdx := -1 // a day with nothing but overlap views has no topic
		if day.Area != "" {
			areaIdx = areas.get(day.Area)
		}
		// The numbers after the range are what tells one day from the next.
		// They are cheap — a day costs nine more integers where a single
		// title costs more than that in bytes — and they are what the
		// ranking, the calendar lenses and the day view all read.
		d.Days = append(d.Days, []any{day.EpochDay, day.Views, areaIdx, day.SessFrom, day.SessTo,
			durS(day.Hours), day.ChainViews, day.ChainMax, day.NightViews, day.AreaN,
			day.ThroughN, day.EdgedN, day.NewChans, day.Peak, peakAxisIdx(day.PeakAxis)})
	}
	d.Trans = make([][]int, 0, len(p.Trans))
	for _, tr := range p.Trans {
		d.Trans = append(d.Trans, []int{areas.get(tr.From), areas.get(tr.To), tr.N})
	}
	// The topic tree ships as names, not indices. Every other aggregate points
	// into a lookup table because it repeats an area thousands of times; here
	// each name appears exactly once, so an index would only add a hop — and
	// the page needs the plain name anyway to put it in a URL.
	d.Clusters = clusterNodes(p.Clusters)
	d.Stats = &statsData{
		Views:        p.Stats.Views,
		Sessions:     p.Stats.Sessions,
		OverlapViews: p.Stats.OverlapViews,
		HoursUpper:   p.Stats.HoursUpper,
		LongestSess:  p.Stats.LongestSession,
		LongestSessN: p.Stats.LongestSessionViews,
		LongestSessS: int(p.Stats.LongestSessionSpan.Seconds()),
		DeepestChain: p.Stats.DeepestChain,
		BusiestDay:   p.Stats.BusiestDay,
		BusiestDayN:  p.Stats.BusiestDayViews,
	}
	// Last, because it is the one part that may still put a name into a lookup
	// table: a subscription never watched appears in no view row, and a
	// report-only mode in no timeline row.
	d.Report = buildReportData(st, chans, modes)

	d.Chans, d.Areas, d.Subs, d.Modes, d.Edges = chans.list, areas.list, subs.list, modes.list, edges.list
	for _, a := range d.Areas {
		d.AreaHues = append(d.AreaHues, areaHue(a))
	}
	return d
}

// RenderWatchPath produces the self-contained page: system fonts, no external
// assets, light/dark by preference. It is the only HTML this package writes —
// the aggregate report is a view of this page, not a second file, which is why
// it takes the stats alongside the path.
//
// st may be nil: the page then renders without the report view, which is what
// keeps the shell independent of an aggregation it does not need.
func RenderWatchPath(p *Path, st *Stats, generated time.Time) ([]byte, error) {
	return RenderWatchPathOpts(p, st, generated, WatchPathOpts{})
}

// WatchPathOpts carries what the page can have but does not need. Everything
// in here is decoration: the page must render, and read correctly, with the
// zero value — which is what keeps a model outage from producing a broken
// dashboard rather than a plainer one.
type WatchPathOpts struct {
	// HoleLabels names chains by index into Path.Chains. Sparse on purpose:
	// only the ones a run actually paid for.
	HoleLabels map[int]string

	// Taxonomy is the provenance of the projection the rows were folded
	// through ("sha256:… <path> <mtime>", or "none").
	Taxonomy string
}

// RenderWatchPathOpts is RenderWatchPath plus the optional extras.
func RenderWatchPathOpts(p *Path, st *Stats, generated time.Time, o WatchPathOpts) ([]byte, error) {
	data := buildPathData(p, st)
	data.Taxonomy = o.Taxonomy
	if len(o.HoleLabels) > 0 {
		// Sorted by index so two runs of the same data produce the same
		// bytes — a page that differs only in map order would look like a
		// change in every diff.
		idx := make([]int, 0, len(o.HoleLabels))
		for ci := range o.HoleLabels {
			if ci >= 0 && ci < len(p.Chains) {
				idx = append(idx, ci)
			}
		}
		sort.Ints(idx)
		data.HoleLabels = make([][]any, 0, len(idx))
		for _, ci := range idx {
			data.HoleLabels = append(data.HoleLabels, []any{ci, o.HoleLabels[ci]})
		}
	}
	// What the head line says about the projection. Short form: the digest
	// alone tells two taxonomies apart by eye, and the path and the mtime
	// stay one keystroke away in the payload.
	//
	// Empty unless a projection actually ran. taxonomyProvenance answers
	// "none" for an unfolded run, and "· taxonomy none" on every such page
	// would be noise about a thing that did not happen.
	head := ""
	if strings.HasPrefix(o.Taxonomy, "sha256:") {
		head = " · taxonomy " + strings.Fields(o.Taxonomy)[0]
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	// json.Marshal escapes <, > and & by default, so a video title can never
	// close the script element it rides in. That escaping is what makes
	// template.JS safe here; do not switch the encoder to SetEscapeHTML(false).
	tpl, err := template.New("watchpath").Funcs(template.FuncMap{
		"date": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.Format("2006-01-02")
		},
	}).Parse(watchPathTpl)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	err = tpl.Execute(&buf, map[string]any{
		"Data":       template.JS(raw),
		"P":          p,
		"RowHeight":  rowHeightPx,
		"RankHeight": rankRowPx,
		"Generated":  generated.Format("2006-01-02 15:04"),
		"Taxonomy":   head,
		"SessionGap": fmt.Sprintf("%.0f", sessionGap.Minutes()),
		"LongVideo":  fmt.Sprintf("%d", longVideoS/60),
		"RabbitLen":  fmt.Sprintf("%d", rabbitMinLen),
		"RabbitGap":  fmt.Sprintf("%.0f", rabbitMaxGap.Minutes()),
		"NightFrom":  fmt.Sprintf("%d", nightFromHour),
		"NightTo":    fmt.Sprintf("%d", nightToHour),
	})
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
