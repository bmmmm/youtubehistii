// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"fmt"
	"io"
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
// a model list, embeddings with two hand-made families (jazz and chess — the
// fixtures are illustrations, not anyone's history), and a namer whose
// answers depend on the prompt's altitude.
//
// Each family has a neighbour — bebop next to jazz, backgammon next to chess
// — sharing the family's axis and adding one of its own. That makes a family
// cluster wide enough for the radius trigger to tear it apart, which is the
// shape both defects live in.
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
			if strings.Contains(text, "jazz") || strings.Contains(text, "bebop") {
				vec[0] = 1
			}
			if strings.Contains(text, "chess") || strings.Contains(text, "schach") || strings.Contains(text, "backgammon") {
				vec[1] = 1 // both spellings of one game land on one axis
			}
			if strings.Contains(text, "bebop") || strings.Contains(text, "backgammon") {
				vec[2] = 1 // the neighbour's own corner, away from its family
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
		// Driven so a split of the jazz family always comes back under ONE
		// name: a bebop-only piece answers "jazz" like the rest of it, so
		// MergeSameNames glues the split straight back together — the loop
		// this run has to stop repeating. The chess family answers with two
		// names, so its split is a real repair and survives.
		name := "jazz"
		switch {
		case strings.Contains(user, "chess"), strings.Contains(user, "schach"):
			name = "chess"
		case strings.Contains(user, "backgammon"):
			name = "backgammon"
		}
		if strings.Contains(system, "top-level") {
			name += "-top"
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, name)
	})
	return httptest.NewServer(mux)
}

// captureStdout runs fn with os.Stdout on a pipe and returns what it
// printed. The run narrates its rounds to the terminal, and "how the loop
// ended" is only readable there.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	read := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		read <- string(b)
	}()
	runErr := fn()
	os.Stdout = saved
	w.Close()
	out := <-read
	r.Close()
	return out, runErr
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
	// game with two language spellings, one unclear view that must stay out
	// — plus a neighbour per family (bebop, backgammon) so both family
	// clusters come out wider than -max-radius and the refinement rounds
	// have something to tear.
	rows := []classify.Verdict{
		{VideoID: "v1", Topic: "music/jazz", Channel: "jazzchan", Title: "jazz set one", WatchedAt: time.Now()},
		{VideoID: "v2", Topic: "music/jazz", Channel: "jazzchan", Title: "jazz set two", WatchedAt: time.Now()},
		{VideoID: "v3", Topic: "entertainment/jazz", Channel: "jazzchan", Title: "jazz interview", WatchedAt: time.Now()},
		{VideoID: "v4", Topic: "music/bebop", Channel: "bebopchan", Title: "bebop heads one", WatchedAt: time.Now()},
		{VideoID: "v5", Topic: "music/bebop", Channel: "bebopchan", Title: "bebop heads two", WatchedAt: time.Now()},
		{VideoID: "v6", Topic: "music/bebop", Channel: "bebopchan", Title: "bebop heads three", WatchedAt: time.Now()},
		{VideoID: "v7", Topic: "sports/chess", Channel: "chesschan", Title: "match highlights", WatchedAt: time.Now()},
		{VideoID: "v8", Topic: "sports/chess", Channel: "chesschan", Title: "more highlights", WatchedAt: time.Now()},
		{VideoID: "v9", Topic: "sports/schach", Channel: "chesschan", Title: "turnier clip", WatchedAt: time.Now()},
		{VideoID: "v10", Topic: "sports/backgammon", Channel: "bgchan", Title: "backgammon match one", WatchedAt: time.Now()},
		{VideoID: "v11", Topic: "sports/backgammon", Channel: "bgchan", Title: "backgammon match two", WatchedAt: time.Now()},
		{VideoID: "v12", Topic: "sports/backgammon", Channel: "bgchan", Title: "backgammon match three", WatchedAt: time.Now()},
		{VideoID: "v13", Topic: "sports/backgammon", Channel: "bgchan", Title: "backgammon match four", WatchedAt: time.Now()},
		{VideoID: "v14", Topic: "unclear", WatchedAt: time.Now()},
	}
	if err := writeJSONL(paths{dataDir: dataDir}.classifiedJSONL(), rows); err != nil {
		t.Fatal(err)
	}

	// -max-radius 0.1 puts both family clusters over the split trigger; five
	// rounds are more than the run may use — settling early is the point.
	runArgs := func(rules string) []string {
		return []string{"-data", dataDir, "-rules", rules, "-min-videos", "1", "-max-radius", "0.1", "-rounds", "5"}
	}
	out, err := captureStdout(t, func() error { return cmdTaxonomy(runArgs(rulesPath)) })
	if err != nil {
		t.Fatalf("cmdTaxonomy: %v\n%s", err, out)
	}

	// The refinement loop converges: the jazz split is undone by the
	// same-name merge, so it is blocked, said out loud, and never repeated —
	// and the run ends on its own instead of at the -rounds limit.
	if !strings.Contains(out, "metrics settled") {
		t.Errorf("the run never settled — it ran into the -rounds limit:\n%s", out)
	}
	if strings.Contains(out, "round 5") {
		t.Errorf("the run needed every round:\n%s", out)
	}
	if !strings.Contains(out, "split blocked: jazz") {
		t.Errorf("the blocked split left no trace on the terminal:\n%s", out)
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
	// The top level covering chess and backgammon is named from the WHOLE
	// group: named after its biggest subject alone it came out
	// "backgammon-top", with chess filed under a game it is not.
	if got := tax.Fold("sports/chess"); got != "chess-top/chess" {
		t.Errorf("Fold(sports/chess) = %q, want chess-top/chess — the top name saw only its biggest subject", got)
	}
	if got := tax.Fold("sports/backgammon"); got != "chess-top/backgammon" {
		t.Errorf("Fold(sports/backgammon) = %q, want chess-top/backgammon", got)
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

	// No name may be split twice: a split the same-name merge undoes changed
	// nothing, so a second one is the loop itself. The block has to be in the
	// log too — a decision that leaves no trace did not happen.
	splitRound, blocked := map[string]int{}, map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(logBytes)), "\n") {
		var ev struct {
			Event  string `json:"event"`
			Detail struct {
				N       int               `json:"n"`
				Changes []taxonomy.Change `json:"changes"`
				Blocked []string          `json:"split_blocked"`
			} `json:"detail"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Event != "round" {
			continue
		}
		for _, c := range ev.Detail.Changes {
			if c.Op != "split" {
				continue
			}
			if prev, ok := splitRound[c.From]; ok {
				t.Errorf("%q was split in round %d and again in round %d — the loop is back", c.From, prev, ev.Detail.N)
			}
			splitRound[c.From] = ev.Detail.N
		}
		for _, name := range ev.Detail.Blocked {
			blocked[name] = true
		}
	}
	if len(splitRound) == 0 {
		t.Error("no round split anything — the fixture stopped provoking the loop")
	}
	if !blocked["jazz"] {
		t.Errorf("run log never reports the blocked split:\n%s", logBytes)
	}

	// Both caches make the second run free: rerun and count requests. This is
	// the run shape the control file asks for — edit, run the same command
	// again — so a rerun must not pay the server twice.
	firstRunChats := chatCalls
	if firstRunChats == 0 {
		t.Fatal("first run never asked the model to name anything")
	}
	if err := cmdTaxonomy(runArgs(rulesPath)); err != nil {
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
	if err := cmdTaxonomy(runArgs(otherRules)); err != nil {
		t.Fatalf("third run: %v", err)
	}
	if chatCalls == firstRunChats {
		t.Error("a different chat model reused the cached names — the model is not in the key")
	}
}
