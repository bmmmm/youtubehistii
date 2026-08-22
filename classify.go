// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/omlx"
	"github.com/bmmmm/youtubehistii/internal/rules"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

// llmFlags are the classification flags shared by "classify" and "run".
type llmFlags struct {
	rulesPath  *string
	noLLM      *bool
	llmLimit   *int
	llmBatch   *int
	llmWorkers *int
}

func addLLMFlags(fs *flag.FlagSet) llmFlags {
	return llmFlags{
		rulesPath:  fs.String("rules", "", "rules file (default: config/rules.yaml, falling back to config/rules.example.yaml)"),
		noLLM:      fs.Bool("no-llm", false, "skip the LLM stage, rules only"),
		llmLimit:   fs.Int("llm-limit", 0, "ask the LLM about at most N videos this run (0 = all)"),
		llmBatch:   fs.Int("llm-batch", 10, "videos per LLM request (1 = one request per video)"),
		llmWorkers: fs.Int("llm-workers", 1, "parallel LLM requests (raise only if the server actually decodes concurrently)"),
	}
}

func (lf llmFlags) opts() classifyOpts {
	return classifyOpts{
		noLLM:      *lf.noLLM,
		llmLimit:   *lf.llmLimit,
		llmBatch:   *lf.llmBatch,
		llmWorkers: *lf.llmWorkers,
	}
}

func cmdClassify(args []string) error {
	fs, dataDir := newFlagSet("classify")
	lf := addLLMFlags(fs)
	includeUnenriched := fs.Bool("include-unenriched", false, "ask the LLM even about videos without cached metadata (title-only verdicts)")
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
	metas, err := enrich.Cache{Dir: p.metaCacheDir()}.ReadAll()
	if err != nil {
		return err
	}
	if len(metas) == 0 {
		fmt.Fprintln(os.Stderr, "note: metadata cache is empty — run \"enrich\" first for tags/categories/durations")
	}
	cached, err := loadNewCacheEntries[classify.LLMVerdict](p.classifyCache(), map[string]bool{})
	if err != nil {
		return err
	}

	opts := lf.opts()
	opts.includeUnenriched = *includeUnenriched
	opts.progress = true
	_, err = classifyPass(p, cfg, views, metas, cached, opts)
	return err
}

// classifyOpts configures one classifyPass; "run" reuses it wave after wave.
type classifyOpts struct {
	noLLM             bool
	llmLimit          int  // max live LLM asks this pass (0 = all)
	llmBatch          int  // videos per LLM request (<=1 = single requests)
	llmWorkers        int  // parallel LLM requests
	includeUnenriched bool // ask title-only even without a meta cache entry
	progress          bool // per-stage prints (off in wave mode — run prints wave lines)
}

// passStats sums up one classifyPass for the wave line.
type passStats struct {
	unique     int // unique videos with an id
	classified int // unique videos with a verdict (rules + llm)
	ruleHits   int
	llmNew     int // live LLM verdicts gained this pass
	waiting    int // unenriched videos skipped until enrich delivers metadata
	llmDown    bool
}

// classifyPass runs one full classification over views: rules first, then the
// LLM for whatever has a meta cache entry (basis "full") or a tombstone
// (title-only is the max there — marked, never re-asked). Unenriched videos
// wait for enrich unless opts.includeUnenriched. New LLM verdicts go to the
// cache AND into cached, so a wave caller hands the same map in every time.
// Ends by rewriting classified.jsonl (atomic).
func classifyPass(p paths, cfg *rules.Config, views []takeout.View, metas map[string]enrich.Meta, cached map[string]classify.LLMVerdict, opts classifyOpts) (passStats, error) {
	var st passStats

	// Per unique video: build the matcher input (canonical metadata wins,
	// the takeout row fills the gaps) and run stage 1.
	inputs := map[string]rules.Input{}
	for _, v := range views {
		if v.VideoID == "" {
			continue
		}
		if _, done := inputs[v.VideoID]; done {
			continue
		}
		in := rules.Input{Title: v.Title, Channel: v.Channel}
		if m, ok := metas[v.VideoID]; ok && !m.Unavailable {
			if m.Title != "" {
				in.Title = m.Title
			}
			if m.Channel != "" {
				in.Channel = m.Channel
			}
			in.Tags = m.Tags
			in.Categories = m.Categories
		}
		inputs[v.VideoID] = in
	}
	st.unique = len(inputs)

	type videoVerdict struct {
		topic, mode, source string
		confidence          float64
	}
	verdicts := map[string]videoVerdict{}
	var needLLM []string
	for id, in := range inputs {
		if topic, mode, ruleID, ok := cfg.Match(in); ok {
			verdicts[id] = videoVerdict{topic: topic, mode: mode, source: "rule:" + ruleID}
		} else {
			needLLM = append(needLLM, id)
		}
	}
	st.ruleHits = len(verdicts)
	sort.Strings(needLLM)
	if opts.progress {
		fmt.Printf("%d unique videos: %d matched by rules, %d for the LLM\n",
			len(inputs), len(verdicts), len(needLLM))
	}

	// Stage 2 — cached LLM verdicts first, then select what to ask live. A
	// stale title-only verdict stays in place as a fallback until its re-ask
	// (with full metadata) lands, so an oMLX outage never loses coverage.
	llmCache := classify.Cache{Dir: p.classifyCache()}
	var live []string
	cachedHits := 0
	for _, id := range needLLM {
		m, hasMeta := metas[id]
		if v, ok := cached[id]; ok {
			verdicts[id] = videoVerdict{topic: v.Topic, mode: v.Mode, source: "llm:" + v.Model, confidence: v.Confidence}
			cachedHits++
			if v.Stale(hasMeta, m.Unavailable) {
				live = append(live, id)
			}
			continue
		}
		if hasMeta || opts.includeUnenriched {
			live = append(live, id)
		} else {
			st.waiting++
		}
	}
	if opts.progress {
		fmt.Printf("LLM: %d cached verdicts, %d to ask, %d waiting for enrich\n",
			cachedHits, len(live), st.waiting)
	}

	llmDown := opts.noLLM
	if opts.llmLimit > 0 && len(live) > opts.llmLimit {
		live = live[:opts.llmLimit]
		if opts.progress {
			fmt.Printf("limiting LLM calls to %d this run\n", len(live))
		}
	}

	var parseWarnings []string
	if !llmDown && len(live) > 0 {
		client := omlx.New(cfg.LLM.Model, cfg.LLM.BaseURL)
		// Discovery doubles as health check: bail out early with the real
		// model list instead of failing per-video.
		models, err := client.Models()
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "warning: %v\nwarning: skipping the LLM this pass — %d videos wait for the next one\n", err, len(live))
			llmDown = true
		case !slices.Contains(models, client.Model):
			return st, fmt.Errorf("model %q not on the oMLX server — available: %s", client.Model, strings.Join(models, ", "))
		}
		if !llmDown && opts.progress {
			fmt.Printf("asking %s (model %s)\n", client.BaseURL, client.Model)
		}

		basisFor := func(id string) string {
			if m, ok := metas[id]; ok && !m.Unavailable {
				return classify.BasisFull
			}
			return classify.BasisTitleOnly
		}
		batchSize := max(opts.llmBatch, 1)
		workers := max(opts.llmWorkers, 1)
		var (
			mu    sync.Mutex
			fatal error
			done  int
		)
		// The helpers below must be called under mu.
		connLost := func(err error) {
			if !llmDown {
				fmt.Fprintf(os.Stderr, "warning: %v\nwarning: skipping the LLM for the rest of this pass — verdicts so far are cached\n", err)
			}
			llmDown = true
		}
		store := func(id string, v classify.LLMVerdict) {
			v.Model = client.Model
			v.Basis = basisFor(id)
			if err := llmCache.Write(id, v); err != nil {
				if fatal == nil {
					fatal = err
				}
				return
			}
			cached[id] = v
			verdicts[id] = videoVerdict{topic: v.Topic, mode: v.Mode, source: "llm:" + v.Model, confidence: v.Confidence}
			st.llmNew++
		}

		process := func(ids []string) {
			rest := ids
			if len(ids) > 1 {
				items := make([]rules.Input, len(ids))
				for i, id := range ids {
					items[i] = inputs[id]
				}
				system, user := classify.BuildBatchPrompt(cfg, items)
				// max_tokens scales with the batch: ~15 tokens per verdict
				// line plus headroom, so replies are never cut off mid-line.
				reply, err := client.ChatMax(system, user, 30*len(ids)+200)
				if err != nil {
					mu.Lock()
					if isConnErr(err) {
						connLost(err)
						mu.Unlock()
						return
					}
					parseWarnings = append(parseWarnings, fmt.Sprintf("batch of %d: %v", len(ids), err))
					mu.Unlock()
				} else if batch, perr := classify.ParseBatchVerdicts(cfg, ids, reply); perr != nil {
					mu.Lock()
					parseWarnings = append(parseWarnings, fmt.Sprintf("batch of %d: %v", len(ids), perr))
					mu.Unlock()
				} else {
					mu.Lock()
					for _, id := range ids {
						store(id, batch[id])
					}
					mu.Unlock()
					rest = nil
				}
			}
			// Single requests: batch of 1, or fallback after a rejected batch
			// reply — the verified per-video path, never guessed mappings.
			for _, id := range rest {
				mu.Lock()
				stop := llmDown || fatal != nil
				mu.Unlock()
				if stop {
					return
				}
				v, err := askLLM(client, cfg, inputs[id])
				mu.Lock()
				switch {
				case err != nil && isConnErr(err):
					connLost(err)
					mu.Unlock()
					return
				case err != nil:
					parseWarnings = append(parseWarnings, fmt.Sprintf("%s: %v", id, err))
				default:
					store(id, v)
				}
				mu.Unlock()
			}
		}

		jobs := make(chan []string)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for ids := range jobs {
					mu.Lock()
					skip := llmDown || fatal != nil
					mu.Unlock()
					if !skip {
						process(ids)
					}
					mu.Lock()
					done += len(ids)
					if opts.progress && (batchSize > 1 || done%25 == 0 || done == len(live)) {
						fmt.Printf("  %d/%d\n", done, len(live))
					}
					mu.Unlock()
				}
			}()
		}
		for off := 0; off < len(live); off += batchSize {
			jobs <- live[off:min(off+batchSize, len(live))]
		}
		close(jobs)
		wg.Wait()
		if fatal != nil {
			return st, fatal
		}
	}
	if len(parseWarnings) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d LLM replies rejected (single-request fallback ran where possible), first %d:\n",
			len(parseWarnings), min(len(parseWarnings), 3))
		for _, w := range parseWarnings[:min(len(parseWarnings), 3)] {
			fmt.Fprintf(os.Stderr, "  %s\n", w)
		}
	}
	st.llmDown = llmDown
	st.classified = len(verdicts)

	// Join verdicts back onto every watch event.
	var out []classify.Verdict
	for _, v := range views {
		row := classify.Verdict{
			VideoID:   v.VideoID,
			Title:     v.Title,
			Channel:   v.Channel,
			ChannelID: takeout.ChannelIDFromURL(v.ChannelURL),
			WatchedAt: v.WatchedAt,
			Topic:     "unclear",
			Source:    "unclassified",
		}
		if m, ok := metas[v.VideoID]; ok {
			row.DurationS = m.Duration
			row.Unavailable = m.Unavailable
			if m.Title != "" {
				row.Title = m.Title
			}
			if m.Channel != "" {
				row.Channel = m.Channel
			}
			if m.ChannelID != "" {
				row.ChannelID = m.ChannelID
			}
		}
		if v.VideoID == "" {
			// No video ID (deleted/private): still give the rules a shot at
			// the takeout title/channel.
			if topic, mode, ruleID, ok := cfg.Match(rules.Input{Title: v.Title, Channel: v.Channel}); ok {
				row.Topic, row.Mode, row.Source = topic, mode, "rule:"+ruleID
			}
		} else if vv, ok := verdicts[v.VideoID]; ok {
			row.Topic, row.Mode, row.Source, row.Confidence = vv.topic, vv.mode, vv.source, vv.confidence
		}
		out = append(out, row)
	}
	if err := writeJSONL(p.classifiedJSONL(), out); err != nil {
		return st, err
	}

	if opts.progress {
		bySource := map[string]int{}
		for _, r := range out {
			switch {
			case strings.HasPrefix(r.Source, "rule:"):
				bySource["rule"]++
			case strings.HasPrefix(r.Source, "llm:"):
				bySource["llm"]++
			default:
				bySource["unclassified"]++
			}
		}
		fmt.Printf("wrote %s: %d views (%d via rules, %d via llm, %d unclassified)\n",
			p.classifiedJSONL(), len(out), bySource["rule"], bySource["llm"], bySource["unclassified"])
		if llmDown && bySource["unclassified"] > 0 && !opts.noLLM {
			fmt.Println("rerun \"classify\" once oMLX is up to fill the gap — verdicts are cached.")
		}
	}
	return st, nil
}

func loadRules(path string) (*rules.Config, error) {
	if path != "" {
		return rules.Load(path)
	}
	cfg, err := rules.Load("config/rules.yaml")
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "note: config/rules.yaml not found, using config/rules.example.yaml — copy and adapt it")
		return rules.Load("config/rules.example.yaml")
	}
	return cfg, err
}

func askLLM(client *omlx.Client, cfg *rules.Config, in rules.Input) (classify.LLMVerdict, error) {
	system, user := classify.BuildPrompt(cfg, in)
	reply, err := client.Chat(system, user)
	if err != nil {
		return classify.LLMVerdict{}, err
	}
	v, err := classify.ParseVerdict(cfg, reply)
	if err != nil {
		return classify.LLMVerdict{}, err
	}
	v.Model = client.Model
	return v, nil
}

func isConnErr(err error) bool {
	return strings.Contains(err.Error(), "unreachable") || strings.Contains(err.Error(), "401")
}
