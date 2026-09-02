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
	rulesPath         *string
	noLLM             *bool
	llmLimit          *int
	llmBatch          *int
	llmWorkers        *int
	keepVerdicts      *bool
	retry             *string
	includeUnenriched *bool
}

func addLLMFlags(fs *flag.FlagSet) llmFlags {
	return llmFlags{
		rulesPath:    fs.String("rules", "", "rules file (default: config/rules.yaml, falling back to config/rules.example.yaml)"),
		noLLM:        fs.Bool("no-llm", false, "skip the LLM stage, rules only"),
		llmLimit:     fs.Int("llm-limit", 0, "ask the LLM about at most N videos this run (0 = all)"),
		llmBatch:     fs.Int("llm-batch", 10, "videos per LLM request (1 = one request per video)"),
		llmWorkers:   fs.Int("llm-workers", 1, "parallel LLM requests (raise only if the server actually decodes concurrently)"),
		keepVerdicts: fs.Bool("keep-verdicts", false, "keep cached verdicts even though the taxonomy changed (for a reworded desc — a changed area list needs a re-ask)"),
		retry: fs.String("retry", "",
			`re-ask cached verdicts by DEFECT: "no-sub" (area without a sub), "no-mode" (topic without a mode), "unclear" (only the ones with usable text), "topic:<exact>" (a topic that is present but wrong), "all", or a comma-separated list`),
		includeUnenriched: fs.Bool("include-unenriched", false, "ask the LLM even about videos without cached metadata (title-only verdicts)"),
	}
}

// opts deliberately leaves includeUnenriched alone. "classify" means it for
// the one pass it runs; "run" means it only for the final sweep, after enrich
// has had its chance — applying it wave after wave would burn title-only asks
// on videos whose metadata is still on its way.
func (lf llmFlags) opts() classifyOpts {
	return classifyOpts{
		noLLM:        *lf.noLLM,
		llmLimit:     *lf.llmLimit,
		llmBatch:     *lf.llmBatch,
		llmWorkers:   *lf.llmWorkers,
		keepVerdicts: *lf.keepVerdicts,
		retry:        *lf.retry,
	}
}

func cmdClassify(args []string) error {
	fs, dataDir := newFlagSet("classify")
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
	// Said once per command, not per pass: "run" classifies in waves and this
	// would otherwise repeat every 60 seconds for hours.
	if !*lf.noLLM {
		if line := modelDriftLine(modelDrift(cached, cfg.LLM.Model), cfg.LLM.Model); line != "" {
			fmt.Fprintln(os.Stderr, line)
		}
	}

	opts := lf.opts()
	opts.includeUnenriched = *lf.includeUnenriched
	opts.progress = true
	st, err := classifyPass(p, cfg, views, metas, cached, opts)
	if err != nil {
		return err
	}
	fmt.Println(st.nextLine())
	return nil
}

// classifyOpts configures one classifyPass; "run" reuses it wave after wave.
type classifyOpts struct {
	noLLM             bool
	llmLimit          int    // max live LLM asks this pass (0 = all)
	llmBatch          int    // videos per LLM request (<=1 = single requests)
	llmWorkers        int    // parallel LLM requests
	includeUnenriched bool   // ask title-only even without a meta cache entry
	keepVerdicts      bool   // do not re-ask verdicts just because the taxonomy changed
	retry             string // -retry: targeted re-asks by defect, see retryTargets
	progress          bool   // per-stage prints (off in wave mode — run prints wave lines)

	// newClient replaces the oMLX constructor. nil means omlx.New; tests set
	// it to drive the live path without a server — httptest cannot bind in
	// the sandbox, so a fake http.RoundTripper is the only mock that works.
	newClient func(model, baseURL string) *omlx.Client
}

func (o classifyOpts) client(model, baseURL string) *omlx.Client {
	if o.newClient != nil {
		return o.newClient(model, baseURL)
	}
	return omlx.New(model, baseURL)
}

// passStats sums up one classifyPass for the wave line.
type passStats struct {
	unique     int // unique videos with an id
	classified int // unique videos with a verdict (rules + llm)
	ruleHits   int
	llmNew     int // live LLM verdicts gained this pass (full asks)
	llmSub     int // sub answers merged in by a "-retry no-sub" round
	llmMode    int // mode answers merged in by a "-retry no-mode" round
	waiting    int // unenriched videos skipped until enrich delivers metadata
	llmDown    bool

	// What is left to re-ask, counted the way retryTargets selects: only
	// verdicts a model answered, only defects no marker says were already
	// asked once. A number that merely looked plausible would send someone
	// on a five-hour run for nothing.
	noSub        int // an area, no sub — what "-retry no-sub" would pick up
	noMode       int // a topic, no mode — what "-retry no-mode" would pick up
	categoryOnly int // area from the YouTube category alone; no model has seen them
}

// nextLine says what is left and with which selector. Clauses appear only
// when they have a count; the tail always does, because the taxonomy and the
// page are the next stages whether or not anything is missing.
//
// It is a pure function of the counters on purpose: the numbers it prints are
// the ones the pass measured, so a test can hold the sentence against them
// without running a pass.
func (s passStats) nextLine() string {
	var parts []string
	if s.noSub > 0 {
		parts = append(parts, fmt.Sprintf("%d with an area but no sub — classify -retry no-sub", s.noSub))
	}
	if s.noMode > 0 {
		parts = append(parts, fmt.Sprintf("%d without a mode — classify -retry no-mode", s.noMode))
	}
	if s.categoryOnly > 0 {
		parts = append(parts, fmt.Sprintf("%d carry only their category's area (they are waiting for metadata — enrich, or classify -include-unenriched)", s.categoryOnly))
	}
	parts = append(parts, `then "taxonomy", then "watchpath -taxonomy"`)
	return "next: " + strings.Join(parts, "; ")
}

// modelDrift counts the cached verdicts each OTHER model produced.
//
// A verdict's cache key carries the taxonomy fingerprint, not the model. Point
// the config at a different model and nothing is invalidated: the old judge's
// verdicts stay, the new one answers only what is new, and the corpus becomes
// two judges' work with no field able to say which is which. That is not a bug
// to fix by re-asking — 28k videos is five hours — but it must not be silent.
//
// Verdicts with no model recorded are skipped: they predate the field, and
// counting them would put a permanent number under a warning about a change
// nobody made.
func modelDrift(cached map[string]classify.LLMVerdict, want string) map[string][]string {
	out := map[string][]string{}
	for id, v := range cached {
		if v.Model == "" || v.Model == want {
			continue
		}
		out[v.Model] = append(out[v.Model], id)
	}
	for _, ids := range out {
		// Map order is random and this line ends up in a terminal people
		// compare between runs.
		sort.Strings(ids)
	}
	return out
}

// driftNameLimit is where naming the strays stops helping. Below it the ID is
// the whole point — it says which cache file to delete, which a count cannot.
// Above it the list is a wall of IDs nobody acts on and the count is the
// useful half.
const driftNameLimit = 10

// modelDriftLine names the way out that does not cost five hours. Not
// "-retry all": that re-asks defects, and these verdicts have none — they are
// answers from a different judge, which is a different thing entirely.
//
// IDs, never titles: the rest of this output is aggregate-free, and a title is
// not an aggregate.
func modelDriftLine(drift map[string][]string, want string) string {
	if len(drift) == 0 {
		return ""
	}
	byModel := make(map[string]int, len(drift))
	total := 0
	for model, ids := range drift {
		byModel[model] = len(ids)
		total += len(ids)
	}
	line := fmt.Sprintf("model changed (%s → %s): those verdicts keep the old judge and nothing is re-asked — \"abtest -model %s\" measures the difference without touching the cache",
		formatCounts(byModel), want, want)
	if total > driftNameLimit {
		return line
	}
	named := make([]string, 0, len(drift))
	for _, model := range counts.Keys(byModel) {
		named = append(named, model+": "+strings.Join(drift[model], " "))
	}
	return line + " (" + strings.Join(named, " · ") + ")"
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
		taxonomy:     cfg.Fingerprint(),
		items:        map[string]classify.Item{},
		verdicts:     map[string]videoVerdict{},
		llmDown:      opts.noLLM,
		retryContext: map[string]bool{},
		retryTopic:   map[string]string{},
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
	retryContext  map[string]bool   // "-retry unclear" ids: get channel context, keep their marker on overwrite
	retryTopic    map[string]string // "-retry topic:<t>" ids -> the topic that selected them, for the marker
	retryRefused  int               // unclear ids -retry refused for carrying no signal (URL-only, no channel)
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

// liveSet is what stage 2 selects for the LLM, split by the question each id
// needs answered. full is the stale/uncached path plus the "unclear" retry
// (a full re-ask, with channel context); sub and mode are the targeted
// rounds that patch ONE field of an otherwise good verdict.
type liveSet struct {
	full, sub, mode []string
}

func (s liveSet) count() int { return len(s.full) + len(s.sub) + len(s.mode) }

// hasUsableText is the honesty gate of "-retry unclear": a tombstoned video
// whose Takeout "title" is just its own URL and whose channel line Takeout
// never wrote carries no signal at all — 3168 of the 3254 unclear videos,
// measured 2026-08-31. Re-asking them cannot produce an answer, only a
// guess (session-neighbour voting was measured at 35.5 % area accuracy
// against a 24.4 % constant baseline), so the retry refuses them and counts
// the refusal out loud instead of manufacturing verdicts.
func hasUsableText(item classify.Item) bool {
	return item.Channel != "" || !strings.HasPrefix(item.Title, "http")
}

// appendRetried adds kind to the retry markers, once.
func appendRetried(old []string, kind string) []string {
	if slices.Contains(old, kind) {
		return old
	}
	return append(slices.Clone(old), kind)
}

// retryTopicPrefix marks the one -retry selector that carries a value. The
// others test a FIELD (missing sub, missing mode, unclear topic); a topic
// that is present but WRONG is not something a field test can see, so it has
// to be named: -retry topic:<area>/<sub>. The case it was built for is a sub
// that grew into a catch-all across unrelated channels — a name that means
// nothing, on videos that each have a real subject.
const retryTopicPrefix = "topic:"

// retryTopics lists the exact topics named by "topic:<t>" parts of -retry.
// Compared against the canonical topic, i.e. the string the report shows.
func (r *pass) retryTopics() []string {
	var out []string
	for _, part := range strings.Split(r.opts.retry, ",") {
		if p := strings.TrimSpace(part); strings.HasPrefix(p, retryTopicPrefix) {
			out = append(out, strings.TrimPrefix(p, retryTopicPrefix))
		}
	}
	return out
}

// retryTargets matches a cached verdict against the -retry selector by
// DEFECT, never by age: the fingerprint is untouched, nothing that already
// answers cleanly is re-asked, and each defect is asked at most once
// (Retried). topic is the canonical topic after the area override — the
// same string the report shows, so "no-sub" counts what the reader sees.
// A video can be both sub and mode target (134 are); the sub round runs
// first so the mode round sees the fresh sub.
func (r *pass) retryTargets(id string, v classify.LLMVerdict, topic string, usable bool) (sub, mode, full bool) {
	want := func(k string) bool {
		for _, part := range strings.Split(r.opts.retry, ",") {
			if p := strings.TrimSpace(part); p == "all" || p == k {
				return true
			}
		}
		return false
	}
	if !usable {
		return false, false, false
	}
	if topic == "unclear" {
		if want("unclear") && !slices.Contains(v.Retried, "context") {
			if hasUsableText(r.items[id]) {
				full = true
				r.retryContext[id] = true
			} else {
				r.retryRefused++
			}
		}
		return false, false, full
	}
	// A named topic is re-asked in FULL and wins over the field selectors:
	// the full round answers sub and mode anyway, so adding a targeted round
	// on top would pay twice for one video. What makes the re-ask more than
	// a paid no-op is on the prompt side — the named topic is dropped from
	// the sub seeds (see askLive), because at temperature 0 the same prompt
	// returns the same answer, and a catch-all sub offered back as a seed IS
	// the prompt that grew it. That seed removal is what deleting the verdict
	// files by hand used to achieve, and why this selector is worth its code.
	if slices.Contains(r.retryTopics(), topic) && !slices.Contains(v.Retried, retryTopicPrefix+topic) {
		r.retryTopic[id] = topic
		return false, false, true
	}
	if want("no-sub") && !strings.Contains(topic, "/") && !slices.Contains(v.Retried, "sub") {
		sub = true
	}
	if want("no-mode") && v.Mode == "" && !slices.Contains(v.Retried, "mode") {
		mode = true
	}
	return sub, mode, false
}

// resolveCached is stage 2 — cached LLM verdicts first, then select what to
// ask live. A stale title-only verdict stays in place as a fallback until its
// re-ask (with full metadata) lands, so an oMLX outage never loses coverage.
func (r *pass) resolveCached(needLLM []string) (live liveSet) {
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
				live.full = append(live.full, id)
			} else if r.opts.retry != "" {
				sub, mode, full := r.retryTargets(id, v, topic, usable)
				if sub {
					live.sub = append(live.sub, id)
				}
				if mode {
					live.mode = append(live.mode, id)
				}
				if full {
					live.full = append(live.full, id)
				}
			}
			continue
		}
		if hasMeta || r.opts.includeUnenriched {
			live.full = append(live.full, id)
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
	// Like the taxonomy line: what a retry selected (and what it refused to
	// select) is printed whether or not -progress is on.
	if r.opts.retry != "" {
		fmt.Printf("-retry %q: %d sub, %d mode, %d full re-asks", r.opts.retry,
			len(live.sub), len(live.mode), len(r.retryContext)+len(r.retryTopic))
		if r.retryRefused > 0 {
			fmt.Printf("; %d carry nothing but their own URL and no channel — not asked", r.retryRefused)
		}
		fmt.Println()
	}
	if r.opts.progress {
		fmt.Printf("LLM: %d cached verdicts, %d to ask, %d waiting for enrich\n",
			cachedHits, live.count(), r.st.waiting)
	}
	return live
}

// lostServer flips llmDown, warning once. The full round calls it under its
// mutex; the sequential retry rounds run before the pool and need no lock.
func (r *pass) lostServer(err error) {
	if !r.llmDown {
		fmt.Fprintf(os.Stderr, "warning: %v\nwarning: skipping the LLM for the rest of this pass — verdicts so far are cached\n", err)
	}
	r.llmDown = true
}

// fillChannelContext builds the channel prior for the unclear retry: the
// top-3 topics OTHER videos of the same channel already carry. Built after
// resolveCached so the counts see every cached verdict, and written only
// onto the retry set — the normal prompt must stay byte-identical (see
// classify.Item.Context).
func (r *pass) fillChannelContext() {
	byChan := map[string]map[string]int{}
	for id, vv := range r.verdicts {
		if vv.topic == "" || vv.topic == "unclear" || r.retryContext[id] {
			continue
		}
		ch := r.items[id].Channel
		if ch == "" {
			continue
		}
		if byChan[ch] == nil {
			byChan[ch] = map[string]int{}
		}
		byChan[ch][vv.topic]++
	}
	for id := range r.retryContext {
		item := r.items[id]
		if m := byChan[item.Channel]; item.Channel != "" && len(m) > 0 {
			keys := counts.Keys(m)
			item.Context = keys[:min(len(keys), 3)]
			r.items[id] = item
		}
	}
}

// askLive is stage 3: the live LLM rounds — the targeted sub and mode
// retries first, then the full round with its batches, parsing and the
// verified single-request fallback. It returns an error only for a broken
// verdict cache; a lost server just flips llmDown and leaves the rest of
// the pass to run on what it has.
func (r *pass) askLive(set liveSet) error {
	// -llm-limit caps the pass across ALL rounds, in round order
	// sub → mode → full — so a small limit is a meaningful smoke test of
	// whichever round runs first.
	if r.opts.llmLimit > 0 && set.count() > r.opts.llmLimit {
		budget := r.opts.llmLimit
		take := func(ids []string) []string {
			n := min(len(ids), budget)
			budget -= n
			return ids[:n]
		}
		set.sub, set.mode, set.full = take(set.sub), take(set.mode), take(set.full)
		if r.opts.progress {
			fmt.Printf("limiting LLM calls to %d this run\n", set.count())
		}
	}

	if !r.llmDown && set.count() > 0 {
		llmCache := classify.Cache{Dir: r.p.classifyCache()}
		client := r.opts.client(r.cfg.LLM.Model, r.cfg.LLM.BaseURL)
		// Discovery doubles as health check: bail out early with the real
		// model list instead of failing per-video.
		models, err := client.Models()
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "warning: %v\nwarning: skipping the LLM this pass — %d videos wait for the next one\n", err, set.count())
			r.llmDown = true
		case !slices.Contains(models, client.Model):
			fmt.Fprintf(os.Stderr, "warning: model %q not on the oMLX server (available: %s)\nwarning: skipping the LLM this pass — %d videos wait for the next one\n",
				client.Model, strings.Join(models, ", "), set.count())
			r.llmDown = true
		}
		if !r.llmDown && r.opts.progress {
			fmt.Printf("asking %s (model %s)\n", client.BaseURL, client.Model)
		}

		seeds := r.subSeeds()

		if len(r.retryContext) > 0 {
			r.fillChannelContext()
		}

		if err := r.askSubs(client, llmCache, set.sub, seeds); err != nil {
			return err
		}
		if err := r.askModes(client, llmCache, set.mode); err != nil {
			return err
		}
		if err := r.askFull(client, llmCache, set.full, seeds); err != nil {
			return err
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

// minSubConfidence drops hesitant subs from the retry: a doubtful sub on a
// settled area is worse than none, because the bare area is already true.
const minSubConfidence = 0.4

// askSubs is the no-sub retry round: verdicts whose area is settled but
// whose sub the full prompt left optional. Grouped by area so every batch
// carries only that area's seeds (see BuildSubPrompt). The merge touches
// Topic and Confidence only — Mode, Basis, Model and Taxonomy stay exactly
// what the original verdict said, because this round did not re-judge them.
// subSeeds is what the model gets to reuse: every sub already assigned, by
// rules and by cached verdicts alike. Computed once, before any round — seeds
// that shift mid-run would make the result depend on batch order.
//
// A method rather than eight lines inside askLive because "abtest" has to
// build the SAME prompt the production pass builds; a second copy of this
// would drift and quietly turn a model comparison into a prompt comparison.
func (r *pass) subSeeds() map[string][]string {
	topicsSoFar := make([]string, 0, len(r.verdicts)+len(r.cached))
	for _, v := range r.verdicts {
		topicsSoFar = append(topicsSoFar, v.topic)
	}
	for _, v := range r.cached {
		topicsSoFar = append(topicsSoFar, v.Topic)
	}
	return collectSubSeeds(r.cfg, topicsSoFar, r.retryTopics())
}

func (r *pass) askSubs(client *omlx.Client, llmCache classify.Cache, ids []string, seeds map[string][]string) error {
	if len(ids) == 0 || r.llmDown {
		return nil
	}
	got, dropped, blank := 0, 0, 0
	apply := func(id string, a classify.SubAnswer) error {
		next := r.cached[id]
		next.Retried = appendRetried(next.Retried, "sub")
		switch {
		case a.Sub == "":
			blank++
		case a.Confidence < minSubConfidence:
			dropped++
		default:
			next.Topic = r.verdicts[id].topic + "/" + a.Sub
			next.Confidence = a.Confidence
			got++
			r.st.llmSub++
		}
		if err := llmCache.Write(id, next); err != nil {
			return err
		}
		r.cached[id] = next
		if a.Sub != "" && a.Confidence >= minSubConfidence {
			vv := r.verdicts[id]
			vv.topic, vv.confidence = next.Topic, next.Confidence
			r.verdicts[id] = vv
		}
		return nil
	}
	askOne := func(id, area string) error {
		system, user := classify.BuildSubPrompt(area, seeds[area], []classify.Item{r.items[id]})
		reply, err := client.ChatMax(system, user, 124)
		if err != nil {
			if isConnErr(err) {
				r.lostServer(err)
				return nil
			}
			r.parseWarnings = append(r.parseWarnings, fmt.Sprintf("%s: %v", id, err))
			return nil
		}
		answers, err := classify.ParseBatchSubs(r.cfg, area, []string{id}, reply)
		if err != nil {
			r.parseWarnings = append(r.parseWarnings, fmt.Sprintf("%s: %v", id, err))
			return nil
		}
		return apply(id, answers[id])
	}

	byArea := map[string][]string{}
	for _, id := range ids {
		area := r.verdicts[id].topic
		byArea[area] = append(byArea[area], id)
	}
	areas := make([]string, 0, len(byArea))
	for a := range byArea {
		areas = append(areas, a)
	}
	sort.Strings(areas)
	batchSize := max(r.opts.llmBatch, 1)
	for _, area := range areas {
		list := byArea[area]
		for off := 0; off < len(list); off += batchSize {
			if r.llmDown {
				return nil
			}
			chunk := list[off:min(off+batchSize, len(list))]
			if len(chunk) == 1 {
				if err := askOne(chunk[0], area); err != nil {
					return err
				}
				continue
			}
			items := make([]classify.Item, len(chunk))
			for i, id := range chunk {
				items[i] = r.items[id]
			}
			system, user := classify.BuildSubPrompt(area, seeds[area], items)
			reply, err := client.ChatMax(system, user, 24*len(chunk)+100)
			var answers map[string]classify.SubAnswer
			if err == nil {
				answers, err = classify.ParseBatchSubs(r.cfg, area, chunk, reply)
			}
			if err != nil {
				if isConnErr(err) {
					r.lostServer(err)
					return nil
				}
				r.parseWarnings = append(r.parseWarnings,
					fmt.Sprintf("sub batch of %d: %v\n    reply began: %s", len(chunk), err, replyHead(reply)))
				for _, id := range chunk {
					if r.llmDown {
						return nil
					}
					if err := askOne(id, area); err != nil {
						return err
					}
				}
				continue
			}
			for _, id := range chunk {
				if err := apply(id, answers[id]); err != nil {
					return err
				}
			}
		}
	}
	fmt.Printf("sub retry: %d asked, %d got a sub, %d dropped below %.1f, %d answered \"?\"\n",
		len(ids), got, dropped, minSubConfidence, blank)
	return nil
}

// askModes is the no-mode retry round: verdicts whose topic is settled but
// whose mode a batch reply left out. The merge touches Mode only. An
// "unclear" answer leaves the mode empty and still marks the retry — the
// model saying "cannot tell" twice is an answer, not an error.
func (r *pass) askModes(client *omlx.Client, llmCache classify.Cache, ids []string) error {
	if len(ids) == 0 || r.llmDown {
		return nil
	}
	got, still := 0, 0
	apply := func(id, mode string) error {
		next := r.cached[id]
		next.Retried = appendRetried(next.Retried, "mode")
		if mode != "" {
			next.Mode = mode
			got++
			r.st.llmMode++
		} else {
			still++
		}
		if err := llmCache.Write(id, next); err != nil {
			return err
		}
		r.cached[id] = next
		if mode != "" {
			vv := r.verdicts[id]
			vv.mode = mode
			r.verdicts[id] = vv
		}
		return nil
	}
	buildFor := func(chunk []string) (system, user string) {
		items := make([]classify.Item, len(chunk))
		topics := make([]string, len(chunk))
		for i, id := range chunk {
			items[i] = r.items[id]
			topics[i] = r.verdicts[id].topic
		}
		return classify.BuildModePrompt(items, topics)
	}
	askOne := func(id string) error {
		system, user := buildFor([]string{id})
		reply, err := client.ChatMax(system, user, 58)
		if err != nil {
			if isConnErr(err) {
				r.lostServer(err)
				return nil
			}
			r.parseWarnings = append(r.parseWarnings, fmt.Sprintf("%s: %v", id, err))
			return nil
		}
		modes, err := classify.ParseBatchModes([]string{id}, reply)
		if err != nil {
			r.parseWarnings = append(r.parseWarnings, fmt.Sprintf("%s: %v", id, err))
			return nil
		}
		return apply(id, modes[id])
	}

	batchSize := max(r.opts.llmBatch, 1)
	for off := 0; off < len(ids); off += batchSize {
		if r.llmDown {
			return nil
		}
		chunk := ids[off:min(off+batchSize, len(ids))]
		if len(chunk) == 1 {
			if err := askOne(chunk[0]); err != nil {
				return err
			}
			continue
		}
		system, user := buildFor(chunk)
		reply, err := client.ChatMax(system, user, 8*len(chunk)+50)
		var modes map[string]string
		if err == nil {
			modes, err = classify.ParseBatchModes(chunk, reply)
		}
		if err != nil {
			if isConnErr(err) {
				r.lostServer(err)
				return nil
			}
			r.parseWarnings = append(r.parseWarnings,
				fmt.Sprintf("mode batch of %d: %v\n    reply began: %s", len(chunk), err, replyHead(reply)))
			for _, id := range chunk {
				if r.llmDown {
					return nil
				}
				if err := askOne(id); err != nil {
					return err
				}
			}
			continue
		}
		for _, id := range chunk {
			if err := apply(id, modes[id]); err != nil {
				return err
			}
		}
	}
	fmt.Printf("mode retry: %d asked, %d got a mode, %d still cannot tell\n", len(ids), got, still)
	return nil
}

// askFull is the full round: complete verdicts for the stale, the uncached
// and the unclear-with-context set, batched with the verified per-video
// fallback.
func (r *pass) askFull(client *omlx.Client, llmCache classify.Cache, live []string, seeds map[string][]string) error {
	if len(live) == 0 || r.llmDown {
		return nil
	}
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
		r.lostServer(err)
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
		if r.retryContext[id] {
			// The retry marker survives the overwrite: even an answer that
			// is unclear AGAIN records that the context question was asked,
			// or the next -retry unclear pays for it anew.
			v.Retried = appendRetried(r.cached[id].Retried, "context")
		} else if t, ok := r.retryTopic[id]; ok {
			// Same for a named topic, and it matters more here: the model
			// may well answer the old topic again, and that answer is the
			// model's, not a cache leftover. The marker records it, so the
			// same selector cannot buy the same requests twice.
			v.Retried = appendRetried(r.cached[id].Retried, retryTopicPrefix+t)
		}
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
	r.st.categoryOnly = categoryOnly

	// Read-only, and deliberately here rather than in the retry stage: this
	// counts what the NEXT run would select, over the verdicts about to be
	// written. The predicate is retryTargets' own, minus the selector — a
	// model's verdict, a defect, and no marker saying that defect was already
	// asked once. Anything looser would name a number no -retry run can meet.
	for id, vv := range r.verdicts {
		if !strings.HasPrefix(vv.source, "llm:") || vv.topic == "unclear" {
			continue
		}
		cv := r.cached[id]
		if !strings.Contains(vv.topic, "/") && !slices.Contains(cv.Retried, "sub") {
			r.st.noSub++
		}
		if vv.mode == "" && !slices.Contains(cv.Retried, "mode") {
			r.st.noMode++
		}
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
//
// drop is what "-retry topic:<t>" is re-asking: those verdicts must not seed
// the prompt that is meant to replace them. Filtered here rather than at the
// call site because the comparison only means anything after NormalizeTopic,
// which is this function's job.
func collectSubSeeds(cfg *rules.Config, topics, drop []string) map[string][]string {
	perArea := map[string]map[string]int{}
	for _, t := range topics {
		canonical, ok := cfg.NormalizeTopic(t)
		if !ok || slices.Contains(drop, canonical) {
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
