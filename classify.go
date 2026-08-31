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
	"github.com/bmmmm/youtubehistii/internal/counts"
	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/omlx"
	"github.com/bmmmm/youtubehistii/internal/rules"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

// llmFlags are the classification flags shared by "classify" and "run".
type llmFlags struct {
	rulesPath    *string
	noLLM        *bool
	llmLimit     *int
	llmBatch     *int
	llmWorkers   *int
	keepVerdicts *bool
}

func addLLMFlags(fs *flag.FlagSet) llmFlags {
	return llmFlags{
		rulesPath:    fs.String("rules", "", "rules file (default: config/rules.yaml, falling back to config/rules.example.yaml)"),
		noLLM:        fs.Bool("no-llm", false, "skip the LLM stage, rules only"),
		llmLimit:     fs.Int("llm-limit", 0, "ask the LLM about at most N videos this run (0 = all)"),
		llmBatch:     fs.Int("llm-batch", 10, "videos per LLM request (1 = one request per video)"),
		llmWorkers:   fs.Int("llm-workers", 1, "parallel LLM requests (raise only if the server actually decodes concurrently)"),
		keepVerdicts: fs.Bool("keep-verdicts", false, "keep cached verdicts even though the taxonomy changed (for a reworded desc — a changed area list needs a re-ask)"),
	}
}

func (lf llmFlags) opts() classifyOpts {
	return classifyOpts{
		noLLM:        *lf.noLLM,
		llmLimit:     *lf.llmLimit,
		llmBatch:     *lf.llmBatch,
		llmWorkers:   *lf.llmWorkers,
		keepVerdicts: *lf.keepVerdicts,
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
	keepVerdicts      bool // do not re-ask verdicts just because the taxonomy changed
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
//
// The pass runs as four stages on one shared state (the pass struct below):
// rules, cached verdicts, the live LLM round, and the write-out. Each stage
// reads what the previous ones left and nothing later.
func classifyPass(p paths, cfg *rules.Config, views []takeout.View, metas map[string]enrich.Meta, cached map[string]classify.LLMVerdict, opts classifyOpts) (passStats, error) {
	r := &pass{
		p: p, cfg: cfg, views: views, metas: metas, cached: cached, opts: opts,
		taxonomy: cfg.Fingerprint(),
		items:    map[string]classify.Item{},
		verdicts: map[string]videoVerdict{},
		llmDown:  opts.noLLM,
	}
	live := r.resolveCached(r.matchRules())
	if err := r.askLive(live); err != nil {
		return r.st, err
	}
	if err := r.write(); err != nil {
		return r.st, err
	}
	return r.st, nil
}

// pass is one classifyPass mid-flight: the inputs it was called with and the
// state its stages hand from one to the next.
type pass struct {
	p      paths
	cfg    *rules.Config
	views  []takeout.View
	metas  map[string]enrich.Meta
	cached map[string]classify.LLMVerdict
	opts   classifyOpts

	taxonomy      string                   // cfg.Fingerprint(), stamped on new verdicts and compared against cached ones
	items         map[string]classify.Item // one matcher input per unique video
	verdicts      map[string]videoVerdict  // what the pass has decided so far, by video
	st            passStats
	llmDown       bool // starts as opts.noLLM, flips on a connection loss
	parseWarnings []string
}

type videoVerdict struct {
	topic, mode, source string
	confidence          float64
}

// matchRules is stage 1: build the matcher input per unique video, derive
// the area from the YouTube category, run the rules, and return the IDs the
// rules could not answer (sorted, so the pass is deterministic).
func (r *pass) matchRules() (needLLM []string) {
	// Per unique video: build the matcher input (canonical metadata wins,
	// the takeout row fills the gaps), derive the area from the YouTube
	// category, and run stage 1.
	//
	// The category IS the area. Every enriched video carries one, the uploader
	// picked it at upload time, and it costs neither a rule nor a model call —
	// so the LLM is left with the two questions no metadata answers: which
	// specific subject (the sub), and consume or learn (the mode). Only videos
	// with no category at all (tombstoned or not yet enriched) still have their
	// area decided by the model.
	for _, v := range r.views {
		if v.VideoID == "" {
			continue
		}
		if _, done := r.items[v.VideoID]; done {
			continue
		}
		item := classify.Item{Input: rules.Input{Title: v.Title, Channel: v.Channel}}
		if m, ok := r.metas[v.VideoID]; ok && !m.Unavailable {
			if m.Title != "" {
				item.Title = m.Title
			}
			if m.Channel != "" {
				item.Channel = m.Channel
			}
			item.Tags = m.Tags
			item.Categories = m.Categories
			item.Area, _ = r.cfg.AreaForCategory(rules.FirstCategory(m.Categories))
		}
		r.items[v.VideoID] = item
	}
	r.st.unique = len(r.items)

	for id, item := range r.items {
		if topic, mode, ruleID, ok := r.cfg.Match(item.Input); ok {
			r.verdicts[id] = videoVerdict{topic: topic, mode: mode, source: "rule:" + ruleID}
		} else {
			needLLM = append(needLLM, id)
		}
	}
	r.st.ruleHits = len(r.verdicts)
	sort.Strings(needLLM)
	if r.opts.progress {
		withArea := 0
		for _, id := range needLLM {
			if r.items[id].Area != "" {
				withArea++
			}
		}
		fmt.Printf("%d unique videos: %d matched by rules, %d for the LLM (%d of those with the area already fixed by their YouTube category)\n",
			len(r.items), len(r.verdicts), len(needLLM), withArea)
	}
	return needLLM
}

// resolveCached is stage 2 — cached LLM verdicts first, then select what to
// ask live. A stale title-only verdict stays in place as a fallback until its
// re-ask (with full metadata) lands, so an oMLX outage never loses coverage.
func (r *pass) resolveCached(needLLM []string) (live []string) {
	cachedHits, taxonomyStale := 0, 0
	oldTaxonomies := map[string]bool{}
	for _, id := range needLLM {
		m, hasMeta := r.metas[id]
		if v, ok := r.cached[id]; ok {
			// Canonicalize on read, so a sub alias added after a run folds
			// old verdicts on the next pass without asking the LLM again.
			topic, usable := r.cfg.NormalizeTopic(v.Topic)
			// The category decides the area — a cached verdict is no
			// exception. An older taxonomy that spelled it differently
			// ("politics" for what is now "news-politics") must not outvote
			// it, and an area that no longer exists at all must not survive
			// as a dead label: with the LLM off or down, that stopgap was
			// what a report ended up showing.
			//
			// The sub goes WITH the area it was judged under. It answered
			// "which subject within THIS area", so under a different one it
			// is not a weaker answer but a wrong one — that is how
			// "sports/other" and "people-blogs/tutorials" got into a report,
			// out of the old "gaming/other" and "dev/tutorials". Where the
			// area is unchanged (a reworded desc, a new alias) the sub still
			// stands. Either way a re-ask is queued below.
			if area := r.items[id].Area; area != "" {
				oldArea, _ := rules.SplitTopic(v.Topic)
				if strings.EqualFold(strings.TrimSpace(oldArea), area) {
					topic, usable = r.cfg.ReplaceArea(v.Topic, area), true
				} else {
					topic, usable = area, true
				}
			}
			if usable {
				r.verdicts[id] = videoVerdict{topic: topic, mode: v.Mode, source: "llm:" + v.Model, confidence: v.Confidence}
				cachedHits++
			}
			// -keep-verdicts pins the check to whatever the verdict already
			// carries, so a taxonomy change cannot make it stale and only the
			// metadata rule still applies.
			want := r.taxonomy
			if r.opts.keepVerdicts {
				want = v.Taxonomy
			} else if v.Taxonomy != r.taxonomy {
				taxonomyStale++
				oldTaxonomies[v.Taxonomy] = true
			}
			if v.Stale(want, hasMeta, m.Unavailable) {
				live = append(live, id)
			}
			continue
		}
		if hasMeta || r.opts.includeUnenriched {
			live = append(live, id)
		} else {
			r.st.waiting++
		}
	}
	// Always reported, progress or not: re-asking the cache is the most
	// expensive thing a taxonomy edit can trigger, so it is never silent.
	if taxonomyStale > 0 {
		olds := make([]string, 0, len(oldTaxonomies))
		for t := range oldTaxonomies {
			if t == "" {
				t = "none" // pre-fingerprint verdicts
			}
			olds = append(olds, t)
		}
		sort.Strings(olds)
		fmt.Printf("taxonomy changed (%s → %s): re-asking %d cached verdicts\n",
			strings.Join(olds, ", "), r.taxonomy, taxonomyStale)
	}
	if r.opts.progress {
		fmt.Printf("LLM: %d cached verdicts, %d to ask, %d waiting for enrich\n",
			cachedHits, len(live), r.st.waiting)
	}
	return live
}

// askLive is stage 3: the live LLM round — batches, parsing, the verified
// single-request fallback — over the IDs stage 2 selected. It returns an
// error only for a broken verdict cache; a lost server just flips llmDown
// and leaves the rest of the pass to run on what it has.
func (r *pass) askLive(live []string) error {
	if r.opts.llmLimit > 0 && len(live) > r.opts.llmLimit {
		live = live[:r.opts.llmLimit]
		if r.opts.progress {
			fmt.Printf("limiting LLM calls to %d this run\n", len(live))
		}
	}

	if !r.llmDown && len(live) > 0 {
		llmCache := classify.Cache{Dir: r.p.classifyCache()}
		client := omlx.New(r.cfg.LLM.Model, r.cfg.LLM.BaseURL)
		// Discovery doubles as health check: bail out early with the real
		// model list instead of failing per-video.
		models, err := client.Models()
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "warning: %v\nwarning: skipping the LLM this pass — %d videos wait for the next one\n", err, len(live))
			r.llmDown = true
		case !slices.Contains(models, client.Model):
			fmt.Fprintf(os.Stderr, "warning: model %q not on the oMLX server (available: %s)\nwarning: skipping the LLM this pass — %d videos wait for the next one\n",
				client.Model, strings.Join(models, ", "), len(live))
			r.llmDown = true
		}
		if !r.llmDown && r.opts.progress {
			fmt.Printf("asking %s (model %s)\n", client.BaseURL, client.Model)
		}

		// What the model gets to reuse: every sub already assigned, by rules
		// and by cached verdicts alike.
		topicsSoFar := make([]string, 0, len(r.verdicts)+len(r.cached))
		for _, v := range r.verdicts {
			topicsSoFar = append(topicsSoFar, v.topic)
		}
		for _, v := range r.cached {
			topicsSoFar = append(topicsSoFar, v.Topic)
		}
		seeds := collectSubSeeds(r.cfg, topicsSoFar)

		basisFor := func(id string) string {
			if m, ok := r.metas[id]; ok && !m.Unavailable {
				return classify.BasisFull
			}
			return classify.BasisTitleOnly
		}
		batchSize := max(r.opts.llmBatch, 1)
		workers := max(r.opts.llmWorkers, 1)
		var (
			mu            sync.Mutex
			fatal         error
			done          int
			areaOverrides int // model answers whose area contradicted the category
		)
		// The helpers below must be called under mu.
		connLost := func(err error) {
			if !r.llmDown {
				fmt.Fprintf(os.Stderr, "warning: %v\nwarning: skipping the LLM for the rest of this pass — verdicts so far are cached\n", err)
			}
			r.llmDown = true
		}
		store := func(id string, v classify.LLMVerdict) {
			// Where the YouTube category fixed the area, the answer's area is
			// not a judgement to respect but a field that may have drifted:
			// keep the sub the model found and put the area back. The prompt
			// says as much, so a mismatch is a prompt-quality signal, counted
			// and reported rather than silently corrected.
			if area := r.items[id].Area; area != "" {
				if fixed := r.cfg.ReplaceArea(v.Topic, area); fixed != v.Topic {
					areaOverrides++
					v.Topic = fixed
				}
			}
			v.Model = client.Model
			v.Basis = basisFor(id)
			v.Taxonomy = r.taxonomy
			if err := llmCache.Write(id, v); err != nil {
				if fatal == nil {
					fatal = err
				}
				return
			}
			r.cached[id] = v
			r.verdicts[id] = videoVerdict{topic: v.Topic, mode: v.Mode, source: "llm:" + v.Model, confidence: v.Confidence}
			r.st.llmNew++
		}

		process := func(ids []string) {
			rest := ids
			if len(ids) > 1 {
				batch := make([]classify.Item, len(ids))
				for i, id := range ids {
					batch[i] = r.items[id]
				}
				system, user := classify.BuildBatchPrompt(r.cfg, batch, seeds)
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
					r.parseWarnings = append(r.parseWarnings, fmt.Sprintf("batch of %d: %v", len(ids), err))
					mu.Unlock()
				} else if batch, perr := classify.ParseBatchVerdicts(r.cfg, ids, reply); perr != nil {
					mu.Lock()
					// The parse error alone says a batch failed, not why. The
					// head of the reply does — a wrong field order, a code
					// fence, a reasoning preamble all look different, and the
					// fallback that follows costs one request PER video.
					r.parseWarnings = append(r.parseWarnings,
						fmt.Sprintf("batch of %d: %v\n    reply began: %s", len(ids), perr, replyHead(reply)))
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
				stop := r.llmDown || fatal != nil
				mu.Unlock()
				if stop {
					return
				}
				v, err := askLLM(client, r.cfg, r.items[id], seeds)
				mu.Lock()
				switch {
				case err != nil && isConnErr(err):
					connLost(err)
					mu.Unlock()
					return
				case err != nil:
					r.parseWarnings = append(r.parseWarnings, fmt.Sprintf("%s: %v", id, err))
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
					skip := r.llmDown || fatal != nil
					mu.Unlock()
					if !skip {
						process(ids)
					}
					mu.Lock()
					done += len(ids)
					if r.opts.progress && (batchSize > 1 || done%25 == 0 || done == len(live)) {
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
			return fatal
		}
		if areaOverrides > 0 && r.opts.progress {
			fmt.Printf("%d of %d answers named an area other than the fixed one — the category won\n",
				areaOverrides, r.st.llmNew)
		}
	}
	if len(r.parseWarnings) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d LLM replies rejected (single-request fallback ran where possible), first %d:\n",
			len(r.parseWarnings), min(len(r.parseWarnings), 3))
		for _, w := range r.parseWarnings[:min(len(r.parseWarnings), 3)] {
			fmt.Fprintf(os.Stderr, "  %s\n", w)
		}
	}
	r.st.llmDown = r.llmDown
	return nil
}

// write is stage 4: collect what every stage decided, join it back onto the
// watch events, and rewrite classified.jsonl — plus the summary line, which
// reads the joined rows and so lives with them.
func (r *pass) write() error {
	// Whatever the LLM did not answer still keeps the area its YouTube
	// category gives it. That fact does not depend on a model being up, on
	// -no-llm or on -llm-limit — only the sub and the mode do, and the source
	// says which half is missing. Without this the whole redesign would hand
	// the area back to the LLM through the back door.
	categoryOnly := 0
	for id, item := range r.items {
		if _, done := r.verdicts[id]; done || item.Area == "" {
			continue
		}
		r.verdicts[id] = videoVerdict{topic: item.Area, source: "category"}
		categoryOnly++
	}
	if categoryOnly > 0 && r.opts.progress {
		fmt.Printf("%d videos carry their category's area but no sub or mode yet\n", categoryOnly)
	}

	r.st.classified = len(r.verdicts)

	// Join verdicts back onto every watch event.
	var out []classify.Verdict
	for _, v := range r.views {
		row := classify.Verdict{
			VideoID:   v.VideoID,
			Title:     v.Title,
			Channel:   v.Channel,
			ChannelID: takeout.ChannelIDFromURL(v.ChannelURL),
			WatchedAt: v.WatchedAt,
			Topic:     "unclear",
			Source:    "unclassified",
		}
		if m, ok := r.metas[v.VideoID]; ok {
			row.DurationS = m.Duration
			row.Unavailable = m.Unavailable
			row.GoneReason = m.GoneReason
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
			if topic, mode, ruleID, ok := r.cfg.Match(rules.Input{Title: v.Title, Channel: v.Channel}); ok {
				row.Topic, row.Mode, row.Source = topic, mode, "rule:"+ruleID
			}
		} else if vv, ok := r.verdicts[v.VideoID]; ok {
			row.Topic, row.Mode, row.Source, row.Confidence = vv.topic, vv.mode, vv.source, vv.confidence
		}
		out = append(out, row)
	}
	if err := writeJSONL(r.p.classifiedJSONL(), out); err != nil {
		return err
	}

	if r.opts.progress {
		bySource := map[string]int{}
		for _, row := range out {
			switch {
			case strings.HasPrefix(row.Source, "rule:"):
				bySource["rule"]++
			case strings.HasPrefix(row.Source, "llm:"):
				bySource["llm"]++
			case row.Source == "category":
				bySource["category"]++
			default:
				bySource["unclassified"]++
			}
		}
		fmt.Printf("wrote %s: %d views (%d via rules, %d via llm, %d area-only from the category, %d unclassified)\n",
			r.p.classifiedJSONL(), len(out), bySource["rule"], bySource["llm"], bySource["category"], bySource["unclassified"])
		if r.llmDown && bySource["unclassified"] > 0 && !r.opts.noLLM {
			fmt.Println("rerun \"classify\" once oMLX is up to fill the gap — verdicts are cached.")
		}
	}
	return nil
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

// subSeedsPerArea bounds the seed list per area: enough to cover what is
// actually watched, few enough that the prompt does not grow with the corpus.
const subSeedsPerArea = 12

// collectSubSeeds counts the subs already assigned per area and returns the
// most-used ones, most frequent first with the name as tie-break. The order
// must be deterministic: a prompt that reshuffles between runs invites the
// model to reshuffle its answers with it.
//
// Every topic passes NormalizeTopic first. The cache still holds verdicts
// from older taxonomies, and seeding their areas would put names into the
// prompt that the area list right above does not contain — the model then
// reuses them one level down, which is how "dev" (an area back then) turned
// up as a sub under music, education and science-technology alike.
func collectSubSeeds(cfg *rules.Config, topics []string) map[string][]string {
	perArea := map[string]map[string]int{}
	for _, t := range topics {
		canonical, ok := cfg.NormalizeTopic(t)
		if !ok {
			continue
		}
		area, sub := rules.SplitTopic(canonical)
		if sub == "" {
			continue
		}
		if perArea[area] == nil {
			perArea[area] = map[string]int{}
		}
		perArea[area][sub]++
	}
	seeds := make(map[string][]string, len(perArea))
	for area, subs := range perArea {
		list := counts.Keys(subs)
		seeds[area] = list[:min(len(list), subSeedsPerArea)]
	}
	return seeds
}

func askLLM(client *omlx.Client, cfg *rules.Config, item classify.Item, seeds map[string][]string) (classify.LLMVerdict, error) {
	system, user := classify.BuildPrompt(cfg, item, seeds)
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

// replyHead renders the start of an LLM reply on one line for a warning:
// enough to recognize the shape of a bad answer, short enough not to spill a
// batch of video titles into the terminal.
func replyHead(reply string) string {
	// Line breaks ARE the format here, so they are shown rather than
	// collapsed: a reply that ran all verdicts together looks exactly like a
	// well-formed one once the fields are joined, and that difference is the
	// whole diagnosis.
	head := strings.Join(strings.Fields(strings.ReplaceAll(reply, "\n", " ⏎ ")), " ")
	if len(head) > 160 {
		head = head[:160] + "…"
	}
	if head == "" {
		return "(empty)"
	}
	return head
}

func isConnErr(err error) bool {
	return strings.Contains(err.Error(), "unreachable") || strings.Contains(err.Error(), "401")
}
