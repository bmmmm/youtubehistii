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
	"github.com/bmmmm/youtubehistii/internal/omlx"
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

// fakeChatTransport answers every chat/completions request with one fixed
// reply, entirely in-process: http.Client hands the request straight to
// RoundTrip and never opens a socket, so this works even though this
// sandbox denies httptest.NewServer's bind ("bind: operation not
// permitted") — the same reason TestCmdTaxonomyEndToEnd (which does use
// httptest, via fakeOMLX) cannot run here.
type fakeChatTransport struct {
	calls *int
	reply string
}

func (f fakeChatTransport) RoundTrip(*http.Request) (*http.Response, error) {
	*f.calls++
	body := fmt.Sprintf(`{"choices":[{"message":{"content":%q}}]}`, f.reply)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

// TestNamerCountsCacheHitsAndRequests nails down the counting newNamer now
// does: the same cluster named twice must cost exactly one real request —
// the first call is a cache miss that pays the (fake) model and writes the
// cache, the second reads it back — and the counters must land on the
// altitude ("subject") actually asked for, not the other one.
func TestNamerCountsCacheHitsAndRequests(t *testing.T) {
	var calls int
	client := &omlx.Client{
		BaseURL: "http://fake.invalid/v1",
		Model:   "test-chat",
		HTTP:    &http.Client{Transport: fakeChatTransport{calls: &calls, reply: "jazz"}},
	}
	log, err := newRunLog(filepath.Join(t.TempDir(), "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.close()

	namer, stats := newNamer(client, false, log, t.TempDir())
	cluster := taxonomy.Cluster{
		Members: []taxonomy.Label{{Area: "music", Sub: "jazz", Views: 3, Videos: 2}},
		Views:   3,
	}

	if got := namer(cluster, "subject"); got != "jazz" {
		t.Fatalf("first call = %q, want jazz", got)
	}
	if got := namer(cluster, "subject"); got != "jazz" {
		t.Fatalf("second call = %q, want jazz", got)
	}

	if calls != 1 {
		t.Errorf("transport saw %d requests, want 1 — the second call should have been a cache hit", calls)
	}
	if stats.subject.hits != 1 {
		t.Errorf("subject hits = %d, want 1", stats.subject.hits)
	}
	if stats.subject.misses != 1 {
		t.Errorf("subject requested = %d, want 1", stats.subject.misses)
	}
	if stats.subject.fallbacks != 0 {
		t.Errorf("subject fallbacks = %d, want 0", stats.subject.fallbacks)
	}
	if stats.subject.reqNanos <= 0 {
		t.Error("the one real request recorded no time")
	}
	if stats.top.hits != 0 || stats.top.misses != 0 || stats.top.fallbacks != 0 {
		t.Error("a subject-kind call must not touch the top counters")
	}

	// A failing request must count as a request AND a fallback, not a hit —
	// and must not poison the cache, so it costs a request every time it is
	// asked again.
	failing := &omlx.Client{
		BaseURL: "http://fake.invalid/v1",
		Model:   "test-chat",
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("connection refused")
		})},
	}
	failNamer, failStats := newNamer(failing, false, log, t.TempDir())
	other := taxonomy.Cluster{Members: []taxonomy.Label{{Area: "sports", Sub: "chess", Views: 1, Videos: 1}}}
	if got, want := failNamer(other, "top"), "chess"; got != want {
		t.Fatalf("fallback = %q, want %q (the failing request's fallback name)", got, want)
	}
	if failStats.top.misses != 1 || failStats.top.fallbacks != 1 || failStats.top.hits != 0 {
		t.Errorf("top stats after a failed request = %+v, want 1 miss, 1 fallback, 0 hits", failStats.top)
	}
}

// roundTripFunc adapts a plain function to http.RoundTripper, the same
// pattern net/http/httptest itself documents for a mock transport.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestNamerNoLLMSkipsStats locks in the -no-llm contract: the namer must
// still return a usable name, and it must do so without touching the
// counters at all — the naming line then reads all zeros, which is the
// true count of a run that never asks the model anything.
func TestNamerNoLLMSkipsStats(t *testing.T) {
	log, err := newRunLog(filepath.Join(t.TempDir(), "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.close()

	// cacheDir does not even need to exist: -no-llm never touches it.
	namer, stats := newNamer(nil, true, log, filepath.Join(t.TempDir(), "does-not-exist"))
	cluster := taxonomy.Cluster{Members: []taxonomy.Label{{Area: "music", Sub: "jazz", Views: 1, Videos: 1}}}

	if got := namer(cluster, "subject"); got != "jazz" {
		t.Fatalf("got %q, want the fallback name jazz", got)
	}
	if *stats != (namingStats{}) {
		t.Errorf("stats = %+v, want all zero — -no-llm must never touch the counters", *stats)
	}
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
