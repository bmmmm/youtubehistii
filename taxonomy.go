// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
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

func cmdTaxonomy(args []string) error {
	fs, dataDir := newFlagSet("taxonomy")
	rulesPath := fs.String("rules", "", "rules file (default: config/rules.yaml, falling back to config/rules.example.yaml)")
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
	client := omlx.New(cfg.LLM.Model, cfg.LLM.BaseURL)

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

	if err := taxonomy.WriteFile(taxonomyPath, subjects, []string{
		fmt.Sprintf("generated: %s", time.Now().Format(time.RFC3339)),
		metricsLine(last),
		fmt.Sprintf("baseline: %s", metricsLine(baseline)),
	}); err != nil {
		return err
	}
	timer.mark("write")
	log.event("write", map[string]any{"path": taxonomyPath, "subjects": len(subjects), "tops": last.Tops})
	log.event("naming", nameStats.detail())
	log.event("timing", timer.spans)
	fmt.Printf("wrote %s (%d subjects under %d top levels)\n", taxonomyPath, len(subjects), last.Tops)
	fmt.Printf("naming     %s\n", nameStats.line())
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
	vecs := make([][]float32, len(labels))
	var missIdx []int
	var missTexts []string
	for i, l := range labels {
		text := l.EmbedText()
		if v, ok := readEmbedCache(cacheDir, model, text); ok {
			vecs[i] = v
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
			if err := writeEmbedCache(cacheDir, model, missTexts[off+n], v); err != nil {
				return nil, 0, err
			}
		}
		if end == len(missIdx) || (off/32)%10 == 9 {
			fmt.Printf("  embedded %d/%d\n", end, len(missIdx))
		}
	}
	return vecs, len(missIdx), nil
}

func embedCachePath(dir, model, text string) string {
	sum := sha256.Sum256([]byte(model + "\x00" + text))
	return filepath.Join(dir, hex.EncodeToString(sum[:12])+".json")
}

func readEmbedCache(dir, model, text string) ([]float32, bool) {
	b, err := os.ReadFile(embedCachePath(dir, model, text))
	if err != nil {
		return nil, false
	}
	var e struct {
		Vector []float32 `json:"vector"`
	}
	if json.Unmarshal(b, &e) != nil || len(e.Vector) == 0 {
		return nil, false
	}
	return e.Vector, true
}

func writeEmbedCache(dir, model, text string, v []float32) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(struct {
		Vector []float32 `json:"vector"`
	}{v})
	if err != nil {
		return err
	}
	return os.WriteFile(embedCachePath(dir, model, text), b, 0o644)
}

// nameCachePath keys a naming reply by the whole prompt plus the model that
// answered it — the same shape as embedCachePath, one directory over.
func nameCachePath(dir, model, system, user string) string {
	sum := sha256.Sum256([]byte(model + "\x00" + system + "\x00" + user))
	return filepath.Join(dir, hex.EncodeToString(sum[:12])+".json")
}

func readNameCache(dir, model, system, user string) (string, bool) {
	if dir == "" {
		return "", false
	}
	b, err := os.ReadFile(nameCachePath(dir, model, system, user))
	if err != nil {
		return "", false
	}
	var e struct {
		Reply string `json:"reply"`
	}
	if json.Unmarshal(b, &e) != nil || e.Reply == "" {
		return "", false
	}
	return e.Reply, true
}

// writeNameCache drops a failed write: the run has the name it needs, and the
// next one simply asks the model again.
func writeNameCache(dir, model, system, user, reply string) {
	if dir == "" {
		return
	}
	b, err := json.Marshal(struct {
		Reply string `json:"reply"`
	}{reply})
	if err != nil {
		return
	}
	os.WriteFile(nameCachePath(dir, model, system, user), b, 0o644)
}

// kindStats counts one altitude's ("subject" or "top") naming outcomes over
// a run: answers the disk cache served, clusters it did not cover, how many
// chat requests those cost (fewer than the clusters, since a request names
// up to nameBatch of them), how many names fell back to the cluster's
// strongest member, and the wall-clock time spent inside ChatMax.
//
// misses and requests are separate on purpose: misses is the work the cache
// could not save, requests is what that work cost the server, and the ratio
// between them is the whole point of batching. Fields are only ever touched
// through atomic.AddInt64 — see namingStats.
type kindStats struct {
	hits, misses, requests, fallbacks int64
	reqNanos                          int64 // sum of time.Since around each ChatMax call
}

// namingStats is the answer to "how many of the 86s run and its 380ms/warm-
// request estimate are real requests, and how many the cache already
// covers" — a question nobody could answer before this existed. One
// kindStats per altitude, because "subject" and "top" go through the same
// closure but at very different counts (a real corpus names ~770 subjects
// and a couple dozen tops).
//
// Atomic counters rather than a mutex: newNamer's closure is called
// serially today, once per cluster, but making it concurrent is the next
// planned change to this run — and atomics cost nothing extra to have
// right, whereas a mutex added after the fact is easy to forget on one of
// the four fields.
type namingStats struct {
	subject, top kindStats
}

func (s *namingStats) forKind(kind string) *kindStats {
	if kind == "top" {
		return &s.top
	}
	return &s.subject
}

// detail is what log.event("naming", ...) writes: one nested object per
// altitude, in the same plain-map style as the "embed" and "write" events.
func (s *namingStats) detail() map[string]any {
	kindDetail := func(k kindStats) map[string]any {
		return map[string]any{
			"cached": k.hits, "uncached": k.misses, "requests": k.requests,
			"fallback": k.fallbacks, "request_ms": time.Duration(k.reqNanos).Milliseconds(),
		}
	}
	return map[string]any{"subject": kindDetail(s.subject), "top": kindDetail(s.top)}
}

// line renders the same counts for the terminal, in metricsLine's
// pipe-separated style.
func (s *namingStats) line() string {
	part := func(kind string, k kindStats) string {
		avg := time.Duration(0)
		if k.requests > 0 {
			avg = time.Duration(k.reqNanos / k.requests)
		}
		return fmt.Sprintf("%s %d cached %d uncached in %d req %d fallback (%s, %s/req)",
			kind, k.hits, k.misses, k.requests, k.fallbacks,
			time.Duration(k.reqNanos).Round(time.Millisecond), avg.Round(time.Millisecond))
	}
	return part("subject", s.subject) + " | " + part("top", s.top)
}

// defaultNameBatch is 1 — one cluster per naming request — and that default
// is a measurement, not caution.
//
// Batching works, and it is fast: naming is latency-bound rather than
// token-bound, so twelve clusters in one prompt turned a cold run's 335
// requests into 44 and 518.6 s into 216.0 s, 2.4x. What it also did was
// change the names, and through them the taxonomy: the same corpus that
// settles at 188 subjects under 9 tops named one at a time came back as 198
// under 11, and its biggest section — 64 subjects, 12751 views, music
// included — was called "subject-a". That is precisely the failure
// taxonomy.GroupPrompt was written to fix. Asking the tops singly did not
// save it (still "subject-a"), because the damage is upstream: batched
// subject names differ, MergeSameNames then merges different clusters, and
// the coarse groups it feeds are no longer the same groups.
//
// So the speed is real and the cost is the output. Raise -name-batch when
// the names do not matter and the run does — calibrating -fine/-coarse,
// where only the shape of the tree is being read — and leave it at 1 when
// the taxonomy is meant to be kept. A warm rerun asks nothing either way.
const defaultNameBatch = 1

// nameAltitude is the one sentence that separates the two naming jobs.
func nameAltitude(kind string) string {
	if kind == "top" {
		return "the broad top-level category the members share, like a site section"
	}
	return "the cluster's one shared subject, as specific as the members allow"
}

// writeClusterBody renders the members and channels a namer reads. prefix is
// "" for the single-cluster prompt and an indent for the batch, where several
// clusters sit under numbers.
//
// Twelve members at most: a namer reads the strongest ones and the tail only
// costs tokens.
func writeClusterBody(b *strings.Builder, c taxonomy.Cluster, prefix string) {
	fmt.Fprintf(b, "%smembers:\n", prefix)
	for i, l := range c.Members {
		if i == 12 {
			fmt.Fprintf(b, "%s  … and %d more\n", prefix, len(c.Members)-i)
			break
		}
		fmt.Fprintf(b, "%s  %s (%d views)\n", prefix, l.Topic(), l.Views)
	}
	if ch := c.TopChannels(5); len(ch) > 0 {
		fmt.Fprintf(b, "%schannels: %s\n", prefix, strings.Join(ch, ", "))
	}
}

// nameSinglePrompt is the one-cluster prompt — and, just as importantly, the
// CACHE KEY for that cluster's name whether the answer arrived alone or
// inside a batch. Keying on the single prompt is what lets batching change
// how names are fetched without invalidating a single cached name: the
// wording here must not drift, or every existing entry goes cold at once.
func nameSinglePrompt(c taxonomy.Cluster, kind string) (system, user string) {
	system = "You name clusters of YouTube watch-history topic labels.\n" +
		"Reply with EXACTLY one short lowercase slug (a-z, 0-9, dashes; at most three words " +
		"joined by dashes) naming " + nameAltitude(kind) + ".\n" +
		"Prefer reusing a member label's name when it already covers the whole cluster. No prose."
	var u strings.Builder
	writeClusterBody(&u, c, "")
	return system, u.String()
}

// nameBatchPrompt renders one prompt for several clusters. Same shape as
// classification's batch prompt, and for the same reason: one LINE per
// cluster keyed by its NUMBER, never by its name. A name is exactly the
// thing the model is being asked to invent, so it cannot also be the key —
// and 1..N is copyable, which the high-entropy alternatives are not.
func nameBatchPrompt(cs []taxonomy.Cluster, kind string) (system, user string) {
	var b strings.Builder
	b.WriteString("You name clusters of YouTube watch-history topic labels.\n")
	fmt.Fprintf(&b, "You get %d numbered clusters. Reply with EXACTLY one line per cluster, in the same order:\n", len(cs))
	b.WriteString("<n> <slug>\n")
	fmt.Fprintf(&b, "<slug> is ONE short lowercase slug (a-z, 0-9, dashes; at most three words joined by dashes) naming %s.\n",
		nameAltitude(kind))
	b.WriteString("Prefer reusing a member label's name when it already covers the whole cluster.\n")
	b.WriteString("No prose, no code fences, no JSON, no blank lines.\n")
	b.WriteString("Example: 2 indie-rock")

	var u strings.Builder
	for i, c := range cs {
		fmt.Fprintf(&u, "%d.\n", i+1)
		writeClusterBody(&u, c, "   ")
	}
	return b.String(), u.String()
}

// nameLineRe matches the reply key "1" / "2." / "3)" plus the slug after it.
var nameLineRe = regexp.MustCompile(`^(\d+)[.):]?\s+(\S.*)$`)

// parseNameBatch maps a batch reply back onto the clusters that were asked
// about; the name on line n belongs to cs[n-1]. STRICT, exactly like
// ParseBatchVerdicts: every number 1..n must appear once with a slug that
// survives slugging, nothing may name a number outside the range, and any
// violation is an error the caller answers with single requests. A name that
// landed on the wrong cluster is invisible afterwards — it just reads as a
// badly named subject — so the mapping is verified, never guessed.
func parseNameBatch(reply string, n int) ([]string, error) {
	out := make([]string, n)
	for _, line := range strings.Split(reply, "\n") {
		m := nameLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue // prose, fences, blank lines — completeness is checked below
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil || idx < 1 || idx > n {
			return nil, fmt.Errorf("name for line %s of %d", m[1], n)
		}
		if out[idx-1] != "" {
			return nil, fmt.Errorf("duplicate name for line %d", idx)
		}
		if rules.SlugifySub(m[2]) == "" {
			return nil, fmt.Errorf("line %d: %q is not a usable slug", idx, m[2])
		}
		// The RAW answer is what comes back, not the slug — the single path
		// caches the model's words and slugs them afterwards, and a batched
		// name has to be stored the same way or the two disagree on rerun.
		out[idx-1] = m[2]
	}
	for i, s := range out {
		if s == "" {
			return nil, fmt.Errorf("reply misses a name for line %d of %d", i+1, n)
		}
	}
	return out, nil
}

// newNamer returns the cluster-naming function: it takes the clusters that
// need a name and returns one name each, in order, falling back to the
// strongest member's sub on any model trouble. kind is "subject" or "top"
// and only changes the prompt's altitude. The returned namingStats fills in
// as the closure runs; read it only after the run is done with it.
//
// Replies are cached on disk under the prompt, because the intended way to
// steer a run is to edit the control file and run the same command again —
// and naming is the expensive half: a real corpus clusters into ~770
// subjects, one request each. The cache key carries the chat model, so
// swapping the model in rules.yaml (the answer to unusable names) bypasses it
// on its own. To throw the names away deliberately, delete the directory.
//
// There is deliberately no worker knob here, the way -llm-workers exists for
// classification. It was measured against the real server before anything
// was written: eight naming requests took 737 ms each in sequence, and
// 986 ms each across two workers, 967 ms across four — concurrency made the
// run 25% SLOWER, because oMLX decodes one chat request at a time and the
// extra workers only add queueing.
//
// So the lever is fewer requests, not more at once, and that is what
// nameBatch is: the clusters a round cannot serve from cache are named a
// dozen per request. The counters say what it bought — a cold run used to
// send 388 requests (286 subjects, 102 top levels, one apiece), a warm one
// still sends none.
func newNamer(client *omlx.Client, noLLM bool, log *runLog, cacheDir string, nameBatch int) (func(cs []taxonomy.Cluster, kind string) []string, *namingStats) {
	warned := false
	stats := &namingStats{}
	// One mkdir up front: a cache that cannot exist costs speed, not the run.
	if !noLLM {
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "warning: name cache unusable (%v) — every rerun will pay the model again\n", err)
			cacheDir = ""
		}
	}
	// slugOr turns a model's raw answer into a name, keeping the cluster's
	// strongest member as the floor. Slugging stays a code decision that a
	// later change may revise; the cached reply is the model's words.
	slugOr := func(reply string, c taxonomy.Cluster) string {
		if slug := rules.SlugifySub(reply); slug != "" {
			return slug
		}
		return taxonomy.FallbackName(c)
	}

	// askOne is the single-cluster request: the path for a lone leftover, and
	// the answer to a batch the model got wrong.
	askOne := func(c taxonomy.Cluster, kind string, ks *kindStats) string {
		system, user := nameSinglePrompt(c, kind)
		t0 := time.Now()
		reply, err := client.ChatMax(system, user, 24)
		atomic.AddInt64(&ks.reqNanos, int64(time.Since(t0)))
		atomic.AddInt64(&ks.requests, 1)
		if err != nil {
			atomic.AddInt64(&ks.fallbacks, 1)
			if !warned {
				fmt.Fprintf(os.Stderr, "warning: naming via LLM failed (%v) — falling back to member names\n", err)
				warned = true
			}
			log.event("name-fallback", map[string]any{"cluster": taxonomy.FallbackName(c), "error": err.Error()})
			return taxonomy.FallbackName(c)
		}
		writeNameCache(cacheDir, client.Model, system, user, reply)
		return slugOr(reply, c)
	}

	namer := func(cs []taxonomy.Cluster, kind string) []string {
		out := make([]string, len(cs))
		if noLLM {
			// -no-llm never asks the model, so it never touches stats: the
			// naming line simply reads all zeros, which is the true count.
			for i, c := range cs {
				out[i] = taxonomy.FallbackName(c)
			}
			return out
		}
		ks := stats.forKind(kind)

		// The cache is consulted per cluster, under the single-cluster
		// prompt, whatever shape the request that filled it had. That is what
		// makes batching free to adopt: a run after this change still finds
		// every name an earlier run paid for.
		var miss []int
		for i, c := range cs {
			system, user := nameSinglePrompt(c, kind)
			reply, cached := readNameCache(cacheDir, client.Model, system, user)
			if !cached {
				miss = append(miss, i)
				continue
			}
			atomic.AddInt64(&ks.hits, 1)
			out[i] = slugOr(reply, c)
		}
		atomic.AddInt64(&ks.misses, int64(len(miss)))

		// Top levels are always asked one at a time, whatever -name-batch
		// says. There are only ever a handful of them — five of a cold run's
		// 44 requests — so batching them buys almost nothing, and a batched
		// run named the 12751-view section "section-a" over 64 subjects
		// including music. A section name is the most visible name the run
		// produces; it gets the prompt's whole attention.
		batchSize := 1
		if kind != "top" {
			batchSize = max(nameBatch, 1)
		}
		for off := 0; off < len(miss); off += batchSize {
			chunk := miss[off:min(off+batchSize, len(miss))]
			if len(chunk) == 1 {
				// A batch prompt for one cluster is the single prompt with
				// extra ceremony, and its reply is not cache-compatible.
				out[chunk[0]] = askOne(cs[chunk[0]], kind, ks)
				continue
			}
			batch := make([]taxonomy.Cluster, len(chunk))
			for n, i := range chunk {
				batch[n] = cs[i]
			}
			system, user := nameBatchPrompt(batch, kind)
			t0 := time.Now()
			// max_tokens scales with the batch: a reply cut off mid-list
			// loses the clusters at its end, and those are real names.
			reply, err := client.ChatMax(system, user, 24*len(chunk))
			atomic.AddInt64(&ks.reqNanos, int64(time.Since(t0)))
			atomic.AddInt64(&ks.requests, 1)
			var names []string
			if err == nil {
				names, err = parseNameBatch(reply, len(chunk))
			}
			if err != nil {
				// One unusable batch costs len(chunk) requests, never a name
				// on the wrong cluster — a misplaced name is invisible later.
				log.event("name-batch-retry", map[string]any{
					"kind": kind, "clusters": len(chunk), "error": err.Error(),
				})
				for _, i := range chunk {
					out[i] = askOne(cs[i], kind, ks)
				}
				continue
			}
			for n, i := range chunk {
				out[i] = slugOr(names[n], cs[i])
				// Stored under the SINGLE prompt, so the next run finds it
				// however this one happened to ask.
				s, u := nameSinglePrompt(cs[i], kind)
				writeNameCache(cacheDir, client.Model, s, u, names[n])
			}
		}
		return out
	}
	return namer, stats
}

// nameSubjects names clusters: all of them on the first pass, only unnamed
// pieces (fresh splits) afterwards — merges keep their names. The ones that
// need a name go to the namer together, so a round of fresh splits costs a
// handful of requests instead of one per piece.
func nameSubjects(cs []taxonomy.Cluster, namer func([]taxonomy.Cluster, string) []string, all bool) {
	var idx []int
	for i := range cs {
		if all || cs[i].Name == "" {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return
	}
	batch := make([]taxonomy.Cluster, len(idx))
	for n, i := range idx {
		batch[n] = cs[i]
	}
	names := namer(batch, "subject")
	for n, i := range idx {
		cs[i].Name = names[n]
	}
}

// assignParents groups subjects into top levels and names each group. Two
// groups landing on the same name simply ARE one top level — nothing to
// dedupe. Groups under the minTopVideos bar are folded into their nearest
// neighbour first, so a far-out subject does not become a section of one.
func assignParents(cs []taxonomy.Cluster, coarse float64, minTopVideos int, keep map[string]bool, namer func([]taxonomy.Cluster, string) []string) {
	groups := taxonomy.FoldSmallGroups(cs, taxonomy.Coarse(cs, coarse), minTopVideos, keep)
	// The WHOLE group carries the naming prompt, one label per subject:
	// naming it after its strongest subject alone called a top level of
	// 31 music subjects "subject-a" and one of 29 sport subjects
	// "cycling". TopChannels then sums across the group too.
	//
	// All groups go in one call: a corpus has around ten top levels, which
	// is under nameBatch, so a round that used to cost ten requests now
	// costs one.
	var prompts []taxonomy.Cluster
	var named [][]int
	for _, group := range groups {
		prompt := taxonomy.GroupPrompt(cs, group)
		if len(prompt.Members) == 0 {
			continue
		}
		prompts = append(prompts, prompt)
		named = append(named, group)
	}
	if len(prompts) == 0 {
		return
	}
	tops := namer(prompts, "top")
	for n, group := range named {
		for _, i := range group {
			cs[i].Parent = tops[n]
		}
	}
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

// phaseTimer records how long each stage took. The probe can only estimate
// the two server-side costs; the clustering that sits between them never
// appeared in any estimate, so the run itself has to say where its time went.
type phaseTimer struct {
	last  time.Time
	spans []phaseSpan
}

type phaseSpan struct {
	Phase string `json:"phase"`
	MS    int64  `json:"ms"`
}

func newPhaseTimer() *phaseTimer { return &phaseTimer{last: time.Now()} }

// mark closes the span that started at the previous mark. Stages are
// sequential, so one call per boundary is the whole bookkeeping.
func (t *phaseTimer) mark(phase string) {
	now := time.Now()
	t.spans = append(t.spans, phaseSpan{Phase: phase, MS: now.Sub(t.last).Milliseconds()})
	t.last = now
}

// line renders the spans for the terminal, skipping anything under 50 ms —
// the point is which stage dominates, not a full accounting.
func (t *phaseTimer) line() string {
	var parts []string
	var total int64
	for _, s := range t.spans {
		total += s.MS
		if s.MS >= 50 {
			parts = append(parts, fmt.Sprintf("%s %.1fs", s.Phase, float64(s.MS)/1000))
		}
	}
	parts = append(parts, fmt.Sprintf("total %.1fs", float64(total)/1000))
	return strings.Join(parts, " | ")
}

// runLog appends one JSON line per event to data/out/taxonomy-run.jsonl —
// the machine-readable mirror of the terminal narration, for tail -f.
type runLog struct{ f *os.File }

func newRunLog(path string) (*runLog, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &runLog{f: f}, nil
}

func (l *runLog) event(kind string, detail any) {
	line := map[string]any{"at": time.Now().Format(time.RFC3339), "event": kind, "detail": detail}
	b, err := json.Marshal(line)
	if err != nil {
		return
	}
	l.f.Write(append(b, '\n'))
}

func (l *runLog) close() { l.f.Close() }
