// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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

	// Create config with one rule
	cfg := &rules.Config{
		Topics: []rules.Topic{
			{ID: "dev/talks", Desc: "conference talks"},
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
