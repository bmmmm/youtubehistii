// SPDX-License-Identifier: GPL-3.0-or-later

package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"html/template"
	"time"
)

// Row heights, shared between Go and the page: every row is the same height
// so the virtual list can place row i at i*rowHeight without measuring
// anything. Variable heights would mean prefix sums and a binary search for
// the first visible row — real code for a gain nobody asked for. The price is
// single-line titles, which is what the ellipsis is for.
const rowHeightPx = 56

// Row type tags, first element of every serialized row.
const (
	rowSession = 0
	rowView    = 1
)

// pathData is what the page gets: lookup tables plus rows of numbers. Titles
// dominate the payload, everything else is an index — that keeps 35k views
// near 4 MB instead of the 7 MB an array of objects would cost.
type pathData struct {
	T0       int64      `json:"t0"` // unix seconds of the oldest view
	Chans    []string   `json:"chans"`
	Areas    []string   `json:"areas"`
	AreaHues []int      `json:"areaHues"` // parallel to Areas
	Subs     []string   `json:"subs"`
	Modes    []string   `json:"modes"`
	Edges    []string   `json:"edges"`
	Rows     [][]any    `json:"rows"`
	Sessions int        `json:"sessions"`
	Views    int        `json:"views"`
	Dropped  int        `json:"dropped"`
	Span     [2]float64 `json:"span"` // unix seconds, oldest and newest

	// The aggregates the other views run on. They index into rows, days and
	// areas rather than repeating anything: the timeline is the one copy of
	// the data, every other view is a lens on it.
	Sess      [][]any    `json:"sess"` // parallel to Path.Sessions, newest first
	Days      [][]any    `json:"days"` // parallel to Path.Days, oldest first
	Trans     [][]int    `json:"trans"`
	AreaViews []int      `json:"areaViews"` // parallel to Areas: main-lane views per area
	Stats     *statsData `json:"stats"`
}

// statsData is PathStats as the page reads it — durations in seconds, because
// JSON has no duration and the page formats everything from seconds anyway.
type statsData struct {
	Views          int     `json:"views"`
	Sessions       int     `json:"sessions"`
	Dropped        int     `json:"dropped"`
	OverlapViews   int     `json:"overlapViews"`
	RabbitViews    int     `json:"rabbitViews"`
	HoursUpper     float64 `json:"hoursUpper"`
	LongestSess    int     `json:"longestSess"`
	LongestSessN   int     `json:"longestSessN"`
	LongestSessS   int     `json:"longestSessS"`
	DeepestRabbit  int     `json:"deepestRabbit"`
	DeepestRabbitN int     `json:"deepestRabbitN"`
	BusiestDay     int     `json:"busiestDay"`
	BusiestDayN    int     `json:"busiestDayN"`
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
func buildPathData(p *Path) *pathData {
	d := &pathData{
		Sessions: len(p.Sessions),
		Views:    p.Views,
		Dropped:  p.Dropped,
	}
	if !p.From.IsZero() {
		d.T0 = p.From.Unix()
		d.Span = [2]float64{float64(p.From.Unix()), float64(p.To.Unix())}
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
	for si, s := range p.Sessions {
		rowIdx := len(d.Rows)
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
		d.Days = append(d.Days, []any{day.EpochDay, day.Views, areaIdx, day.SessFrom, day.SessTo})
	}
	d.Trans = make([][]int, 0, len(p.Trans))
	for _, tr := range p.Trans {
		d.Trans = append(d.Trans, []int{areas.get(tr.From), areas.get(tr.To), tr.N})
	}
	d.Stats = &statsData{
		Views:          p.Stats.Views,
		Sessions:       p.Stats.Sessions,
		Dropped:        p.Stats.Dropped,
		OverlapViews:   p.Stats.OverlapViews,
		RabbitViews:    p.Stats.RabbitViews,
		HoursUpper:     p.Stats.HoursUpper,
		LongestSess:    p.Stats.LongestSession,
		LongestSessN:   p.Stats.LongestSessionViews,
		LongestSessS:   int(p.Stats.LongestSessionSpan.Seconds()),
		DeepestRabbit:  p.Stats.DeepestRabbit,
		DeepestRabbitN: p.Stats.DeepestRabbitLen,
		BusiestDay:     p.Stats.BusiestDay,
		BusiestDayN:    p.Stats.BusiestDayViews,
	}

	d.Chans, d.Areas, d.Subs, d.Modes, d.Edges = chans.list, areas.list, subs.list, modes.list, edges.list
	for _, a := range d.Areas {
		d.AreaHues = append(d.AreaHues, areaHue(a))
	}
	return d
}

// RenderWatchPath produces the self-contained timeline page — same rules as
// RenderHTML: system fonts, no external assets, light/dark by preference.
func RenderWatchPath(p *Path, generated time.Time) ([]byte, error) {
	data := buildPathData(p)
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
		"Generated":  generated.Format("2006-01-02 15:04"),
		"SessionGap": fmt.Sprintf("%.0f", sessionGap.Minutes()),
		"LongVideo":  fmt.Sprintf("%d", longVideoS/60),
		"RabbitLen":  fmt.Sprintf("%d", rabbitMinLen),
		"RabbitGap":  fmt.Sprintf("%.0f", rabbitMaxGap.Minutes()),
	})
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
