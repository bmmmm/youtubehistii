// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bmmmm/youtubehistii/internal/takeout"
)

// The export the import parses in these tests: the same fixture the parser
// tests read, so the numbers below (7 views, 6 unique videos, 2026-07-01 ..
// 2026-07-06) are the parser's own and not a second copy of the truth.
const sampleExport = "testdata/watch-history.sample.json"

// existingHistory writes a history.jsonl to overwrite: n views spanning from
// `from` in daily steps, each its own video.
func existingHistory(t *testing.T, p paths, n int, from time.Time) {
	t.Helper()
	var views []takeout.View
	for i := range n {
		views = append(views, takeout.View{
			VideoID:   "old" + string(rune('a'+i%26)) + strings.Repeat("0", 7),
			Title:     "an old view",
			WatchedAt: from.AddDate(0, 0, i),
		})
	}
	if err := writeJSONL(p.historyJSONL(), views); err != nil {
		t.Fatal(err)
	}
}

func checksum(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func exportPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(sampleExport)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// Google deletes watch history after 3, 18 or 36 months depending on the
// account's auto-delete setting, so a legitimate fresh export can be a
// fraction of the file it replaces. The import used to write it anyway.
func TestImportRefusesAShorterExport(t *testing.T) {
	cases := []struct {
		name  string
		views int
		from  time.Time
		want  string // the part of the refusal that names WHICH signal fired
	}{
		// The everyday shape: nine years on disk, three months in the export.
		{"fewer views than are on disk", 40, time.Date(2017, 3, 2, 12, 0, 0, 0, time.UTC),
			"7 views instead of 40"},
		// The signal the view count cannot give: the export has MORE views
		// than the file — a heavy year against a thin decade — and still
		// begins nine years later, because auto-delete takes the old end.
		{"the old end is gone even though the file grew", 5, time.Date(2017, 3, 2, 12, 0, 0, 0, time.UTC),
			"it starts at 2026-07-01, the file on disk at 2017-03-02"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := paths{dataDir: t.TempDir()}
			existingHistory(t, p, c.views, c.from)
			before := checksum(t, p.historyJSONL())

			out, err := captureStdout(t, func() error {
				return cmdImport([]string{"-data", p.dataDir, exportPath(t)})
			})
			if err == nil {
				t.Fatalf("import replaced the history without a word:\n%s", out)
			}
			msg := err.Error()
			for _, want := range []string{c.want, "auto-delete", "-force", "views"} {
				if !strings.Contains(msg, want) {
					t.Errorf("the refusal never mentions %q:\n%s", want, msg)
				}
			}
			// The point of refusing is the file, not the message.
			if after := checksum(t, p.historyJSONL()); after != before {
				t.Errorf("history.jsonl changed despite the refusal: %s -> %s", before, after)
			}
		})
	}
}

func TestImportForceOverwrites(t *testing.T) {
	p := paths{dataDir: t.TempDir()}
	existingHistory(t, p, 40, time.Date(2017, 3, 2, 12, 0, 0, 0, time.UTC))
	before := checksum(t, p.historyJSONL())

	out, err := captureStdout(t, func() error {
		return cmdImport([]string{"-data", p.dataDir, "-force", exportPath(t)})
	})
	if err != nil {
		t.Fatalf("cmdImport -force: %v\n%s", err, out)
	}
	if after := checksum(t, p.historyJSONL()); after == before {
		t.Error("-force left the old history in place")
	}
	views, err := readJSONL[takeout.View](p.historyJSONL())
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 7 {
		t.Errorf("history has %d views, want the export's 7", len(views))
	}
}

// A growing history is the normal case, and the numbers that say how much it
// grew are the ones nobody can reconstruct after the overwrite.
func TestImportPrintsTheDelta(t *testing.T) {
	p := paths{dataDir: t.TempDir()}
	// Three views inside the export's own span: nothing is lost, so the
	// import proceeds and reports the difference.
	if err := writeJSONL(p.historyJSONL(), []takeout.View{
		{VideoID: "abc123DEF45", WatchedAt: time.Date(2026, 7, 1, 18, 30, 0, 0, time.UTC)},
		{VideoID: "def456GHI78", WatchedAt: time.Date(2026, 7, 2, 9, 15, 30, 0, time.UTC)},
		{VideoID: "ghi789JKL01", WatchedAt: time.Date(2026, 7, 3, 21, 0, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error {
		return cmdImport([]string{"-data", p.dataDir, exportPath(t)})
	})
	if err != nil {
		t.Fatalf("cmdImport: %v\n%s", err, out)
	}
	want := "since last: +4 views, +3 unique videos (was 2026-07-01 .. 2026-07-03)"
	if !strings.Contains(out, want) {
		t.Errorf("import printed no delta line\nwant: %s\ngot:\n%s", want, out)
	}

	// Re-importing the same export changes nothing, and says so instead of
	// reporting a +0 that reads like a failed import.
	out, err = captureStdout(t, func() error {
		return cmdImport([]string{"-data", p.dataDir, exportPath(t)})
	})
	if err != nil {
		t.Fatalf("second cmdImport: %v\n%s", err, out)
	}
	if !strings.Contains(out, "since last: unchanged") {
		t.Errorf("re-importing the same export did not report it as unchanged:\n%s", out)
	}
}

// The first import has nothing to compare against, and must not say it does.
func TestImportFirstRunSaysNothingAboutADelta(t *testing.T) {
	p := paths{dataDir: t.TempDir()}
	out, err := captureStdout(t, func() error {
		return cmdImport([]string{"-data", p.dataDir, exportPath(t)})
	})
	if err != nil {
		t.Fatalf("cmdImport: %v\n%s", err, out)
	}
	if strings.Contains(out, "since last") {
		t.Errorf("a first import compared itself against nothing:\n%s", out)
	}
}

// A half-written history is a state the import is supposed to get you OUT of;
// refusing to read it would close that door.
func TestImportProceedsPastAnUnreadableHistory(t *testing.T) {
	p := paths{dataDir: t.TempDir()}
	if err := os.MkdirAll(p.dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.historyJSONL(), []byte("{not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error {
		return cmdImport([]string{"-data", p.dataDir, exportPath(t)})
	})
	if err != nil {
		t.Fatalf("a corrupt old history blocked a good export: %v\n%s", err, out)
	}
	views, err := readJSONL[takeout.View](p.historyJSONL())
	if err != nil || len(views) != 7 {
		t.Errorf("history not replaced: %d views, err %v", len(views), err)
	}
}
