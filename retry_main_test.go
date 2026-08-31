// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/omlx"
	"github.com/bmmmm/youtubehistii/internal/rules"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

// retryConfig is testConfig's shape plus the areas the retry fixtures use.
func retryConfig() *rules.Config {
	return &rules.Config{
		LLM: rules.LLM{Model: "test-chat", BaseURL: "http://fake.invalid/v1"},
		Topics: []rules.Topic{
			{ID: "dev", Desc: "software engineering"},
			{ID: "music", Desc: "music"},
			{ID: "unclear", Desc: "cannot tell"},
		},
	}
}

// newRetryPass builds the pass the way classifyPass does, runs stages 1+2,
// and hands back both the pass and what stage 2 selected.
func newRetryPass(t *testing.T, views []takeout.View, metas map[string]enrich.Meta, cached map[string]classify.LLMVerdict, opts classifyOpts) (*pass, liveSet) {
	t.Helper()
	r := &pass{
		p: paths{dataDir: t.TempDir()}, cfg: retryConfig(), views: views, metas: metas, cached: cached, opts: opts,
		taxonomy:     retryConfig().Fingerprint(),
		items:        map[string]classify.Item{},
		verdicts:     map[string]videoVerdict{},
		llmDown:      opts.noLLM,
		retryContext: map[string]bool{},
	}
	return r, r.resolveCached(r.matchRules())
}

func view(id, title, channel string) takeout.View {
	return takeout.View{VideoID: id, Title: title, Channel: channel, WatchedAt: time.Now()}
}

// TestRetryTargetsSelectOnlyTheirSet: one verdict per defect plus a healthy
// one — each -retry selector picks exactly its set and nothing else.
func TestRetryTargetsSelectOnlyTheirSet(t *testing.T) {
	taxonomy := retryConfig().Fingerprint()
	views := []takeout.View{
		view("aaaaaaaaaa1", "no sub here", "c1"),
		view("bbbbbbbbbb2", "no mode here", "c2"),
		view("cccccccccc3", "an unclear one with a real title", "c3"),
		view("dddddddddd4", "healthy", "c4"),
	}
	metas := map[string]enrich.Meta{
		"cccccccccc3": {ID: "cccccccccc3", Unavailable: true, GoneReason: "private"},
	}
	cached := map[string]classify.LLMVerdict{
		"aaaaaaaaaa1": {Topic: "music", Mode: "consume", Confidence: 0.7, Model: "m", Basis: classify.BasisTitleOnly, Taxonomy: taxonomy},
		"bbbbbbbbbb2": {Topic: "dev/talks", Confidence: 0.8, Model: "m", Basis: classify.BasisTitleOnly, Taxonomy: taxonomy},
		"cccccccccc3": {Topic: "unclear", Confidence: 0.1, Model: "m", Basis: classify.BasisTitleOnly, Taxonomy: taxonomy},
		"dddddddddd4": {Topic: "music/jazz", Mode: "consume", Confidence: 0.9, Model: "m", Basis: classify.BasisTitleOnly, Taxonomy: taxonomy},
	}
	for _, tc := range []struct {
		retry             string
		wantSub, wantMode []string
		wantFull          []string
	}{
		{"no-sub", []string{"aaaaaaaaaa1"}, nil, nil},
		{"no-mode", nil, []string{"bbbbbbbbbb2"}, nil},
		{"unclear", nil, nil, []string{"cccccccccc3"}},
		{"all", []string{"aaaaaaaaaa1"}, []string{"bbbbbbbbbb2"}, []string{"cccccccccc3"}},
		{"no-sub,no-mode", []string{"aaaaaaaaaa1"}, []string{"bbbbbbbbbb2"}, nil},
	} {
		t.Run(tc.retry, func(t *testing.T) {
			_, live := newRetryPass(t, views, metas, cached,
				classifyOpts{noLLM: true, retry: tc.retry})
			if !reflect.DeepEqual(live.sub, tc.wantSub) || !reflect.DeepEqual(live.mode, tc.wantMode) || !reflect.DeepEqual(live.full, tc.wantFull) {
				t.Errorf("retry %q selected sub=%v mode=%v full=%v, want %v/%v/%v",
					tc.retry, live.sub, live.mode, live.full, tc.wantSub, tc.wantMode, tc.wantFull)
			}
		})
	}

	// Without -retry nothing is selected at all — the healthy cache stays put.
	_, live := newRetryPass(t, views, metas, cached, classifyOpts{noLLM: true})
	if live.count() != 0 {
		t.Errorf("no -retry selected sub=%v mode=%v full=%v, want nothing", live.sub, live.mode, live.full)
	}
}

// TestRetryUnclearRefusesURLOnlyTombstones: a tombstone whose "title" is its
// own URL and whose channel Takeout never wrote carries no signal — the
// retry must refuse it and count the refusal, not manufacture a guess.
func TestRetryUnclearRefusesURLOnlyTombstones(t *testing.T) {
	taxonomy := retryConfig().Fingerprint()
	views := []takeout.View{
		view("aaaaaaaaaa1", "https://www.youtube.com/watch?v=aaaaaaaaaa1", ""),
		view("bbbbbbbbbb2", "a real title that says something", "some channel"),
	}
	metas := map[string]enrich.Meta{
		"aaaaaaaaaa1": {ID: "aaaaaaaaaa1", Unavailable: true, GoneReason: "private"},
		"bbbbbbbbbb2": {ID: "bbbbbbbbbb2", Unavailable: true, GoneReason: "private"},
	}
	cached := map[string]classify.LLMVerdict{
		"aaaaaaaaaa1": {Topic: "unclear", Confidence: 0.1, Model: "m", Basis: classify.BasisTitleOnly, Taxonomy: taxonomy},
		"bbbbbbbbbb2": {Topic: "unclear", Confidence: 0.1, Model: "m", Basis: classify.BasisTitleOnly, Taxonomy: taxonomy},
	}
	r, live := newRetryPass(t, views, metas, cached, classifyOpts{noLLM: true, retry: "unclear"})
	if !reflect.DeepEqual(live.full, []string{"bbbbbbbbbb2"}) {
		t.Errorf("full = %v, want just the one with usable text", live.full)
	}
	if r.retryRefused != 1 {
		t.Errorf("retryRefused = %d, want 1", r.retryRefused)
	}
	// The marker keeps a retried-but-still-unclear verdict out of the next run.
	cached["bbbbbbbbbb2"] = classify.LLMVerdict{Topic: "unclear", Confidence: 0.1, Model: "m",
		Basis: classify.BasisTitleOnly, Taxonomy: taxonomy, Retried: []string{"context"}}
	_, live = newRetryPass(t, views, metas, cached, classifyOpts{noLLM: true, retry: "unclear"})
	if live.count() != 0 {
		t.Errorf("already-retried verdict selected again: %v", live.full)
	}
}

// retryTransport routes the two endpoints the live path needs: the model
// list (health check) and chat completions, answered from a fixed script.
// In-process like fakeChatTransport — httptest cannot bind in this sandbox.
type retryTransport struct {
	calls *int
	reply string
}

func (f retryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := fmt.Sprintf(`{"data":[{"id":%q}]}`, "test-chat")
	if strings.Contains(r.URL.Path, "chat") {
		*f.calls++
		body = fmt.Sprintf(`{"choices":[{"message":{"content":%q}}]}`, f.reply)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func fakeClientOpts(calls *int, reply string) classifyOpts {
	return classifyOpts{
		llmBatch: 2,
		newClient: func(model, baseURL string) *omlx.Client {
			return &omlx.Client{
				BaseURL: "http://fake.invalid/v1",
				Model:   "test-chat",
				HTTP:    &http.Client{Transport: retryTransport{calls: calls, reply: reply}},
			}
		},
	}
}

// TestModeRoundFillsOnlyTheMode is the guard against the one bug that would
// destroy good verdicts: a mode retry may set Mode and its marker, and
// nothing else — Topic, Confidence, Basis, Model and Taxonomy stay
// byte-for-byte what the original verdict said.
func TestModeRoundFillsOnlyTheMode(t *testing.T) {
	taxonomy := retryConfig().Fingerprint()
	views := []takeout.View{
		view("aaaaaaaaaa1", "video one", "c1"),
		view("bbbbbbbbbb2", "video two", "c2"),
	}
	before := map[string]classify.LLMVerdict{
		"aaaaaaaaaa1": {Topic: "dev/talks", Confidence: 0.8, Model: "old-model", Basis: classify.BasisTitleOnly, Taxonomy: taxonomy},
		"bbbbbbbbbb2": {Topic: "music/jazz", Confidence: 0.6, Model: "old-model", Basis: classify.BasisTitleOnly, Taxonomy: taxonomy},
	}
	cached := map[string]classify.LLMVerdict{}
	for id, v := range before {
		cached[id] = v
	}
	var calls int
	opts := fakeClientOpts(&calls, "1 learn\n2 consume")
	opts.retry = "no-mode"
	r, live := newRetryPass(t, views, nil, cached, opts)
	if err := r.askLive(live); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("chat calls = %d, want 1 batch", calls)
	}
	wantModes := map[string]string{"aaaaaaaaaa1": "learn", "bbbbbbbbbb2": "consume"}
	llmCache := classify.Cache{Dir: r.p.classifyCache()}
	for id, old := range before {
		got, ok := llmCache.Read(id)
		if !ok {
			t.Fatalf("%s: no cache entry written", id)
		}
		want := old
		want.Mode = wantModes[id]
		want.Retried = []string{"mode"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %+v, want %+v — the mode round touched more than the mode", id, got, want)
		}
	}

	// Idempotence: the same retry again asks nothing — the marker holds.
	calls = 0
	r2, live2 := newRetryPass(t, views, nil, cached, opts)
	if err := r2.askLive(live2); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || live2.count() != 0 {
		t.Errorf("second retry made %d calls over %d ids, want 0/0", calls, live2.count())
	}
}

// TestSubRoundDropsHesitantAnswers: below minSubConfidence the area stays
// bare — a doubtful sub is worse than none — but the marker is still set.
func TestSubRoundDropsHesitantAnswers(t *testing.T) {
	taxonomy := retryConfig().Fingerprint()
	views := []takeout.View{
		view("aaaaaaaaaa1", "video one", "c1"),
		view("bbbbbbbbbb2", "video two", "c2"),
	}
	cached := map[string]classify.LLMVerdict{
		"aaaaaaaaaa1": {Topic: "music", Mode: "consume", Confidence: 0.7, Model: "m", Basis: classify.BasisTitleOnly, Taxonomy: taxonomy},
		"bbbbbbbbbb2": {Topic: "music", Mode: "consume", Confidence: 0.7, Model: "m", Basis: classify.BasisTitleOnly, Taxonomy: taxonomy},
	}
	var calls int
	opts := fakeClientOpts(&calls, "1 jazz 0.9\n2 blues 0.2")
	opts.retry = "no-sub"
	r, live := newRetryPass(t, views, nil, cached, opts)
	if err := r.askLive(live); err != nil {
		t.Fatal(err)
	}
	llmCache := classify.Cache{Dir: r.p.classifyCache()}
	confident, _ := llmCache.Read("aaaaaaaaaa1")
	if confident.Topic != "music/jazz" || confident.Confidence != 0.9 || confident.Mode != "consume" {
		t.Errorf("confident sub: got %+v, want music/jazz at 0.9 with the mode untouched", confident)
	}
	hesitant, _ := llmCache.Read("bbbbbbbbbb2")
	if hesitant.Topic != "music" || hesitant.Confidence != 0.7 {
		t.Errorf("hesitant sub: got %+v, want the bare area and the old confidence kept", hesitant)
	}
	if !reflect.DeepEqual(hesitant.Retried, []string{"sub"}) {
		t.Errorf("hesitant sub: Retried = %v, want the marker even without an accepted answer", hesitant.Retried)
	}
}
