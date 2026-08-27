// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/report"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

// cmdWatchPath renders the one page this tool produces: the classified views
// along the time axis, with the aggregate report as a view of the same page at
// #/report. "report" itself writes the CSV and the terminal summary — the two
// used to be separate files, and whoever opened the report never learned that
// the timeline, the calendar and the topic tree existed.
func cmdWatchPath(args []string) error {
	fs, dataDir := newFlagSet("watchpath")
	useTaxonomy := fs.Bool("taxonomy", false, "project topics through "+taxonomyPath+" before building the path")
	fs.Parse(args)
	p := paths{dataDir: *dataDir}

	rows, err := readJSONL[classify.Verdict](p.classifiedJSONL())
	if err != nil {
		return fmt.Errorf("read classified views (run \"classify\" first): %w", err)
	}
	if *useTaxonomy {
		if err := foldThroughTaxonomy(rows); err != nil {
			return err
		}
	}
	// The subscription export is optional; without it the report view simply
	// has no subscription panel.
	subs, err := readJSONL[takeout.Subscription](p.subscriptionsJSONL())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	path := report.BuildPath(rows)
	st := report.Aggregate(rows, subs)

	if err := os.MkdirAll(p.outDir(), 0o755); err != nil {
		return err
	}
	htmlPath := filepath.Join(p.outDir(), "watchpath.html")
	html, err := report.RenderWatchPath(path, st, time.Now())
	if err != nil {
		return err
	}
	if err := os.WriteFile(htmlPath, html, 0o644); err != nil {
		return err
	}

	// Aggregates only — the page carries the titles, the terminal does not
	// need them. They come straight from the stats the page runs on, so the
	// two can never drift apart.
	ps := path.Stats
	fmt.Printf("%d views on the timeline in %d sessions", ps.Views, ps.Sessions)
	if ps.Dropped > 0 {
		fmt.Printf(", %d without a timestamp left off", ps.Dropped)
	}
	fmt.Println()
	fmt.Printf("%d views suspected of overlapping, %d inside a same-area chain\n",
		ps.OverlapViews, ps.RabbitViews)
	if len(path.Days) > 0 {
		d := path.Days[ps.BusiestDay]
		fmt.Printf("%d days carried a sitting (%s … %s), busiest %s with %d views\n",
			len(path.Days), path.Days[0].Date, path.Days[len(path.Days)-1].Date,
			d.Date, ps.BusiestDayViews)
	}
	fmt.Printf("wrote %s (%.1f MB)\n", htmlPath, float64(len(html))/(1<<20))
	return nil
}
