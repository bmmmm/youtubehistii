// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"maps"
	"os"
	"os/signal"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

// cmdRun overlaps the two slow stages: a goroutine runs the full enrich while
// the main loop classifies in waves whatever metadata has landed so far. When
// enrich finishes, one final pass sweeps up the rest and the report renders.
func cmdRun(args []string) error {
	fs, dataDir := newFlagSet("run")
	ef := addEnrichFlags(fs)
	lf := addLLMFlags(fs)
	wf := addWatchPathFlags(fs)
	fs.Parse(args)
	p := paths{dataDir: *dataDir}

	// -retry used to be accepted here and silently dropped two screens down.
	// A wave re-classifies whatever enrich has just delivered; a targeted
	// re-ask mixed into that would re-ask the same defects once per wave. So
	// it is refused rather than ignored — a flag that does nothing is worse
	// than one that says no, because the run afterwards looks like it worked.
	if *lf.retry != "" {
		return fmt.Errorf("-retry belongs to \"classify\", not to \"run\": a wave re-classifies what enrich just delivered, so a targeted re-ask would run once per wave. Let this run finish, then: classify -retry %s", *lf.retry)
	}

	cfg, err := loadRules(*lf.rulesPath)
	if err != nil {
		return err
	}
	// Same idea as loadRules above, for the flag that is read LAST. -taxonomy
	// only takes effect in the report and the page, hours after the run
	// starts, so a missing config/taxonomy.yaml would throw away a full
	// enrich and classification with a message the reader could have had in
	// the first second. Folding an empty slice loads the file and touches
	// nothing.
	if *wf.useTaxonomy {
		if err := foldThroughTaxonomy(nil); err != nil {
			return err
		}
	}
	views, err := readJSONL[takeout.View](p.historyJSONL())
	if err != nil {
		return fmt.Errorf("read history (run \"import\" first): %w", err)
	}

	// First Ctrl-C: stop feeding enrich chunks and let in-flight cache writes
	// finish, so every cache file stays whole and a rerun resumes seamlessly.
	// A second Ctrl-C kills as usual.
	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		fmt.Fprintln(os.Stderr, "\ninterrupt — finishing in-flight chunks (Ctrl-C again to kill); rerun \"run\" to resume")
		close(stop)
		signal.Stop(sig)
	}()

	eopts := ef.opts()
	eopts.stop = stop
	enrichDone := make(chan error, 1)
	go func() { enrichDone <- enrichAll(p, views, eopts) }()

	// The wave consumer keeps metas and verdicts in memory and only ever
	// reads cache files it has not seen yet — never the whole cache per wave.
	var (
		metaSeen    = map[string]bool{}
		verdictSeen = map[string]bool{}
		metas       = map[string]enrich.Meta{}
		cached      = map[string]classify.LLMVerdict{}
	)
	scan := func() (int, error) {
		newMetas, err := loadNewCacheEntries[enrich.Meta](p.metaCacheDir(), metaSeen)
		if err != nil {
			return 0, err
		}
		maps.Copy(metas, newMetas)
		newVerdicts, err := loadNewCacheEntries[classify.LLMVerdict](p.classifyCache(), verdictSeen)
		if err != nil {
			return 0, err
		}
		maps.Copy(cached, newVerdicts)
		return len(newMetas), nil
	}

	baseOpts := lf.opts()
	wave := 0
	asked := 0
	var prev passStats
	runWave := func() error {
		wave++
		opts := baseOpts
		if opts.llmLimit > 0 {
			// -llm-limit caps the whole run, not each wave.
			remaining := opts.llmLimit - asked
			if remaining <= 0 {
				opts.noLLM = true
			} else {
				opts.llmLimit = remaining
			}
		}
		st, err := classifyPass(p, cfg, views, metas, cached, opts)
		if err != nil {
			return err
		}
		asked += st.llmNew + st.llmSub + st.llmMode
		line := fmt.Sprintf("wave %d: +%d classified (llm %d, rules %d), %d waiting",
			wave, st.classified-prev.classified, st.llmNew, st.ruleHits-prev.ruleHits, st.waiting)
		if st.llmDown && !opts.noLLM {
			line += " — oMLX unreachable, retrying next wave"
		}
		fmt.Println(line)
		prev = st
		return nil
	}

	// Wave 1 sweeps the backlog from earlier runs before enrich delivers
	// anything new — a restarted run resumes right where it left off.
	if _, err := scan(); err != nil {
		return err
	}
	if err := runWave(); err != nil {
		return err
	}

	const (
		waveEvery = 60 * time.Second
		waveBatch = 200 // new metas that trigger an early wave
	)
	lastWave := time.Now()
	pending := 0
	var enrichErr error
loop:
	for {
		select {
		case enrichErr = <-enrichDone:
			break loop
		case <-stop:
			enrichErr = <-enrichDone // workers drain their in-flight chunks
			break loop
		case <-time.After(10 * time.Second):
		}
		n, err := scan()
		if err != nil {
			return err
		}
		pending += n
		if pending >= waveBatch || time.Since(lastWave) >= waveEvery {
			if err := runWave(); err != nil {
				return err
			}
			pending = 0
			lastWave = time.Now()
		}
	}
	interrupted := false
	select {
	case <-stop:
		interrupted = true
	default:
	}
	if interrupted {
		if enrichErr != nil {
			fmt.Fprintf(os.Stderr, "enrich stopped: %v\n", enrichErr)
		}
		fmt.Println("interrupted — progress is cached, rerun \"run\" to resume")
		fmt.Println(prev.nextLine())
		// What is already classified is already worth looking at. Without
		// this the run ends on "rerun", and the CSV and the page that the
		// finished waves have earned go unmentioned.
		fmt.Println(`or read what is already there: "report", then "watchpath"`)
		return nil
	}

	// Final pass: everything has metadata or a tombstone now (minus transient
	// enrich failures), then the report — names-free in the terminal.
	//
	// -include-unenriched applies HERE and nowhere earlier: enrich has had its
	// whole run, so what is still without metadata will not get any. Set on
	// every wave it would have spent title-only asks on videos whose metadata
	// was still in flight.
	baseOpts.includeUnenriched = *lf.includeUnenriched
	if _, err := scan(); err != nil {
		return err
	}
	if err := runWave(); err != nil {
		return err
	}
	fmt.Println(prev.nextLine())
	if enrichErr != nil {
		return fmt.Errorf("enrich: %w (classification progress is cached — rerun \"run\" to resume, or read what is there with \"report\" and \"watchpath\")", enrichErr)
	}
	// Both stages read the SAME taxonomy switch: a terminal summary folded
	// differently from the page it points at would be two answers to one
	// question.
	reportArgs := []string{"-data", p.dataDir, "-no-names"}
	if *wf.useTaxonomy {
		reportArgs = append(reportArgs, "-taxonomy")
	}
	if err := cmdReport(reportArgs); err != nil {
		return err
	}
	// The page last, so its path is the final line — it is what the run was
	// for. Before ca1703d "report" wrote one and this was covered by the line
	// above; since then it did not, and nothing here noticed.
	return writeWatchPath(p, *lf.rulesPath, wf.opts())
}
