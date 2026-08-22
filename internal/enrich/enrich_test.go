// SPDX-License-Identifier: GPL-3.0-or-later

package enrich

import (
	"testing"
)

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
