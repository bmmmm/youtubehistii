// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/omlx"
	"github.com/bmmmm/youtubehistii/internal/rules"
	"github.com/bmmmm/youtubehistii/internal/taxonomy"
)

// Fixed file locations, following the config/rules.yaml convention: relative
// to the working directory, gitignored because the generated taxonomy lists
// the user's real subjects — a profile of a person.
const (
	taxonomyPath = "config/taxonomy.yaml"
	controlPath  = "config/taxonomy-control.yaml"

	// defaultRounds bounds the refinement loop. 10, not 5: with one automatic
	// split per name the churn decays instead of cycling, and the real corpus
	// (2679 labels) settled at round 8 — a lower limit hides convergence
	// behind "ran out of rounds". Late rounds are cheap: they split almost
	// nothing and their names come from the cache. The probe's naming estimate
	// reads the same constant, so the two cannot drift apart.
	defaultRounds = 10
)

// newOMLXClient is the seam classifyOpts.newClient is on the classify side:
// the sandbox denies httptest.NewServer its bind, so the only mock that can
// drive cmdTaxonomy end to end is an http.RoundTripper handed in from a test.
var newOMLXClient = omlx.New

// addTaxonomyFileFlag registers -taxonomy-file on every command that reads or
// writes the projection. Shared rather than repeated because -rules already
// showed what happens otherwise: two commands disagreeing about which file
// they mean (see the note at loadRules in classify.go). controlPath stays a
// constant — the control file steers a running loop and is read from the
// working directory by design.
func addTaxonomyFileFlag(fs *flag.FlagSet) *string {
	return fs.String("taxonomy-file", taxonomyPath, "the taxonomy to read or write")
}

func cmdTaxonomy(args []string) error {
	fs, dataDir := newFlagSet("taxonomy")
	rulesPath := fs.String("rules", "", "rules file (default: config/rules.yaml, falling back to config/rules.example.yaml)")
	taxFile := addTaxonomyFileFlag(fs)
	embedModel := fs.String("embed-model", "bge-m3-mlx-fp16", "embedding model on the oMLX server (multilingual, so chess and schach meet)")
	// Thresholds live on the CENTERED distance scale (see -center), where the
	// structure degrades gently instead of collapsing: calibrated on a 35k
	// corpus to 191 subjects under 17 top levels.
	fine := fs.Float64("fine", 0.70, "cosine-distance threshold for subjects (smaller = more, tighter clusters)")
	coarse := fs.Float64("coarse", 0.85, "cosine-distance threshold for the top level")
	minVideos := fs.Int("min-videos", 3, "fold subjects with fewer unique videos into their nearest neighbor")
	minTopVideos := fs.Int("min-top-videos", 25, "fold top levels with fewer unique videos into their nearest neighbor")
	maxRadius := fs.Float64("max-radius", 0.50, "split subjects wider than this in refinement rounds")
	rounds := fs.Int("rounds", defaultRounds, "refinement rounds at most; the control file can stop earlier")
	tailN := fs.Int("tail-n", 1, "the tail metric counts subjects with at most this many videos")
	center := fs.Bool("center", true, "subtract the mean vector before clustering — spreads out embeddings that crowd into one band")
	noLLM := fs.Bool("no-llm", false, "name clusters from their strongest member instead of asking the chat model")
	nameBatch := fs.Int("name-batch", defaultNameBatch, "subjects per naming request; >1 is 2.4x faster cold but changes the names, so raise it only when calibrating thresholds (top levels are always named singly)")
	probe := fs.Bool("probe", false, "measure server latency (one embedding batch, one chat request) and estimate the run; changes nothing")
	fs.Parse(args)
	p := paths{dataDir: *dataDir}

	cfg, err := loadRules(*rulesPath)
	if err != nil {
		return err
	}
	client := newOMLXClient(cfg.LLM.Model, cfg.LLM.BaseURL)

	if *probe {
		return probeRun(client, *embedModel, p)
	}

	timer := newPhaseTimer()

	// Collect: classified.jsonl + meta cache -> labels. Purely local.
	rows, err := readJSONL[classify.Verdict](p.classifiedJSONL())
	if err != nil {
		return fmt.Errorf("read classified views (run \"classify\" first): %w", err)
	}
	metas, err := enrich.Cache{Dir: p.metaCacheDir()}.ReadAll()
	if err != nil {
		return err
	}
	views := make([]taxonomy.View, 0, len(rows))
	for _, r := range rows {
		area, sub := rules.SplitTopic(r.Topic)
		v := taxonomy.View{VideoID: r.VideoID, Area: area, Sub: sub, Channel: r.Channel, Title: r.Title}
		if m, ok := metas[r.VideoID]; ok {
			v.Tags = m.Tags
		}
		views = append(views, v)
	}
	labels := taxonomy.Collect(views)
	if len(labels) == 0 {
		return fmt.Errorf("no classified topics to cluster — run \"classify\" first")
	}

	if err := os.MkdirAll(p.outDir(), 0o755); err != nil {
		return err
	}
	log, err := newRunLog(filepath.Join(p.outDir(), "taxonomy-run.jsonl"))
	if err != nil {
		return err
	}
	defer log.close()

	baseline := taxonomy.MeasureLabels(labels, *tailN)
	log.event("collect", map[string]any{"labels": len(labels), "views": len(views)})
	log.event("baseline", baseline)
	fmt.Printf("collected %d labels from %d views\n", len(labels), len(views))
	fmt.Printf("baseline   %s\n", metricsLine(baseline))
	timer.mark("collect")

	// Embed: one vector per label, batched, cached under data/cache/embed so
	// the second run is free.
	vecs, fresh, err := embedLabels(client, *embedModel, labels, p.embedCacheDir())
	if err != nil {
		return err
	}
	log.event("embed", map[string]any{"model": *embedModel, "fresh": fresh, "cached": len(labels) - fresh})
	if *center {
		vecs = taxonomy.Center(vecs)
	}
	fmt.Printf("embedded %d labels (%d fresh, %d from cache)%s\n", len(labels), fresh, len(labels)-fresh,
		map[bool]string{true: ", mean-centered"}[*center])
	timer.mark("embed")

	namer, nameStats := newNamer(client, *noLLM, log, p.nameCacheDir(), *nameBatch)
	opts := taxonomy.RefineOpts{
		SplitAt:    *fine * 0.7,
		MaxRadius:  *maxRadius,
		MergeBelow: *fine * 0.6,
		MinVideos:  *minVideos,
		// Filled in as the rounds go: a name whose split the same-name merge
		// undid stays blocked for every later round, so the loop cannot
		// re-enter through it.
		NoSplit: map[string]bool{},
	}

	// Round 1: cluster, fold the tail, name everything, group into tops.
	ctl, err := taxonomy.LoadControl(controlPath)
	if err != nil {
		return err
	}
	subjects := taxonomy.ClusterLabels(labels, vecs, *fine)
	timer.mark("cluster")
	fmt.Printf("clustered into %d subjects at threshold %.2f\n", len(subjects), *fine)
	subjects = taxonomy.FoldSmall(subjects, *minVideos, ctl.KeepSet())
	timer.mark("fold")
	nameSubjects(subjects, namer, true)
	timer.mark("name")
	subjects = taxonomy.MergeSameNames(subjects)
	assignParents(subjects, *coarse, *minTopVideos, ctl.KeepSet(), namer)
	timer.mark("coarse")

	last := taxonomy.Measure(subjects, *tailN)
	log.event("round", map[string]any{"n": 1, "metrics": last})
	fmt.Printf("round 1    %s\n", metricsLine(last))

	// Refinement: split where coherence tears, merge what lies closer than
	// the threshold, re-read the control file between rounds.
	for round := 2; round <= *rounds; round++ {
		ctl, err = taxonomy.LoadControl(controlPath)
		if err != nil {
			return err
		}
		if ctl.Stop {
			log.event("stop", map[string]any{"round": round, "reason": "control file"})
			fmt.Println("control file says stop")
			break
		}
		var changes []taxonomy.Change
		subjects, changes = taxonomy.Refine(subjects, ctl, opts)
		nameSubjects(subjects, namer, false)
		subjects = taxonomy.MergeSameNames(subjects)
		blocked := blockSecondSplit(changes, opts.NoSplit)
		assignParents(subjects, *coarse, *minTopVideos, ctl.KeepSet(), namer)

		m := taxonomy.Measure(subjects, *tailN)
		timer.mark(fmt.Sprintf("round-%d", round))
		detail := map[string]any{"n": round, "metrics": m, "changes": changes}
		if len(blocked) > 0 {
			detail["split_blocked"] = blocked
		}
		log.event("round", detail)
		fmt.Printf("round %d    %s\n", round, metricsLine(m))
		for _, c := range biggestChanges(changes, 8) {
			fmt.Printf("  %s\n", c)
		}
		for _, name := range blocked {
			fmt.Printf("  split blocked: %s — one automatic attempt per name, the namer keeps putting it back together\n", name)
		}
		if m == last {
			fmt.Println("metrics settled — done")
			break
		}
		last = m
	}

	if err := taxonomy.WriteFile(*taxFile, subjects, []string{
		fmt.Sprintf("generated: %s", time.Now().Format(time.RFC3339)),
		metricsLine(last),
		fmt.Sprintf("baseline: %s", metricsLine(baseline)),
	}); err != nil {
		return err
	}
	timer.mark("write")
	log.event("write", map[string]any{"path": *taxFile, "subjects": len(subjects), "tops": last.Tops})
	log.event("naming", nameStats.Detail())
	log.event("timing", timer.spans)
	fmt.Printf("wrote %s (%d subjects under %d top levels)\n", *taxFile, len(subjects), last.Tops)
	fmt.Printf("naming     %s\n", nameStats.Line())
	fmt.Printf("timing     %s\n", timer.line())
	fmt.Println("compare with: youtubehistii report -taxonomy / watchpath -taxonomy")
	return nil
}

// warmThenTime runs the same call twice and returns both durations. oMLX
// swaps models under memory pressure, so a single cold call measures the
// load, not the throughput: the same 32-text batch came back at 30 ms/text
// warm and 1314 ms/text right after a model change, which flipped the hour
// verdict to false. The first number is real — it is just paid once per
// phase, not per batch — so it is reported rather than hidden.
func warmThenTime(call func() error) (load, measured time.Duration, err error) {
	t0 := time.Now()
	if err = call(); err != nil {
		return 0, 0, err
	}
	load = time.Since(t0)
	t0 = time.Now()
	if err = call(); err != nil {
		return load, 0, err
	}
	return load, time.Since(t0), nil
}

// probeRun is step 0 made executable: whether the models are there, what one
// embedding batch and one chat request cost warm, and what that means for the
// hour budget. The client resolves OMLX_URL/OMLX_API_KEY itself (.env
// fallback), so no secret ever appears on a command line.
func probeRun(client *omlx.Client, embedModel string, p paths) error {
	models, err := client.Models()
	if err != nil {
		return err
	}
	fmt.Printf("server %s offers: %s\n", client.BaseURL, strings.Join(models, ", "))
	for _, m := range []string{embedModel, client.Model} {
		if !slices.Contains(models, m) {
			fmt.Printf("MISSING: %q is not loaded — load it in oMLX first\n", m)
		}
	}

	// Each half is measured even when the other is missing: a server without
	// the embedding model still yields a real chat number, and the probe's
	// job is to report everything it can, not to stop at the first gap.
	texts := make([]string, 32)
	for i := range texts {
		texts[i] = fmt.Sprintf("topic: sample subject %d\nchannels: alpha channel, beta media, gamma tv\n"+
			"tags: music, live, concert, tour, interview\ntitles: a sample video title %d | another sample title", i, i)
	}
	embedLoad, embedBatch, err := warmThenTime(func() error {
		_, e := client.Embed(embedModel, texts)
		return e
	})
	if err != nil {
		fmt.Printf("embed: FAILED — %v\n", err)
	} else {
		fmt.Printf("embed: 32 texts in %s warm (%.0f ms/text), first call %s\n",
			embedBatch.Round(time.Millisecond), float64(embedBatch.Milliseconds())/32, embedLoad.Round(time.Millisecond))
	}

	chatLoad, chatReq, err := warmThenTime(func() error {
		_, e := client.ChatMax("Reply with the single word: ok", "ok?", 8)
		return e
	})
	if err != nil {
		fmt.Printf("chat:  FAILED — %v\n", err)
	} else {
		fmt.Printf("chat:  1 request in %s warm, first call %s\n",
			chatReq.Round(time.Millisecond), chatLoad.Round(time.Millisecond))
	}
	if embedBatch == 0 || chatReq == 0 {
		return fmt.Errorf("probe incomplete — fix the failures above and rerun")
	}

	// Real label count when the data is there; the planning estimate if not.
	nLabels := 2000
	if rows, err := readJSONL[classify.Verdict](p.classifiedJSONL()); err == nil {
		seen := map[string]bool{}
		for _, r := range rows {
			seen[r.Topic] = true
		}
		nLabels = len(seen)
	}
	embedCost := time.Duration(nLabels/32+1) * embedBatch
	nameCost := 300 * chatReq // cluster count is unknown before the run; 300 is generous
	fmt.Printf("estimate for %d labels: embeddings ≈ %s, naming (at ~300 clusters, %d rounds worst case) ≈ %s\n",
		nLabels, embedCost.Round(time.Second), defaultRounds, (defaultRounds * nameCost).Round(time.Second))
	fmt.Printf("server work fits the hour: %v — clustering runs locally and is NOT in that number;\n"+
		"  \"taxonomy -no-llm -rounds 1\" measures it and prints the timing line\n",
		embedCost+defaultRounds*nameCost < time.Hour)
	return nil
}

// embedLabels returns one vector per label, reading data/cache/embed first
// and asking the server only for misses, in batches of 32.
func embedLabels(client *omlx.Client, model string, labels []taxonomy.Label, cacheDir string) ([][]float32, int, error) {
	cache := hashCache[embedEntry]{dir: cacheDir}
	vecs := make([][]float32, len(labels))
	var missIdx []int
	var missTexts []string
	for i, l := range labels {
		text := l.EmbedText()
		if e, ok := cache.read(model, text); ok && len(e.Vector) > 0 {
			vecs[i] = e.Vector
			continue
		}
		missIdx = append(missIdx, i)
		missTexts = append(missTexts, text)
	}
	for off := 0; off < len(missIdx); off += 32 {
		end := min(off+32, len(missIdx))
		out, err := client.Embed(model, missTexts[off:end])
		if err != nil {
			return nil, 0, fmt.Errorf("embedding batch at %d/%d: %w", off, len(missIdx), err)
		}
		for n, v := range out {
			i := missIdx[off+n]
			vecs[i] = v
			if err := cache.write(embedEntry{Vector: v}, model, missTexts[off+n]); err != nil {
				return nil, 0, err
			}
		}
		if end == len(missIdx) || (off/32)%10 == 9 {
			fmt.Printf("  embedded %d/%d\n", end, len(missIdx))
		}
	}
	return vecs, len(missIdx), nil
}

// blockSecondSplit gives every name one automatic split per run. The pieces
// come back unnamed, the namer hands several of them the same name again, and
// MergeSameNames glues those into a cluster that carries the name and is still
// wider than -max-radius — so the next round splits it again.
//
// The split is not undone exactly, only undone in effect: measured on the real
// corpus, "split music" fired in all five rounds at 346, 190, 229 and 236
// views, never twice the same size. An identity check on the restored views
// therefore misses it, which is why the rule is the blunt one — a name whose
// pieces the namer cannot tell apart is tried once and then left alone. Since
// names are finite and each gets at most one automatic split, the loop
// terminates by construction.
//
// A split the control file asks for is unaffected: Refine checks that first.
// Returns the newly blocked names, for the run log and the terminal.
func blockSecondSplit(changes []taxonomy.Change, noSplit map[string]bool) []string {
	var blocked []string
	for _, ch := range changes {
		if ch.Op != "split" || ch.From == "" || noSplit[ch.From] {
			continue
		}
		noSplit[ch.From] = true
		blocked = append(blocked, ch.From)
	}
	return blocked
}

// biggestChanges picks the highest-view changes for the terminal.
func biggestChanges(changes []taxonomy.Change, n int) []taxonomy.Change {
	out := append([]taxonomy.Change{}, changes...)
	slices.SortStableFunc(out, func(a, b taxonomy.Change) int { return b.Views - a.Views })
	return out[:min(len(out), n)]
}

func metricsLine(m taxonomy.Metrics) string {
	return fmt.Sprintf("subjects %d under %d tops | spread %d | tail %.0f%% | chan mean %.1f max %d | coherence %.3f",
		m.Subjects, m.Tops, m.Spread, 100*m.TailShare, m.ChanMean, m.ChanMax, m.Coherence)
}
