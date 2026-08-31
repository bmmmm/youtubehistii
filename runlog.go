// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"os"
	"time"
)

// runLog appends one JSON line per event to data/out/taxonomy-run.jsonl —
// the machine-readable mirror of the terminal narration, for tail -f.
type runLog struct{ f *os.File }

func newRunLog(path string) (*runLog, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &runLog{f: f}, nil
}

func (l *runLog) event(kind string, detail any) {
	line := map[string]any{"at": time.Now().Format(time.RFC3339), "event": kind, "detail": detail}
	b, err := json.Marshal(line)
	if err != nil {
		return
	}
	l.f.Write(append(b, '\n'))
}

func (l *runLog) close() { l.f.Close() }
