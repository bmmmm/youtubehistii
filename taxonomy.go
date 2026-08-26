// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
)

func cmdTaxonomy(args []string) error {
	fs, dataDir := newFlagSet("taxonomy")
	rulesPath := fs.String("rules", "", "rules file (default: config/rules.yaml, falling back to config/rules.example.yaml)")
	embedModel := fs.String("embed-model", "bge-m3-mlx-fp16", "embedding model on the oMLX server (multilingual, so chess and schach meet)")
	fine := fs.Float64("fine", 0.35, "cosine-distance threshold for subjects (smaller = more, tighter clusters)")
	coarse := fs.Float64("coarse", 0.60, "cosine-distance threshold for the top level")
	minVideos := fs.Int("min-videos", 3, "fold subjects with fewer unique videos into their nearest neighbor")
	maxRadius := fs.Float64("max-radius", 0.50, "split subjects wider than this in refinement rounds")
	rounds := fs.Int("rounds", 5, "refinement rounds at most; the control file can stop earlier")
	tailN := fs.Int("tail-n", 1, "the tail metric counts subjects with at most this many videos")
	noLLM := fs.Bool("no-llm", false, "name clusters from their strongest member instead of asking the chat model")
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

	// Embed: one vector per label, batched, cached under data/cache/embed so
	// the second run is free.
	vecs, fresh, err := embedLabels(client, *embedModel, labels, p.embedCacheDir())
	if err != nil {
		return err
	}
	log.event("embed", map[string]any{"model": *embedModel, "fresh": fresh, "cached": len(labels) - fresh})
	fmt.Printf("embedded %d labels (%d fresh, %d from cache)\n", len(labels), fresh, len(labels)-fresh)

	namer := newNamer(client, *noLLM, log)
	opts := taxonomy.RefineOpts{
		SplitAt:    *fine * 0.7,
		MaxRadius:  *maxRadius,
		MergeBelow: *fine * 0.6,
		MinVideos:  *minVideos,
	}

	// Round 1: cluster, fold the tail, name everything, group into tops.
	ctl, err := taxonomy.LoadControl(controlPath)
	if err != nil {
		return err
	}
	subjects := taxonomy.ClusterLabels(labels, vecs, *fine)
	fmt.Printf("clustered into %d subjects at threshold %.2f\n", len(subjects), *fine)
	subjects = taxonomy.FoldSmall(subjects, *minVideos, ctl.KeepSet())
	nameSubjects(subjects, namer, true)
	subjects = taxonomy.MergeSameNames(subjects)
	assignParents(subjects, *coarse, namer)

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
		assignParents(subjects, *coarse, namer)

		m := taxonomy.Measure(subjects, *tailN)
		log.event("round", map[string]any{"n": round, "metrics": m, "changes": changes})
		fmt.Printf("round %d    %s\n", round, metricsLine(m))
		for _, c := range biggestChanges(changes, 8) {
			fmt.Printf("  %s\n", c)
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
	log.event("write", map[string]any{"path": taxonomyPath, "subjects": len(subjects), "tops": last.Tops})
	fmt.Printf("wrote %s (%d subjects under %d top levels)\n", taxonomyPath, len(subjects), last.Tops)
	fmt.Println("compare with: youtubehistii report -taxonomy / watchpath -taxonomy")
	return nil
}

// probeRun is step 0 made executable: whether the models are there, what one
// embedding batch and one chat request cost, and what that means for the
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
	var embedBatch time.Duration
	t0 := time.Now()
	if _, err := client.Embed(embedModel, texts); err != nil {
		fmt.Printf("embed: FAILED — %v\n", err)
	} else {
		embedBatch = time.Since(t0)
		fmt.Printf("embed: 32 texts in %s (%.0f ms/text)\n", embedBatch.Round(time.Millisecond), float64(embedBatch.Milliseconds())/32)
	}

	var chatReq time.Duration
	t0 = time.Now()
	if _, err := client.ChatMax("Reply with the single word: ok", "ok?", 8); err != nil {
		fmt.Printf("chat:  FAILED — %v\n", err)
	} else {
		chatReq = time.Since(t0)
		fmt.Printf("chat:  1 request in %s\n", chatReq.Round(time.Millisecond))
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
	fmt.Printf("estimate for %d labels: embeddings ≈ %s, naming (at ~300 clusters, 5 rounds worst case) ≈ %s\n",
		nLabels, embedCost.Round(time.Second), (5 * nameCost).Round(time.Second))
	fmt.Printf("hour budget holds: %v\n", embedCost+5*nameCost < time.Hour)
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

// newNamer returns the cluster-naming function: one chat request per cluster,
// falling back to the strongest member's sub on any model trouble. kind is
// "subject" or "top" and only changes the prompt's altitude.
func newNamer(client *omlx.Client, noLLM bool, log *runLog) func(c taxonomy.Cluster, kind string) string {
	warned := false
	return func(c taxonomy.Cluster, kind string) string {
		fallback := taxonomy.FallbackName(c)
		if noLLM {
			return fallback
		}
		altitude := "the cluster's one shared subject, as specific as the members allow"
		if kind == "top" {
			altitude = "the broad top-level category the members share, like a site section"
		}
		system := "You name clusters of YouTube watch-history topic labels.\n" +
			"Reply with EXACTLY one short lowercase slug (a-z, 0-9, dashes; at most three words " +
			"joined by dashes) naming " + altitude + ".\n" +
			"Prefer reusing a member label's name when it already covers the whole cluster. No prose."
		var u strings.Builder
		u.WriteString("members:\n")
		for i, l := range c.Members {
			if i == 12 {
				fmt.Fprintf(&u, "  … and %d more\n", len(c.Members)-i)
				break
			}
			fmt.Fprintf(&u, "  %s (%d views)\n", l.Topic(), l.Views)
		}
		if ch := c.TopChannels(5); len(ch) > 0 {
			u.WriteString("channels: " + strings.Join(ch, ", ") + "\n")
		}
		reply, err := client.ChatMax(system, u.String(), 24)
		if err != nil {
			if !warned {
				fmt.Fprintf(os.Stderr, "warning: naming via LLM failed (%v) — falling back to member names\n", err)
				warned = true
			}
			log.event("name-fallback", map[string]any{"cluster": fallback, "error": err.Error()})
			return fallback
		}
		slug := rules.SlugifySub(reply)
		if slug == "" {
			return fallback
		}
		return slug
	}
}

// nameSubjects names clusters: all of them on the first pass, only unnamed
// pieces (fresh splits) afterwards — merges keep their names.
func nameSubjects(cs []taxonomy.Cluster, namer func(taxonomy.Cluster, string) string, all bool) {
	for i := range cs {
		if all || cs[i].Name == "" {
			cs[i].Name = namer(cs[i], "subject")
		}
	}
}

// assignParents groups subjects into top levels and names each group. Two
// groups landing on the same name simply ARE one top level — nothing to
// dedupe.
func assignParents(cs []taxonomy.Cluster, coarse float64, namer func(taxonomy.Cluster, string) string) {
	for _, group := range taxonomy.Coarse(cs, coarse) {
		// The group's strongest subject carries the naming prompt: its
		// members already hold the labels and channels that describe it.
		strongest := group[0]
		for _, i := range group {
			if cs[i].Views > cs[strongest].Views {
				strongest = i
			}
		}
		top := namer(cs[strongest], "top")
		for _, i := range group {
			cs[i].Parent = top
		}
	}
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
