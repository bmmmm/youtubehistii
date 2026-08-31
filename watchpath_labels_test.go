// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/omlx"
	"github.com/bmmmm/youtubehistii/internal/report"
)

// chainFixture is a path with two chains of different depth, so the
// selection has something to order.
func chainFixture(t *testing.T) *report.Path {
	t.Helper()
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.Local)
	mk := func(off time.Duration, topic string, n int) classify.Verdict {
		return classify.Verdict{
			VideoID: fmt.Sprintf("v%d", int(off.Seconds())), Title: topic + " video",
			Channel: "chan-" + topic, WatchedAt: base.Add(off), Topic: topic,
			Mode: "consume", DurationS: 300,
		}
	}
	var rows []classify.Verdict
	// Five on music, then — after a session break — four on sports.
	for i := 0; i < 5; i++ {
		rows = append(rows, mk(time.Duration(i)*6*time.Minute, "music", i))
	}
	for i := 0; i < 4; i++ {
		rows = append(rows, mk(3*time.Hour+time.Duration(i)*6*time.Minute, "sports", i))
	}
	p := report.BuildPath(rows)
	if len(p.Chains) != 2 {
		t.Fatalf("fixture built %d chains, want 2", len(p.Chains))
	}
	return p
}

// labelTransport answers every chat request with the same short name and
// counts the calls. In-process, because httptest cannot bind in this sandbox.
type labelTransport struct {
	calls *int
	reply string
	fail  bool
}

func (f labelTransport) RoundTrip(*http.Request) (*http.Response, error) {
	*f.calls++
	if f.fail {
		return nil, errors.New("connection refused")
	}
	body := fmt.Sprintf(`{"choices":[{"message":{"content":%q}}]}`, f.reply)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func labelClient(calls *int, reply string, fail bool) *omlx.Client {
	return &omlx.Client{
		BaseURL: "http://fake.invalid/v1", Model: "test-chat",
		HTTP: &http.Client{Transport: labelTransport{calls: calls, reply: reply, fail: fail}},
	}
}

// TestHoleLabelsAreCachedPerChain: the second run must ask nothing. The cache
// is what makes the flag safe to leave on — a rerun of watchpath is free.
func TestHoleLabelsAreCachedPerChain(t *testing.T) {
	p := paths{dataDir: t.TempDir()}
	path := chainFixture(t)

	var calls int
	got := labelHoles(labelClient(&calls, "berlin drill", false), p, path, 10)
	if len(got) != 2 {
		t.Fatalf("named %d chains, want 2", len(got))
	}
	if calls != 2 {
		t.Errorf("first run made %d requests, want one per chain", calls)
	}

	calls = 0
	again := labelHoles(labelClient(&calls, "berlin drill", false), p, path, 10)
	if calls != 0 {
		t.Errorf("second run made %d requests, want 0 — the cache did not hold", calls)
	}
	if len(again) != len(got) {
		t.Errorf("second run named %d chains, first named %d", len(again), len(got))
	}
}

// TestHoleLabelsFallBackWhenTheServerIsGone: no labels, no error, and no
// long wait — one refusal is enough to know the rest will be refused too.
func TestHoleLabelsFallBackWhenTheServerIsGone(t *testing.T) {
	p := paths{dataDir: t.TempDir()}
	path := chainFixture(t)
	var calls int
	got := labelHoles(labelClient(&calls, "", true), p, path, 10)
	if len(got) != 0 {
		t.Errorf("a dead server produced %d labels", len(got))
	}
	if calls != 1 {
		t.Errorf("made %d requests against a dead server, want 1 before giving up", calls)
	}
	// And the page still renders, without the block.
	html, err := report.RenderWatchPathOpts(path, nil, time.Now(), report.WatchPathOpts{HoleLabels: got})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(html), `"holeLabels"`) {
		t.Error("the payload carries an empty holeLabels block")
	}
}

// TestHoleLabelSelectionIsDeterministic: deepest first. A selection that
// moved between runs would key the cache to an order rather than to a chain.
func TestHoleLabelSelectionIsDeterministic(t *testing.T) {
	p := paths{dataDir: t.TempDir()}
	path := chainFixture(t)
	var calls int
	// Only one label allowed: it has to be the deeper chain (5 videos).
	got := labelHoles(labelClient(&calls, "the deep one", false), p, path, 1)
	if len(got) != 1 {
		t.Fatalf("named %d chains, want 1", len(got))
	}
	for ci := range got {
		if path.Chains[ci].Len != 5 {
			t.Errorf("named the chain of %d, want the deepest (5)", path.Chains[ci].Len)
		}
	}
}

// TestHoleLabelsAreCleanedOrDropped pins what reaches the page: one short
// line, or nothing. A model that answers with a paragraph must not be able
// to push a table column off the screen.
func TestHoleLabelsAreCleanedOrDropped(t *testing.T) {
	for _, tc := range []struct{ reply, want string }{
		{"berlin drill", "berlin drill"},
		{`"Berlin Drill."`, "berlin drill"},
		{"  Berlin   Drill  \nand more prose", "berlin drill"},
		{"", ""},
		{strings.Repeat("very long name ", 6), ""},
		{"one two three four five six seven eight nine", ""},
	} {
		if got := cleanHoleLabel(tc.reply); got != tc.want {
			t.Errorf("cleanHoleLabel(%q) = %q, want %q", tc.reply, got, tc.want)
		}
	}
}

// TestHolePromptIsAStableCacheKey: the prompt IS the cache key, so it must
// not depend on anything that varies between runs — a channel set read out
// of a map, say.
func TestHolePromptIsAStableCacheKey(t *testing.T) {
	path := chainFixture(t)
	sys, user := holePrompt(path, 0)
	for i := 0; i < 20; i++ {
		s2, u2 := holePrompt(path, 0)
		if s2 != sys || u2 != user {
			t.Fatal("the naming prompt is not stable between calls")
		}
	}
	if !strings.Contains(user, "area:") || !strings.Contains(user, "titles:") {
		t.Errorf("the prompt misses its fields:\n%s", user)
	}
}
