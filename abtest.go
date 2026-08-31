// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/omlx"
	"github.com/bmmmm/youtubehistii/internal/rules"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

// cmdABTest answers one question and refuses every other: would a DIFFERENT
// model on the oMLX server classify this corpus better than the one the
// verdicts were made with?
//
// The question is forced by a property of the cache: a verdict's key carries
// the taxonomy fingerprint, NOT the model (see rules.Config.Fingerprint,
// which hashes only Topics[].ID/Desc). Pointing the pipeline at another model
// therefore invalidates nothing — it silently mixes two judges into one
// corpus, and the only consistent way back is re-asking all ~28k videos, five
// hours at the measured rate. This command measures whether that is worth
// paying before it is paid.
//
// It writes nothing: no verdict cache, no classified.jsonl, no taxonomy. The
// sample is deterministic (ids sorted, fixed stride), so two invocations on
// unchanged data compare the same videos and the numbers are comparable
// across days and across candidate models.
func cmdABTest(args []string) error {
	fs, dataDir := newFlagSet("abtest")
	rulesPath := fs.String("rules", "", "rules file (default: config/rules.yaml, falling back to config/rules.example.yaml)")
	modelB := fs.String("model", "", "the candidate model on the oMLX server (required)")
	modelA := fs.String("baseline", "", "the incumbent model (default: the one in the rules file)")
	judge := fs.String("judge", "", "referee model that decides the disagreements (empty = report the disagreement rate and stop)")
	n := fs.Int("n", 200, "videos in the sample")
	batch := fs.Int("batch", 20, "videos per request; the same for both models, because batch size changes answers")
	fs.Parse(args)
	if *modelB == "" {
		return fmt.Errorf("-model is required: name the candidate, e.g. -model Qwen3.8-27B-4bit")
	}
	p := paths{dataDir: *dataDir}

	cfg, err := loadRules(*rulesPath)
	if err != nil {
		return err
	}
	if *modelA == "" {
		*modelA = cfg.LLM.Model
	}
	if *modelA == *modelB {
		return fmt.Errorf("baseline and candidate are the same model (%q) — nothing to compare", *modelA)
	}

	// Loaded exactly like cmdClassify, so the items carry the same title,
	// channel, fixed area and creator tags the production prompt shows. An
	// external tool could not do this: the tags live in the meta cache, and a
	// prompt missing them is not the prompt the corpus was built with.
	views, err := readJSONL[takeout.View](p.historyJSONL())
	if err != nil {
		return fmt.Errorf("read history (run \"import\" first): %w", err)
	}
	metas, err := enrich.Cache{Dir: p.metaCacheDir()}.ReadAll()
	if err != nil {
		return err
	}
	cached, err := loadNewCacheEntries[classify.LLMVerdict](p.classifyCache(), map[string]bool{})
	if err != nil {
		return err
	}

	// noLLM keeps the pass off the network: matchRules fills r.items, and
	// nothing here ever calls askLive or write.
	r := &pass{
		p: p, cfg: cfg, views: views, metas: metas, cached: cached,
		opts:         classifyOpts{noLLM: true},
		taxonomy:     cfg.Fingerprint(),
		items:        map[string]classify.Item{},
		verdicts:     map[string]videoVerdict{},
		llmDown:      true,
		retryContext: map[string]bool{},
		retryTopic:   map[string]string{},
	}
	needLLM := r.matchRules()
	r.resolveCached(needLLM)

	sample := abSample(needLLM, r.items, *n)
	if len(sample) == 0 {
		return fmt.Errorf("no sampled videos carry metadata — run \"enrich\" first")
	}
	seeds := r.subSeeds()

	clientFor := func(model string) (*omlx.Client, error) {
		c := r.opts.client(model, cfg.LLM.BaseURL)
		models, err := c.Models()
		if err != nil {
			return nil, err
		}
		// Fail here, not per batch: a typo in a model name is worth one
		// request, not a half-finished comparison.
		if !slices.Contains(models, model) {
			return nil, fmt.Errorf("model %q is not on the server (available: %s)", model, strings.Join(models, ", "))
		}
		return c, nil
	}
	ca, err := clientFor(*modelA)
	if err != nil {
		return err
	}
	cb, err := clientFor(*modelB)
	if err != nil {
		return err
	}

	fmt.Printf("A/B on %d videos, batch %d, identical prompts\n  A (baseline)  %s\n  B (candidate) %s\n",
		len(sample), *batch, *modelA, *modelB)

	va, statA := abAsk(ca, cfg, r.items, sample, seeds, *batch)
	vb, statB := abAsk(cb, cfg, r.items, sample, seeds, *batch)
	fmt.Printf("\n%-14s %8s %8s\n", "", "A", "B")
	fmt.Printf("%-14s %8d %8d\n", "answered", len(va), len(vb))
	fmt.Printf("%-14s %8d %8d   (batches the parser rejected)\n", "rejected", statA.rejected, statB.rejected)
	fmt.Printf("%-14s %7.1fs %7.1fs   (warm, the sample only)\n", "wall clock", statA.elapsed.Seconds(), statB.elapsed.Seconds())
	fmt.Printf("%-14s %7.1fs %7.1fs   (first call — model load, excluded above)\n", "warm-up", statA.load.Seconds(), statB.load.Seconds())
	fmt.Printf("%-14s %7.0fms %7.0fms   (per video, warm)\n", "per video",
		float64(statA.elapsed.Milliseconds())/float64(max(len(sample), 1)),
		float64(statB.elapsed.Milliseconds())/float64(max(len(sample), 1)))

	// Agreement is NOT quality — two models can agree on the same mistake,
	// and a disagreement says only that one of them is wrong. It is reported
	// because it sizes the referee's work, and because near-total agreement
	// is itself the answer: a candidate that says what the incumbent says
	// cannot be worth five hours.
	var disagree []string
	sameTopic, sameArea, sameMode := 0, 0, 0
	for _, id := range sample {
		a, oka := va[id]
		b, okb := vb[id]
		if !oka || !okb {
			continue
		}
		if a.Topic == b.Topic {
			sameTopic++
		} else {
			disagree = append(disagree, id)
		}
		if abArea(a.Topic) == abArea(b.Topic) {
			sameArea++
		}
		if a.Mode == b.Mode {
			sameMode++
		}
	}
	both := len(disagree) + sameTopic
	if both == 0 {
		return fmt.Errorf("no video got an answer from both models — nothing to compare")
	}
	fmt.Printf("\nboth answered %d videos: topic identical %d (%.0f %%), area identical %d (%.0f %%), mode identical %d (%.0f %%)\n",
		both, sameTopic, pct(sameTopic, both), sameArea, pct(sameArea, both), sameMode, pct(sameMode, both))

	if *judge == "" {
		fmt.Printf("%d disagreements — pass -judge <model> to have a third model decide them\n", len(disagree))
		return nil
	}
	if len(disagree) == 0 {
		fmt.Println("no disagreements to judge")
		return nil
	}
	cj, err := clientFor(*judge)
	if err != nil {
		return err
	}
	wins := abJudge(cj, r.items, va, vb, disagree, *batch)
	fmt.Printf("\nreferee %s on %d disagreements: A %d, B %d, neither %d, unparsed %d\n",
		*judge, len(disagree), wins.a, wins.b, wins.neither, wins.unparsed)
	// The verdict the caller came for, stated as a rule and not as a vibe:
	// the candidate has to WIN the disagreements, not merely differ.
	decided := wins.a + wins.b
	switch {
	case decided == 0:
		fmt.Println("verdict: the referee decided nothing — the comparison is inconclusive")
	case wins.b > wins.a:
		fmt.Printf("verdict: the candidate wins %.0f %% of the decided disagreements (%d of %d)\n",
			pct(wins.b, decided), wins.b, decided)
	default:
		fmt.Printf("verdict: the candidate does NOT win (%d of %d decided) — the re-run is not worth its five hours\n",
			wins.b, decided)
	}
	return nil
}

// abSample picks the videos to compare: ids sorted, then a fixed stride, so
// the set is stable across invocations and spread over the whole corpus
// instead of clustering in whatever order the history happened to have.
// Videos without metadata are skipped — a title-only prompt measures the
// tombstone problem, not the model.
func abSample(needLLM []string, items map[string]classify.Item, n int) []string {
	var usable []string
	for _, id := range needLLM {
		if it, ok := items[id]; ok && it.Title != "" {
			usable = append(usable, id)
		}
	}
	sort.Strings(usable)
	if n <= 0 || n >= len(usable) {
		return usable
	}
	stride := float64(len(usable)) / float64(n)
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, usable[int(float64(i)*stride)])
	}
	return out
}

type abStat struct {
	rejected int
	load     time.Duration // first call, i.e. what the model cost to become resident
	elapsed  time.Duration // the sample itself, warm
}

// abAsk runs one model over the sample. Both models are driven through this
// same function with the same batch size and the same item order: batch size
// and neighbours change a batched answer, so anything that differs between
// the two runs other than the model name would be measuring itself.
func abAsk(client *omlx.Client, cfg *rules.Config, items map[string]classify.Item,
	sample []string, seeds map[string][]string, batch int) (map[string]classify.LLMVerdict, abStat) {

	out := make(map[string]classify.LLMVerdict, len(sample))
	var st abStat

	// Warm the model before the clock starts. oMLX loads a model on first
	// use, and the load is charged to whichever request happens to be first:
	// measured here at 10.3s vs 60.8s for two models whose real difference
	// was a fraction of that, purely because one was already resident from an
	// earlier run. A warm-up buys nothing in the pipeline (the load is paid
	// either way) but it is the whole difference between an honest and a
	// dishonest latency number in a COMPARISON, so it lives here and not
	// there.
	loadStart := time.Now()
	if _, err := client.ChatMax("Reply with the single word: ok", "ok?", 8); err != nil {
		fmt.Fprintf(os.Stderr, "  %s: warm-up failed: %v\n", client.Model, err)
	}
	st.load = time.Since(loadStart)

	start := time.Now()
	for off := 0; off < len(sample); off += batch {
		end := min(off+batch, len(sample))
		ids := sample[off:end]
		batchItems := make([]classify.Item, 0, len(ids))
		for _, id := range ids {
			batchItems = append(batchItems, items[id])
		}
		system, user := classify.BuildBatchPrompt(cfg, batchItems, seeds)
		reply, err := client.Chat(system, user)
		if err != nil {
			st.rejected++
			fmt.Fprintf(os.Stderr, "  %s: request failed: %v\n", client.Model, err)
			continue
		}
		got, err := classify.ParseBatchVerdicts(cfg, ids, reply)
		if err != nil {
			st.rejected++
			fmt.Fprintf(os.Stderr, "  %s: batch rejected: %v\n", client.Model, err)
			continue
		}
		for id, v := range got {
			out[id] = v
		}
		fmt.Printf("  %s %d/%d\n", client.Model, end, len(sample))
	}
	st.elapsed = time.Since(start)
	return out, st
}

type abWins struct{ a, b, neither, unparsed int }

// abJudge lets a third model decide the disagreements. Two things make this
// more than a coin toss:
//
//   - Position bias is cancelled, not hoped away. The referee sees the two
//     candidate topics in an order that flips with the item's index, and the
//     answer is mapped back afterwards. A referee that simply prefers the
//     first option scores 50/50 instead of handing the win to whichever model
//     was passed first.
//   - The reply is constrained to one digit per line, which is the shape this
//     server answers reliably in batches; unconstrained free text degrades
//     when batched (measured on the naming prompts, hence -name-batch 1).
func abJudge(client *omlx.Client, items map[string]classify.Item,
	va, vb map[string]classify.LLMVerdict, disagree []string, batch int) abWins {

	var w abWins
	for off := 0; off < len(disagree); off += batch {
		end := min(off+batch, len(disagree))
		ids := disagree[off:end]

		var b strings.Builder
		b.WriteString("You decide which of two labels fits a YouTube video better.\n")
		fmt.Fprintf(&b, "You get %d numbered videos, each with two candidate labels.\n", len(ids))
		b.WriteString("Reply with EXACTLY one line per video, in the same order:\n")
		b.WriteString("<n> <1|2|0>\n")
		b.WriteString("  1 = the first label fits better\n")
		b.WriteString("  2 = the second label fits better\n")
		b.WriteString("  0 = neither is defensible\n")
		b.WriteString("No prose, no code fences, no JSON.\nExample: 2 1")

		var u strings.Builder
		// firstIsA[i] records which model's label was printed first, so the
		// digit can be mapped back to a model after the reply.
		firstIsA := make([]bool, len(ids))
		for i, id := range ids {
			firstIsA[i] = (off+i)%2 == 0
			one, two := va[id].Topic, vb[id].Topic
			if !firstIsA[i] {
				one, two = two, one
			}
			fmt.Fprintf(&u, "%d.\n", i+1)
			writeABItem(&u, items[id], "   ")
			fmt.Fprintf(&u, "   label 1: %s\n   label 2: %s\n", one, two)
		}
		reply, err := client.Chat(b.String(), u.String())
		if err != nil {
			w.unparsed += len(ids)
			fmt.Fprintf(os.Stderr, "  referee request failed: %v\n", err)
			continue
		}
		seen := map[int]bool{}
		for _, line := range strings.Split(reply, "\n") {
			f := strings.Fields(line)
			if len(f) != 2 {
				continue
			}
			nth, err1 := strconv.Atoi(strings.TrimSuffix(f[0], "."))
			pick, err2 := strconv.Atoi(f[1])
			if err1 != nil || err2 != nil || nth < 1 || nth > len(ids) || pick < 0 || pick > 2 {
				continue
			}
			if seen[nth] {
				continue
			}
			seen[nth] = true
			switch {
			case pick == 0:
				w.neither++
			case (pick == 1) == firstIsA[nth-1]:
				w.a++
			default:
				w.b++
			}
		}
		w.unparsed += len(ids) - len(seen)
		fmt.Printf("  referee %d/%d\n", end, len(disagree))
	}
	return w
}

// writeABItem shows the referee the same fields the classifier saw. It is a
// deliberate copy of what the prompt builder does rather than a call into it:
// classify's renderer is unexported, and reaching for it would tie a
// diagnostic to the shape of the production prompt.
func writeABItem(u *strings.Builder, item classify.Item, indent string) {
	fmt.Fprintf(u, "%stitle: %s\n", indent, item.Title)
	if item.Channel != "" {
		fmt.Fprintf(u, "%schannel: %s\n", indent, item.Channel)
	}
	if item.Area != "" {
		fmt.Fprintf(u, "%sarea: %s (fixed)\n", indent, item.Area)
	}
	if len(item.Tags) > 0 {
		tags := item.Tags
		if len(tags) > 15 {
			tags = tags[:15]
		}
		fmt.Fprintf(u, "%screator tags: %s\n", indent, strings.Join(tags, ", "))
	}
}

func abArea(topic string) string {
	if i := strings.IndexByte(topic, '/'); i >= 0 {
		return topic[:i]
	}
	return topic
}

func pct(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) * 100 / float64(whole)
}
