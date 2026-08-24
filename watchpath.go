// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/report"
)

// cmdWatchPath renders the timeline view — the same classified views as
// "report", read along the time axis instead of aggregated. Its own file:
// report.html answers what was watched, this one answers how.
func cmdWatchPath(args []string) error {
	fs, dataDir := newFlagSet("watchpath")
	fs.Parse(args)
	p := paths{dataDir: *dataDir}

	rows, err := readJSONL[classify.Verdict](p.classifiedJSONL())
	if err != nil {
		return fmt.Errorf("read classified views (run \"classify\" first): %w", err)
	}
	path := report.BuildPath(rows)

	if err := os.MkdirAll(p.outDir(), 0o755); err != nil {
		return err
	}
	htmlPath := filepath.Join(p.outDir(), "watchpath.html")
	html, err := report.RenderWatchPath(path, time.Now())
	if err != nil {
		return err
	}
	if err := os.WriteFile(htmlPath, html, 0o644); err != nil {
		return err
	}

	// Aggregates only — the page carries the titles, the terminal does not
	// need them. They come straight from the stats the page runs on, so the
	// two can never drift apart.
	st := path.Stats
	fmt.Printf("%d views on the timeline in %d sessions", st.Views, st.Sessions)
	if st.Dropped > 0 {
		fmt.Printf(", %d without a timestamp left off", st.Dropped)
	}
	fmt.Println()
	fmt.Printf("%d views suspected of overlapping, %d inside a same-area chain\n",
		st.OverlapViews, st.RabbitViews)
	if len(path.Days) > 0 {
		d := path.Days[st.BusiestDay]
		fmt.Printf("%d days carried a sitting (%s … %s), busiest %s with %d views\n",
			len(path.Days), path.Days[0].Date, path.Days[len(path.Days)-1].Date,
			d.Date, st.BusiestDayViews)
	}
	fmt.Printf("wrote %s (%.1f MB)\n", htmlPath, float64(len(html))/(1<<20))
	return nil
}
