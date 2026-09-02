// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	rulesPath := fs.String("rules", "", "rules file — only read for the chat model behind -label-holes")
	wf := addWatchPathFlags(fs)
	fs.Parse(args)
	return writeWatchPath(paths{dataDir: *dataDir}, *rulesPath, wf.opts())
}

// watchPathFlags are the page flags shared by "watchpath" and "run". "run"
// owns -rules already (the LLM stage needs it), so it is not in here.
type watchPathFlags struct {
	useTaxonomy  *bool
	taxonomyFile *string
	labelHoles   *int
}

func addWatchPathFlags(fs *flag.FlagSet) watchPathFlags {
	return watchPathFlags{
		useTaxonomy:  fs.Bool("taxonomy", false, "project topics through the taxonomy before building the path"),
		taxonomyFile: addTaxonomyFileFlag(fs),
		labelHoles:   fs.Int("label-holes", 0, "ask the chat model for a short name for the N deepest rabbit holes (0 = off; replies are cached, a rerun asks nothing)"),
	}
}

// pageOpts is what writeWatchPath needs, past the paths and the rules file.
type pageOpts struct {
	useTaxonomy  bool
	taxonomyFile string
	labelHoles   int
}

func (wf watchPathFlags) opts() pageOpts {
	return pageOpts{useTaxonomy: *wf.useTaxonomy, taxonomyFile: *wf.taxonomyFile, labelHoles: *wf.labelHoles}
}

// writeWatchPath renders the page and writes it. Split out of cmdWatchPath so
// "run" can end on the page as well: it used to, back when "report" still
// wrote an HTML file of its own, and lost it in ca1703d when the report moved
// into the app — nothing in run.go was pulled along, and a full run has been
// leaving the reader without the one page this tool makes ever since.
func writeWatchPath(p paths, rulesPath string, o pageOpts) error {
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
	opts := report.WatchPathOpts{Taxonomy: provenance}
	if o.labelHoles > 0 {
		cfg, err := loadRules(rulesPath)
		if err != nil {
			return err
		}
		opts.HoleLabels = labelHoles(omlx.New(cfg.LLM.Model, cfg.LLM.BaseURL), p, path, o.labelHoles)
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
	if note := pageSizeNote(len(html), ps.Views); note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	if line := folded.line(provenance); line != "" {
		fmt.Println(line)
	}
	runPageCheck(os.Stderr, htmlPath)
	return nil
}

// pageSizeWarnMB is a fixed ceiling, not a budget that grows with the corpus.
// Measured: 35,144 views render to 4.1 MB, so 4.3 leaves room for the corpus
// to grow without crying wolf. A per-view budget was considered and dropped —
// it would rise along with any regression that made each row more expensive,
// which is exactly the change worth catching. A page that stops opening on a
// phone is a page nobody reads.
const pageSizeWarnMB = 4.3

// pageSizeNote reports bytes per view alongside the total, because the total
// alone cannot say whether the page grew because the history did or because
// the payload got fatter.
func pageSizeNote(bytes, views int) string {
	mb := float64(bytes) / (1 << 20)
	if mb <= pageSizeWarnMB {
		return ""
	}
	perView := 0
	if views > 0 {
		perView = bytes / views
	}
	return fmt.Sprintf("note: the page is %.1f MB, past the %.1f MB mark (%d bytes per view over %d views) — check what grew before it stops opening on a phone",
		mb, pageSizeWarnMB, perView, views)
}

// runPageCheck runs the page's own JavaScript over the page that was just
// written. The Go tests build this page as a STRING and never execute a line
// of it; pagecheck does, and it caught a syntax error that would have killed
// the page before its first. CI runs it against an invented fixture — this
// runs it against the real thing, which is the only copy that has the real
// data's shapes in it.
//
// Never fatal, and never noisy: no script (the binary was installed away from
// its repo) is silence, no node is one line naming the command, and a failing
// check is a warning. The gate is CI; this is a heads-up on the file you are
// about to open.
func runPageCheck(w io.Writer, htmlPath string) {
	const script = "tools/pagecheck/pagecheck.js"
	if _, err := os.Stat(script); err != nil {
		return
	}
	node, err := exec.LookPath("node")
	if err != nil {
		fmt.Fprintf(w, "note: node not found, so the page's own JavaScript was not run — node %s %s\n", script, htmlPath)
		return
	}
	out, err := exec.Command(node, script, htmlPath).CombinedOutput()
	if err != nil {
		fmt.Fprintf(w, "warning: the page did not pass its own checks (%v):\n%s\n", err, out)
		return
	}
	fmt.Fprint(w, lastLine(out))
}

// lastLine returns pagecheck's verdict line and drops its per-check chatter.
func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1] + "\n"
}
