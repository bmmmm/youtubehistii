// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
//
// It hands back a RoundTripper rather than a server. The routing is the same
// ServeMux either way, but a recorder never opens a socket, and this sandbox
// denies httptest.NewServer its bind — with a server here the one test that
// exercises cmdTaxonomy from end to end could only ever run in CI, which is
// the same as saying it could not be used while changing the thing it covers.
func fakeOMLX(t *testing.T, chatCalls *int) http.RoundTripper {
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
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec.Result(), nil
	})
}

// fakeChatTransport answers every chat/completions request with one fixed
// reply, entirely in-process: http.Client hands the request straight to
// RoundTrip and never opens a socket, so this works even though this sandbox
// denies httptest.NewServer's bind ("bind: operation not permitted").
// fakeOMLX takes the same route for the same reason; this one skips the mux
// because a single canned answer needs no routing.
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

	namer, stats := newNamer(client, false, log, t.TempDir(), 1)
	cluster := taxonomy.Cluster{
		Members: []taxonomy.Label{{Area: "music", Sub: "jazz", Views: 3, Videos: 2}},
		Views:   3,
	}

	if got := namer([]taxonomy.Cluster{cluster}, "subject")[0]; got != "jazz" {
		t.Fatalf("first call = %q, want jazz", got)
	}
	if got := namer([]taxonomy.Cluster{cluster}, "subject")[0]; got != "jazz" {
		t.Fatalf("second call = %q, want jazz", got)
	}

	if calls != 1 {
		t.Errorf("transport saw %d requests, want 1 — the second call should have been a cache hit", calls)
	}
	if stats.Subject.Hits != 1 {
		t.Errorf("subject hits = %d, want 1", stats.Subject.Hits)
	}
	if stats.Subject.Misses != 1 {
		t.Errorf("subject requested = %d, want 1", stats.Subject.Misses)
	}
	if stats.Subject.Fallbacks != 0 {
		t.Errorf("subject fallbacks = %d, want 0", stats.Subject.Fallbacks)
	}
	if stats.Subject.ReqNanos <= 0 {
		t.Error("the one real request recorded no time")
	}
	if stats.Top.Hits != 0 || stats.Top.Misses != 0 || stats.Top.Fallbacks != 0 {
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
	failNamer, failStats := newNamer(failing, false, log, t.TempDir(), 1)
	other := taxonomy.Cluster{Members: []taxonomy.Label{{Area: "sports", Sub: "chess", Views: 1, Videos: 1}}}
	if got, want := failNamer([]taxonomy.Cluster{other}, "top")[0], "chess"; got != want {
		t.Fatalf("fallback = %q, want %q (the failing request's fallback name)", got, want)
	}
	if failStats.Top.Misses != 1 || failStats.Top.Fallbacks != 1 || failStats.Top.Hits != 0 {
		t.Errorf("top stats after a failed request = %+v, want 1 miss, 1 fallback, 0 hits", failStats.Top)
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
	namer, stats := newNamer(nil, true, log, filepath.Join(t.TempDir(), "does-not-exist"), 1)
	cluster := taxonomy.Cluster{Members: []taxonomy.Label{{Area: "music", Sub: "jazz", Views: 1, Videos: 1}}}

	if got := namer([]taxonomy.Cluster{cluster}, "subject")[0]; got != "jazz" {
		t.Fatalf("got %q, want the fallback name jazz", got)
	}
	if *stats != (taxonomy.NamingStats{}) {
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
	rt := fakeOMLX(t, &chatCalls)
	// The client the command builds for itself is replaced wholesale: only
	// the transport is fake, so flag parsing, rules loading, embedding,
	// naming and the write all run for real.
	newOMLXClient = func(model, baseURL string) *omlx.Client {
		return &omlx.Client{BaseURL: baseURL, Model: model, HTTP: &http.Client{Transport: rt}}
	}
	t.Cleanup(func() { newOMLXClient = omlx.New })

	// The taxonomy and its run log land relative to the working directory,
	// exactly like config/rules.yaml — so the test runs in its own.
	t.Chdir(t.TempDir())
	dataDir := t.TempDir()

	rulesPath := filepath.Join(dataDir, "rules.yaml")
	rulesYAML := "llm:\n  model: test-chat\n  base_url: http://oml.invalid/v1\n"
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

// nameBatchForTest is the batch size the batching tests run at. The shipped
// default is 1 (see defaultNameBatch), so these tests have to ask for the
// batched path explicitly — which is the point: the machinery must stay
// correct for whoever raises the flag.
const nameBatchForTest = 12

// namedCluster builds a cluster whose prompt is distinguishable from the
// others', so the cache cannot confuse two of them.
func namedCluster(area, sub string, views int) taxonomy.Cluster {
	return taxonomy.Cluster{
		Members: []taxonomy.Label{{Area: area, Sub: sub, Views: views, Videos: views}},
		Views:   views,
	}
}

// TestNamerBatchesAndStaysCacheCompatible is the whole point of batching in
// one test: several clusters cost ONE request, the reply maps back by number
// rather than by arrival order, and — the part that makes the change safe to
// adopt — a second run finds every one of those names in the cache, because
// they were stored under each cluster's own single-cluster prompt.
func TestNamerBatchesAndStaysCacheCompatible(t *testing.T) {
	cacheDir := t.TempDir()
	var calls int
	// Deliberately out of order: if the code trusted position instead of the
	// number, this test would hand back chess/jazz/techno.
	client := &omlx.Client{
		BaseURL: "http://fake.invalid/v1",
		Model:   "test-chat",
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			body := `{"choices":[{"message":{"content":"3 chess\n1 jazz\n2 techno"}}]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		})},
	}
	log, err := newRunLog(filepath.Join(t.TempDir(), "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.close()

	cs := []taxonomy.Cluster{
		namedCluster("music", "bebop", 30),
		namedCluster("music", "detroit", 20),
		namedCluster("sports", "blitz", 10),
	}
	want := []string{"jazz", "techno", "chess"}

	namer, stats := newNamer(client, false, log, cacheDir, nameBatchForTest)
	got := namer(cs, "subject")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %v, want %v — the reply's numbers are the mapping", got, want)
	}
	if calls != 1 {
		t.Errorf("three clusters cost %d requests, want 1", calls)
	}
	if stats.Subject.Misses != 3 || stats.Subject.Requests != 1 {
		t.Errorf("stats = %d uncached in %d requests, want 3 in 1", stats.Subject.Misses, stats.Subject.Requests)
	}

	// The second run shares only the cache directory. If batching had cached
	// under the batch prompt, none of these would be found.
	namer2, stats2 := newNamer(client, false, log, cacheDir, nameBatchForTest)
	got2 := namer2(cs, "subject")
	if !reflect.DeepEqual(got2, want) {
		t.Errorf("second run names = %v, want %v", got2, want)
	}
	if calls != 1 {
		t.Errorf("second run sent %d more requests, want 0 — batched names must be cache-compatible", calls-1)
	}
	if stats2.Subject.Hits != 3 || stats2.Subject.Requests != 0 {
		t.Errorf("second run = %d hits in %d requests, want 3 in 0", stats2.Subject.Hits, stats2.Subject.Requests)
	}
}

// TestNamerRetriesABadBatchSingly locks in the fallback: a reply that does
// not account for every cluster is thrown away whole — never partially
// used — and each cluster is asked on its own instead.
func TestNamerRetriesABadBatchSingly(t *testing.T) {
	var calls int
	client := &omlx.Client{
		BaseURL: "http://fake.invalid/v1",
		Model:   "test-chat",
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			// The batch answer covers two of the three asked for; every
			// later (single) request answers "solo".
			content := "solo"
			if calls == 1 {
				content = "1 jazz\n2 techno"
			}
			body := fmt.Sprintf(`{"choices":[{"message":{"content":%q}}]}`, content)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		})},
	}
	log, err := newRunLog(filepath.Join(t.TempDir(), "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.close()

	cs := []taxonomy.Cluster{
		namedCluster("music", "bebop", 30),
		namedCluster("music", "detroit", 20),
		namedCluster("sports", "blitz", 10),
	}
	namer, stats := newNamer(client, false, log, t.TempDir(), nameBatchForTest)
	got := namer(cs, "subject")

	for i, name := range got {
		if name != "solo" {
			t.Errorf("cluster %d = %q, want solo — the bad batch must not be used at all", i, name)
		}
	}
	if calls != 4 {
		t.Errorf("saw %d requests, want 4 (one failed batch, then three singles)", calls)
	}
	if stats.Subject.Requests != 4 || stats.Subject.Misses != 3 {
		t.Errorf("stats = %d uncached in %d requests, want 3 in 4", stats.Subject.Misses, stats.Subject.Requests)
	}
}

// TestRunLogAppendsAcrossRuns: the run log is how a threshold calibration is
// read back — round metrics from one setting against another. os.Create
// truncated it, so every run erased the run it was meant to be compared
// against, and the loss was silent because a fresh file looks like a fresh
// run. The "run" event is what makes the appended stream separable again.
func TestRunLogAppendsAcrossRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	for i := range 2 {
		log, err := newRunLog(path)
		if err != nil {
			t.Fatal(err)
		}
		fs := flag.NewFlagSet("taxonomy", flag.ContinueOnError)
		fs.Int("rounds", 10, "")
		fs.Float64("fine", 0.70, "")
		if err := fs.Parse([]string{"-rounds", fmt.Sprint(3 + i)}); err != nil {
			t.Fatal(err)
		}
		log.logRunStart(fs)
		log.event("write", map[string]any{"n": i})
		log.close()
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 4 {
		t.Fatalf("run log has %d lines, want 4 — the second run truncated the first", len(lines))
	}
	var first struct {
		Event  string `json:"event"`
		Detail struct {
			Version string            `json:"version"`
			Flags   map[string]string `json:"flags"`
		} `json:"detail"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Event != "run" {
		t.Errorf("first event = %q, want \"run\"", first.Event)
	}
	if first.Detail.Version == "" {
		t.Error("the run event carries no version — two runs of different binaries read alike")
	}
	// Only what was SET. The defaults are in the binary and in the usage
	// text; repeating fourteen of them would bury the one that changed.
	if got := first.Detail.Flags["rounds"]; got != "3" {
		t.Errorf("flags[rounds] = %q, want 3", got)
	}
	if _, ok := first.Detail.Flags["fine"]; ok {
		t.Error("an untouched flag was logged as if it had been chosen")
	}
}

// TestRunLeakPatternsNeedsARepoAndTheScript: the patterns are derived from the
// taxonomy's subjects, so a taxonomy run is exactly when they go stale — but
// the regeneration must stay inert wherever it cannot help. A binary run
// outside its source tree has no script, and a directory that is not a repo
// has no .git/leak-patterns to write.
func TestRunLeakPatternsNeedsARepoAndTheScript(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	var out strings.Builder
	regenerateLeakPatterns(&out, taxonomyPath)
	if out.String() != "" {
		t.Errorf("outside a repo, the regeneration still spoke: %q", out.String())
	}

	// A repo, still no script: the binary is installed away from its source.
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	regenerateLeakPatterns(&out, taxonomyPath)
	if out.String() != "" {
		t.Errorf("without the script, the regeneration still spoke: %q", out.String())
	}

	// Both there: the script runs and its output is passed through. A stub
	// stands in — the real one reads a taxonomy that is nobody's business
	// here, and this test is about the wiring, not the generator.
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	stub := "#!/usr/bin/env bash\nprintf 'gen-leak-patterns: 7 subjects, 2 allowed, 5 patterns\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "scripts", "gen-leak-patterns.sh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	regenerateLeakPatterns(&out, taxonomyPath)
	if !strings.Contains(out.String(), "5 patterns") {
		t.Errorf("the script ran but its counts were swallowed: %q", out.String())
	}
}

// TestRunLeakPatternsRefusesANonDefaultTaxonomyFile: -taxonomy-file writes
// somewhere the gates do not read. Regenerating from it would leave the
// patterns describing a file nothing checks — a gate pointed at the wrong
// source is worse than a stale one, because it looks deliberate.
func TestRunLeakPatternsRefusesANonDefaultTaxonomyFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	stub := "#!/usr/bin/env bash\nprintf 'THE STUB RAN\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "scripts", "gen-leak-patterns.sh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	regenerateLeakPatterns(&out, "somewhere/else.yaml")
	if strings.Contains(out.String(), "THE STUB RAN") {
		t.Error("a -taxonomy-file run regenerated the patterns for a file the gates do not read")
	}
	if !strings.Contains(out.String(), taxonomyPath) {
		t.Errorf("the note does not name the file the patterns come from: %q", out.String())
	}
}
