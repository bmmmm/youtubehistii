// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// foldThroughTaxonomy projects every verdict's topic through the generated
// taxonomy — the read-side application of "youtubehistii taxonomy", right
// after readJSONL and before any aggregate sees the rows. The aggregates
// themselves stay untouched, so old and new view are one flag apart.
func foldThroughTaxonomy(rows []classify.Verdict) error {
	t, err := taxonomy.LoadFile(taxonomyPath)
	if err != nil {
		return fmt.Errorf("read %s (run \"taxonomy\" first): %w", taxonomyPath, err)
	}
	for i := range rows {
		rows[i].Topic = t.Fold(rows[i].Topic)
	}
	return nil
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
