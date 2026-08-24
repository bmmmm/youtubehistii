// SPDX-License-Identifier: GPL-3.0-or-later

package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
)

var pathT0 = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

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
	html, err := RenderWatchPath(p, pathT0)
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
	for _, bad := range []string{"http://", "https://", "<link", "src="} {
		if strings.Contains(page, bad) {
			t.Errorf("page reaches outside via %q", bad)
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
	html, err := RenderWatchPath(BuildPath(rows), pathT0)
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

func TestPathDataInternsRepeats(t *testing.T) {
	// The same channel across many views must cost one string, not many —
	// that is the whole reason for the lookup tables.
	rows := make([]classify.Verdict, 0, 6)
	for i := 0; i < 6; i++ {
		v := view(time.Duration(i)*time.Minute, "music", 300)
		v.Channel = "One Channel"
		rows = append(rows, v)
	}
	d := buildPathData(BuildPath(rows))
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
