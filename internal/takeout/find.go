// SPDX-License-Identifier: GPL-3.0-or-later

package takeout

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// Known per-locale file names inside a Takeout export ("YouTube und YouTube
// Music" (DE) / "YouTube and YouTube Music" (EN) directory or its parent).
var (
	historyNames = map[string]bool{"wiedergabeverlauf.json": true, "watch-history.json": true}
	subsNames    = map[string]bool{"abos.csv": true, "subscriptions.csv": true}
)

const findMaxDepth = 4

// FindExport walks root (a data dir, a Takeout root, or the "YouTube …"
// folder itself) and returns the watch-history JSON and subscriptions CSV
// paths. Either result may be "" when not present.
func FindExport(root string) (historyPath, subsPath string) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if d.IsDir() {
			if strings.Count(rel, string(filepath.Separator)) >= findMaxDepth {
				return fs.SkipDir
			}
			// Never descend into the tool's own output/cache.
			if rel == "cache" || rel == "out" {
				return fs.SkipDir
			}
			return nil
		}
		name := strings.ToLower(d.Name())
		switch {
		case historyNames[name] && historyPath == "":
			historyPath = path
		case subsNames[name] && subsPath == "":
			subsPath = path
		}
		return nil
	})
	return historyPath, subsPath
}
