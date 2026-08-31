// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/bmmmm/youtubehistii/internal/fscache"
)

// hashCache is the shape the embed and name caches share: one small JSON
// value per entry, keyed by the sha256 of the parts that define it (the
// model plus the text or prompt it answered), NUL-joined so no part can
// bleed into the next. The key IS the question: carrying the model in it is
// what lets a model swap in rules.yaml bypass the cache on its own, and any
// drift in a prompt's wording is a different file — which is why the
// prompts must not be reworded casually (see NameSinglePrompt).
type hashCache[T any] struct{ dir string }

func (c hashCache[T]) path(parts []string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:12])+".json")
}

// read treats an unset dir as a miss: a run whose cache directory could not
// be created still works, it just pays the model again.
func (c hashCache[T]) read(parts ...string) (T, bool) {
	if c.dir == "" {
		var zero T
		return zero, false
	}
	return fscache.ReadJSON[T](c.path(parts))
}

func (c hashCache[T]) write(v T, parts ...string) error {
	if c.dir == "" {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return fscache.WriteFile(c.path(parts), b)
}

// embedEntry and nameEntry mirror the existing cache files on disk exactly —
// one field each, under the same JSON name the free functions used to write.
type embedEntry struct {
	Vector []float32 `json:"vector"`
}

type nameEntry struct {
	Reply string `json:"reply"`
}
