// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/report"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

func cmdReport(args []string) error {
	fs, dataDir := newFlagSet("report")
	noNames := fs.Bool("no-names", false, "terminal summary without channel/subscription names (aggregates and topics only, safe to paste)")
	useTaxonomy := fs.Bool("taxonomy", false, "project topics through the taxonomy before aggregating")
	taxFile := addTaxonomyFileFlag(fs)
	fs.Parse(args)
	return writeReport(paths{dataDir: *dataDir}, reportOpts{
		noNames:      *noNames,
		useTaxonomy:  *useTaxonomy,
		taxonomyFile: *taxFile,
	})
}

// reportOpts is what writeReport needs past the paths. "run" builds one
// directly rather than assembling a []string and parsing it back — the
// round trip through flags could only ever lose a value or invent one.
type reportOpts struct {
	noNames      bool
	useTaxonomy  bool
	taxonomyFile string

	// chained is set when "run" calls this mid-pipeline. The run prints its
	// own taxonomy line and ends on the page, so repeating either here would
	// mean telling the reader to run the command that is about to run.
	chained bool
}

func writeReport(p paths, o reportOpts) error {
	rows, err := readJSONL[classify.Verdict](p.classifiedJSONL())
	if err != nil {
		return fmt.Errorf("read classified views (run \"classify\" first): %w", err)
	}
	provenance := "none"
	var folded foldStats
	if o.useTaxonomy {
		provenance = taxonomyProvenance(o.taxonomyFile)
		if folded, err = foldThroughTaxonomy(p, o.taxonomyFile, rows); err != nil {
			return err
		}
	}
	subs, err := readJSONL[takeout.Subscription](p.subscriptionsJSONL())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	st := report.Aggregate(rows, subs)

	if err := os.MkdirAll(p.outDir(), 0o755); err != nil {
		return err
	}
	csvPath := filepath.Join(p.outDir(), "report.csv")
	cf, err := os.Create(csvPath)
	if err != nil {
		return err
	}
	if err := report.WriteCSV(cf, rows, st.SubbedSet, provenance); err != nil {
		cf.Close()
		return err
	}
	if err := cf.Close(); err != nil {
		return err
	}

	fmt.Print(report.RenderText(st, !o.noNames))
	fmt.Printf("\nwrote %s\n", csvPath)
	if o.chained {
		return nil
	}
	if line := folded.line(provenance); line != "" {
		fmt.Println(line)
	}
	// There is no report.html any more: the same numbers are a VIEW of the
	// watch path page, next to the timeline, the calendar and the topic tree.
	fmt.Printf("the same numbers to look at: run \"watchpath\", then open %s at #/report\n",
		filepath.Join(p.outDir(), "watchpath.html"))
	return nil
}
