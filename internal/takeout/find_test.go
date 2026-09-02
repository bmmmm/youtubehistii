// SPDX-License-Identifier: GPL-3.0-or-later

package takeout

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindExportGermanLayout(t *testing.T) {
	// The layout the user drops into data/ verbatim.
	root := t.TempDir()
	touch(t, filepath.Join(root, "YouTube und YouTube Music", "Verlauf", "Wiedergabeverlauf.json"))
	touch(t, filepath.Join(root, "YouTube und YouTube Music", "Verlauf", "Suchverlauf.json"))
	touch(t, filepath.Join(root, "YouTube und YouTube Music", "Abos", "Abos.csv"))
	touch(t, filepath.Join(root, "YouTube und YouTube Music", "Playlists", "Playlists.csv"))

	h, s, _, _ := FindExport(root)
	if filepath.Base(h) != "Wiedergabeverlauf.json" {
		t.Errorf("history = %q", h)
	}
	if filepath.Base(s) != "Abos.csv" {
		t.Errorf("subs = %q", s)
	}
}

func TestFindExportEnglishFlat(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "watch-history.json"))
	touch(t, filepath.Join(root, "subscriptions.csv"))
	h, s, _, _ := FindExport(root)
	if filepath.Base(h) != "watch-history.json" || filepath.Base(s) != "subscriptions.csv" {
		t.Errorf("h=%q s=%q", h, s)
	}
}

func TestFindExportDetectsHTMLOnlyExport(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "YouTube und YouTube Music", "Verlauf", "Wiedergabeverlauf.html"))
	h, _, html, _ := FindExport(root)
	if h != "" {
		t.Errorf("history = %q, want none", h)
	}
	if filepath.Base(html) != "Wiedergabeverlauf.html" {
		t.Errorf("html = %q", html)
	}
}

func TestFindExportSkipsToolDirs(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "cache", "meta", "watch-history.json"))
	h, s, _, _ := FindExport(root)
	if h != "" || s != "" {
		t.Errorf("found files inside cache/: h=%q s=%q", h, s)
	}
}

func TestFindExportEmpty(t *testing.T) {
	h, s, html, ignored := FindExport(t.TempDir())
	if h != "" || s != "" || html != "" || len(ignored) != 0 {
		t.Errorf("h=%q s=%q html=%q ignored=%v", h, s, html, ignored)
	}
}

// unpackExport writes one export root, optionally without its subscriptions
// CSV, and stamps every file with mtime so the choice between two of them is
// decided by the code rather than by how fast the test wrote its fixture.
func unpackExport(t *testing.T, root, name string, mtime time.Time, withSubs bool) (history, subs string) {
	t.Helper()
	history = filepath.Join(root, name, "YouTube und YouTube Music", "Verlauf", "Wiedergabeverlauf.json")
	touch(t, history)
	stamp(t, history, mtime)
	if withSubs {
		subs = filepath.Join(root, name, "YouTube und YouTube Music", "Abos", "Abos.csv")
		touch(t, subs)
		stamp(t, subs, mtime)
	}
	return history, subs
}

func stamp(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// Two Takeout folders side by side is the normal state of a downloads folder,
// and picking each file independently paired one export's history with the
// other's subscriptions: two different points in time, silently joined.
func TestFindExportPrefersTheNewestExport(t *testing.T) {
	now := time.Now()

	t.Run("history and subscriptions come from the same export", func(t *testing.T) {
		root := t.TempDir()
		oldHistory, oldSubs := unpackExport(t, root, "Takeout", now.Add(-48*time.Hour), true)
		newHistory, newSubs := unpackExport(t, root, "Takeout-2", now, true)

		h, s, _, ignored := FindExport(root)
		if h != newHistory {
			t.Errorf("history = %q, want %q", h, newHistory)
		}
		if s != newSubs {
			t.Errorf("subs = %q, want %q — a history and a subscriptions file from different exports", s, newSubs)
		}
		want := map[string]bool{oldHistory: true, oldSubs: true}
		if len(ignored) != len(want) {
			t.Fatalf("ignored = %v, want the two files of the older export", ignored)
		}
		for _, p := range ignored {
			if !want[p] {
				t.Errorf("ignored names %q, which is not from the older export", p)
			}
		}
	})

	// The literal defect: the older export has no subscriptions CSV, so
	// taking the first hit of each kind read January's views and August's
	// subscriptions — one report, two points in time.
	t.Run("the older history does not pull in the newer subscriptions", func(t *testing.T) {
		root := t.TempDir()
		oldHistory, _ := unpackExport(t, root, "Takeout", now.Add(-48*time.Hour), false)
		newHistory, newSubs := unpackExport(t, root, "Takeout-2", now, true)

		h, s, _, ignored := FindExport(root)
		if h != newHistory {
			t.Errorf("history = %q, want %q", h, newHistory)
		}
		if s != newSubs {
			t.Errorf("subs = %q, want %q", s, newSubs)
		}
		if len(ignored) != 1 || ignored[0] != oldHistory {
			t.Errorf("ignored = %v, want [%s]", ignored, oldHistory)
		}
	})

	// The newer export was downloaded without the subscriptions box ticked.
	// Falling back to the older export's CSV would describe a set of
	// subscriptions that has had a year to change.
	t.Run("no subscriptions beats the other export's subscriptions", func(t *testing.T) {
		root := t.TempDir()
		unpackExport(t, root, "Takeout", now.Add(-48*time.Hour), true)
		newHistory, _ := unpackExport(t, root, "Takeout-2", now, false)

		h, s, _, ignored := FindExport(root)
		if h != newHistory {
			t.Errorf("history = %q, want %q", h, newHistory)
		}
		if s != "" {
			t.Errorf("subs = %q, want none — it belongs to the export that was not chosen", s)
		}
		if len(ignored) != 2 {
			t.Errorf("ignored = %v, want both files of the older export", ignored)
		}
	})
}

// The export's own folder as root: history under Verlauf/, subscriptions
// under Abos/. They share no path element, so pairing by directory has to
// stay off where there is only one export to pair within.
func TestFindExportInsideTheYouTubeFolder(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "Verlauf", "Wiedergabeverlauf.json"))
	touch(t, filepath.Join(root, "Abos", "Abos.csv"))

	h, s, _, _ := FindExport(root)
	if filepath.Base(h) != "Wiedergabeverlauf.json" || filepath.Base(s) != "Abos.csv" {
		t.Errorf("h=%q s=%q", h, s)
	}
}
