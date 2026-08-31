// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"math"
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
	fmt.Print(abVerdict(wins, len(disagree)))
	return nil
}

// abVerdict is the sentence the caller came for, and it has two chances to
// refuse. Both were added after it stated something untrue about real data.
//
//   - Quorum. A referee whose replies do not parse still produces a ratio,
//     and "86 % (6 of 7)" out of 61 disagreements reads exactly like a result
//     computed from all 61. Below half decided there is no verdict at all.
//   - Noise. 20 wins against 16 on 36 decided is a coin flip: under a fair
//     coin the difference has standard deviation sqrt(n), so a margin inside
//     2*sqrt(n) says nothing about the models and everything about the sample
//     size. Naming a winner there is how a 5-hour re-run gets justified by
//     four votes.
//
// Pure, so its behaviour is a test and not a 6-minute run against a server.
func abVerdict(w abWins, disagreements int) string {
	decided := w.a + w.b
	switch {
	case decided == 0:
		return "verdict: the referee decided nothing — the comparison is inconclusive\n"
	case decided*2 < disagreements:
		return fmt.Sprintf("verdict: NONE — the referee only decided %d of %d disagreements (%.0f %%).\n"+
			"  Its replies did not fit the expected shape (see the warnings above), so any\n"+
			"  ratio from them would be noise. Try another -judge model.\n",
			decided, disagreements, pct(decided, disagreements))
	}
	margin := w.a - w.b
	if margin < 0 {
		margin = -margin
	}
	if float64(margin) < 2*math.Sqrt(float64(decided)) {
		return fmt.Sprintf("verdict: TOO CLOSE — %d/%d on %d decided is inside the noise band of a\n"+
			"  fair coin (a margin under %.0f proves nothing at this sample size). The models\n"+
			"  differ, neither is better. Raise -n if the question is worth more requests.\n",
			w.b, w.a, decided, 2*math.Sqrt(float64(decided)))
	}
	if w.b > w.a {
		return fmt.Sprintf("verdict: the candidate wins %.0f %% of the decided disagreements (%d of %d)\n",
			pct(w.b, decided), w.b, decided)
	}
	return fmt.Sprintf("verdict: the candidate LOSES (%d of %d decided) — the re-run is not worth its hours\n",
		w.b, decided)
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
	return abSampleIDs(usable, n)
}

// abSampleIDs is the stride itself, kept pure so a test can hold it to the
// property the comparison rests on: the same ids in, the same sample out.
func abSampleIDs(sorted []string, n int) []string {
	if n <= 0 || n >= len(sorted) {
		return sorted
	}
	stride := float64(len(sorted)) / float64(n)
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, sorted[int(float64(i)*stride)])
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
		seen := abReadPicks(reply, len(ids))
		for nth, pick := range seen {
			switch {
			case pick == 0:
				w.neither++
			case (pick == 1) == firstIsA[nth-1]:
				w.a++
			default:
				w.b++
			}
		}
		// A referee that answers in the wrong shape is invisible otherwise:
		// its lines simply do not parse, the counters stay small, and the
		// verdict below reads as confident because it divides one small
		// number by another. Show the reply the moment it does not fit, the
		// way classify's parse warnings do.
		if missing := len(ids) - len(seen); missing > 0 {
			w.unparsed += missing
			fmt.Fprintf(os.Stderr, "  referee: %d of %d lines unreadable, reply began: %q\n",
				missing, len(ids), abTruncate(reply, 200))
		}
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

// abReadPicks turns a referee reply into item number -> pick.
//
// The numbered form ("<n> <pick>") is what the prompt asks for and what the
// Qwen models produce. gemma-4-26b answers the same content WITHOUT numbering
// its lines — 20 lines of "<something> <pick>" whose first column is not an
// index. Read strictly, that reply looks like twenty duplicates of item 1 and
// item 2, and 18 of 20 real answers get thrown away as unreadable: the
// referee was right, the parser was wrong.
//
// So: if the first column is a permutation of 1..n, believe it. Otherwise, if
// the reply has exactly n candidate lines and every pick is in range, read it
// POSITIONALLY. Both guards have to hold — a reply with a missing line would
// shift every answer onto the wrong video, and that is worse than reporting
// nothing.
func abReadPicks(reply string, n int) map[int]int {
	type row struct{ first, pick int }
	var rows []row
	for _, line := range strings.Split(reply, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		first, err1 := strconv.Atoi(strings.TrimSuffix(f[0], "."))
		pick, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil || pick < 0 || pick > 2 {
			continue
		}
		rows = append(rows, row{first, pick})
	}

	numbered := make(map[int]int, len(rows))
	for _, r := range rows {
		if r.first >= 1 && r.first <= n {
			if _, dup := numbered[r.first]; !dup {
				numbered[r.first] = r.pick
			}
		}
	}
	if len(numbered) == n {
		return numbered
	}
	if len(rows) == n {
		positional := make(map[int]int, n)
		for i, r := range rows {
			positional[i+1] = r.pick
		}
		return positional
	}
	return numbered
}

func abTruncate(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " / ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
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
