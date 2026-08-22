// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// paths centralizes every file the pipeline reads or writes, so each stage
// stays inspectable: nothing is stored outside the data directory.
type paths struct {
	dataDir string
}

func (p paths) historyJSONL() string  { return filepath.Join(p.dataDir, "history.jsonl") }
func (p paths) metaCacheDir() string  { return filepath.Join(p.dataDir, "cache", "meta") }
func (p paths) classifyCache() string { return filepath.Join(p.dataDir, "cache", "classify") }
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
