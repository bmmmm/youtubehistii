// SPDX-License-Identifier: GPL-3.0-or-later

package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

func sampleData() ([]classify.Verdict, []takeout.Subscription) {
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	rows := []classify.Verdict{
		{VideoID: "a", Title: "rust raid", Channel: "RustGuy", ChannelID: "UCrust", WatchedAt: t0,
			Topic: "gaming/rust", Mode: "consume", Source: "rule:rust-game", DurationS: 3600},
		{VideoID: "a", Title: "rust raid", Channel: "RustGuy", ChannelID: "UCrust", WatchedAt: t0.AddDate(0, 1, 0),
			Topic: "gaming/rust", Mode: "consume", Source: "rule:rust-game", DurationS: 3600},
		{VideoID: "b", Title: "talk", Channel: "media.ccc.de", ChannelID: "UCccc", WatchedAt: t0,
			Topic: "dev/talks", Mode: "learn", Source: "llm:m", Confidence: 0.9, DurationS: 1800},
		{VideoID: "c", Title: "???", Channel: "Mystery", WatchedAt: t0,
			Topic: "unclear", Source: "unclassified"},
	}
	subs := []takeout.Subscription{
		{ChannelID: "UCccc", Title: "media.ccc.de"},
		{ChannelID: "UCdead", Title: "Never Watched"},
	}
	return rows, subs
}

func TestAggregate(t *testing.T) {
	rows, subs := sampleData()
	st := Aggregate(rows, subs)

	if st.Views != 4 || st.UniqueVideos != 3 {
		t.Errorf("views=%d unique=%d", st.Views, st.UniqueVideos)
	}
	if st.HoursUpper != 2.5 {
		t.Errorf("hours=%v want 2.5", st.HoursUpper)
	}
	// Topics aggregate on the AREA; the sub is the level underneath it.
	if st.Topics[0].Topic != "gaming" || st.Topics[0].Views != 2 || st.Topics[0].Mode != "consume" {
		t.Errorf("top topic = %+v", st.Topics[0])
	}
	if len(st.Topics[0].Subs) != 1 || st.Topics[0].Subs[0].Sub != "rust" || st.Topics[0].Subs[0].Views != 2 {
		t.Errorf("top topic subs = %+v", st.Topics[0].Subs)
	}
	// "unclear" carries no sub, and a bare area must not invent one.
	for _, tp := range st.Topics {
		if tp.Topic == "unclear" && len(tp.Subs) != 0 {
			t.Errorf("unclear must have no subs, got %+v", tp.Subs)
		}
	}
	if st.Sources["rule"] != 2 || st.Sources["llm"] != 1 || st.Sources["unclassified"] != 1 {
		t.Errorf("sources = %v", st.Sources)
	}
	if len(st.Months) != 2 {
		t.Errorf("months = %d, want 2", len(st.Months))
	}

	// Subscription linkage: 1 of 4 views is on a subscribed channel, one
	// subscription was never watched, and the watched one carries its topic.
	if st.SubbedViews != 1 || st.DeadSubs != 1 {
		t.Errorf("subbedViews=%d deadSubs=%d", st.SubbedViews, st.DeadSubs)
	}
	if st.Subs[0].Title != "media.ccc.de" || st.Subs[0].TopTopic != "dev/talks" {
		t.Errorf("subs[0] = %+v", st.Subs[0])
	}
	if st.Subs[1].Views != 0 || st.Subs[1].TopTopic != "" {
		t.Errorf("dead sub = %+v", st.Subs[1])
	}
	if len(st.UnclearNames) != 1 || st.UnclearNames[0] != "Mystery" {
		t.Errorf("unclear = %v", st.UnclearNames)
	}
}

// A view watched just before midnight UTC belongs to the month the viewer
// was actually in — the same wall-clock basis BuildPath uses for its days.
// Two views of one page may not run on two time bases.
func TestMonthsFollowWallClock(t *testing.T) {
	local := time.Local
	time.Local = time.FixedZone("TEST+2", 2*3600)
	t.Cleanup(func() { time.Local = local })

	rows := []classify.Verdict{
		{VideoID: "a", Title: "late", Channel: "C", ChannelID: "UCc",
			WatchedAt: time.Date(2026, 7, 31, 23, 30, 0, 0, time.UTC),
			Topic:     "gaming/rust", Mode: "consume", Source: "rule:x", DurationS: 60},
	}
	st := Aggregate(rows, nil)

	if len(st.Months) != 1 || st.Months[0].Month != "2026-08" {
		t.Errorf("months = %+v, want one bucket 2026-08", st.Months)
	}
	if got := st.From.Format("2006-01-02 15:04"); got != "2026-08-01 01:30" {
		t.Errorf("from = %s, want 2026-08-01 01:30", got)
	}
}

// The report renders no HTML of its own any more — it is a view of the watch
// path page, and the guard on it lives in watchpath_test.go with the rest of
// that page.

func TestWriteCSV(t *testing.T) {
	rows, subs := sampleData()
	st := Aggregate(rows, subs)
	var buf bytes.Buffer
	if err := WriteCSV(&buf, rows, st.SubbedSet); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("csv lines = %d, want header+4", len(lines))
	}
	if !strings.Contains(lines[3], ",true,") {
		t.Errorf("subscribed view not flagged: %s", lines[3])
	}
	// The full topic and its area both ship, so a pivot can group on either.
	if !strings.Contains(lines[0], "topic,area,") {
		t.Errorf("header misses the area column: %s", lines[0])
	}
	if !strings.Contains(lines[3], "dev/talks,dev,") {
		t.Errorf("row misses topic+area: %s", lines[3])
	}
}

func TestRenderText(t *testing.T) {
	rows, subs := sampleData()
	txt := RenderText(Aggregate(rows, subs), true)
	// The area row plus its sub line — both levels are visible.
	for _, want := range []string{"gaming ", "rust 2", "subscriptions: 2 total, 1 never watched", "Mystery"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text misses %q in:\n%s", want, txt)
		}
	}
}

func TestRenderTextNoNamesOmitsEveryName(t *testing.T) {
	rows, subs := sampleData()
	txt := RenderText(Aggregate(rows, subs), false)
	// Aggregates stay, but no channel or subscription name may appear.
	if !strings.Contains(txt, "subscriptions: 2 total") {
		t.Errorf("aggregate line missing:\n%s", txt)
	}
	for _, name := range []string{"Mystery", "media.ccc.de", "RustGuy", "Never Watched"} {
		if strings.Contains(txt, name) {
			t.Errorf("no-names output leaks %q:\n%s", name, txt)
		}
	}
}
