// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/omlx"
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
	rulesPath := fs.String("rules", "", "rules file — only read for the chat model behind -label-holes")
	labelHolesN := fs.Int("label-holes", 0, "ask the chat model for a short name for the N deepest rabbit holes (0 = off; replies are cached, a rerun asks nothing)")
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

	// The labels are the one part of this command that can talk to a server,
	// and the one part whose absence changes nothing structural: without
	// them the page names its chains after their depth and area, which is
	// what it did before they existed.
	var opts report.WatchPathOpts
	if *labelHolesN > 0 {
		cfg, err := loadRules(*rulesPath)
		if err != nil {
			return err
		}
		opts.HoleLabels = labelHoles(omlx.New(cfg.LLM.Model, cfg.LLM.BaseURL), p, path, *labelHolesN)
	}

	html, err := report.RenderWatchPathOpts(path, st, time.Now(), opts)
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
	if len(path.Chains) > 0 {
		c := path.Chains[ps.DeepestChain]
		fmt.Printf("%d rabbit holes, deepest %d videos of %s on %s\n",
			len(path.Chains), c.Len, c.Area,
			path.Sessions[c.Session].Start.Format("2006-01-02"))
	}
	if len(path.Days) > 0 {
		d := path.Days[ps.BusiestDay]
		fmt.Printf("%d days carried a sitting (%s … %s), busiest %s with %d views\n",
			len(path.Days), path.Days[0].Date, path.Days[len(path.Days)-1].Date,
			d.Date, ps.BusiestDayViews)
	}
	fmt.Printf("wrote %s (%.1f MB)\n", htmlPath, float64(len(html))/(1<<20))
	return nil
}
