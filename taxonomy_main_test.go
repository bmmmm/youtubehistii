// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/taxonomy"
)

// fakeOMLX serves just enough of the OpenAI surface for a taxonomy run:
// a model list, embeddings with two hand-made axes (jazz vs. chess — the
// fixtures are illustrations, not anyone's history), and a namer whose
// answers depend on the prompt's altitude.
func fakeOMLX(t *testing.T, chatCalls *int) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"test-chat"},{"id":"test-embed"}]}`)
	})
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("embeddings body: %v", err)
		}
		type datum struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var out struct {
			Data []datum `json:"data"`
		}
		for i, text := range req.Input {
			vec := []float32{0.01, 0.01, 0.01}
			if strings.Contains(text, "jazz") {
				vec[0] = 1
			}
			if strings.Contains(text, "chess") || strings.Contains(text, "schach") {
				vec[1] = 1 // both spellings of one game land on one axis
			}
			out.Data = append(out.Data, datum{Index: i, Embedding: vec})
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		*chatCalls++
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("chat body: %v", err)
		}
		system, user := req.Messages[0].Content, req.Messages[1].Content
		name := "jazz"
		if strings.Contains(user, "chess") || strings.Contains(user, "schach") {
			name = "chess"
		}
		if strings.Contains(system, "top-level") {
			name += "-top"
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, name)
	})
	return httptest.NewServer(mux)
}

func TestCmdTaxonomyEndToEnd(t *testing.T) {
	var chatCalls int
	srv := fakeOMLX(t, &chatCalls)
	defer srv.Close()

	// The taxonomy and its run log land relative to the working directory,
	// exactly like config/rules.yaml — so the test runs in its own.
	t.Chdir(t.TempDir())
	dataDir := t.TempDir()

	rulesPath := filepath.Join(dataDir, "rules.yaml")
	rulesYAML := "llm:\n  model: test-chat\n  base_url: " + srv.URL + "/v1\n"
	if err := os.WriteFile(rulesPath, []byte(rulesYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Today's mess in miniature: one subject scattered over two areas, one
	// game with two language spellings, one unclear view that must stay out.
	rows := []classify.Verdict{
		{VideoID: "v1", Topic: "music/jazz", Channel: "jazzchan", Title: "jazz set one", WatchedAt: time.Now()},
		{VideoID: "v2", Topic: "music/jazz", Channel: "jazzchan", Title: "jazz set two", WatchedAt: time.Now()},
		{VideoID: "v3", Topic: "entertainment/jazz", Channel: "jazzchan", Title: "jazz interview", WatchedAt: time.Now()},
		{VideoID: "v4", Topic: "sports/chess", Channel: "chesschan", Title: "match highlights", WatchedAt: time.Now()},
		{VideoID: "v5", Topic: "sports/chess", Channel: "chesschan", Title: "more highlights", WatchedAt: time.Now()},
		{VideoID: "v6", Topic: "sports/schach", Channel: "chesschan", Title: "turnier clip", WatchedAt: time.Now()},
		{VideoID: "v7", Topic: "unclear", WatchedAt: time.Now()},
	}
	if err := writeJSONL(paths{dataDir: dataDir}.classifiedJSONL(), rows); err != nil {
		t.Fatal(err)
	}

	err := cmdTaxonomy([]string{"-data", dataDir, "-rules", rulesPath, "-min-videos", "1", "-rounds", "2"})
	if err != nil {
		t.Fatalf("cmdTaxonomy: %v", err)
	}

	tax, err := taxonomy.LoadFile(taxonomyPath)
	if err != nil {
		t.Fatalf("generated taxonomy unreadable: %v", err)
	}
	// The two success criteria in miniature: the scattered subject lands
	// under ONE top level, the two spellings fold into one subject.
	if got := tax.Fold("entertainment/jazz"); got != "jazz-top/jazz" {
		t.Errorf("Fold(entertainment/jazz) = %q", got)
	}
	if got, want := tax.Fold("sports/schach"), tax.Fold("sports/chess"); got != want || !strings.Contains(got, "chess") {
		t.Errorf("spellings did not meet: schach -> %q, chess -> %q", got, want)
	}
	if got := tax.Fold("unclear"); got != "unclear" {
		t.Errorf("Fold(unclear) = %q, want it untouched", got)
	}

	logPath := filepath.Join(dataDir, "out", "taxonomy-run.jsonl")
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("run log missing: %v", err)
	}
	for _, event := range []string{"collect", "baseline", "embed", "round", "write"} {
		if !strings.Contains(string(logBytes), `"event":"`+event+`"`) {
			t.Errorf("run log misses event %q", event)
		}
	}

	// Both caches make the second run free: rerun and count requests. This is
	// the run shape the control file asks for — edit, run the same command
	// again — so a rerun must not pay the server twice.
	firstRunChats := chatCalls
	if firstRunChats == 0 {
		t.Fatal("first run never asked the model to name anything")
	}
	if err := cmdTaxonomy([]string{"-data", dataDir, "-rules", rulesPath, "-min-videos", "1", "-rounds", "1"}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	logBytes, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logBytes), `"fresh":0`) {
		t.Error("second run embedded fresh vectors — the cache did not hold")
	}
	if chatCalls != firstRunChats {
		t.Errorf("second run made %d naming requests, want 0 — the name cache did not hold",
			chatCalls-firstRunChats)
	}

	// A different chat model must not read the first model's names.
	otherRules := filepath.Join(dataDir, "rules-other.yaml")
	if err := os.WriteFile(otherRules, []byte(strings.Replace(rulesYAML, "test-chat", "other-chat", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdTaxonomy([]string{"-data", dataDir, "-rules", otherRules, "-min-videos", "1", "-rounds", "1"}); err != nil {
		t.Fatalf("third run: %v", err)
	}
	if chatCalls == firstRunChats {
		t.Error("a different chat model reused the cached names — the model is not in the key")
	}
}
