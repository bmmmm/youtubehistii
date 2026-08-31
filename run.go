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
	fs.Parse(args)
	p := paths{dataDir: *dataDir}

	cfg, err := loadRules(*lf.rulesPath)
	if err != nil {
		return err
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
	// Wave mode is not retry mode: a wave re-classifies whatever enrich just
	// delivered, and mixing targeted re-asks into that would re-ask the same
	// defects once per wave.
	baseOpts.retry = ""
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
		asked += st.llmNew
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
		return nil
	}

	// Final pass: everything has metadata or a tombstone now (minus transient
	// enrich failures), then the report — names-free in the terminal.
	if _, err := scan(); err != nil {
		return err
	}
	if err := runWave(); err != nil {
		return err
	}
	if enrichErr != nil {
		return fmt.Errorf("enrich: %w (classification progress is cached — rerun \"run\" to resume)", enrichErr)
	}
	return cmdReport([]string{"-data", p.dataDir, "-no-names"})
}
