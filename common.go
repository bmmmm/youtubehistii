// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/taxonomy"
)

// paths centralizes every file the pipeline reads or writes, so each stage
// stays inspectable: nothing is stored outside the data directory.
type paths struct {
	dataDir string
}

func (p paths) historyJSONL() string       { return filepath.Join(p.dataDir, "history.jsonl") }
func (p paths) subscriptionsCSV() string   { return filepath.Join(p.dataDir, "subscriptions.csv") }
func (p paths) subscriptionsJSONL() string { return filepath.Join(p.dataDir, "subscriptions.jsonl") }
func (p paths) metaCacheDir() string       { return filepath.Join(p.dataDir, "cache", "meta") }
func (p paths) classifyCache() string      { return filepath.Join(p.dataDir, "cache", "classify") }
func (p paths) embedCacheDir() string      { return filepath.Join(p.dataDir, "cache", "embed") }
func (p paths) nameCacheDir() string       { return filepath.Join(p.dataDir, "cache", "name") }
func (p paths) holeLabelCacheDir() string  { return filepath.Join(p.dataDir, "cache", "holes") }
func (p paths) classifiedJSONL() string {
	return filepath.Join(p.dataDir, "classified.jsonl")
}
func (p paths) outDir() string { return filepath.Join(p.dataDir, "out") }

// newFlagSet returns a FlagSet with the shared -data flag pre-registered.
func newFlagSet(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	dataDir := fs.String("data", "data", "data directory")
	return fs, dataDir
}

// writeJSONL writes one JSON document per line, atomically via a temp file so
// an interrupted run never leaves a half-written output behind.
func writeJSONL[T any](path string, items []T) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	w := bufio.NewWriter(tmp)
	enc := json.NewEncoder(w)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// loadNewCacheEntries reads every <id>.json in dir that is not yet in seen
// and returns id -> decoded value. Files that fail to read or decode are
// skipped WITHOUT being marked seen: a concurrent writer (enrich in another
// terminal, or the run pipeline itself) may be mid-write, and the next scan
// picks them up. This is what makes wave scans incremental — the caller keeps
// seen across calls and only ever pays for NEW files.
func loadNewCacheEntries[T any](dir string, seen map[string]bool) (map[string]T, error) {
	out := map[string]T{}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || seen[name] {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var v T
		if err := json.Unmarshal(b, &v); err != nil {
			continue
		}
		out[strings.TrimSuffix(name, ".json")] = v
		seen[name] = true
	}
	return out, nil
}

// foldStats counts how much of the corpus the taxonomy still describes.
//
// Per DISTINCT topic, not per view: 35k rows carry a few hundred topics, and
// "12 unknown" is a number somebody can act on where "431 unknown rows" is
// not. The views come along anyway, because they say how much of the reading
// the gap actually touches.
// The split is between an EXACT hit and everything else, deliberately. A
// topic whose area the map knows but whose subject it does not still folds —
// it lands under the right top level carrying a subject the taxonomy never
// named. That is the shape the CSV/page divergence took, so counting it as
// described would hide the very gap this line exists to show.
type foldStats struct {
	folded       int // the projection knew this exact area/sub pair
	unknown      int // it knew at most the area — the subject is newer than the map
	unknownViews int
}

// line is the one sentence the fold used to keep to itself: which projection
// ran, and what it could not describe. Empty when nothing was folded, so the
// preflight (which folds no rows) stays silent.
func (s foldStats) line(provenance string) string {
	if s.folded == 0 && s.unknown == 0 {
		return ""
	}
	line := fmt.Sprintf("taxonomy: %s — %d topics folded", provenance, s.folded)
	if s.unknown > 0 {
		line += fmt.Sprintf(", %d unknown (%d views) classified after it was built; rerun \"taxonomy\"", s.unknown, s.unknownViews)
	}
	return line
}

// taxonomyProvenance identifies WHICH taxonomy a rendering used: content hash,
// path, modification time. Every artefact of one run carries the same string —
// the CSV's first line, a field in the page payload, the terminal line — so a
// CSV and a page that disagree about their topics can be told apart instead of
// both looking authoritative. 103 of 189 topics in this repo's own CSV did not
// match the page beside it, and neither file said why.
func taxonomyProvenance(file string) string {
	f, err := os.Open(file)
	if err != nil {
		return "none"
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "none"
	}
	// Twelve hex digits: enough to tell two taxonomies apart by eye, short
	// enough to sit in a terminal line without pushing the counts off it.
	sum := hex.EncodeToString(h.Sum(nil))[:12]
	mtime := "unknown"
	if fi, err := f.Stat(); err == nil {
		mtime = fi.ModTime().UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("sha256:%s %s %s", sum, file, mtime)
}

// warnIfTaxonomyIsBehind says when the projection is older than the verdicts
// it projects. Nothing fails: the fold still works and the newer topics pass
// through. But a report that silently under-describes looks exactly like a
// report that describes everything, and the run that produced it took hours.
func warnIfTaxonomyIsBehind(w io.Writer, p paths, file string) {
	tax, err1 := os.Stat(file)
	cls, err2 := os.Stat(p.classifiedJSONL())
	if err1 != nil || err2 != nil {
		return
	}
	if cls.ModTime().After(tax.ModTime()) {
		fmt.Fprintf(w, "note: %s is newer than %s — topics classified since are not in the projection; rerun \"taxonomy\"\n",
			p.classifiedJSONL(), file)
	}
}

// foldThroughTaxonomy projects every verdict's topic through the generated
// taxonomy — the read-side application of "youtubehistii taxonomy", right
// after readJSONL and before any aggregate sees the rows. The aggregates
// themselves stay untouched, so old and new view are one flag apart.
func foldThroughTaxonomy(p paths, file string, rows []classify.Verdict) (foldStats, error) {
	var st foldStats
	t, err := taxonomy.LoadFile(file)
	if err != nil {
		return st, fmt.Errorf("read %s (run \"taxonomy\" first): %w", file, err)
	}
	warnIfTaxonomyIsBehind(os.Stderr, p, file)

	// Memoized on the PRE-fold topic. The projection is a map lookup, so this
	// is not about speed — it is what makes the counts per topic instead of
	// per row.
	type memo struct {
		folded string
		kind   taxonomy.FoldKind
	}
	seen := map[string]memo{}
	for i := range rows {
		before := rows[i].Topic
		m, ok := seen[before]
		if !ok {
			folded, kind := t.FoldWithKind(before)
			m = memo{folded: folded, kind: kind}
			seen[before] = m
			// "unclear" is not a topic the taxonomy failed to describe. It is
			// the classifier saying it could not tell, and Collect skips it
			// when building the taxonomy in the first place — counting it as
			// unknown would put a permanent floor under a number that is
			// supposed to be able to reach zero.
			switch {
			case before == "unclear":
			case m.kind == taxonomy.FoldExact:
				st.folded++
			default:
				st.unknown++
			}
		}
		if before != "unclear" && m.kind != taxonomy.FoldExact {
			st.unknownViews++
		}
		rows[i].Topic = m.folded
	}
	return st, nil
}

// readJSONL reads one JSON document per line.
func readJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var items []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var it T
		if err := json.Unmarshal(line, &it); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		items = append(items, it)
	}
	return items, sc.Err()
}
