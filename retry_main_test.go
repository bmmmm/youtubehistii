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
		retryTopic:   map[string]string{},
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
		// A named topic matches the whole canonical string, not a prefix:
		// "topic:music" leaves "music/jazz" alone and takes the bare area.
		{"topic:music/jazz", nil, nil, []string{"dddddddddd4"}},
		{"topic:music", nil, nil, []string{"aaaaaaaaaa1"}},
		// ...and it wins over the field round that would also have selected
		// it — the full re-ask answers the sub anyway.
		{"no-sub,topic:music", nil, nil, []string{"aaaaaaaaaa1"}},
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
	calls   *int
	reply   string
	prompts *[]string // nil unless a test asserts on what was asked
}

func (f retryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := fmt.Sprintf(`{"data":[{"id":%q}]}`, "test-chat")
	if strings.Contains(r.URL.Path, "chat") {
		*f.calls++
		if f.prompts != nil && r.Body != nil {
			asked, _ := io.ReadAll(r.Body)
			*f.prompts = append(*f.prompts, string(asked))
		}
		body = fmt.Sprintf(`{"choices":[{"message":{"content":%q}}]}`, f.reply)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func fakeClientOpts(calls *int, reply string) classifyOpts {
	return fakeClientRecording(calls, reply, nil)
}

// fakeClientRecording additionally collects every request body, for the test
// that has to prove what the prompt did NOT contain.
func fakeClientRecording(calls *int, reply string, prompts *[]string) classifyOpts {
	return classifyOpts{
		llmBatch: 2,
		newClient: func(model, baseURL string) *omlx.Client {
			return &omlx.Client{
				BaseURL: "http://fake.invalid/v1",
				Model:   "test-chat",
				HTTP:    &http.Client{Transport: retryTransport{calls: calls, reply: reply, prompts: prompts}},
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

// TestRetryTopicDropsItsOwnSeed is the reason "-retry topic:<t>" exists as
// code instead of a shell recipe. Deleting the verdict files by hand did two
// things: it forced a re-ask, and it took the bad sub out of the sub seeds
// (collectSubSeeds counts surviving verdicts). Only the first is obvious.
// At temperature 0 a re-ask with the catch-all still in the seed list gets
// the same answer back — the round would cost requests and change nothing.
// So the prompt must not contain the topic being re-asked, and the marker
// must keep a model that answers it AGAIN from being asked a third time.
func TestRetryTopicDropsItsOwnSeed(t *testing.T) {
	taxonomy := retryConfig().Fingerprint()
	views := []takeout.View{
		view("aaaaaaaaaa1", "video one", "c1"),
		view("bbbbbbbbbb2", "video two", "c2"),
		view("cccccccccc3", "video three", "c3"), // healthy, must not be touched
	}
	healthy := classify.LLMVerdict{Topic: "music/blues", Mode: "consume", Confidence: 0.9,
		Model: "m", Basis: classify.BasisTitleOnly, Taxonomy: taxonomy}
	cached := map[string]classify.LLMVerdict{
		"aaaaaaaaaa1": {Topic: "music/jazz", Mode: "consume", Confidence: 0.7, Model: "m", Basis: classify.BasisTitleOnly, Taxonomy: taxonomy},
		"bbbbbbbbbb2": {Topic: "music/jazz", Mode: "consume", Confidence: 0.7, Model: "m", Basis: classify.BasisTitleOnly, Taxonomy: taxonomy},
		"cccccccccc3": healthy,
	}
	var (
		calls   int
		prompts []string
	)
	// The model answers the old topic again for the first video: that is the
	// case the marker is for, and the one a re-run must not pay for twice.
	opts := fakeClientRecording(&calls, "1 music/jazz consume 0.9\n2 dev/talks learn 0.8", &prompts)
	opts.retry = "topic:music/jazz"
	r, live := newRetryPass(t, views, nil, cached, opts)
	if !reflect.DeepEqual(live.full, []string{"aaaaaaaaaa1", "bbbbbbbbbb2"}) {
		t.Fatalf("full = %v, want exactly the two carrying the named topic", live.full)
	}
	if live.sub != nil || live.mode != nil || len(r.retryContext) != 0 {
		t.Errorf("sub=%v mode=%v context=%v, want a full re-ask and no channel context",
			live.sub, live.mode, r.retryContext)
	}
	if err := r.askLive(live); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("chat calls = %d, want 1 batch of 2", calls)
	}
	if strings.Contains(prompts[0], "jazz") {
		t.Errorf("the re-asked topic seeded its own replacement prompt:\n%s", prompts[0])
	}
	if !strings.Contains(prompts[0], "blues") {
		t.Errorf("the area lost its other seeds too:\n%s", prompts[0])
	}

	llmCache := classify.Cache{Dir: r.p.classifyCache()}
	same, _ := llmCache.Read("aaaaaaaaaa1")
	if same.Topic != "music/jazz" || !reflect.DeepEqual(same.Retried, []string{"topic:music/jazz"}) {
		t.Errorf("re-answered verdict = %+v, want the model's answer kept and marked", same)
	}
	moved, _ := llmCache.Read("bbbbbbbbbb2")
	if moved.Topic != "dev/talks" || moved.Mode != "learn" {
		t.Errorf("re-asked verdict = %+v, want the new answer", moved)
	}
	if _, ok := llmCache.Read("cccccccccc3"); ok {
		t.Errorf("the healthy verdict was rewritten, want it untouched")
	}

	// Idempotence: the same selector again asks nothing, even though one
	// verdict still carries the named topic. It is the model's answer now.
	calls = 0
	r2, live2 := newRetryPass(t, views, nil, cached, opts)
	if err := r2.askLive(live2); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || live2.count() != 0 {
		t.Errorf("second retry made %d calls over %d ids, want 0/0", calls, live2.count())
	}
}
