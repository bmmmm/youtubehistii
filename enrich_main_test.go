// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

// viewsN builds n distinct video IDs as views, most-watched first is
// irrelevant here — only the count matters.
func viewsN(n int) []takeout.View {
	out := make([]takeout.View, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, takeout.View{VideoID: fmt.Sprintf("vid%08d", i)})
	}
	return out
}

// runEnrichAll runs enrichAll under a deadline and fails the test if it does
// not return — a hang here is the bug, not a slow machine.
func runEnrichAll(t *testing.T, p paths, views []takeout.View, opts enrichOpts) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- enrichAll(p, views, opts) }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("enrichAll did not return — worker pool deadlocked")
		return nil
	}
}

// TestEnrichAllSurvivesEveryChunkFailing pins the deadlock that used to hang
// the whole command: workers left the jobs channel on the first error while
// the feeder was still blocked sending on it. With more chunks than workers
// and a fetcher that always fails, every worker bailed out and the feeder
// waited forever. This is what a missing yt-dlp binary looks like.
func TestEnrichAllSurvivesEveryChunkFailing(t *testing.T) {
	boom := errors.New("yt-dlp: executable file not found in $PATH")
	opts := enrichOpts{
		chunk: 1, workers: 2,
		fetch: func([]string, enrich.FetchOpts) (enrich.ChunkResult, error) {
			// The delay is what makes this deterministic: both workers must
			// still be busy when the feeder blocks on the next send, so the
			// feeder is already parked on the channel when they bail out.
			time.Sleep(50 * time.Millisecond)
			return enrich.ChunkResult{}, boom
		},
	}
	err := runEnrichAll(t, paths{dataDir: t.TempDir()}, viewsN(10), opts)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the fetcher error wrapped", err)
	}
}

func TestMissingByPriority(t *testing.T) {
	views := []takeout.View{
		{VideoID: "aaaaaaaaaa1"},                                                     // 1 view
		{VideoID: "bbbbbbbbbb2"}, {VideoID: "bbbbbbbbbb2"}, {VideoID: "bbbbbbbbbb2"}, // 3 views
		{VideoID: "cccccccccc3"}, {VideoID: "cccccccccc3"}, // 2 views, cached below
		{VideoID: ""}, // no-URL views never enter the fetch list
	}
	cache := enrich.Cache{Dir: t.TempDir()}
	if err := cache.Write(enrich.Meta{ID: "cccccccccc3", Title: "cached"}); err != nil {
		t.Fatal(err)
	}

	cached, err := cache.IDs()
	if err != nil {
		t.Fatal(err)
	}
	missing, uniqueTotal := missingByPriority(views, cached)
	if uniqueTotal != 3 {
		t.Errorf("uniqueTotal = %d, want 3", uniqueTotal)
	}
	if strings.Join(missing, ",") != "bbbbbbbbbb2,aaaaaaaaaa1" {
		t.Errorf("missing = %v, want most-watched first without cached", missing)
	}
}

// recorder captures the FetchOpts of every pass so a test can assert which
// yt-dlp configuration each pass actually asked for.
type recorder struct {
	mu    sync.Mutex
	calls []struct {
		ids  []string
		opts enrich.FetchOpts
	}
}

func (r *recorder) record(ids []string, o enrich.FetchOpts) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, struct {
		ids  []string
		opts enrich.FetchOpts
	}{append([]string(nil), ids...), o})
}

func (r *recorder) n() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func newState() *runState { return &runState{pauseUnit: time.Millisecond} }

// TestFetchOneFallsBackToDefaultClients is the safety net under the speed
// win: pinning one player client is only allowed because whatever it cannot
// resolve gets a second look on yt-dlp's own defaults.
func TestFetchOneFallsBackToDefaultClients(t *testing.T) {
	var rec recorder
	fetch := func(ids []string, o enrich.FetchOpts) (enrich.ChunkResult, error) {
		rec.record(ids, o)
		if o.Client != "" { // fast pass
			return enrich.ChunkResult{
				Fetched:     []enrich.Meta{{ID: "ok0000001"}},
				Unavailable: []string{"gone000002"},
				Failed:      []string{"slow000003"},
			}, nil
		}
		return enrich.ChunkResult{Fetched: []enrich.Meta{{ID: "slow000003"}}}, nil
	}

	res, err := fetchOne(fetch, []string{"ok0000001", "gone000002", "slow000003"}, 1.0, newState())
	if err != nil {
		t.Fatal(err)
	}
	if rec.n() != 2 {
		t.Fatalf("%d passes, want 2", rec.n())
	}
	if got := rec.calls[0].opts.Client; got != enrich.FastClient {
		t.Errorf("pass 1 client = %q, want %q", got, enrich.FastClient)
	}
	if got := rec.calls[1].opts.Client; got != "" {
		t.Errorf("pass 2 client = %q, want yt-dlp defaults", got)
	}
	// Only the transient failure is retried — a tombstone is permanent and
	// re-fetching it would waste a request on every single run.
	if strings.Join(rec.calls[1].ids, ",") != "slow000003" {
		t.Errorf("pass 2 ids = %v, want only the transient failure", rec.calls[1].ids)
	}
	if len(res.Fetched) != 2 || len(res.Unavailable) != 1 || len(res.Failed) != 0 {
		t.Errorf("merged = %+v", res)
	}
}

// TestFetchOneSkipsFallbackWhenRateLimited: retrying into an active bot check
// makes the throttling worse, so the second pass must be suppressed.
func TestFetchOneSkipsFallbackWhenRateLimited(t *testing.T) {
	var rec recorder
	fetch := func(ids []string, o enrich.FetchOpts) (enrich.ChunkResult, error) {
		rec.record(ids, o)
		return enrich.ChunkResult{Failed: []string{"bot0000001"}, RateLimited: true}, nil
	}
	res, err := fetchOne(fetch, []string{"bot0000001"}, 1.0, newState())
	if err != nil {
		t.Fatal(err)
	}
	if rec.n() != 1 {
		t.Errorf("%d passes, want 1 — no retry while rate limited", rec.n())
	}
	if !res.RateLimited {
		t.Error("RateLimited lost on the way out")
	}
}

// TestFetchOneDropsCookiesAndRetries: an unusable cookie source must degrade
// the run, not end it.
func TestFetchOneDropsCookiesAndRetries(t *testing.T) {
	var rec recorder
	fetch := func(ids []string, o enrich.FetchOpts) (enrich.ChunkResult, error) {
		rec.record(ids, o)
		if o.Cookies != "" {
			return enrich.ChunkResult{Failed: ids, CookiesFailed: true}, nil
		}
		return enrich.ChunkResult{Fetched: []enrich.Meta{{ID: ids[0]}}}, nil
	}
	st := newState()
	st.cookies = "chrome"

	res, err := fetchOne(fetch, []string{"ok0000001"}, 1.0, st)
	if err != nil {
		t.Fatal(err)
	}
	if rec.n() != 2 || rec.calls[1].opts.Cookies != "" {
		t.Fatalf("calls = %+v, want a cookie-less retry", rec.calls)
	}
	if len(res.Fetched) != 1 {
		t.Errorf("res = %+v, want the retry's result", res)
	}
	if _, cookies := st.snapshot(); cookies != "" {
		t.Errorf("cookies = %q, want them dropped for the rest of the run", cookies)
	}
}

// TestFetchOneAppliesBackoffToSleep: the backoff has to reach yt-dlp, not
// just the progress line.
func TestFetchOneAppliesBackoffToSleep(t *testing.T) {
	var rec recorder
	fetch := func(ids []string, o enrich.FetchOpts) (enrich.ChunkResult, error) {
		rec.record(ids, o)
		return enrich.ChunkResult{Fetched: []enrich.Meta{{ID: ids[0]}}}, nil
	}
	st := newState()
	st.penalise()
	st.penalise() // backoff 2 -> 4x

	if _, err := fetchOne(fetch, []string{"ok0000001"}, 1.5, st); err != nil {
		t.Fatal(err)
	}
	if got := rec.calls[0].opts.Sleep; got != 6.0 {
		t.Errorf("sleep = %v, want 1.5 * 2^2 = 6", got)
	}
}

func TestRunStateBackoffCapsAndRecovers(t *testing.T) {
	st := newState()
	for i := 0; i < maxBackoff+5; i++ {
		st.penalise()
	}
	if backoff, _ := st.snapshot(); backoff != maxBackoff {
		t.Errorf("backoff = %d, want it capped at %d", backoff, maxBackoff)
	}
	for i := 0; i < maxBackoff+5; i++ {
		st.recover()
	}
	if backoff, _ := st.snapshot(); backoff != 0 {
		t.Errorf("backoff = %d, want it back to 0", backoff)
	}
}

// TestEnrichAllBacksOffAndFinishes: a rate-limited run must still terminate
// and still report what it managed to fetch.
func TestEnrichAllBacksOffAndFinishes(t *testing.T) {
	var rec recorder
	opts := enrichOpts{
		chunk: 1, workers: 2, pauseUnit: time.Millisecond,
		fetch: func(ids []string, o enrich.FetchOpts) (enrich.ChunkResult, error) {
			rec.record(ids, o)
			return enrich.ChunkResult{Failed: ids, RateLimited: true}, nil
		},
	}
	if err := runEnrichAll(t, paths{dataDir: t.TempDir()}, viewsN(6), opts); err != nil {
		t.Fatalf("a rate-limited run must not fail outright: %v", err)
	}
	if rec.n() != 6 {
		t.Errorf("%d fetches, want all 6 chunks attempted", rec.n())
	}
}

// TestEnrichAllRecoversBackoffOnTombstoneOnlyChunk: a chunk where every video
// turns out to be deleted or members-only fetches nothing, but YouTube did
// answer for every ID. That is a clean chunk and must let the backoff decay —
// tying recovery to "fetched something" left a run throttled for no reason
// whenever it hit a patch of dead videos.
func TestEnrichAllRecoversBackoffOnTombstoneOnlyChunk(t *testing.T) {
	st := newState()
	st.penalise()
	st.penalise()
	if before, _ := st.snapshot(); before != 2 {
		t.Fatalf("setup: backoff = %d, want 2", before)
	}

	opts := enrichOpts{
		chunk: 1, workers: 1, pauseUnit: time.Millisecond, state: st,
		fetch: func(ids []string, o enrich.FetchOpts) (enrich.ChunkResult, error) {
			return enrich.ChunkResult{Unavailable: ids}, nil
		},
	}
	if err := runEnrichAll(t, paths{dataDir: t.TempDir()}, viewsN(3), opts); err != nil {
		t.Fatal(err)
	}
	if after, _ := st.snapshot(); after != 0 {
		t.Errorf("backoff = %d after 3 clean tombstone chunks, want it decayed to 0", after)
	}
}

func TestResolveCookieSource(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"none", ""},
		{"firefox", "firefox"},
		{"chrome:Profile 2", "chrome:Profile 2"}, // full yt-dlp syntax survives
	} {
		if got := resolveCookieSource(tc.in); got != tc.want {
			t.Errorf("resolveCookieSource(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// "auto" depends on what is installed; it must return either nothing or
	// one of the browsers it knows, never a probe path.
	got := resolveCookieSource("auto")
	if got != "" {
		known := false
		for _, src := range cookieSources {
			if src.browser == got {
				known = true
			}
		}
		if !known {
			t.Errorf("auto = %q, not a known browser", got)
		}
	}
}

func TestParseRejectsHTMLExport(t *testing.T) {
	_, _, err := takeout.Parse(strings.NewReader("<!DOCTYPE html><html>…</html>"))
	if err == nil || !strings.Contains(err.Error(), "HTML export") {
		t.Errorf("err = %v, want HTML-export hint", err)
	}
}
