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

func cmdReport(args []string) error {
	fs, dataDir := newFlagSet("report")
	fs.Parse(args)
	p := paths{dataDir: *dataDir}

	rows, err := readJSONL[classify.Verdict](p.classifiedJSONL())
	if err != nil {
		return fmt.Errorf("read classified views (run \"classify\" first): %w", err)
	}
	subs, err := readJSONL[takeout.Subscription](p.subscriptionsJSONL())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	st := report.Aggregate(rows, subs)

	if err := os.MkdirAll(p.outDir(), 0o755); err != nil {
		return err
	}
	htmlPath := filepath.Join(p.outDir(), "report.html")
	html, err := report.RenderHTML(st, time.Now())
	if err != nil {
		return err
	}
	if err := os.WriteFile(htmlPath, html, 0o644); err != nil {
		return err
	}
	csvPath := filepath.Join(p.outDir(), "report.csv")
	cf, err := os.Create(csvPath)
	if err != nil {
		return err
	}
	if err := report.WriteCSV(cf, rows, st.SubbedSet); err != nil {
		cf.Close()
		return err
	}
	if err := cf.Close(); err != nil {
		return err
	}

	fmt.Print(report.RenderText(st))
	fmt.Printf("\nwrote %s and %s\n", htmlPath, csvPath)
	return nil
}
