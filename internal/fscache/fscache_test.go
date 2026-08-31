// SPDX-License-Identifier: GPL-3.0-or-later

package fscache

import (
	"os"
	"path/filepath"
	"testing"
)

type entry struct {
	Reply string `json:"reply"`
}

func TestRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "a.json")
	if err := WriteFile(p, []byte(`{"reply":"jazz"}`)); err != nil {
		t.Fatal(err)
	}
	got, ok := ReadJSON[entry](p)
	if !ok || got.Reply != "jazz" {
		t.Fatalf("ReadJSON = %+v, %v", got, ok)
	}
}

func TestEveryReadFailureIsAMiss(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadJSON[entry](filepath.Join(dir, "missing.json")); ok {
		t.Error("missing file read as a hit")
	}
	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte(`{"reply": tru`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadJSON[entry](corrupt); ok {
		t.Error("corrupt file read as a hit")
	}
}

func TestWriteLeavesNoTempBehind(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFile(filepath.Join(dir, "a.json"), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "a.json" {
		t.Fatalf("dir = %v, want just a.json", entries)
	}
}
