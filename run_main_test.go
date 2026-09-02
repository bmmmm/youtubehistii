// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
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

// writeTaxonomyFile writes a projection next to the fixture. The map is
// deliberately partial: one topic it knows, and the fixture's own topics it
// does not, so the "unknown" count has something to count.
func writeTaxonomyFile(t *testing.T, path string, m map[string]string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("map:\n")
	for k, v := range m {
		fmt.Fprintf(&b, "  %q: %q\n", k, v)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFoldWarnsWhenTheTaxonomyIsOlderThanTheVerdicts: a projection older than
// the verdicts it projects describes a corpus that has moved on. Nothing
// fails — the newer topics pass through — and that is the problem: a report
// that silently under-describes looks exactly like one that does not.
func TestFoldWarnsWhenTheTaxonomyIsOlderThanTheVerdicts(t *testing.T) {
	p := paths{dataDir: t.TempDir()}
	taxFile := filepath.Join(t.TempDir(), "taxonomy.yaml")
	writeTaxonomyFile(t, taxFile, map[string]string{"music/jazz": "sound/jazz"})
	if err := writeJSONL(p.classifiedJSONL(), []classify.Verdict{
		{VideoID: "a", Topic: "music/jazz"},
		{VideoID: "b", Topic: "science/rockets"},
		{VideoID: "c", Topic: "science/rockets"},
		{VideoID: "d", Topic: "unclear"},
	}); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(taxFile, old, old); err != nil {
		t.Fatal(err)
	}

	var warn strings.Builder
	warnIfTaxonomyIsBehind(&warn, p, taxFile)
	if !strings.Contains(warn.String(), taxFile) || !strings.Contains(warn.String(), p.classifiedJSONL()) {
		t.Errorf("the warning names neither file: %q", warn.String())
	}

	// The other direction: a taxonomy built after the verdicts is current,
	// and a warning there would be noise that teaches people to skip it.
	newer := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(taxFile, newer, newer); err != nil {
		t.Fatal(err)
	}
	warn.Reset()
	warnIfTaxonomyIsBehind(&warn, p, taxFile)
	if warn.String() != "" {
		t.Errorf("a current taxonomy still warned: %q", warn.String())
	}

	// And the counts: two distinct topics, one known, one not — per topic,
	// over three views, with "unclear" in neither bucket because it is the
	// classifier declining to answer, not a gap in the taxonomy.
	rows, err := readJSONL[classify.Verdict](p.classifiedJSONL())
	if err != nil {
		t.Fatal(err)
	}
	st, err := foldThroughTaxonomy(p, taxFile, rows)
	if err != nil {
		t.Fatal(err)
	}
	if st.folded != 1 || st.unknown != 1 || st.unknownViews != 2 {
		t.Errorf("foldStats = %+v, want 1 folded, 1 unknown over 2 views", st)
	}

	// The load-bearing half of the split: a topic whose AREA the map knows
	// but whose subject it does not still folds, and still counts as unknown.
	// Counting it as described would report a taxonomy that covers everything
	// while the page and the CSV show different subjects — the divergence
	// this line exists to surface.
	areaOnly := filepath.Join(t.TempDir(), "area.yaml")
	writeTaxonomyFile(t, areaOnly, map[string]string{"music": "sound"})
	partial := []classify.Verdict{{VideoID: "a", Topic: "music/jazz"}}
	st2, err := foldThroughTaxonomy(p, areaOnly, partial)
	if err != nil {
		t.Fatal(err)
	}
	if partial[0].Topic != "sound/jazz" {
		t.Errorf("topic = %q, want sound/jazz — the area still folds", partial[0].Topic)
	}
	if st2.folded != 0 || st2.unknown != 1 {
		t.Errorf("foldStats = %+v, want 0 folded, 1 unknown — the subject is newer than the map", st2)
	}
	if line := st.line("sha256:deadbeefcafe x.yaml t"); !strings.Contains(line, "1 unknown (2 views)") {
		t.Errorf("the line does not carry the counts: %s", line)
	}
	if line := (foldStats{}).line("sha256:deadbeefcafe x.yaml t"); line != "" {
		t.Errorf("a fold over no rows still printed a line: %q", line)
	}
}

// TestTaxonomyFileFlagLeavesTheConstantBehind: -taxonomy-file exists so a
// second taxonomy can be compared against the first without moving files
// around. The test runs in a directory with NO config/taxonomy.yaml, so a
// path that still resolved to the constant would fail outright.
func TestTaxonomyFileFlagLeavesTheConstantBehind(t *testing.T) {
	t.Chdir(t.TempDir())
	dataDir := t.TempDir()
	p := paths{dataDir: dataDir}
	writeRunFixture(t, p)

	taxFile := filepath.Join(dataDir, "elsewhere.yaml")
	writeTaxonomyFile(t, taxFile, map[string]string{"music": "sound"})

	if _, err := os.Stat(taxonomyPath); err == nil {
		t.Fatalf("the test directory has a %s after all — the assertion below proves nothing", taxonomyPath)
	}
	out, err := captureStdout(t, func() error {
		return cmdRun([]string{"-data", dataDir, "-rules", filepath.Join(dataDir, "rules.yaml"),
			"-no-llm", "-taxonomy", "-taxonomy-file", taxFile})
	})
	if err != nil {
		t.Fatalf("cmdRun with -taxonomy-file: %v\n%s", err, out)
	}
	if !strings.Contains(out, taxFile) {
		t.Errorf("the run never named the taxonomy it used:\n%s", out)
	}
}

// TestRunDoesNotTellYouToRunTheThingItJustRan: "report" ends by advising a
// watchpath run, which is right when a person typed "report" and wrong when
// run is about to render the page two lines later. Same for the taxonomy
// line — printed by both stages it would say the same thing twice.
func TestRunDoesNotTellYouToRunTheThingItJustRan(t *testing.T) {
	t.Chdir(t.TempDir())
	dataDir := t.TempDir()
	p := paths{dataDir: dataDir}
	writeRunFixture(t, p)
	taxFile := filepath.Join(dataDir, "taxonomy.yaml")
	writeTaxonomyFile(t, taxFile, map[string]string{"music": "sound"})

	out, err := captureStdout(t, func() error {
		return cmdRun([]string{"-data", dataDir, "-rules", filepath.Join(dataDir, "rules.yaml"),
			"-no-llm", "-taxonomy", "-taxonomy-file", taxFile})
	})
	if err != nil {
		t.Fatalf("cmdRun: %v\n%s", err, out)
	}
	if strings.Contains(out, `run "watchpath"`) {
		t.Errorf("a full run told the reader to run watchpath, which it just did:\n%s", out)
	}
	if n := strings.Count(out, "taxonomy: sha256:"); n != 1 {
		t.Errorf("the run printed %d taxonomy provenance lines, want exactly 1:\n%s", n, out)
	}
}
