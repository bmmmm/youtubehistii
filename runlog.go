// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"flag"
	"os"
	"time"
)

// runLog appends one JSON line per event to data/out/taxonomy-run.jsonl —
// the machine-readable mirror of the terminal narration, for tail -f.
//
// Appends across runs, and that is the point: the file is how a threshold
// calibration is read back, and os.Create truncated it, so every run erased
// the one it was meant to be compared against. Each run opens with a "run"
// event carrying the version and the flags that were actually set, which is
// what makes the appended stream separable afterwards.
type runLog struct{ f *os.File }

func newRunLog(path string) (*runLog, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &runLog{f: f}, nil
}

// logRunStart records who ran and with what. fs.Visit, not VisitAll: the
// defaults are in the binary and in usageText already, and repeating fourteen
// of them per run would bury the two that were changed.
func (l *runLog) logRunStart(fs *flag.FlagSet) {
	set := map[string]string{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = f.Value.String() })
	l.event("run", map[string]any{"version": resolveVersion(), "flags": set})
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
