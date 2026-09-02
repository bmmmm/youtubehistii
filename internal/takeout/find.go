// SPDX-License-Identifier: GPL-3.0-or-later

package takeout

import (
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// Known per-locale file names inside a Takeout export ("YouTube und YouTube
// Music" (DE) / "YouTube and YouTube Music" (EN) directory or its parent).
var (
	historyNames = map[string]bool{"wiedergabeverlauf.json": true, "watch-history.json": true}
	subsNames    = map[string]bool{"abos.csv": true, "subscriptions.csv": true}
	// The HTML variants are Takeout's default export format — useless to us,
	// but finding one lets the error message say precisely what went wrong.
	htmlNames = map[string]bool{"wiedergabeverlauf.html": true, "watch-history.html": true}
)

const findMaxDepth = 4

// candidate is one recognized file plus what it takes to choose between
// several of them: when it was written, and which export it came from.
type candidate struct {
	path  string
	mtime time.Time
	group string
}

// exportGroup names the export a file belongs to: its first path element
// under root, which is what tells "Takeout/" from "Takeout-2/". A file lying
// directly in root gets ".".
func exportGroup(rel string) string {
	if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
		return rel[:i]
	}
	return "."
}

// newest returns the candidate written last. Ties keep the first the walk
// saw — same mtime makes two files indistinguishable, and the choice still
// has to be the same on every run.
func newest(cands []candidate) (candidate, bool) {
	if len(cands) == 0 {
		return candidate{}, false
	}
	best := cands[0]
	for _, c := range cands[1:] {
		if c.mtime.After(best.mtime) {
			best = c
		}
	}
	return best, true
}

func inGroup(cands []candidate, group string) []candidate {
	var out []candidate
	for _, c := range cands {
		if c.group == group {
			out = append(out, c)
		}
	}
	return out
}

func groupCount(cands []candidate) int {
	seen := map[string]bool{}
	for _, c := range cands {
		seen[c.group] = true
	}
	return len(seen)
}

// FindExport walks root (a data dir, a Takeout root, or the "YouTube …"
// folder itself) and returns the watch-history JSON, subscriptions CSV and —
// when only the HTML export exists — the history HTML path. Any result may
// be "" when not present. ignored lists the recognized files that lost, so
// the caller can name them instead of choosing in silence.
//
// Every match is collected before anything is chosen. Taking the first hit
// per kind meant that two exports unpacked side by side — Takeout/ from
// January and Takeout-2/ from August — paired January's views with August's
// subscriptions, two moments in time joined with nothing saying so.
func FindExport(root string) (historyPath, subsPath, htmlPath string, ignored []string) {
	var histories, subscriptions, htmls []candidate
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
		c := candidate{path: path, group: exportGroup(rel)}
		if info, infoErr := d.Info(); infoErr == nil {
			c.mtime = info.ModTime()
		}
		switch name := strings.ToLower(d.Name()); {
		case historyNames[name]:
			histories = append(histories, c)
		case subsNames[name]:
			subscriptions = append(subscriptions, c)
		case htmlNames[name]:
			htmls = append(htmls, c)
		}
		return nil
	})

	history, haveHistory := newest(histories)

	// The subscriptions CSV has to come from the same export as the history
	// file — but only where there is more than one export to confuse. The
	// group is the first path element, and when root IS the "YouTube und
	// YouTube Music" folder the history sits under Verlauf/ and the CSV under
	// Abos/: different groups, one export. Constraining that layout would
	// drop a perfectly good subscriptions file to guard against an ambiguity
	// it does not have.
	pool := subscriptions
	if haveHistory && groupCount(histories) > 1 {
		pool = inGroup(subscriptions, history.group)
	}
	subs, haveSubs := newest(pool)

	// Only the two files that get used are worth reporting on. Extra HTML
	// exports are noise: htmlPath exists to explain an error, and once a
	// JSON history was found it is never read at all.
	for _, c := range append(append([]candidate{}, histories...), subscriptions...) {
		if haveHistory && c.path == history.path {
			continue
		}
		if haveSubs && c.path == subs.path {
			continue
		}
		ignored = append(ignored, c.path)
	}

	html, _ := newest(htmls)
	return history.path, subs.path, html.path, ignored
}
