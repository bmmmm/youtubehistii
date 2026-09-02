// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

// writeRunFixture lays out a data directory a full run can process WITHOUT a
// network: every video is already in the meta cache, so enrich finds nothing
// missing and returns before it reaches for yt-dlp. The rules file points at
// a host that does not resolve, so "-no-llm" meaning "no server is contacted"
// is not a claim but a property of the fixture.
func writeRunFixture(t *testing.T, p paths) {
	t.Helper()
	rules := "llm:\n  model: test-chat\n  base_url: http://offline.invalid/v1\n" +
		"topics:\n  - id: music\n    desc: music\n  - id: unclear\n    desc: cannot tell\n"
	if err := os.WriteFile(filepath.Join(p.dataDir, "rules.yaml"), []byte(rules), 0o644); err != nil {
		t.Fatal(err)
	}

	// Enough views for a timeline with more than one day on it: the page
	// renders from them, and a page built from nothing would prove nothing.
	t0 := time.Date(2025, 5, 5, 18, 0, 0, 0, time.UTC)
	var views []takeout.View
	cache := enrich.Cache{Dir: p.metaCacheDir()}
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("runvid%05d", i)
		views = append(views, takeout.View{
			VideoID: id, Title: fmt.Sprintf("run fixture %d", i),
			Channel:   fmt.Sprintf("run channel %d", i%3),
			WatchedAt: t0.Add(time.Duration(i)*7*time.Minute).AddDate(0, 0, i/4),
		})
		if err := cache.Write(enrich.Meta{
			ID: id, Title: fmt.Sprintf("run fixture %d", i),
			Channel: fmt.Sprintf("run channel %d", i%3), Duration: 300,
			Categories: []string{"Music"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeJSONL(p.historyJSONL(), views); err != nil {
		t.Fatal(err)
	}
}

// TestCmdRunEndsOnThePage is the guard on the one thing a full run is for.
//
// "run" used to end on a page, back when "report" wrote an HTML file of its
// own. ca1703d moved the report INTO the watch-path app and deleted that
// file; run.go was not part of that commit, so from then on a full run wrote
// a CSV and a terminal summary and no page at all — and nothing said so.
// A command that quietly stops producing its output is exactly what an
// end-to-end test is for.
func TestCmdRunEndsOnThePage(t *testing.T) {
	// loadRules and the taxonomy resolve relative to the working directory.
	t.Chdir(t.TempDir())
	dataDir := t.TempDir()
	p := paths{dataDir: dataDir}
	writeRunFixture(t, p)

	if err := cmdRun([]string{"-data", dataDir, "-rules", filepath.Join(dataDir, "rules.yaml"), "-no-llm"}); err != nil {
		t.Fatal(err)
	}

	// The CSV was never the point; the page is. Both are asserted, so a
	// future change that drops either one says which.
	for _, name := range []string{"report.csv", "watchpath.html"} {
		if _, err := os.Stat(filepath.Join(p.outDir(), name)); err != nil {
			t.Errorf("a full run left no %s: %v", name, err)
		}
	}
	page, err := os.ReadFile(filepath.Join(p.outDir(), "watchpath.html"))
	if err != nil {
		t.Fatal(err)
	}
	// Not just a file: the router and the payload have to be in it, or the
	// page is a shell that renders nothing.
	for _, want := range []string{"<!doctype html>", "const D = {", "#/report"} {
		if !strings.Contains(strings.ToLower(string(page)), strings.ToLower(want)) {
			t.Errorf("the page a full run wrote misses %q (%d bytes)", want, len(page))
		}
	}
}

// TestCmdRunChecksTheTaxonomyFirst: -taxonomy is not read until the report
// and the page, at the very end of a run that takes hours. A missing
// config/taxonomy.yaml there would throw all of that away, with a message
// that was available before the first video was fetched.
//
// The test proves the check comes FIRST, not merely that it exists: without
// the preflight this run (offline, everything cached) gets all the way
// through classification and leaves a classified.jsonl behind before it
// fails. So the assertion is on the absence of that file.
func TestCmdRunChecksTheTaxonomyFirst(t *testing.T) {
	t.Chdir(t.TempDir()) // no config/taxonomy.yaml in here
	dataDir := t.TempDir()
	p := paths{dataDir: dataDir}
	writeRunFixture(t, p)

	err := cmdRun([]string{"-data", dataDir, "-rules", filepath.Join(dataDir, "rules.yaml"), "-no-llm", "-taxonomy"})
	if err == nil {
		t.Fatal("a run with -taxonomy and no taxonomy file returned no error")
	}
	if !strings.Contains(err.Error(), taxonomyPath) {
		t.Errorf("the error does not name the missing file: %v", err)
	}
	if _, statErr := os.Stat(p.classifiedJSONL()); statErr == nil {
		t.Error("the run classified everything before noticing the missing taxonomy")
	}
}

// TestCmdRunRefusesRetry: "run" registered -retry because it takes classify's
// flags, then set it to "" before the first wave. The run looked normal, cost
// hours, and re-asked nothing. A flag that does nothing is worse than one
// that says no — so it now says no, and names the command that means it.
func TestCmdRunRefusesRetry(t *testing.T) {
	t.Chdir(t.TempDir())
	dataDir := t.TempDir()
	p := paths{dataDir: dataDir}
	writeRunFixture(t, p)

	err := cmdRun([]string{"-data", dataDir, "-rules", filepath.Join(dataDir, "rules.yaml"), "-no-llm", "-retry", "no-sub"})
	if err == nil {
		t.Fatal("run accepted -retry and returned no error")
	}
	if !strings.Contains(err.Error(), "classify -retry no-sub") {
		t.Errorf("the refusal does not name the command that would work: %v", err)
	}
	// It has to refuse BEFORE the work, or the advice arrives after the hours
	// it was supposed to save.
	if _, statErr := os.Stat(p.classifiedJSONL()); statErr == nil {
		t.Error("the run classified everything before refusing the flag")
	}
}

// TestCmdRunEndsOnTheWhatNowLine: a finished run used to stop on a path and
// leave the next step to memory — which retry selector, whether the taxonomy
// needs rebuilding. The line is printed from the pass's own counters, so it
// cannot advertise a selector that would select nothing.
func TestCmdRunEndsOnTheWhatNowLine(t *testing.T) {
	t.Chdir(t.TempDir())
	dataDir := t.TempDir()
	p := paths{dataDir: dataDir}
	writeRunFixture(t, p)

	out, err := captureStdout(t, func() error {
		return cmdRun([]string{"-data", dataDir, "-rules", filepath.Join(dataDir, "rules.yaml"), "-no-llm"})
	})
	if err != nil {
		t.Fatalf("cmdRun: %v\n%s", err, out)
	}
	if !strings.Contains(out, "next:") {
		t.Errorf("the run never said what comes next:\n%s", out)
	}
	// -no-llm over a fixture whose every video gets its area from the Music
	// category: no model answered anything, so no retry selector applies and
	// offering one would be advice that does nothing.
	for _, unwanted := range []string{"-retry no-sub", "-retry no-mode"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("a run with no model verdicts offered %q:\n%s", unwanted, out)
		}
	}
}
