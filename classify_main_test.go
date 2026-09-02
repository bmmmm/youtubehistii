// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/rules"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

func TestClassifyPassRulesOnly(t *testing.T) {
	// Setup temporary data directory
	dataDir := t.TempDir()
	p := paths{dataDir: dataDir}

	// Create config with one rule. "dev" is the AREA; "dev/talks" below is
	// area+sub, which is what a rule and a verdict carry — an id with a "/"
	// in it is not a valid area and Load rejects one.
	cfg := &rules.Config{
		Topics: []rules.Topic{
			{ID: "dev", Desc: "conference talks"},
			{ID: "unclear", Desc: "cannot tell"},
		},
		Rules: []rules.Rule{
			{
				ID:       "talks",
				TitleAny: []string{"keynote"},
				Topic:    "dev/talks",
				Mode:     "learn",
			},
		},
	}

	// Create test views
	views := []takeout.View{
		{
			VideoID:   "aaaaaaaaaa1",
			Title:     "GopherCon keynote",
			Channel:   "Gopher Academy",
			WatchedAt: time.Now(),
		},
		{
			VideoID:   "aaaaaaaaaa1",
			Title:     "GopherCon keynote",
			Channel:   "Gopher Academy",
			WatchedAt: time.Now().Add(1 * time.Hour),
		},
		{
			VideoID:   "bbbbbbbbbb2",
			Title:     "Random Video",
			Channel:   "Some Channel",
			WatchedAt: time.Now(),
		},
		{
			VideoID:   "cccccccccc3",
			Title:     "Another Video",
			Channel:   "Other Channel",
			WatchedAt: time.Now(),
		},
		{
			VideoID:   "",
			Title:     "Video with no ID",
			Channel:   "Mystery",
			WatchedAt: time.Now(),
		},
	}

	// Create metas: only the cached LLM verdict video
	metas := map[string]enrich.Meta{
		"cccccccccc3": {
			ID:      "cccccccccc3",
			Title:   "Another Video",
			Channel: "Other Channel",
		},
	}

	// Create cached verdicts for one video
	cached := map[string]classify.LLMVerdict{
		"cccccccccc3": {
			Topic:      "dev/talks",
			Mode:       "learn",
			Confidence: 0.8,
			Model:      "test-model",
			Basis:      classify.BasisFull,
		},
	}

	// Run classifyPass
	st, err := classifyPass(p, cfg, views, metas, cached, classifyOpts{
		noLLM:    true,
		progress: false,
	})
	if err != nil {
		t.Fatalf("classifyPass failed: %v", err)
	}

	// Verify stats
	if st.ruleHits != 1 {
		t.Errorf("ruleHits = %d, want 1", st.ruleHits)
	}
	if st.waiting != 1 {
		t.Errorf("waiting = %d, want 1 (bbbbbbbbbb2)", st.waiting)
	}
	if st.classified != 2 {
		t.Errorf("classified = %d, want 2 (rule hit + cached, not waiting)", st.classified)
	}
	if st.llmNew != 0 {
		t.Errorf("llmNew = %d, want 0", st.llmNew)
	}

	// Verify classified.jsonl was written
	verdicts, err := readJSONL[classify.Verdict](p.classifiedJSONL())
	if err != nil {
		t.Fatalf("read classified.jsonl: %v", err)
	}
	if len(verdicts) != 5 {
		t.Fatalf("expected 5 verdict rows (5 views), got %d", len(verdicts))
	}

	// Check sources
	sourceCount := make(map[string]int)
	for _, v := range verdicts {
		sourceCount[v.Source]++
	}

	if sourceCount["rule:talks"] != 2 {
		t.Errorf("rule:talks count = %d, want 2 (2 views of same video)", sourceCount["rule:talks"])
	}
	if sourceCount["llm:test-model"] != 1 {
		t.Errorf("llm:test-model count = %d, want 1", sourceCount["llm:test-model"])
	}
	if sourceCount["unclassified"] != 2 {
		t.Errorf("unclassified count = %d, want 2 (bbbbbbbbbb2 + empty ID)", sourceCount["unclassified"])
	}

	// Verify verdict details
	for _, v := range verdicts {
		switch v.VideoID {
		case "aaaaaaaaaa1":
			if v.Source != "rule:talks" || v.Topic != "dev/talks" || v.Mode != "learn" {
				t.Errorf("aaaaaaaaaa1: got Source=%s Topic=%s Mode=%s", v.Source, v.Topic, v.Mode)
			}
		case "bbbbbbbbbb2":
			if v.Source != "unclassified" || v.Topic != "unclear" {
				t.Errorf("bbbbbbbbbb2: got Source=%s Topic=%s", v.Source, v.Topic)
			}
		case "cccccccccc3":
			if v.Source != "llm:test-model" || v.Topic != "dev/talks" || v.Mode != "learn" || v.Confidence != 0.8 {
				t.Errorf("cccccccccc3: got Source=%s Topic=%s Mode=%s Confidence=%v", v.Source, v.Topic, v.Mode, v.Confidence)
			}
		case "":
			if v.Source != "unclassified" {
				t.Errorf("empty VideoID: got Source=%s", v.Source)
			}
		}
	}
}

func TestLoadNewCacheEntries(t *testing.T) {
	dir := t.TempDir()

	// Write initial files
	entry1 := map[string]interface{}{"topic": "dev/talks", "mode": "learn", "confidence": 0.9}
	entry2 := map[string]interface{}{"topic": "gaming/rust", "mode": "consume", "confidence": 0.7}

	writeTestJSON(t, filepath.Join(dir, "a.json"), entry1)
	writeTestJSON(t, filepath.Join(dir, "b.json"), entry2)

	// First call: load both files
	seen := make(map[string]bool)
	result1, err := loadNewCacheEntries[map[string]interface{}](dir, seen)
	if err != nil {
		t.Fatalf("first load failed: %v", err)
	}
	if len(result1) != 2 {
		t.Errorf("first load: got %d entries, want 2", len(result1))
	}
	if len(seen) != 2 {
		t.Errorf("first load: got %d seen entries, want 2", len(seen))
	}

	// Add a new file
	entry3 := map[string]interface{}{"topic": "unclear", "mode": "mixed", "confidence": 0.5}
	writeTestJSON(t, filepath.Join(dir, "c.json"), entry3)

	// Second call with same seen map: load only new file
	result2, err := loadNewCacheEntries[map[string]interface{}](dir, seen)
	if err != nil {
		t.Fatalf("second load failed: %v", err)
	}
	if len(result2) != 1 {
		t.Errorf("second load: got %d entries, want 1 (only c.json)", len(result2))
	}
	if _, ok := result2["c"]; !ok {
		t.Errorf("second load: c.json not found in result")
	}
	if len(seen) != 3 {
		t.Errorf("second load: got %d seen entries, want 3", len(seen))
	}

	// Write a corrupted file
	brokenFile := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(brokenFile, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Third call: corrupt file is skipped
	result3, err := loadNewCacheEntries[map[string]interface{}](dir, seen)
	if err != nil {
		t.Fatalf("third load (with corrupt) failed: %v", err)
	}
	if len(result3) != 0 {
		t.Errorf("third load: got %d entries, want 0 (broken.json skipped, no new valid files)", len(result3))
	}

	// Fix the corrupt file
	writeTestJSON(t, brokenFile, entry3)

	// Fourth call: now the fixed file is loaded
	result4, err := loadNewCacheEntries[map[string]interface{}](dir, seen)
	if err != nil {
		t.Fatalf("fourth load (after fix) failed: %v", err)
	}
	if len(result4) != 1 {
		t.Errorf("fourth load: got %d entries, want 1 (broken.json now fixed)", len(result4))
	}
	if _, ok := result4["broken"]; !ok {
		t.Errorf("fourth load: broken.json not found in result after fix")
	}

	// Verify a and b are never loaded again even in fresh seen map
	freshSeen := make(map[string]bool)
	result5, err := loadNewCacheEntries[map[string]interface{}](dir, freshSeen)
	if err != nil {
		t.Fatalf("fifth load (fresh seen) failed: %v", err)
	}
	if len(result5) != 4 {
		t.Errorf("fifth load with fresh seen: got %d entries, want 4 (all files)", len(result5))
	}
}

// writeTestJSON writes a JSON object to a file for testing.
func writeTestJSON(t *testing.T, path string, obj interface{}) {
	b, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatalf("write JSON file: %v", err)
	}
}

func TestCollectSubSeeds(t *testing.T) {
	cfg := &rules.Config{Topics: []rules.Topic{
		{ID: "gaming"}, {ID: "dev"}, {ID: "unclear"},
	}}
	seeds := collectSubSeeds(cfg, []string{
		"gaming/rust", "gaming/rust", "gaming/cs2",
		"gaming", // a bare area contributes no sub
		"dev/talks", "unclear",
	}, nil)
	if got := seeds["gaming"]; len(got) != 2 || got[0] != "rust" || got[1] != "cs2" {
		t.Errorf("gaming seeds = %v, want [rust cs2] — most used first", got)
	}
	if got := seeds["dev"]; len(got) != 1 || got[0] != "talks" {
		t.Errorf("dev seeds = %v, want [talks]", got)
	}
	if _, ok := seeds["unclear"]; ok {
		t.Errorf("unclear must contribute no seeds, got %v", seeds["unclear"])
	}

	// Equal counts sort by name: a prompt that reshuffles between runs
	// invites the model to reshuffle its answers with it.
	if got := collectSubSeeds(cfg, []string{"gaming/zelda", "gaming/aoe"}, nil)["gaming"]; got[0] != "aoe" {
		t.Errorf("tie-break = %v, want the name to decide", got)
	}

	// What "-retry topic:<t>" needs: the named topic leaves the seeds, its
	// area's other subs stay. Otherwise the re-ask offers the catch-all sub
	// back to the model and buys the same answer again (temperature 0).
	dropped := collectSubSeeds(cfg, []string{
		"gaming/rust", "gaming/cs2", "dev/talks",
	}, []string{"gaming/cs2"})
	if got := dropped["gaming"]; len(got) != 1 || got[0] != "rust" {
		t.Errorf("gaming seeds = %v, want the dropped sub gone and rust kept", got)
	}
	if got := dropped["dev"]; len(got) != 1 || got[0] != "talks" {
		t.Errorf("dev seeds = %v, want an untouched area untouched", got)
	}

	// Bounded, so the prompt does not grow with the corpus.
	var many []string
	for i := 0; i < subSeedsPerArea*3; i++ {
		many = append(many, fmt.Sprintf("gaming/sub%02d", i))
	}
	if got := collectSubSeeds(cfg, many, nil)["gaming"]; len(got) != subSeedsPerArea {
		t.Errorf("seeds = %d, want them capped at %d", len(got), subSeedsPerArea)
	}
}

// TestClassifyPassAreaFromCategory is the end-to-end proof of the redesign:
// with -no-llm (no model anywhere near this test) the YouTube category alone
// must decide the area, a rule must still win over it, and a video without a
// category must NOT be given one.
func TestClassifyPassAreaFromCategory(t *testing.T) {
	p := paths{dataDir: t.TempDir()}
	cfg, err := rules.Load("config/rules.example.yaml")
	if err != nil {
		t.Fatal(err)
	}

	views := []takeout.View{
		{VideoID: "catgaming01", Title: "boss fight", WatchedAt: time.Now()},
		{VideoID: "catmusic002", Title: "chill vibes mix", WatchedAt: time.Now()},
		{VideoID: "catsciteq03", Title: "how transistors work", WatchedAt: time.Now()},
		{VideoID: "rulewins004", Title: "CS2 clutch", WatchedAt: time.Now()},
		{VideoID: "tombstone05", Title: "gone from youtube", WatchedAt: time.Now()},
	}
	metas := map[string]enrich.Meta{
		"catgaming01": {ID: "catgaming01", Categories: []string{"Gaming"}},
		"catmusic002": {ID: "catmusic002", Categories: []string{"Music"}},
		"catsciteq03": {ID: "catsciteq03", Categories: []string{"Science & Technology"}},
		// Same category as the first one, but a rule matches the title and a
		// rule ends the classification — including the sub it names.
		"rulewins004": {ID: "rulewins004", Categories: []string{"Gaming"}},
		"tombstone05": {ID: "tombstone05", Unavailable: true},
	}

	if _, err := classifyPass(p, cfg, views, metas, map[string]classify.LLMVerdict{},
		classifyOpts{noLLM: true}); err != nil {
		t.Fatal(err)
	}
	rows, err := readJSONL[classify.Verdict](p.classifiedJSONL())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]classify.Verdict{}
	for _, r := range rows {
		got[r.VideoID] = r
	}

	for id, want := range map[string]string{
		"catgaming01": "gaming",
		"catmusic002": "music",
		"catsciteq03": "science-technology",
		"rulewins004": "gaming/cs2",
	} {
		if got[id].Topic != want {
			t.Errorf("%s: topic %q, want %q", id, got[id].Topic, want)
		}
	}

	// The rule keeps naming its source; the category path does not pretend to
	// be a rule, and with the LLM off it has no verdict of its own yet.
	if src := got["rulewins004"].Source; src != "rule:cs2" {
		t.Errorf("rule source = %q, want %q", src, "rule:cs2")
	}
	if src := got["catgaming01"].Source; src != "category" {
		t.Errorf("category-only source = %q, want %q — the area holds without "+
			"the LLM, and the source has to say that sub and mode are still open", src, "category")
	}
	if mode := got["catgaming01"].Mode; mode != "" {
		t.Errorf("category-only mode = %q — the mode is not derivable from a category", mode)
	}

	// No category, no area: a tombstone must not be guessed into a bucket.
	if topic := got["tombstone05"].Topic; topic != "unclear" {
		t.Errorf("tombstone topic = %q, want %q", topic, "unclear")
	}
}

// TestCachedVerdictNeverOutvotesTheCategory covers what a -no-llm run on real
// data exposed: verdicts cached under an older taxonomy carry area names that
// no longer exist ("politics" for today's "news-politics"), and they were
// kept as a stopgap — outvoting the area the YouTube category gives for free
// and putting dead labels into the report.
func TestCachedVerdictNeverOutvotesTheCategory(t *testing.T) {
	p := paths{dataDir: t.TempDir()}
	cfg, err := rules.Load("config/rules.example.yaml")
	if err != nil {
		t.Fatal(err)
	}

	views := []takeout.View{
		{VideoID: "deadareaid1", Title: "Bundestag debate", WatchedAt: time.Now()},
		{VideoID: "deadarea002", Title: "long gone", WatchedAt: time.Now()},
		{VideoID: "wrongarea03", Title: "some mix", WatchedAt: time.Now()},
	}
	metas := map[string]enrich.Meta{
		"deadareaid1": {ID: "deadareaid1", Categories: []string{"News & Politics"}},
		"deadarea002": {ID: "deadarea002", Unavailable: true},
		"wrongarea03": {ID: "wrongarea03", Categories: []string{"Music"}},
	}
	cached := map[string]classify.LLMVerdict{
		// Area from a taxonomy that no longer exists, sub still meaningful.
		"deadareaid1": {Topic: "politics/bundestag", Mode: "learn", Model: "old"},
		// Same, but nothing to fall back on — no category either.
		"deadarea002": {Topic: "politics/bundestag", Mode: "learn", Model: "old"},
		// Area exists, but contradicts the category.
		"wrongarea03": {Topic: "gaming/rust", Mode: "consume", Model: "old"},
	}

	if _, err := classifyPass(p, cfg, views, metas, cached, classifyOpts{noLLM: true}); err != nil {
		t.Fatal(err)
	}
	rows, err := readJSONL[classify.Verdict](p.classifiedJSONL())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]classify.Verdict{}
	for _, r := range rows {
		got[r.VideoID] = r
	}

	// The category wins the area, and the sub goes with the area it was
	// judged under: "bundestag" was the answer to "which subject within
	// politics", and nothing says it is the right answer within a different
	// area. A re-ask is queued, so this is a stopgap either way — one that
	// keeps quiet rather than one that makes something up.
	if topic := got["deadareaid1"].Topic; topic != "news-politics" {
		t.Errorf("dead area + category: topic %q, want %q", topic, "news-politics")
	}
	// No category to rescue it — better open than a label that names nothing.
	if topic := got["deadarea002"].Topic; topic != "unclear" {
		t.Errorf("dead area, no category: topic %q, want %q", topic, "unclear")
	}
	// A live but contradicting area loses too, and takes its sub along —
	// "music/rust" would be exactly the nonsense this guards against.
	if topic := got["wrongarea03"].Topic; topic != "music" {
		t.Errorf("category vs. cached area: topic %q, want %q", topic, "music")
	}
}

// TestCachedSubSurvivesAnUnchangedArea is the other half: a taxonomy edit that
// leaves an area alone (a reworded desc, an added alias) must not throw away
// the subs judged under it — only a CHANGED area invalidates them.
func TestCachedSubSurvivesAnUnchangedArea(t *testing.T) {
	p := paths{dataDir: t.TempDir()}
	cfg, err := rules.Load("config/rules.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	views := []takeout.View{{VideoID: "keepthesub1", Title: "raid night", WatchedAt: time.Now()}}
	metas := map[string]enrich.Meta{"keepthesub1": {ID: "keepthesub1", Categories: []string{"Gaming"}}}
	cached := map[string]classify.LLMVerdict{
		"keepthesub1": {Topic: "gaming/factorio", Mode: "consume", Model: "old"},
	}
	if _, err := classifyPass(p, cfg, views, metas, cached, classifyOpts{noLLM: true}); err != nil {
		t.Fatal(err)
	}
	rows, err := readJSONL[classify.Verdict](p.classifiedJSONL())
	if err != nil {
		t.Fatal(err)
	}
	if topic := rows[0].Topic; topic != "gaming/factorio" {
		t.Errorf("topic %q, want %q — the area did not change, so the sub stands", topic, "gaming/factorio")
	}
}

// TestCollectSubSeedsSkipsDeadAreas covers what a 200-video probe exposed:
// the verdict cache still holds topics from older taxonomies, and seeding
// their areas puts names into the prompt that the area list does not contain.
// The model then reuses them one level down — "dev" was an area back then and
// came back as a sub under music, education and science-technology alike.
func TestCollectSubSeedsSkipsDeadAreas(t *testing.T) {
	cfg, err := rules.Load("config/rules.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	seeds := collectSubSeeds(cfg, []string{
		"gaming/rust", "gaming/rust", "gaming/cs2",
		"dev/tutorials",    // "dev" was an area two taxonomies ago
		"security/malware", // so was "security"
		"music",            // an area with no sub seeds nothing
		"Gaming/AOE",       // case folds into the canonical area and sub
	}, nil)
	if _, ok := seeds["dev"]; ok {
		t.Errorf("dead area seeded into the prompt: %v", seeds)
	}
	if _, ok := seeds["security"]; ok {
		t.Errorf("dead area seeded into the prompt: %v", seeds)
	}
	want := []string{"rust", "aoe", "cs2"} // by count desc, then name
	got := seeds["gaming"]
	if len(got) != len(want) {
		t.Fatalf("gaming seeds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("gaming seeds = %v, want %v (order is part of it)", got, want)
		}
	}
}

// TestPassStatsCountsOnlyWhatRetryCanReach pins the counters to retryTargets'
// own predicate. A "next: N with an area but no sub" that overstates sends
// someone into a five-hour re-ask for videos the selector will not pick, and
// the run afterwards looks broken rather than finished. Four shapes, four
// different reasons to count or not:
//
//   - an LLM verdict with an area and no sub: countable
//   - the same, but already carrying the "sub" marker: asked once, done
//   - a verdict a RULE produced: -retry never looks at rules
//   - a video with no verdict at all, area from its YouTube category: its own
//     bucket, because no selector re-asks it — enrich does
func TestPassStatsCountsOnlyWhatRetryCanReach(t *testing.T) {
	p := paths{dataDir: t.TempDir()}
	cfg, err := rules.Load("config/rules.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	views := []takeout.View{
		{VideoID: "nosubvideo1", Title: "some music", WatchedAt: time.Now()},
		{VideoID: "askedalready", Title: "more music", WatchedAt: time.Now()},
		{VideoID: "nomodevideo1", Title: "third music", WatchedAt: time.Now()},
		{VideoID: "categoryonly", Title: "fourth music", WatchedAt: time.Now()},
	}
	metas := map[string]enrich.Meta{
		"nosubvideo1":  {ID: "nosubvideo1", Categories: []string{"Music"}},
		"askedalready": {ID: "askedalready", Categories: []string{"Music"}},
		"nomodevideo1": {ID: "nomodevideo1", Categories: []string{"Music"}},
		"categoryonly": {ID: "categoryonly", Categories: []string{"Music"}},
	}
	cached := map[string]classify.LLMVerdict{
		"nosubvideo1":  {Topic: "music", Mode: "consume", Model: "m"},
		"askedalready": {Topic: "music", Mode: "consume", Model: "m", Retried: []string{"sub"}},
		"nomodevideo1": {Topic: "music/jazz", Model: "m"},
		// "categoryonly" has no cached verdict at all.
	}

	st, err := classifyPass(p, cfg, views, metas, cached, classifyOpts{noLLM: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.noSub != 1 {
		t.Errorf("noSub = %d, want 1 — the marked one was already asked, the category-only one is not a model's verdict", st.noSub)
	}
	if st.noMode != 1 {
		t.Errorf("noMode = %d, want 1", st.noMode)
	}
	if st.categoryOnly != 1 {
		t.Errorf("categoryOnly = %d, want 1", st.categoryOnly)
	}
}

func TestNextLineNamesOnlyTheSelectorsItCanFill(t *testing.T) {
	// Nothing left: the tail still names the stages that come next, because
	// they come next either way.
	empty := passStats{}.nextLine()
	for _, unwanted := range []string{"-retry no-sub", "-retry no-mode", "-include-unenriched"} {
		if strings.Contains(empty, unwanted) {
			t.Errorf("a pass with nothing outstanding still offered %q: %s", unwanted, empty)
		}
	}
	for _, want := range []string{"taxonomy", "watchpath -taxonomy"} {
		if !strings.Contains(empty, want) {
			t.Errorf("the tail does not name %q: %s", want, empty)
		}
	}

	full := passStats{noSub: 3, noMode: 4, categoryOnly: 5}.nextLine()
	for _, want := range []string{"3 with an area but no sub", "classify -retry no-sub",
		"4 without a mode", "classify -retry no-mode", "5 carry only their category's area",
		"-include-unenriched"} {
		if !strings.Contains(full, want) {
			t.Errorf("the line does not carry %q: %s", want, full)
		}
	}

	// One clause at a time, so a count of zero cannot smuggle in a selector
	// that would select nothing.
	if line := (passStats{noSub: 1}).nextLine(); strings.Contains(line, "no-mode") {
		t.Errorf("a pass with no missing modes offered -retry no-mode: %s", line)
	}
	if line := (passStats{noMode: 1}).nextLine(); strings.Contains(line, "no-sub") {
		t.Errorf("a pass with no missing subs offered -retry no-sub: %s", line)
	}
}

// TestModelDriftWarnsOnlyWhenTheJudgeChanged: a verdict's cache key carries
// the taxonomy fingerprint, not the model, so pointing the config at another
// model invalidates nothing. The old judge's verdicts stay and the corpus
// becomes two judges' work — which is a decision, not a defect, and so has to
// be said out loud rather than fixed by re-asking 28k videos.
func TestModelDriftWarnsOnlyWhenTheJudgeChanged(t *testing.T) {
	cached := map[string]classify.LLMVerdict{
		"a": {Model: "old-judge"},
		"b": {Model: "old-judge"},
		"c": {Model: "new-judge"},
		// No model recorded: predates the field, and counting it would put a
		// permanent number under a warning about a change nobody made.
		"d": {},
	}
	drift := modelDrift(cached, "new-judge")
	if len(drift) != 1 || len(drift["old-judge"]) != 2 {
		t.Fatalf("modelDrift = %v, want one entry old-judge with 2 ids", drift)
	}
	line := modelDriftLine(drift, "new-judge")
	for _, want := range []string{"old-judge 2", "new-judge", `abtest -model new-judge`} {
		if !strings.Contains(line, want) {
			t.Errorf("the line misses %q: %s", want, line)
		}
	}
	// Never -retry all: that re-asks DEFECTS, and these verdicts have none.
	// They are complete answers from a different judge.
	if strings.Contains(line, "-retry") {
		t.Errorf("the line offers a retry selector that would select nothing: %s", line)
	}

	// One judge, no drift, no line — a warning that fires on the normal case
	// is a warning people learn to scroll past.
	same := modelDrift(map[string]classify.LLMVerdict{"a": {Model: "one"}, "b": {}}, "one")
	if len(same) != 0 {
		t.Errorf("modelDrift on an unchanged model = %v, want empty", same)
	}
	if got := modelDriftLine(same, "one"); got != "" {
		t.Errorf("an unchanged model still warned: %q", got)
	}
}

// TestModelDriftNamesTheStraysWhenThereAreFew: a count says a foreign judge
// is in the corpus, an ID says which cache file to delete. Naming them is only
// an instruction while the list is short — past driftNameLimit it is a wall,
// so the line falls back to the count alone.
func TestModelDriftNamesTheStraysWhenThereAreFew(t *testing.T) {
	verdicts := func(model string, ids ...string) map[string]classify.LLMVerdict {
		out := map[string]classify.LLMVerdict{"kept": {Model: "new-judge"}}
		for _, id := range ids {
			out[id] = classify.LLMVerdict{Model: model}
		}
		return out
	}

	few := []string{"vidC", "vidA", "vidB"}
	line := modelDriftLine(modelDrift(verdicts("old-judge", few...), "new-judge"), "new-judge")
	for _, id := range few {
		if !strings.Contains(line, id) {
			t.Errorf("three strays and %q is not named: %s", id, line)
		}
	}
	// Sorted, because the line ends up in a terminal people compare between
	// runs and a map would reorder it for free.
	if !strings.Contains(line, "old-judge: vidA vidB vidC") {
		t.Errorf("the ids are not named in a stable order: %s", line)
	}

	many := make([]string, 0, driftNameLimit+1)
	for i := 0; i <= driftNameLimit; i++ {
		many = append(many, fmt.Sprintf("vid%02d", i))
	}
	wall := modelDriftLine(modelDrift(verdicts("old-judge", many...), "new-judge"), "new-judge")
	if !strings.Contains(wall, fmt.Sprintf("old-judge %d", len(many))) {
		t.Errorf("the count is gone above the limit: %s", wall)
	}
	for _, id := range many {
		if strings.Contains(wall, id) {
			t.Fatalf("%d strays and the line still names %q: %s", len(many), id, wall)
		}
	}

	// No drift, no line — naming nothing must not turn an empty warning into
	// a pair of brackets.
	if got := modelDriftLine(modelDrift(verdicts("new-judge"), "new-judge"), "new-judge"); got != "" {
		t.Errorf("an unchanged model produced a line: %q", got)
	}
}

// TestModelDriftIsSilentWithoutTheLLM: -no-llm means no model is going to be
// asked anything, so which judge produced the cache is not a decision anybody
// is making on this run.
func TestModelDriftIsSilentWithoutTheLLM(t *testing.T) {
	p := paths{dataDir: t.TempDir()}
	cfg, err := rules.Load("config/rules.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	views := []takeout.View{{VideoID: "driftvideo1", Title: "some music", WatchedAt: time.Now()}}
	if err := writeJSONL(p.historyJSONL(), views); err != nil {
		t.Fatal(err)
	}
	cache := classify.Cache{Dir: p.classifyCache()}
	if err := cache.Write("driftvideo1", classify.LLMVerdict{
		Topic: "music/jazz", Mode: "consume", Model: "a-different-judge", Taxonomy: cfg.Fingerprint(),
	}); err != nil {
		t.Fatal(err)
	}

	stderr, err := captureStderr(t, func() error {
		return cmdClassify([]string{"-data", p.dataDir, "-rules", "config/rules.example.yaml", "-no-llm"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "model changed") {
		t.Errorf("-no-llm still warned about the judge:\n%s", stderr)
	}
}

// TestModelDriftWarnsFromTheCommand: the pure function above is only worth
// having if the command actually reaches it. The rules file points at a host
// that does not resolve, so the warning cannot be coming from anything the
// server said — and the LLM stage going down must not swallow it either.
func TestModelDriftWarnsFromTheCommand(t *testing.T) {
	p := paths{dataDir: t.TempDir()}
	rulesPath := filepath.Join(p.dataDir, "rules.yaml")
	rulesYAML := "llm:\n  model: the-new-judge\n  base_url: http://offline.invalid/v1\n" +
		"topics:\n  - id: music\n    desc: music\n  - id: unclear\n    desc: cannot tell\n"
	if err := os.WriteFile(rulesPath, []byte(rulesYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONL(p.historyJSONL(), []takeout.View{
		{VideoID: "driftvideo2", Title: "some music", WatchedAt: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	cache := classify.Cache{Dir: p.classifyCache()}
	if err := cache.Write("driftvideo2", classify.LLMVerdict{
		Topic: "music/jazz", Mode: "consume", Model: "the-old-judge", Taxonomy: cfg.Fingerprint(),
	}); err != nil {
		t.Fatal(err)
	}

	stderr, err := captureStderr(t, func() error {
		_, runErr := captureStdout(t, func() error {
			return cmdClassify([]string{"-data", p.dataDir, "-rules", rulesPath})
		})
		return runErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "model changed (the-old-judge 1 → the-new-judge)") {
		t.Errorf("the command never warned about the other judge:\n%s", stderr)
	}
}
