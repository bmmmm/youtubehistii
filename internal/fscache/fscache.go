// SPDX-License-Identifier: GPL-3.0-or-later

// Package fscache is the shared core of the on-disk caches: one JSON value
// per file in a directory. Only the obvious kernel lives here — key
// validation, listing and bulk reading stay with the callers, whose rules
// (video-id regexes, hashed prompts, error ordering) are their own.
package fscache

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ReadJSON reads path into a T. Every failure — missing file, unreadable
// file, syntax error — is a cache miss, never an error: a cache entry that
// cannot be read is indistinguishable from one that was never written, and
// the caller's answer to both is the same (recompute it).
func ReadJSON[T any](path string) (T, bool) {
	var v T
	b, err := os.ReadFile(path)
	if err != nil {
		return v, false
	}
	if json.Unmarshal(b, &v) != nil {
		var zero T
		return zero, false
	}
	return v, true
}

// WriteFile stores b at path atomically (temp file + rename), creating the
// directory first. A plain write would leave truncated JSON behind on a
// crash, and that is worse than no entry at all: an existence check would
// report the entry as cached so it never gets recomputed, and a bulk read
// would fail the WHOLE cache on the one unparseable file.
func WriteFile(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
