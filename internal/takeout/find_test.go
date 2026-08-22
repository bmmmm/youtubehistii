// SPDX-License-Identifier: GPL-3.0-or-later

package takeout

import (
	"os"
	"path/filepath"
	"testing"
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

	h, s, _ := FindExport(root)
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
	h, s, _ := FindExport(root)
	if filepath.Base(h) != "watch-history.json" || filepath.Base(s) != "subscriptions.csv" {
		t.Errorf("h=%q s=%q", h, s)
	}
}

func TestFindExportDetectsHTMLOnlyExport(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "YouTube und YouTube Music", "Verlauf", "Wiedergabeverlauf.html"))
	h, _, html := FindExport(root)
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
	h, s, _ := FindExport(root)
	if h != "" || s != "" {
		t.Errorf("found files inside cache/: h=%q s=%q", h, s)
	}
}

func TestFindExportEmpty(t *testing.T) {
	h, s, html := FindExport(t.TempDir())
	if h != "" || s != "" || html != "" {
		t.Errorf("h=%q s=%q html=%q", h, s, html)
	}
}
