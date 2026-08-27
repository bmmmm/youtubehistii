// SPDX-License-Identifier: GPL-3.0-or-later

package enrich

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeYtDLP is a stand-in for the real binary: it reproduces yt-dlp's shape —
// one JSON document per line on stdout, "ERROR: [youtube] <id>: ..." on
// stderr, non-zero exit when anything failed — and decides per ID from the ID
// itself. That covers the paths a network test never could reach reliably.
//
//	ok*    -> full record
//	gone*  -> permanently unavailable
//	bot*   -> rate limiting (typographic apostrophe, exactly as yt-dlp emits)
//	other  -> transient failure
const fakeYtDLP = `#!/bin/sh
printf '%s\n' "$*" >> "$ARGLOG"
case "$*" in *--cookies-from-browser*) [ -n "$COOKIE_FAIL" ] && {
  echo "ERROR: could not find chrome cookies database" >&2; exit 1; } ;; esac
batch=""; prev=""
for a in "$@"; do
  [ "$prev" = "-a" ] && batch="$a"
  prev="$a"
done
rc=0
while read -r url; do
  id=${url##*v=}
  case "$id" in
    ok*)   ;;
    gone*) echo "ERROR: [youtube] $id: Video unavailable. This video has been removed" >&2; rc=1; continue ;;
    bot*)  echo "ERROR: [youtube] $id: Sign in to confirm you’re not a bot" >&2; rc=1; continue ;;
    *)     echo "ERROR: [youtube] $id: Unable to download webpage: timed out" >&2; rc=1; continue ;;
  esac
  printf '{"id":"%s","title":"T","channel":"C","duration":42.0,"categories":["Gaming"],"tags":["t1"],"upload_date":"20260101"}\n' "$id"
done < "$batch"
exit $rc
`

// useFakeYtDLP points FetchChunk at the fake and returns a reader for the
// arguments it was invoked with, so tests can assert on the yt-dlp flags.
func useFakeYtDLP(t *testing.T) func() string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-yt-dlp")
	if err := os.WriteFile(bin, []byte(fakeYtDLP), 0o755); err != nil {
		t.Fatal(err)
	}
	argLog := filepath.Join(dir, "args.log")
	t.Setenv("ARGLOG", argLog)

	orig := ytDLPBin
	ytDLPBin = bin
	t.Cleanup(func() { ytDLPBin = orig })

	return func() string {
		b, err := os.ReadFile(argLog)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func TestFetchChunkSplitsOutcomes(t *testing.T) {
	args := useFakeYtDLP(t)
	res, err := FetchChunk([]string{"ok0000001", "gone000002", "flaky00003"}, FetchOpts{Sleep: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fetched) != 1 || res.Fetched[0].ID != "ok0000001" {
		t.Errorf("Fetched = %+v", res.Fetched)
	}
	if len(res.Fetched) > 0 && res.Fetched[0].Categories[0] != "Gaming" {
		t.Errorf("categories lost: %+v", res.Fetched[0])
	}
	if strings.Join(res.Unavailable, ",") != "gone000002" {
		t.Errorf("Unavailable = %v", res.Unavailable)
	}
	if strings.Join(res.Failed, ",") != "flaky00003" {
		t.Errorf("Failed = %v", res.Failed)
	}
	if res.RateLimited {
		t.Error("RateLimited set without a bot check")
	}
	// A chunk where every single video is gone must not be fatal — that was
	// a run-killer before 5e1f239 and stays covered here.
	if got := args(); !strings.Contains(got, "--ignore-errors") {
		t.Errorf("args = %q", got)
	}
}

func TestFetchChunkPassesSleepAndCookies(t *testing.T) {
	args := useFakeYtDLP(t)
	if _, err := FetchChunk([]string{"ok0000001"}, FetchOpts{Sleep: 2.5, Cookies: "brave"}); err != nil {
		t.Fatal(err)
	}
	got := args()
	for _, want := range []string{"--cookies-from-browser brave", "--sleep-requests 2.5"} {
		if !strings.Contains(got, want) {
			t.Errorf("args %q missing %q", got, want)
		}
	}
	// yt-dlp picks its own player clients; pinning one was measured to buy
	// no wall clock and cost a whole fallback pass, so nothing overrides it.
	if strings.Contains(got, "player_client") {
		t.Errorf("args %q pin a player client", got)
	}
}

func TestFetchChunkFlagsRateLimiting(t *testing.T) {
	useFakeYtDLP(t)
	res, err := FetchChunk([]string{"ok0000001", "bot0000002"}, FetchOpts{Sleep: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !res.RateLimited {
		t.Error("bot check did not set RateLimited")
	}
	// A bot check is transient: it must never tombstone the video.
	if len(res.Unavailable) != 0 {
		t.Errorf("Unavailable = %v, want none", res.Unavailable)
	}
	if strings.Join(res.Failed, ",") != "bot0000002" {
		t.Errorf("Failed = %v", res.Failed)
	}
}

func TestFetchChunkReportsUnusableCookies(t *testing.T) {
	useFakeYtDLP(t)
	t.Setenv("COOKIE_FAIL", "1")
	res, err := FetchChunk([]string{"ok0000001"}, FetchOpts{Sleep: 0, Cookies: "chrome"})
	if err != nil {
		t.Fatalf("a broken cookie source must not be fatal: %v", err)
	}
	if !res.CookiesFailed {
		t.Error("CookiesFailed not set")
	}
	if len(res.Unavailable) != 0 {
		t.Errorf("cookie failure tombstoned %v", res.Unavailable)
	}
	if strings.Join(res.Failed, ",") != "ok0000001" {
		t.Errorf("Failed = %v, want the whole chunk retryable", res.Failed)
	}
}

func TestFetchChunkFatalWhenBinaryMissing(t *testing.T) {
	orig := ytDLPBin
	ytDLPBin = filepath.Join(t.TempDir(), "does-not-exist")
	t.Cleanup(func() { ytDLPBin = orig })

	if _, err := FetchChunk([]string{"ok0000001"}, FetchOpts{Sleep: 0}); err == nil {
		t.Fatal("want an error when yt-dlp cannot be executed at all")
	}
}

func TestCacheWriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	c := Cache{Dir: dir}
	if err := c.Write(Meta{ID: "abc123DEF45", Title: "T"}); err != nil {
		t.Fatal(err)
	}
	// A temp file left behind would be picked up as a cache entry by
	// Has/ReadAll and poison the whole cache, so nothing but the final
	// .json may remain.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "abc123DEF45.json" {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("cache dir = %v, want exactly abc123DEF45.json", names)
	}
}

func TestCacheIDs(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	ids, err := c.IDs()
	if err != nil || len(ids) != 0 {
		t.Fatalf("empty cache: ids = %v, err = %v", ids, err)
	}
	for _, id := range []string{"aaa1111111", "bbb2222222"} {
		if err := c.Write(Meta{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	ids, err = c.IDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || !ids["aaa1111111"] || !ids["bbb2222222"] {
		t.Errorf("IDs = %v", ids)
	}
}

// TestCacheIDsMatchesHas keeps the fast index and the per-ID check from
// drifting apart — they answer the same question on different paths.
func TestCacheIDsMatchesHas(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	if err := c.Write(Meta{ID: "aaa1111111"}); err != nil {
		t.Fatal(err)
	}
	ids, err := c.IDs()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"aaa1111111", "zzz9999999"} {
		if ids[id] != c.Has(id) {
			t.Errorf("%s: IDs=%v Has=%v", id, ids[id], c.Has(id))
		}
	}
}

func TestPrune(t *testing.T) {
	// Shape of a real yt-dlp -j record, reduced; duration arrives as float.
	raw := []byte(`{
		"id": "abc123DEF45",
		"title": "Rust Base Building Guide 2026",
		"channel": "RustLetsPlayGuy",
		"channel_id": "UCrust000000000000000001",
		"duration": 1234.0,
		"categories": ["Gaming"],
		"tags": ["rust", "base building"],
		"upload_date": "20260630",
		"formats": [{"format_id": "137", "ext": "mp4"}],
		"description": "very long text we do not keep"
	}`)
	m, err := Prune(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Duration != 1234 {
		t.Errorf("Duration = %d, want 1234", m.Duration)
	}
	if len(m.Tags) != 2 || m.Tags[0] != "rust" {
		t.Errorf("Tags = %v", m.Tags)
	}
	if m.Categories[0] != "Gaming" {
		t.Errorf("Categories = %v", m.Categories)
	}
}

func TestPruneRejectsNoID(t *testing.T) {
	if _, err := Prune([]byte(`{"title": "x"}`)); err == nil {
		t.Fatal("want error for record without id")
	}
}

func TestClassifyErrors(t *testing.T) {
	stderr := `ERROR: [youtube] gone0000001: Video unavailable. This video has been removed
ERROR: [youtube] priv0000002: Private video. Sign in if you've been granted access
ERROR: [youtube] flaky000003: Unable to download webpage: timed out`
	gone, failed := ClassifyErrors(stderr, []string{"gone0000001", "priv0000002", "flaky000003", "silent00004"})
	if len(gone) != 2 || gone[0] != "gone0000001" || gone[1] != "priv0000002" {
		t.Errorf("gone = %v", gone)
	}
	// timeout and an ID with no stderr line at all are both transient
	if len(failed) != 2 || failed[0] != "flaky000003" || failed[1] != "silent00004" {
		t.Errorf("failed = %v", failed)
	}
}

func TestClassifyErrorsAgeRestrictedIsGoneNotBotCheck(t *testing.T) {
	stderr := `ERROR: [youtube] age00000001: Sign in to confirm your age. This video may be inappropriate for some users
ERROR: [youtube] bot00000002: Sign in to confirm you're not a bot`
	gone, failed := ClassifyErrors(stderr, []string{"age00000001", "bot00000002"})
	// Age verification is permanent without --cookies -> tombstone it.
	if len(gone) != 1 || gone[0] != "age00000001" {
		t.Errorf("gone = %v, want [age00000001]", gone)
	}
	// Bot-check signals IP-level rate limiting -> transient, retry later.
	if len(failed) != 1 || failed[0] != "bot00000002" {
		t.Errorf("failed = %v, want [bot00000002]", failed)
	}
}

// TestClassifyErrorsMembersOnlyIsGone: verbatim yt-dlp output for a
// members-only video. Before this was a gone marker it counted as transient,
// so every run paid the full request cost to fail on it again.
func TestClassifyErrorsMembersOnlyIsGone(t *testing.T) {
	stderr := `ERROR: [youtube] mem00000001: This video is available to this channel's members on level: LTT Members Plus (or any higher level). Join this channel to get access to members-only content and other exclusive perks.`
	gone, failed := ClassifyErrors(stderr, []string{"mem00000001"})
	if len(gone) != 1 || gone[0] != "mem00000001" {
		t.Errorf("gone = %v, want the members-only video tombstoned", gone)
	}
	if len(failed) != 0 {
		t.Errorf("failed = %v, want none", failed)
	}
}

func TestCacheRoundtrip(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	if c.Has("abc123DEF45") {
		t.Fatal("empty cache claims to have entry")
	}
	want := Meta{ID: "abc123DEF45", Title: "T", Duration: 60, Tags: []string{"x"}}
	if err := c.Write(want); err != nil {
		t.Fatal(err)
	}
	if !c.Has("abc123DEF45") {
		t.Fatal("cache misses written entry")
	}
	all, err := c.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := all["abc123DEF45"]
	if !ok || got.Title != "T" || got.Duration != 60 {
		t.Errorf("ReadAll = %+v", all)
	}
}

func TestCacheRejectsPathTraversal(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	if err := c.Write(Meta{ID: "../evil"}); err == nil {
		t.Fatal("want error for path-traversal id")
	}
	if c.Has("../evil") {
		t.Fatal("Has accepted traversal id")
	}
}

// TestCacheReadAllConcurrentIdentity guards the one property that makes the
// worker pool in ReadAll safe: however many goroutines race to fill it, the
// resulting map must be byte-for-byte what the old serial loop produced.
func TestCacheReadAllConcurrentIdentity(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	want := map[string]Meta{}
	for i := range 50 {
		id := fmt.Sprintf("vid%08d", i)
		m := Meta{ID: id, Title: fmt.Sprintf("Title %d", i), Duration: i, Tags: []string{"a", "b"}}
		if err := c.Write(m); err != nil {
			t.Fatal(err)
		}
		want[id] = m
	}
	got, err := c.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("ReadAll returned %d entries, want %d", len(got), len(want))
	}
	for id, wm := range want {
		gm, ok := got[id]
		if !ok {
			t.Fatalf("missing entry %s", id)
		}
		if gm.Title != wm.Title || gm.Duration != wm.Duration || strings.Join(gm.Tags, ",") != strings.Join(wm.Tags, ",") {
			t.Errorf("%s: got %+v, want %+v", id, gm, wm)
		}
	}
}

// TestCacheReadAllErrorNamesFile: a single corrupt JSON file among good ones
// must still surface as an error naming that file, exactly as the serial
// loop did before the worker pool.
func TestCacheReadAllErrorNamesFile(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	for _, id := range []string{"aaa1111111", "ccc3333333"} {
		if err := c.Write(Meta{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	broken := filepath.Join(c.Dir, "bbb2222222.json")
	if err := os.WriteFile(broken, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := c.ReadAll()
	if err == nil {
		t.Fatal("want an error for the corrupt entry")
	}
	if !strings.Contains(err.Error(), "bbb2222222.json") {
		t.Errorf("error %q does not name the broken file", err)
	}
}

// TestCacheReadAllErrorIsDeterministic: with several bad files, workers can
// hit them "at the same time" -- ReadAll must still always resolve to the
// SAME error (the one whose file sorts first in os.ReadDir order), run after
// run, or the same broken cache would report a different failure depending
// on scheduling luck.
func TestCacheReadAllErrorIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	c := Cache{Dir: dir}
	if err := c.Write(Meta{ID: "aaa1111111"}); err != nil {
		t.Fatal(err)
	}
	// "bbb..." sorts before "ccc..." in the directory listing ReadAll uses,
	// so bbb's parse error is the one that must always win.
	if err := os.WriteFile(filepath.Join(dir, "bbb2222222.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ccc3333333.json"), []byte("also not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 20; i++ {
		_, err := c.ReadAll()
		if err == nil {
			t.Fatal("want an error")
		}
		if !strings.Contains(err.Error(), "bbb2222222.json") {
			t.Errorf("run %d: error %q, want it to always name bbb2222222.json", i, err)
		}
	}
}

// TestCacheReadAllEmptyDir and TestCacheReadAllMissingDir: ReadAll's behavior
// on no data must be untouched by the switch to a worker pool -- an empty
// map, no error, whether the directory exists and is empty or doesn't exist
// at all.
func TestCacheReadAllEmptyDir(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	got, err := c.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("ReadAll = %v, want empty map", got)
	}
}

func TestCacheReadAllMissingDir(t *testing.T) {
	c := Cache{Dir: filepath.Join(t.TempDir(), "does-not-exist")}
	got, err := c.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("ReadAll = %v, want empty map", got)
	}
}
