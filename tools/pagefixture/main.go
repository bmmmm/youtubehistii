// SPDX-License-Identifier: GPL-3.0-or-later

// pagefixture writes a watchpath page built from INVENTED views to stdout, so
// pagecheck has something to run in CI.
//
// The real page is somebody's watch history: gitignored, never in a runner.
// Without this, pagecheck could only ever run by hand — and a check that runs
// by hand is a check that runs when someone remembers.
//
// The fixture is not a smaller corpus, it is a corpus shaped to keep every
// check sharp: enough months for the small multiples, enough sittings for the
// neighbour links, chains long enough to be rabbit holes, several areas and
// channels so the filters and the "new channel" arithmetic have something to
// be wrong about. Each generated number is derived, never random, so two runs
// produce the same bytes and a diff in CI means the PAGE changed.
package main

import (
	"fmt"
	"os"
	"time"
	// Embedded, so LoadLocation below works on a runner image that ships no
	// zone files at all. ~450 KB in a tool that only CI runs.
	_ "time/tzdata"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/report"
)

// fixtureZone pins the time zone the fixture renders in. The page buckets by
// LOCAL day, so without this the same views produce a different day count per
// machine — measured: 566 days here, 512 on the UTC runner, same input. A
// zone WITH daylight saving is the deliberate choice over UTC: it keeps the
// two shifted days a UTC runner would never produce, and those are exactly
// the days the day axis is known to wobble on.
const fixtureZone = "Europe/Berlin"

// t0 is the fixture's first view. Fixed, in UTC, on a Monday: the page buckets
// by local day and by month, and a moving start would move those buckets.
var t0 = time.Date(2023, 1, 2, 8, 0, 0, 0, time.UTC)

// topics are picked per sitting, so a sitting mostly stays in one area — that
// is what makes a chain a chain. Subs vary within an area so the area filter
// and the taxonomy level both have more than one value to show.
var topics = []string{
	"music/electronic", "music/live-sets", "music/gear",
	"gaming/strategy", "gaming/roguelike", "gaming/speedrun",
	"science-technology/space", "science-technology/materials",
	"education/history", "education/languages",
	"news-politics/analysis", "comedy/sketch", "sports/climbing",
	"film-animation/shorts", "howto-style/repair", "unclear",
}

var modes = []string{"consume", "learn", "mixed"}

// sitting describes one visit: when it starts, how many views, and how far
// apart they sit. A gap under 15 minutes keeps a run of one area together as
// a chain (rabbitMaxGap); four such views in a row make it a rabbit hole
// (rabbitMinLen). The mix below deliberately produces some of each.
type sitting struct {
	start time.Time
	views int
	gap   time.Duration
	topic int // index into topics; the sitting's home area
}

// plan lays out sittings from arithmetic alone. The shape is what matters: a
// long tail of short visits, a handful of deep ones, a few days carrying
// several sittings, and clusters that leave whole weeks empty so the calendar
// has both extremes to colour.
//
// The SIZE, though, is a measurement and not a taste: both ranking lists are
// virtualised, and the check that they promise every row once caught a cap at
// 300. A fixture under that cap passes whether the cap is there or not —
// verified by putting the cap back and watching pagecheck stay green. So the
// count runs until chains and days are both past 300, and the numbers printed
// on stderr are what says whether that is still true.
func plan() []sitting {
	var out []sitting
	day := t0
	for i := 0; i < 620; i++ {
		// Advance by 1-4 days, cycling: some days get two sittings, some
		// weeks none. Purely derived from i, so the calendar is stable.
		step := time.Duration(i%4) * 24 * time.Hour
		if i%7 == 0 {
			step += 5 * 24 * time.Hour // a quiet stretch, so the lens has a floor
		}
		day = day.Add(step)
		hour := time.Duration(9+i%11) * time.Hour
		start := day.Truncate(24 * time.Hour).Add(hour)

		views := 2 + i%9 // 2..10
		if i%13 == 0 {
			views = 14 + i%8 // the deep ones: 14..21 videos in one go
		}
		gap := time.Duration(4+i%9) * time.Minute // 4..12 min: inside the chain gap
		if i%5 == 0 {
			gap = 20 * time.Minute // wide enough to break the chain, not the sitting
		}
		out = append(out, sitting{start: start, views: views, gap: gap, topic: i % len(topics)})

		// Every third day carries a second, later sitting — a day with two
		// sittings is what the day view's neighbour and tile logic reads.
		if i%3 == 0 {
			out = append(out, sitting{
				start: start.Add(6 * time.Hour),
				views: 3 + i%4,
				gap:   6 * time.Minute,
				topic: (i * 3) % len(topics),
			})
		}
		// Every eleventh sitting runs past midnight, so the night window and
		// the hour histogram have something in them.
		if i%11 == 0 {
			out = append(out, sitting{
				start: start.Truncate(24 * time.Hour).Add(23*time.Hour + 20*time.Minute),
				views: 5,
				gap:   8 * time.Minute,
				topic: (i * 5) % len(topics),
			})
		}
	}
	return out
}

// build turns the plan into views. Titles and channels are numbered, never
// invented prose: the fixture must not read like a real history, and a number
// makes it obvious in a diff which view moved.
func build() []classify.Verdict {
	var rows []classify.Verdict
	n := 0
	for si, s := range plan() {
		at := s.start
		for v := 0; v < s.views; v++ {
			topic := topics[s.topic]
			// Every seventh view wanders into a neighbouring area, so a chain
			// ends for a reason the page has to show rather than at a seam.
			// Seventh and not fourth: a run has to reach rabbitMinLen (4) to
			// BE a chain, and breaking every fourth view left the fixture
			// with 45 of them.
			if v%7 == 6 {
				topic = topics[(s.topic+1+v)%len(topics)]
			}
			n++
			rows = append(rows, classify.Verdict{
				VideoID:    fmt.Sprintf("vid%08d", n),
				Title:      fmt.Sprintf("Fixture video %d", n),
				Channel:    fmt.Sprintf("Channel %02d", (si*3+v)%37),
				ChannelID:  fmt.Sprintf("chan%04d", (si*3+v)%37),
				WatchedAt:  at,
				Topic:      topic,
				Mode:       modes[(si+v)%len(modes)],
				Source:     "llm:fixture",
				Confidence: 0.5 + float64((si+v)%5)/10,
				DurationS:  180 + (si*7+v*13)%2400,
			})
			at = at.Add(s.gap)
		}
	}
	// One tombstone and one undated view: both are states the real corpus has
	// and the page has to survive — the undated one is counted as dropped and
	// must never reach the timeline.
	rows = append(rows, classify.Verdict{
		VideoID: "vidtombstone", Title: "https://www.youtube.com/watch?v=vidtombstone",
		WatchedAt: t0.Add(48 * time.Hour), Topic: "unclear", Source: "llm:fixture",
		Unavailable: true, GoneReason: "removed",
	})
	rows = append(rows, classify.Verdict{
		VideoID: "vidundated", Title: "Fixture video undated", Topic: "music/electronic",
		Source: "llm:fixture",
	})
	return rows
}

func main() {
	loc, err := time.LoadLocation(fixtureZone)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pagefixture:", err)
		os.Exit(1)
	}
	time.Local = loc

	rows := build()
	p := report.BuildPath(rows)

	// Label a few chains, so the label checks run against real labels instead
	// of taking their "no labels is a valid state" branch. Deepest first is
	// what -label-holes does, and the labels are numbered for the same reason
	// the titles are.
	labels := map[int]string{}
	for i := 0; i < len(p.Chains) && i < 12; i++ {
		labels[i] = fmt.Sprintf("fixture hole %d", i)
	}

	html, err := report.RenderWatchPathOpts(p, report.Aggregate(rows, nil), t0, report.WatchPathOpts{HoleLabels: labels})
	if err != nil {
		fmt.Fprintln(os.Stderr, "pagefixture:", err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(html); err != nil {
		fmt.Fprintln(os.Stderr, "pagefixture:", err)
		os.Exit(1)
	}
	// On stderr, so it lands in the CI log next to pagecheck's count without
	// touching the page. The two numbers that decide whether the fixture can
	// still fail are the list lengths: the virtual list once capped its rows
	// at 300, and a fixture below that cap cannot catch the cap coming back.
	fmt.Fprintf(os.Stderr, "pagefixture: %d views, %d sittings, %d chains, %d days, %d labels\n",
		p.Views, len(p.Sessions), len(p.Chains), len(p.Days), len(labels))
}
