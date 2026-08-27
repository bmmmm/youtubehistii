// SPDX-License-Identifier: GPL-3.0-or-later

package report

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

// pathT0 is local on purpose. BuildPath reads every timestamp in wall-clock
// time, so a fixture written in UTC would assert a different calendar day on
// every machine. July has no daylight-saving transition anywhere, which is
// what keeps the offsets below plain wall-clock arithmetic.
var pathT0 = time.Date(2026, 7, 1, 12, 0, 0, 0, time.Local)

// view builds one classified row at t0 + offset, so a test reads as a
// timeline rather than as a list of timestamps.
func view(offset time.Duration, topic string, durationS int) classify.Verdict {
	return classify.Verdict{
		VideoID:   topic,
		Title:     topic,
		Channel:   "chan",
		WatchedAt: pathT0.Add(offset),
		Topic:     topic,
		Mode:      "consume",
		DurationS: durationS,
	}
}

// flat returns every view of the path in display order (newest first),
// sessions concatenated.
func flat(p *Path) []PathView {
	var out []PathView
	for _, s := range p.Sessions {
		out = append(out, s.Views...)
	}
	return out
}

// pathFixture is a hand-countable path: two sittings on 2026-07-01 (the
// second holding a four-video chain), one on 2026-07-03 with music running
// through a documentary, and one row the export never dated.
func pathFixture() []classify.Verdict {
	return []classify.Verdict{
		view(0, "music", 300),
		view(5*time.Minute, "music", 300),

		view(60*time.Minute, "sports", 300),
		view(66*time.Minute, "sports", 300),
		view(72*time.Minute, "sports", 300),
		view(78*time.Minute, "sports", 300),
		view(84*time.Minute, "music", 300),

		view(48*time.Hour, "film-animation", 45*60),
		view(48*time.Hour+5*time.Minute, "music", 200),
		view(48*time.Hour+10*time.Minute, "film-animation", 45*60),

		{VideoID: "undated", Title: "undated", Topic: "music"},
	}
}

// hoursEqual compares watch-hour sums. They are accumulated per view, so they
// only ever match a closed-form expectation to within a rounding step.
func hoursEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSessionSplitAtTheThreshold(t *testing.T) {
	// 29 minutes holds a sitting together, 31 breaks it — the boundary itself
	// is the whole point of the rule.
	p := BuildPath([]classify.Verdict{
		view(0, "music", 300),
		view(29*time.Minute, "music", 300),
		view(29*time.Minute+31*time.Minute, "music", 300),
	})
	if len(p.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(p.Sessions))
	}
	// Newest session first.
	if len(p.Sessions[0].Views) != 1 || len(p.Sessions[1].Views) != 2 {
		t.Errorf("session sizes = %d and %d, want 1 (newest) then 2",
			len(p.Sessions[0].Views), len(p.Sessions[1].Views))
	}
	if got := p.Sessions[0].GapBefore; got != 31*time.Minute {
		t.Errorf("gap before the newest session = %v, want 31m", got)
	}
	if p.Sessions[1].GapBefore != 0 {
		t.Errorf("the oldest session has nothing before it, got %v", p.Sessions[1].GapBefore)
	}
}

func TestEdgeSemantics(t *testing.T) {
	// One 10-minute video per case, followed after a gap that decides the label.
	cases := []struct {
		name string
		gap  time.Duration
		dur  int
		want string
	}{
		{"gap covers the full length", 10 * time.Minute, 600, EdgeThrough},
		{"gap longer than the video", 12 * time.Minute, 600, EdgeThrough},
		{"over half, under all", 7 * time.Minute, 600, EdgeMost},
		{"exactly half is already short", 5 * time.Minute, 600, EdgeMost},
		{"well under half", 1 * time.Minute, 600, EdgeSkipped},
		{"no duration, no claim", 1 * time.Minute, 0, ""},
	}
	for _, c := range cases {
		p := BuildPath([]classify.Verdict{
			view(0, "music", c.dur),
			view(c.gap, "music", 600),
		})
		vs := flat(p)
		// vs[0] is the newer one; the edge belongs to the view it leaves.
		if got := vs[1].Edge; got != c.want {
			t.Errorf("%s: edge = %q, want %q", c.name, got, c.want)
		}
		if got := vs[1].GapS; got != int(c.gap.Seconds()) {
			t.Errorf("%s: gap = %ds, want %v", c.name, got, c.gap)
		}
	}
	// The last view of a session leaves nothing, so it carries no edge.
	if vs := flat(BuildPath([]classify.Verdict{view(0, "music", 600)})); vs[0].Edge != "" || vs[0].GapS != 0 {
		t.Errorf("a lone view has no edge, got %q / %ds", vs[0].Edge, vs[0].GapS)
	}
}

func TestOverlapNeedsAllThreeConditions(t *testing.T) {
	// The case from the spec: music entries falling inside a 45-minute
	// documentary. The doc keeps the main lane, the music is set aside.
	p := BuildPath([]classify.Verdict{
		view(0, "film-animation", 45*60),
		view(5*time.Minute, "music", 200),
		view(9*time.Minute, "music", 200),
		view(50*time.Minute, "music", 200), // after the doc ended: main lane again
	})
	vs := flat(p) // newest first
	if vs[0].Overlap {
		t.Error("a view starting after the long video ended is not an overlap")
	}
	if !vs[1].Overlap || !vs[2].Overlap {
		t.Errorf("music inside the documentary should be marked: %v / %v", vs[1].Overlap, vs[2].Overlap)
	}
	if vs[3].Overlap {
		t.Error("the documentary itself holds the main lane")
	}

	// Same gaps, short video: clicking through is not an overlap, it is a
	// series of abandoned videos, and we do not dress that up as parallel.
	short := flat(BuildPath([]classify.Verdict{
		view(0, "music", 240),
		view(1*time.Minute, "sports", 240),
		view(2*time.Minute, "sports", 240),
	}))
	for i, v := range short {
		if v.Overlap {
			t.Errorf("short video %d must not be an overlap", i)
		}
	}

	// Long video, but the same area: that is one topic continuing, not two
	// streams interleaving.
	same := flat(BuildPath([]classify.Verdict{
		view(0, "music", 45*60),
		view(5*time.Minute, "music", 200),
	}))
	for i, v := range same {
		if v.Overlap {
			t.Errorf("same-area view %d must not be an overlap", i)
		}
	}
}

func TestRabbitHoleChains(t *testing.T) {
	// Four cycling videos back to back — a chain.
	four := flat(BuildPath([]classify.Verdict{
		view(0, "sports", 300),
		view(6*time.Minute, "sports", 300),
		view(12*time.Minute, "sports", 300),
		view(18*time.Minute, "sports", 300),
	}))
	for i, v := range four {
		if !v.Rabbit {
			t.Errorf("view %d should be part of the chain", i)
		}
	}

	// Three is below the bar.
	three := flat(BuildPath([]classify.Verdict{
		view(0, "sports", 300),
		view(6*time.Minute, "sports", 300),
		view(12*time.Minute, "sports", 300),
	}))
	for i, v := range three {
		if v.Rabbit {
			t.Errorf("view %d: three in a row is not a rabbit hole yet", i)
		}
	}

	// Another area in the middle breaks it: 2 + 2, neither long enough.
	broken := flat(BuildPath([]classify.Verdict{
		view(0, "sports", 300),
		view(6*time.Minute, "sports", 300),
		view(12*time.Minute, "music", 300),
		view(18*time.Minute, "sports", 300),
		view(24*time.Minute, "sports", 300),
	}))
	for i, v := range broken {
		if v.Rabbit {
			t.Errorf("view %d: the interruption breaks the chain", i)
		}
	}

	// A gap over rabbitMaxGap breaks it too, without ending the session.
	slow := flat(BuildPath([]classify.Verdict{
		view(0, "sports", 300),
		view(16*time.Minute, "sports", 300),
		view(32*time.Minute, "sports", 300),
		view(48*time.Minute, "sports", 300),
	}))
	if len(slow) != 4 {
		t.Fatalf("gaps under %v stay one session, got %d views", sessionGap, len(slow))
	}
	for i, v := range slow {
		if v.Rabbit {
			t.Errorf("view %d: 16 minutes apart is not back to back", i)
		}
	}

	// Background music inside a long documentary does not break a chain of
	// documentaries — it is set aside, not counted against it.
	withMusic := flat(BuildPath([]classify.Verdict{
		view(0, "film-animation", 45*60),
		view(5*time.Minute, "music", 200),
		view(10*time.Minute, "film-animation", 45*60),
		view(20*time.Minute, "film-animation", 45*60),
		view(30*time.Minute, "film-animation", 45*60),
	}))
	docs, music := 0, 0
	for _, v := range withMusic {
		switch {
		case v.Area == "music":
			music++
			if v.Rabbit {
				t.Error("an overlap view is not part of the chain")
			}
		case v.Rabbit:
			docs++
		}
	}
	if docs != 4 || music != 1 {
		t.Errorf("chain = %d documentaries (want 4), %d music aside (want 1)", docs, music)
	}
}

func TestBuildPathDropsUndatedViews(t *testing.T) {
	rows := []classify.Verdict{
		view(0, "music", 300),
		{VideoID: "x", Title: "no timestamp", Topic: "music"}, // zero WatchedAt
		view(5*time.Minute, "sports", 300),
	}
	p := BuildPath(rows)
	if p.Views != 2 || p.Dropped != 1 {
		t.Errorf("views=%d dropped=%d, want 2 and 1", p.Views, p.Dropped)
	}
	vs := flat(p)
	if vs[0].Area != "sports" || vs[1].Area != "music" {
		t.Errorf("order broke: %q then %q, want sports then music (newest first)", vs[0].Area, vs[1].Area)
	}
	if p.From != pathT0 || p.To != pathT0.Add(5*time.Minute) {
		t.Errorf("range = %v … %v", p.From, p.To)
	}
}

func TestBuildPathEmpty(t *testing.T) {
	p := BuildPath(nil)
	if len(p.Sessions) != 0 || p.Views != 0 {
		t.Errorf("empty input, got %+v", p)
	}
}

func TestRenderWatchPathIsSelfContained(t *testing.T) {
	p := BuildPath([]classify.Verdict{
		view(0, "music", 300),
		view(5*time.Minute, "sports", 300),
		view(90*time.Minute, "gaming", 300),
	})
	// With the stats, so the guard covers the report view too — it is the
	// biggest single block of markup on the page.
	html, err := RenderWatchPath(p, Aggregate(pathFixture(), nil), pathT0)
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, want := range []string{"prefers-color-scheme", "-apple-system", "watch path",
		"Takeout logs when a video was STARTED", "overlap suspected"} {
		if !strings.Contains(page, want) {
			t.Errorf("page misses %q", want)
		}
	}
	// No external asset may sneak in — the page has to work offline forever.
	//
	// The check runs on the page WITHOUT its data payload: a video title may
	// legitimately contain a URL, and a title is data, not a reference. What
	// the guard is really about is whether the page can be made to LOAD
	// something, so it names the constructs that load rather than banning the
	// substring "http".
	shell := strings.Replace(page, string(mustPayload(t, html)), "", 1)
	for _, bad := range []string{"<link", "<iframe", "<img", "src=", "@import",
		`href="http`, "href='http", "fetch(", "XMLHttpRequest", "importScripts"} {
		if strings.Contains(shell, bad) {
			t.Errorf("page reaches outside via %q", bad)
		}
	}
	// A url() is allowed only where it points back into this page — that is how
	// an SVG marker is referenced. Anything else would be a fetch.
	for i := 0; ; {
		j := strings.Index(shell[i:], "url(")
		if j < 0 {
			break
		}
		i += j + len("url(")
		if !strings.HasPrefix(shell[i:], "#") {
			t.Errorf("url() does not point at a fragment of this page: %.40q", shell[i-4:])
		}
	}
	// After that, the only URL left in the shell is the SVG namespace, which
	// createElementNS needs as an identifier and never fetches. Naming the one
	// exception keeps the guard exact instead of blunt.
	rest := strings.ReplaceAll(shell, "http://www.w3.org/2000/svg", "")
	for _, scheme := range []string{"http://", "https://", "//fonts."} {
		if strings.Contains(rest, scheme) {
			t.Errorf("page carries a URL (%q) beyond the SVG namespace", scheme)
		}
	}
}

func TestRenderWatchPathEscapesTitles(t *testing.T) {
	// A title is attacker-adjacent data: it comes from YouTube, and it rides
	// inside a script element. json.Marshal's escaping is what stops it from
	// closing that element — this test is the guard on that assumption.
	nasty := `</script><img src=x onerror=alert(1)>`
	rows := []classify.Verdict{view(0, "music", 300)}
	rows[0].Title = nasty
	// The channel name is the report view's half of the same problem: it
	// travels in the shared Chans table, which the report block indexes into.
	rows[0].Channel = nasty
	html, err := RenderWatchPath(BuildPath(rows), Aggregate(rows, nil), pathT0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(html), "</script><img") {
		t.Error("a video title closed the script element")
	}
	if !strings.Contains(string(html), `</script>`) {
		t.Error("the title should survive as escaped data, not vanish")
	}

	// And the payload must still parse as JSON.
	var d pathData
	if err := json.Unmarshal(mustPayload(t, html), &d); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if len(d.Rows) != 2 { // one session row, one view row
		t.Fatalf("rows = %d, want 2", len(d.Rows))
	}
	title, _ := d.Rows[1][10].(string)
	if title != nasty {
		t.Errorf("title round-tripped as %q", title)
	}
}

// mustPayload extracts the JSON that the page assigns to D.
func mustPayload(t *testing.T, html []byte) []byte {
	t.Helper()
	const marker = "const D = "
	i := bytes.Index(html, []byte(marker))
	if i < 0 {
		t.Fatal("no data payload in the page")
	}
	rest := html[i+len(marker):]
	j := bytes.Index(rest, []byte(";\n"))
	if j < 0 {
		t.Fatal("payload not terminated")
	}
	return rest[:j]
}

// num reads a number back out of a positional row. Everything inside a
// [][]any comes back from JSON as float64, and a row is read by offset.
func num(t *testing.T, v any) int {
	t.Helper()
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("not a number: %#v", v)
	}
	return int(f)
}

// The report is a VIEW of this page now, so its numbers ride on the same
// payload. They have to be the numbers Aggregate produced — one export may not
// read as two different sets of figures depending on which view you open.
func TestRenderWatchPathCarriesTheReport(t *testing.T) {
	rows := []classify.Verdict{
		view(0, "music/live", 300),
		view(5*time.Minute, "music/live", 300),
		view(70*time.Minute, "dev/talks", 600),
		// No timestamp: no place on a timeline, but it is a view and the
		// report counts it. That difference is the point of the first check.
		{VideoID: "undated", Title: "undated", Channel: "chan", Topic: "music/live"},
	}
	subs := []takeout.Subscription{{ChannelID: "UCnever", Title: "Never Watched"}}
	st := Aggregate(rows, subs)

	html, err := RenderWatchPath(BuildPath(rows), st, pathT0)
	if err != nil {
		t.Fatal(err)
	}
	var d pathData
	if err := json.Unmarshal(mustPayload(t, html), &d); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	r := d.Report
	if r == nil {
		t.Fatal("the payload carries no report block")
	}

	if r.Views != st.Views || r.Views != 4 {
		t.Errorf("report views = %d, want %d from the stats", r.Views, st.Views)
	}
	if d.Views != 3 {
		t.Errorf("timeline views = %d, want 3 — the undated view has no place there", d.Views)
	}
	if r.Unique != st.UniqueVideos || r.DurS != durS(st.HoursUpper) {
		t.Errorf("unique = %d/%d, durS = %d/%d", r.Unique, st.UniqueVideos, r.DurS, durS(st.HoursUpper))
	}

	// Topics: area, mode as an index into the shared table, views, seconds,
	// and the subjects underneath.
	if len(r.Topics) != len(st.Topics) {
		t.Fatalf("topics = %d, want %d", len(r.Topics), len(st.Topics))
	}
	top, want := r.Topics[0], st.Topics[0]
	if top[0] != want.Topic || num(t, top[2]) != want.Views || num(t, top[3]) != durS(want.Hours) {
		t.Errorf("top topic = %v, want %q with %d views", top, want.Topic, want.Views)
	}
	if got := d.Modes[num(t, top[1])]; got != want.Mode {
		t.Errorf("top topic mode = %q, want %q", got, want.Mode)
	}
	kids, ok := top[4].([]any)
	if !ok || len(kids) != len(want.Subs) {
		t.Fatalf("subjects under %q = %#v, want %d", want.Topic, top[4], len(want.Subs))
	}
	kid := kids[0].([]any)
	if kid[0] != want.Subs[0].Sub || num(t, kid[2]) != want.Subs[0].Views {
		t.Errorf("first subject = %v, want %q with %d views", kid, want.Subs[0].Sub, want.Subs[0].Views)
	}

	// Months: one bucket, and the per-mode counts are parallel to ModeOrder,
	// which is what lets the page name a column through the modes table.
	if len(r.Months) != 1 || r.Months[0][0] != st.Months[0].Month {
		t.Fatalf("months = %v, want one bucket %q", r.Months, st.Months[0].Month)
	}
	counts, ok := r.Months[0][1].([]any)
	if !ok || len(counts) != len(ModeOrder) {
		t.Fatalf("month counts = %#v, want one per mode", r.Months[0][1])
	}
	total := 0
	for i, c := range counts {
		if n := num(t, c); n > 0 {
			total += n
			if d.Modes[i] != ModeOrder[i] {
				t.Errorf("month column %d is %q, but the modes table says %q", i, ModeOrder[i], d.Modes[i])
			}
		}
	}
	if total != d.Views {
		t.Errorf("the months hold %d views, the timeline %d", total, d.Views)
	}

	// Channel names ride in the ONE lookup table, watched or merely subscribed.
	if len(r.Chans) != len(st.Channels) {
		t.Fatalf("channels = %d, want %d", len(r.Chans), len(st.Channels))
	}
	if got := d.Chans[num(t, r.Chans[0][0])]; got != st.Channels[0].Name {
		t.Errorf("top channel = %q, want %q", got, st.Channels[0].Name)
	}
	if len(r.Subs) != 1 || !r.HasSubs || r.DeadSubs != 1 {
		t.Fatalf("subscriptions = %v, hasSubs = %v, dead = %d", r.Subs, r.HasSubs, r.DeadSubs)
	}
	if got := d.Chans[num(t, r.Subs[0][0])]; got != "Never Watched" {
		t.Errorf("subscription title = %q, want the interned name", got)
	}
	if last := num(t, r.Subs[0][4]); last != -1 {
		t.Errorf("a never-watched subscription must carry no date, got %d", last)
	}

	// And the view is reachable: a route and a card, not just a payload.
	page := string(html)
	for _, want := range []string{`parts[0] === "report"`, `card("#/report"`, "renderReport"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page never offers the report view: %q missing", want)
		}
	}
}

// Without stats the block stays off the payload entirely — the shell has to
// render, and the page must offer neither the card nor the route.
func TestRenderWatchPathWithoutStats(t *testing.T) {
	p := BuildPath(pathFixture())
	html, err := RenderWatchPath(p, nil, pathT0)
	if err != nil {
		t.Fatal(err)
	}
	payload := mustPayload(t, html)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if _, ok := raw["report"]; ok {
		t.Error("no stats were handed in, but the payload carries a report key")
	}
	if _, ok := raw["stats"]; !ok {
		t.Error("the timeline's own stats went missing")
	}
	if !strings.Contains(string(html), "watch path") || len(html) < 10000 {
		t.Errorf("the page did not render without stats (%d bytes)", len(html))
	}
}

func TestPathDataInternsRepeats(t *testing.T) {
	// The same channel across many views must cost one string, not many —
	// that is the whole reason for the lookup tables.
	rows := make([]classify.Verdict, 0, 6)
	for i := 0; i < 6; i++ {
		v := view(time.Duration(i)*time.Minute, "music", 300)
		v.Channel = "One Channel"
		rows = append(rows, v)
	}
	d := buildPathData(BuildPath(rows), nil)
	if len(d.Chans) != 1 || d.Chans[0] != "One Channel" {
		t.Errorf("channels = %v, want one entry", d.Chans)
	}
	if len(d.Areas) != 1 || len(d.AreaHues) != 1 {
		t.Errorf("areas = %v, hues = %v", d.Areas, d.AreaHues)
	}
	// A stable hue per name is what keeps colours from moving between runs.
	if areaHue("music") != areaHue("music") || areaHue("music") == areaHue("sports") {
		t.Error("area hues must be stable per name and differ between names")
	}
}

func TestDayOwnsTheSittingThatStartedOnIt(t *testing.T) {
	// 23:30 → 00:10. The sitting is the unit, so it stays whole on the day it
	// began instead of being cut in two at midnight.
	p := BuildPath([]classify.Verdict{
		view(11*time.Hour+30*time.Minute, "music", 300),
		view(11*time.Hour+50*time.Minute, "music", 300),
		view(12*time.Hour+10*time.Minute, "music", 600),
	})
	if len(p.Sessions) != 1 {
		t.Fatalf("20 minutes apart is one sitting, got %d", len(p.Sessions))
	}
	if len(p.Days) != 1 {
		t.Fatalf("days = %d, want 1 — the night does not open a new one", len(p.Days))
	}
	d := p.Days[0]
	if d.Date != "2026-07-01" {
		t.Errorf("date = %q, want the day the sitting started", d.Date)
	}
	if d.Views != 3 {
		t.Errorf("views = %d, want all 3 on the start day", d.Views)
	}
	if want := 1200.0 / 3600; !hoursEqual(d.Hours, want) {
		t.Errorf("hours = %v, want %v", d.Hours, want)
	}
	if d.EpochDay != 20635 { // 2026-07-01, cross-checked against date(1)
		t.Errorf("epoch day = %d, want 20635", d.EpochDay)
	}
}

func TestDaysAreCalendarOrderedAndMeasureTheirGaps(t *testing.T) {
	// The heatmap draws empty cells from the distance between EpochDays, so
	// that distance has to be the calendar one and not the index one.
	p := BuildPath([]classify.Verdict{
		view(0, "music", 300),
		view(24*time.Hour, "sports", 300),
		view(5*24*time.Hour, "gaming", 300),
	})
	if len(p.Days) != 3 {
		t.Fatalf("days = %d, want 3", len(p.Days))
	}
	for i, want := range []string{"2026-07-01", "2026-07-02", "2026-07-06"} {
		if p.Days[i].Date != want {
			t.Errorf("day %d = %q, want %q (oldest first)", i, p.Days[i].Date, want)
		}
	}
	if got := p.Days[1].EpochDay - p.Days[0].EpochDay; got != 1 {
		t.Errorf("consecutive days are %d apart, want 1", got)
	}
	if got := p.Days[2].EpochDay - p.Days[1].EpochDay; got != 4 {
		t.Errorf("a four-day hole measured %d, want 4", got)
	}
}

func TestDaySessionRangePointsAtItsSittings(t *testing.T) {
	// Sessions run newest first, days oldest first: SessFrom is the day's
	// NEWEST sitting and SessTo its oldest, and the ranges together have to
	// account for every sitting exactly once.
	p := BuildPath(pathFixture())
	if len(p.Sessions) != 3 || len(p.Days) != 2 {
		t.Fatalf("fixture drifted: %d sittings, %d days", len(p.Sessions), len(p.Days))
	}
	if p.Days[0].SessFrom != 1 || p.Days[0].SessTo != 2 {
		t.Errorf("2026-07-01 owns sittings %d…%d, want 1…2", p.Days[0].SessFrom, p.Days[0].SessTo)
	}
	if p.Days[1].SessFrom != 0 || p.Days[1].SessTo != 0 {
		t.Errorf("2026-07-03 owns sittings %d…%d, want 0…0", p.Days[1].SessFrom, p.Days[1].SessTo)
	}
	seen := 0
	for _, d := range p.Days {
		views := 0
		for si := d.SessFrom; si <= d.SessTo; si++ {
			if got := p.Sessions[si].Start.Format("2006-01-02"); got != d.Date {
				t.Errorf("sitting %d starts on %s but sits in day %s", si, got, d.Date)
			}
			views += len(p.Sessions[si].Views)
			seen++
		}
		if views != d.Views {
			t.Errorf("day %s counts %d views, its sittings hold %d", d.Date, d.Views, views)
		}
	}
	if seen != len(p.Sessions) {
		t.Errorf("the day ranges cover %d sittings, want %d", seen, len(p.Sessions))
	}
}

func TestDayAreaBreaksTiesByName(t *testing.T) {
	// Two views each. dominant() settles it by name, so a run of the tool
	// twice on the same export cannot produce two different days.
	p := BuildPath([]classify.Verdict{
		view(0, "music", 300),
		view(5*time.Minute, "gaming", 300),
		view(10*time.Minute, "music", 300),
		view(15*time.Minute, "gaming", 300),
	})
	if p.Days[0].Area != "gaming" {
		t.Errorf("area = %q, want the alphabetically first of the tied pair", p.Days[0].Area)
	}
}

func TestDayAreaIgnoresOverlapViews(t *testing.T) {
	// Four music entries inside one 45-minute documentary: the background is
	// in the majority and must still not decide what the day was about.
	p := BuildPath([]classify.Verdict{
		view(0, "film-animation", 45*60),
		view(2*time.Minute, "music", 200),
		view(4*time.Minute, "music", 200),
		view(6*time.Minute, "music", 200),
		view(8*time.Minute, "music", 200),
	})
	overlaps := 0
	for _, v := range flat(p) {
		if v.Overlap {
			overlaps++
		}
	}
	if overlaps != 4 {
		t.Fatalf("fixture drifted: %d overlap views, want 4 against 1 on the main lane", overlaps)
	}
	if p.Days[0].Views != 5 {
		t.Errorf("views = %d, want 5 — the day still counts the background", p.Days[0].Views)
	}
	if p.Days[0].Area != "film-animation" {
		t.Errorf("area = %q, want the main lane to win", p.Days[0].Area)
	}
}

func TestTransitionsStayInsideOneSitting(t *testing.T) {
	// A jump over a night is not a statement about what followed what.
	p := BuildPath([]classify.Verdict{
		view(0, "music", 300),
		view(10*time.Minute, "sports", 300),
		view(100*time.Minute, "gaming", 300),
	})
	if len(p.Sessions) != 2 {
		t.Fatalf("fixture drifted: %d sittings, want 2", len(p.Sessions))
	}
	want := []Transition{{From: "music", To: "sports", N: 1}}
	if !reflect.DeepEqual(p.Trans, want) {
		t.Errorf("transitions = %v, want %v", p.Trans, want)
	}
}

func TestTransitionsSkipOverlapAndKeepSelfLoops(t *testing.T) {
	// Music inside the documentary is stepped over, not counted and not
	// treated as a break — the same rule the rabbit holes use. What is left
	// is a chain staying on one topic, and that self-loop is worth counting.
	p := BuildPath([]classify.Verdict{
		view(0, "film-animation", 45*60),
		view(2*time.Minute, "music", 200),
		view(10*time.Minute, "film-animation", 45*60),
	})
	if vs := flat(p); !vs[1].Overlap {
		t.Fatalf("fixture drifted: the music view is not an overlap")
	}
	want := []Transition{{From: "film-animation", To: "film-animation", N: 1}}
	if !reflect.DeepEqual(p.Trans, want) {
		t.Errorf("transitions = %v, want %v", p.Trans, want)
	}
}

func TestTransitionOrderIsDeterministic(t *testing.T) {
	// Counts first, then the names — otherwise the list would follow Go's map
	// order and change between runs on the same data.
	seq := []string{"music", "sports", "music", "sports", "gaming", "music", "gaming", "music"}
	rows := make([]classify.Verdict, 0, len(seq))
	for i, a := range seq {
		rows = append(rows, view(time.Duration(i)*5*time.Minute, a, 300))
	}
	p := BuildPath(rows)
	if len(p.Sessions) != 1 {
		t.Fatalf("fixture drifted: %d sittings, want 1", len(p.Sessions))
	}
	want := []Transition{
		{From: "gaming", To: "music", N: 2},
		{From: "music", To: "sports", N: 2},
		{From: "music", To: "gaming", N: 1},
		{From: "sports", To: "gaming", N: 1},
		{From: "sports", To: "music", N: 1},
	}
	if !reflect.DeepEqual(p.Trans, want) {
		t.Errorf("transitions = %v,\nwant %v", p.Trans, want)
	}
}

func TestPathStatsOnAKnownFixture(t *testing.T) {
	p := BuildPath(pathFixture())
	st := p.Stats
	want := PathStats{
		Views: 10, Sessions: 3, Dropped: 1,
		OverlapViews: 1, RabbitViews: 4,
		LongestSession: 1, LongestSessionViews: 5, LongestSessionSpan: 24 * time.Minute,
		DeepestRabbit: 1, DeepestRabbitLen: 4,
		BusiestDay: 0, BusiestDayViews: 7,
	}
	want.HoursUpper = st.HoursUpper // compared separately, it is a float sum
	if st != want {
		t.Errorf("stats = %+v,\nwant %+v", st, want)
	}
	if h := 7700.0 / 3600; !hoursEqual(st.HoursUpper, h) {
		t.Errorf("hours = %v, want %v", st.HoursUpper, h)
	}
	// The indices have to land on the sittings and days they name.
	if got := len(p.Sessions[st.LongestSession].Views); got != st.LongestSessionViews {
		t.Errorf("longest sitting holds %d views, stats say %d", got, st.LongestSessionViews)
	}
	if got := p.Days[st.BusiestDay].Views; got != st.BusiestDayViews {
		t.Errorf("busiest day holds %d views, stats say %d", got, st.BusiestDayViews)
	}
	if p.Days[st.BusiestDay].Date != "2026-07-01" {
		t.Errorf("busiest day = %q, want 2026-07-01", p.Days[st.BusiestDay].Date)
	}
	rabbits := 0
	for _, v := range p.Sessions[st.DeepestRabbit].Views {
		if v.Rabbit {
			rabbits++
		}
	}
	if rabbits != st.DeepestRabbitLen {
		t.Errorf("deepest sitting holds %d chain views, stats say %d", rabbits, st.DeepestRabbitLen)
	}
}

func TestEmptyPathHasNoIndices(t *testing.T) {
	// The page reads the headline numbers unconditionally, so an empty export
	// has to answer "there is none" rather than point at row 0.
	for name, p := range map[string]*Path{
		"no rows at all": BuildPath(nil),
		"only undated":   BuildPath([]classify.Verdict{{VideoID: "x", Title: "x", Topic: "music"}}),
	} {
		if len(p.Days) != 0 || len(p.Trans) != 0 {
			t.Errorf("%s: days = %v, trans = %v", name, p.Days, p.Trans)
		}
		st := p.Stats
		if st.LongestSession != -1 || st.DeepestRabbit != -1 || st.BusiestDay != -1 {
			t.Errorf("%s: indices = %d/%d/%d, want -1", name,
				st.LongestSession, st.DeepestRabbit, st.BusiestDay)
		}
		if st.Views != 0 || st.Sessions != 0 || st.HoursUpper != 0 || st.BusiestDayViews != 0 {
			t.Errorf("%s: stats = %+v", name, st)
		}
		d := buildPathData(p, nil)
		if d.Stats == nil || d.Stats.BusiestDay != -1 {
			t.Errorf("%s: payload stats = %+v", name, d.Stats)
		}
		if len(d.Sess) != 0 || len(d.Days) != 0 || len(d.Trans) != 0 || len(d.AreaViews) != 0 {
			t.Errorf("%s: payload is not empty", name)
		}
	}
	if got := BuildPath([]classify.Verdict{{VideoID: "x", Title: "x", Topic: "music"}}).Stats.Dropped; got != 1 {
		t.Errorf("an undated row still counts as dropped, got %d", got)
	}
}

func TestPathDataSessionsPointAtTheirRows(t *testing.T) {
	// The whole page rests on this: sess[i] names a row index, and the nViews
	// rows behind it are that sitting's views, in the same order. Nothing is
	// copied, so if this drifts every other view shows the wrong videos.
	p := BuildPath(pathFixture())
	d := buildPathData(p, nil)
	if len(d.Sess) != len(p.Sessions) {
		t.Fatalf("sess = %d entries, sessions = %d", len(d.Sess), len(p.Sessions))
	}
	for i, s := range d.Sess {
		rowIdx, nViews := s[0].(int), s[3].(int)
		if d.Rows[rowIdx][0] != rowSession {
			t.Fatalf("sess[%d] rowIdx %d is not a session row", i, rowIdx)
		}
		if nViews != len(p.Sessions[i].Views) {
			t.Fatalf("sess[%d] claims %d views, the sitting has %d", i, nViews, len(p.Sessions[i].Views))
		}
		if got, want := s[1].(int64), d.Rows[rowIdx][1].(int64); got != want {
			t.Errorf("sess[%d] starts at %d, its row says %d", i, got, want)
		}
		for k := 0; k < nViews; k++ {
			row := d.Rows[rowIdx+1+k]
			if row[0] != rowView {
				t.Fatalf("sess[%d]: row %d is not a view row", i, rowIdx+1+k)
			}
			if got, want := row[1].(int64), p.Sessions[i].Views[k].WatchedAt.Unix()-d.T0; got != want {
				t.Errorf("sess[%d] view %d: ts = %d, want %d", i, k, got, want)
			}
		}
		// And the block ends there: the next row starts the next sitting.
		if end := rowIdx + 1 + nViews; end < len(d.Rows) && d.Rows[end][0] != rowSession {
			t.Errorf("sess[%d]: row %d should open the next sitting", i, end)
		}
	}
}

func TestPathDataAggregatesIndexTheLookupTables(t *testing.T) {
	p := BuildPath(pathFixture())
	d := buildPathData(p, nil)

	if len(d.Days) != len(p.Days) {
		t.Fatalf("days = %d entries, want %d", len(d.Days), len(p.Days))
	}
	for i, day := range d.Days {
		if day[0] != p.Days[i].EpochDay || day[1] != p.Days[i].Views {
			t.Errorf("days[%d] = %v, want epochDay %d and %d views", i, day, p.Days[i].EpochDay, p.Days[i].Views)
		}
		if got := d.Areas[day[2].(int)]; got != p.Days[i].Area {
			t.Errorf("days[%d] area = %q, want %q", i, got, p.Days[i].Area)
		}
		from, to := day[3].(int), day[4].(int)
		for si := from; si <= to; si++ {
			if got := d.Sess[si][5].(int); got != i {
				t.Errorf("sess[%d] points at day %d, but day %d claims it", si, got, i)
			}
		}
	}

	if len(d.Trans) != len(p.Trans) {
		t.Fatalf("trans = %d entries, want %d", len(d.Trans), len(p.Trans))
	}
	for i, tr := range d.Trans {
		if d.Areas[tr[0]] != p.Trans[i].From || d.Areas[tr[1]] != p.Trans[i].To || tr[2] != p.Trans[i].N {
			t.Errorf("trans[%d] = %v, want %+v", i, tr, p.Trans[i])
		}
	}

	// areaViews stays parallel to areas and counts the main lane only, so the
	// legend can size an area without walking 35k rows.
	if len(d.AreaViews) != len(d.Areas) {
		t.Fatalf("areaViews = %d, areas = %d", len(d.AreaViews), len(d.Areas))
	}
	total := 0
	for _, n := range d.AreaViews {
		total += n
	}
	if want := p.Stats.Views - p.Stats.OverlapViews; total != want {
		t.Errorf("areaViews sum to %d, want %d main-lane views", total, want)
	}

	// The key names are the contract the page is written against.
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"sess", "days", "trans", "areaViews", "stats"} {
		if _, ok := obj[k]; !ok {
			t.Errorf("payload misses %q", k)
		}
	}
	var stats map[string]json.RawMessage
	if err := json.Unmarshal(obj["stats"], &stats); err != nil {
		t.Fatal(err)
	}
	// Exactly the numbers a view puts on screen — the exact-count check is the
	// point of this list: it fails when a field is serialised for nobody, which
	// is how "dropped" (already at the payload's top level) and "rabbitViews"
	// (printed by the CLI, never by the page) rode along unread until 2026-08-25.
	keys := []string{"views", "sessions", "overlapViews",
		"hoursUpper", "longestSess", "longestSessN", "longestSessS",
		"deepestRabbit", "deepestRabbitN", "busiestDay", "busiestDayN"}
	for _, k := range keys {
		if _, ok := stats[k]; !ok {
			t.Errorf("stats misses %q", k)
		}
	}
	if len(stats) != len(keys) {
		t.Errorf("stats has %d keys, want exactly %d", len(stats), len(keys))
	}
	// The span travels as seconds — JSON has no duration.
	var spanS int
	if err := json.Unmarshal(stats["longestSessS"], &spanS); err != nil {
		t.Fatal(err)
	}
	if want := int(p.Stats.LongestSessionSpan.Seconds()); spanS != want {
		t.Errorf("longestSessS = %d, want %d", spanS, want)
	}
}

// clusterByName finds a node by name among siblings, so a test can say what
// it is looking for instead of counting positions.
func clusterByName(cs []Cluster, name string) *Cluster {
	for i := range cs {
		if cs[i].Name == name {
			return &cs[i]
		}
	}
	return nil
}

// sumViews is what every parent in the tree has to equal.
func sumViews(cs []Cluster) int {
	n := 0
	for _, c := range cs {
		n += c.Views
	}
	return n
}

func TestClusterCountsRollUp(t *testing.T) {
	// Area totals are the sum of their subjects, subjects the sum of their
	// channels. In a drawing where the area of a circle IS the count, a parent
	// that disagrees with its children is a visible lie.
	p := BuildPath(pathFixture())
	if got, want := sumViews(p.Clusters), p.Views; got != want {
		t.Errorf("areas sum to %d views, the path has %d", got, want)
	}
	var walk func(c Cluster, depth int)
	walk = func(c Cluster, depth int) {
		if len(c.Children) == 0 {
			if depth != 2 {
				t.Errorf("%q is a leaf at depth %d, want the channel level (2)", c.Name, depth)
			}
			return
		}
		if got := sumViews(c.Children); got != c.Views {
			t.Errorf("%q has %d views, its children sum to %d", c.Name, c.Views, got)
		}
		durs := 0
		for _, k := range c.Children {
			durs += k.DurationS
		}
		if durs != c.DurationS {
			t.Errorf("%q has %ds, its children sum to %ds", c.Name, c.DurationS, durs)
		}
		for _, k := range c.Children {
			walk(k, depth+1)
		}
	}
	for _, c := range p.Clusters {
		walk(c, 0)
	}
}

func TestClusterTreeIsAlwaysThreeLevels(t *testing.T) {
	// A view classified to the bare area still gets a subject node. Ragged
	// depth would draw a channel and a subject at the same size and call them
	// the same thing.
	rows := []classify.Verdict{
		view(0, "gaming", 300),              // no sub
		view(5*time.Minute, "gaming", 300),  // no sub
		view(10*time.Minute, "gaming", 300), // gets a sub below
	}
	rows[2].Topic = "gaming/factorio"
	rows[2].Channel = "" // and no channel either
	p := BuildPath(rows)

	area := clusterByName(p.Clusters, "gaming")
	if area == nil {
		t.Fatalf("no gaming area in %v", p.Clusters)
	}
	if area.Views != 3 {
		t.Errorf("gaming = %d views, want 3", area.Views)
	}
	bare := clusterByName(area.Children, NoSubject)
	if bare == nil || bare.Views != 2 {
		t.Fatalf("the two subject-less views need a %q node, got %v", NoSubject, area.Children)
	}
	sub := clusterByName(area.Children, "factorio")
	if sub == nil || sub.Views != 1 {
		t.Fatalf("factorio = %v, want 1 view", sub)
	}
	if ch := clusterByName(sub.Children, NoChannel); ch == nil || ch.Views != 1 {
		t.Errorf("a view with no channel needs a %q node, got %v", NoChannel, sub.Children)
	}
	if ch := clusterByName(bare.Children, "chan"); ch == nil || ch.Views != 2 {
		t.Errorf("the named channel should carry both views, got %v", bare.Children)
	}
}

func TestClusterOrderIsDeterministic(t *testing.T) {
	// Most-watched first, ties by name — the packing draws them in this order,
	// so two runs over the same data have to hand it the same list.
	rows := []classify.Verdict{
		view(0, "sports", 60),
		view(5*time.Minute, "sports", 60),
		view(10*time.Minute, "music", 60),
		view(15*time.Minute, "music", 60),
		view(20*time.Minute, "gaming", 60), // ties music and sports? no: 1 view
	}
	p := BuildPath(rows)
	var got []string
	for _, c := range p.Clusters {
		got = append(got, c.Name)
	}
	// music and sports both have 2; the name breaks the tie, gaming trails.
	want := []string{"music", "sports", "gaming"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("areas = %v, want %v", got, want)
	}
}

func TestClusterCountsOverlapViewsToo(t *testing.T) {
	// The calendar asks what a day was ABOUT and lets background music abstain.
	// This asks what was watched, and a track that ran under a documentary was
	// watched — so the two aggregates deliberately disagree here.
	p := BuildPath([]classify.Verdict{
		view(0, "film-animation", 45*60),
		view(5*time.Minute, "music", 200),
		view(9*time.Minute, "music", 200),
	})
	overlaps := 0
	for _, v := range flat(p) {
		if v.Overlap {
			overlaps++
		}
	}
	if overlaps != 2 {
		t.Fatalf("fixture needs its two overlap views, got %d", overlaps)
	}
	if p.Days[0].Area != "film-animation" {
		t.Errorf("the day is about the documentary, got %q", p.Days[0].Area)
	}
	music := clusterByName(p.Clusters, "music")
	if music == nil || music.Views != 2 {
		t.Errorf("the tree counts the background too: music = %v, want 2 views", music)
	}
	if sumViews(p.Clusters) != 3 {
		t.Errorf("the tree holds every view, got %d of 3", sumViews(p.Clusters))
	}
}

func TestClusterTreeEmpty(t *testing.T) {
	if cs := BuildPath(nil).Clusters; len(cs) != 0 {
		t.Errorf("empty path, got %v", cs)
	}
}

func TestPathDataSerialisesTheClusterTree(t *testing.T) {
	// Leaves carry three fields, everything above carries a fourth that holds
	// the children — that shape is what the packing walks.
	d := buildPathData(BuildPath(pathFixture()), nil)
	if len(d.Clusters) != len(BuildPath(pathFixture()).Clusters) {
		t.Fatalf("areas = %d serialised, want %d", len(d.Clusters), len(BuildPath(pathFixture()).Clusters))
	}
	var walk func(n []any, depth int)
	walk = func(n []any, depth int) {
		if _, ok := n[0].(string); !ok {
			t.Fatalf("node at depth %d starts with %T, want the name", depth, n[0])
		}
		if depth == 2 {
			if len(n) != 3 {
				t.Errorf("channel node has %d fields, want 3 (no children)", len(n))
			}
			return
		}
		if len(n) != 4 {
			t.Fatalf("node at depth %d has %d fields, want 4", depth, len(n))
		}
		kids, ok := n[3].([][]any)
		if !ok || len(kids) == 0 {
			t.Fatalf("node at depth %d carries no children", depth)
		}
		for _, k := range kids {
			walk(k, depth+1)
		}
	}
	for _, n := range d.Clusters {
		walk(n, 0)
	}

	// And it has to survive the JSON round trip the page actually reads.
	raw, err := json.Marshal(d.Clusters)
	if err != nil {
		t.Fatal(err)
	}
	var back []any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("cluster payload is not valid JSON: %v", err)
	}
	if len(back) != len(d.Clusters) {
		t.Errorf("round trip gave %d areas, want %d", len(back), len(d.Clusters))
	}
}
