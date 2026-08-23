// SPDX-License-Identifier: GPL-3.0-or-later

package classify

import (
	"strings"
	"testing"

	"github.com/bmmmm/youtubehistii/internal/rules"
)

func testConfig() *rules.Config {
	return &rules.Config{
		// Areas only — "gaming/rust" and "dev/talks" below are area+sub, the
		// second level the LLM fills in and that is never configured here.
		Topics: []rules.Topic{
			{ID: "gaming", Desc: "video game content"},
			{ID: "dev", Desc: "software engineering, talks and tutorials"},
			{ID: "unclear", Desc: "cannot tell"},
		},
	}
}

func TestBuildPromptContainsTaxonomyAndMetadata(t *testing.T) {
	system, user := BuildPrompt(testConfig(), Item{Input: rules.Input{
		Title: "GopherCon keynote", Channel: "Gopher Academy",
		Tags: []string{"go", "conference"}, Categories: []string{"Science & Technology"},
	}}, map[string][]string{"dev": {"talks", "tutorials"}})
	for _, want := range []string{"gaming", "dev", "unclear", "talks, tutorials"} {
		if !strings.Contains(system, want) {
			t.Errorf("system prompt misses %q", want)
		}
	}
	for _, want := range []string{"GopherCon keynote", "Gopher Academy", "Science & Technology", "go, conference"} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt misses %q", want)
		}
	}
}

func TestParseVerdict(t *testing.T) {
	cfg := testConfig()

	v, err := ParseVerdict(cfg, `{"topic": "dev/talks", "mode": "learn", "confidence": 0.9}`)
	if err != nil {
		t.Fatal(err)
	}
	if v.Topic != "dev/talks" || v.Mode != "learn" || v.Confidence != 0.9 {
		t.Errorf("verdict = %+v", v)
	}

	// Fenced/prosey replies still parse.
	v, err = ParseVerdict(cfg, "Sure! Here is the answer:\n```json\n{\"topic\": \"gaming/rust\", \"mode\": \"consume\", \"confidence\": 0.7}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if v.Topic != "gaming/rust" {
		t.Errorf("verdict = %+v", v)
	}
}

func TestParseVerdictRejectsGarbage(t *testing.T) {
	cfg := testConfig()
	for name, reply := range map[string]string{
		"no json":       "I cannot classify this.",
		"unknown topic": `{"topic": "made/up", "mode": "learn", "confidence": 0.9}`,
		"bad mode":      `{"topic": "dev/talks", "mode": "binge", "confidence": 0.9}`,
		"bad conf":      `{"topic": "dev/talks", "mode": "learn", "confidence": 7}`,
	} {
		if _, err := ParseVerdict(cfg, reply); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestCacheRoundtrip(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	if _, ok := c.Read("abc123DEF45"); ok {
		t.Fatal("empty cache returned a verdict")
	}
	want := LLMVerdict{Topic: "dev/talks", Mode: "learn", Confidence: 0.8, Model: "m"}
	if err := c.Write("abc123DEF45", want); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Read("abc123DEF45")
	if !ok || got != want {
		t.Errorf("got %+v ok=%v", got, ok)
	}
	if err := c.Write("../evil", want); err == nil {
		t.Fatal("cache accepted traversal id")
	}
}

func TestParseBatchVerdicts(t *testing.T) {
	cfg := testConfig()

	// Happy path: plain numbered lines for 3 videos, out of order on purpose
	// — the line number, not the position, carries the mapping.
	reply := `2 gaming/rust consume 0.7
1 dev/talks learn 0.9
3 unclear mixed 0.5`
	ids := []string{"aaaaaaaaaa1", "bbbbbbbbbb2", "cccccccccc3"}
	verdicts, err := ParseBatchVerdicts(cfg, ids, reply)
	if err != nil {
		t.Fatalf("happy path failed: %v", err)
	}
	if len(verdicts) != 3 {
		t.Fatalf("expected 3 verdicts, got %d", len(verdicts))
	}
	if v := verdicts["aaaaaaaaaa1"]; v.Topic != "dev/talks" || v.Mode != "learn" || v.Confidence != 0.9 {
		t.Errorf("aaaaaaaaaa1: got %+v", v)
	}
	if v := verdicts["bbbbbbbbbb2"]; v.Topic != "gaming/rust" || v.Mode != "consume" || v.Confidence != 0.7 {
		t.Errorf("bbbbbbbbbb2: got %+v", v)
	}
	if v := verdicts["cccccccccc3"]; v.Topic != "unclear" || v.Mode != "mixed" || v.Confidence != 0.5 {
		t.Errorf("cccccccccc3: got %+v", v)
	}

	// Prose + code fences + "1." / "2)" / "3:" number styles still parse
	reply2 := "Here are the classifications:\n```\n1. dev/talks learn 0.9\n2) gaming/rust consume 0.7\n3: unclear mixed 0.5\n```\nDone!"
	verdicts2, err := ParseBatchVerdicts(cfg, ids, reply2)
	if err != nil {
		t.Fatalf("prose/fences path failed: %v", err)
	}
	if len(verdicts2) != 3 {
		t.Fatalf("expected 3 verdicts from prose reply, got %d", len(verdicts2))
	}
	if v := verdicts2["aaaaaaaaaa1"]; v.Topic != "dev/talks" || v.Mode != "learn" || v.Confidence != 0.9 {
		t.Errorf("prose aaaaaaaaaa1: got %+v", v)
	}
}

func TestParseBatchVerdictsRejects(t *testing.T) {
	cfg := testConfig()

	tests := map[string]struct {
		ids   []string
		reply string
	}{
		"missing line": {
			ids:   []string{"aaaaaaaaaa1", "bbbbbbbbbb2", "cccccccccc3"},
			reply: "1 dev/talks learn 0.9\n3 unclear mixed 0.5",
		},
		"line number out of range": {
			ids:   []string{"aaaaaaaaaa1"},
			reply: "1 dev/talks learn 0.9\n7 gaming/rust consume 0.7",
		},
		"id instead of line number": {
			ids:   []string{"aaaaaaaaaa1"},
			reply: "aaaaaaaaaa1 dev/talks learn 0.9",
		},
		"unknown topic": {
			ids:   []string{"aaaaaaaaaa1"},
			reply: "1 made/up learn 0.9",
		},
		"invalid mode": {
			ids:   []string{"aaaaaaaaaa1"},
			reply: "1 dev/talks binge 0.9",
		},
		"confidence out of range (7)": {
			ids:   []string{"aaaaaaaaaa1"},
			reply: "1 dev/talks learn 7",
		},
		"confidence non-numeric": {
			ids:   []string{"aaaaaaaaaa1"},
			reply: "1 dev/talks learn high",
		},
		"duplicate line": {
			ids:   []string{"aaaaaaaaaa1"},
			reply: "1 dev/talks learn 0.9\n1 gaming/rust consume 0.7",
		},
		"JSON array instead of lines": {
			ids:   []string{"aaaaaaaaaa1"},
			reply: `[{"id": "aaaaaaaaaa1", "topic": "dev/talks", "mode": "learn", "confidence": 0.9}]`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParseBatchVerdicts(cfg, tc.ids, tc.reply)
			if err == nil {
				t.Errorf("expected error but got none")
			}
		})
	}
}

func TestParseBatchVerdictsUnclearMode(t *testing.T) {
	cfg := testConfig()

	// Observed model quirks: mode "unclear" on unclear topics, and the
	// collapsed three-field form "<n> unclear <confidence>".
	verdicts, err := ParseBatchVerdicts(cfg, []string{"aaaaaaaaaa1", "bbbbbbbbbb2"},
		"1 unclear unclear 0.1\n2 unclear 0.2")
	if err != nil {
		t.Fatal(err)
	}
	if v := verdicts["aaaaaaaaaa1"]; v.Topic != "unclear" || v.Mode != "" || v.Confidence != 0.1 {
		t.Errorf("four-field unclear = %+v", v)
	}
	if v := verdicts["bbbbbbbbbb2"]; v.Topic != "unclear" || v.Mode != "" || v.Confidence != 0.2 {
		t.Errorf("three-field unclear = %+v", v)
	}
}

func TestParseVerdictUnclearMode(t *testing.T) {
	cfg := testConfig()
	for name, reply := range map[string]string{
		"explicit unclear": `{"topic": "unclear", "mode": "unclear", "confidence": 0.1}`,
		"omitted mode":     `{"topic": "unclear", "confidence": 0.1}`,
	} {
		v, err := ParseVerdict(cfg, reply)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if v.Topic != "unclear" || v.Mode != "" {
			t.Errorf("%s: verdict = %+v, want empty mode", name, v)
		}
	}
}

func TestBuildBatchPrompt(t *testing.T) {
	cfg := testConfig()
	items := []Item{
		{Input: rules.Input{Title: "First Video", Channel: "Channel A"}},
		{Input: rules.Input{Title: "Second Video", Channel: "Channel B"}},
	}

	system, user := BuildBatchPrompt(cfg, items, map[string][]string{"gaming": {"rust", "cs2"}})

	// Check system prompt contains all areas
	for _, topic := range cfg.Topics {
		if !strings.Contains(system, topic.ID) {
			t.Errorf("system prompt missing area %q", topic.ID)
		}
	}

	// The seeded subs are what keeps the free level converging.
	if !strings.Contains(system, "gaming: rust, cs2") {
		t.Error("system prompt missing the seeded subs")
	}

	// Check system prompt contains format line
	if !strings.Contains(system, "<n> <topic> <mode> <confidence>") {
		t.Error("system prompt missing format line")
	}

	// Check user prompt numbers the videos and carries the titles
	if !strings.Contains(user, "1.\n   title: First Video") {
		t.Error("user prompt missing numbered first video")
	}
	if !strings.Contains(user, "2.\n   title: Second Video") {
		t.Error("user prompt missing numbered second video")
	}
}

func TestVerdictStale(t *testing.T) {
	const current = "abc12345"
	tests := []struct {
		name        string
		basis       string
		taxonomy    string
		hasMeta     bool
		unavailable bool
		wantStale   bool
	}{
		{
			name:        "full basis never stale",
			basis:       BasisFull,
			taxonomy:    current,
			hasMeta:     true,
			unavailable: false,
			wantStale:   false,
		},
		{
			name:        "title-only with meta goes stale",
			basis:       BasisTitleOnly,
			taxonomy:    current,
			hasMeta:     true,
			unavailable: false,
			wantStale:   true,
		},
		{
			name:        "title-only no meta not stale",
			basis:       BasisTitleOnly,
			taxonomy:    current,
			hasMeta:     false,
			unavailable: false,
			wantStale:   false,
		},
		{
			name:        "unavailable never stale",
			basis:       BasisTitleOnly,
			taxonomy:    current,
			hasMeta:     true,
			unavailable: true,
			wantStale:   false,
		},
		{
			name:        "empty basis (legacy) with meta goes stale",
			basis:       "",
			taxonomy:    current,
			hasMeta:     true,
			unavailable: false,
			wantStale:   true,
		},
		{
			// The verdict names areas that may no longer exist, so nothing
			// about the metadata can save it.
			name:        "taxonomy change re-asks a full-basis verdict",
			basis:       BasisFull,
			taxonomy:    "0ldtaxon",
			hasMeta:     true,
			unavailable: false,
			wantStale:   true,
		},
		{
			name:        "taxonomy change outranks the tombstone rule",
			basis:       BasisTitleOnly,
			taxonomy:    "0ldtaxon",
			hasMeta:     true,
			unavailable: true,
			wantStale:   true,
		},
		{
			name:        "pre-fingerprint verdict is stale",
			basis:       BasisFull,
			taxonomy:    "",
			hasMeta:     true,
			unavailable: false,
			wantStale:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := LLMVerdict{Basis: tc.basis, Taxonomy: tc.taxonomy, Topic: "dev/talks", Mode: "learn", Confidence: 0.9}
			got := v.Stale(current, tc.hasMeta, tc.unavailable)
			if got != tc.wantStale {
				t.Errorf("Stale() = %v, want %v", got, tc.wantStale)
			}
		})
	}
}

// The free sub level: an area the taxonomy knows carries whatever sub the
// model wrote, canonicalized; an unknown AREA still rejects the batch.
func TestParseBatchVerdictsFreeSubs(t *testing.T) {
	cfg := testConfig()
	ids := []string{"aaaaaaaaaa1", "bbbbbbbbbb2", "cccccccccc3"}

	verdicts, err := ParseBatchVerdicts(cfg, ids,
		"1 gaming/Factorio consume 0.9\n2 gaming/counter_strike consume 0.8\n3 dev learn 0.7")
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		"aaaaaaaaaa1": "gaming/factorio",       // lowercased
		"bbbbbbbbbb2": "gaming/counter-strike", // underscore becomes a dash
		"cccccccccc3": "dev",                   // bare area stays bare
	} {
		if got := verdicts[id].Topic; got != want {
			t.Errorf("%s: topic = %q, want %q", id, got, want)
		}
	}

	if _, err := ParseBatchVerdicts(cfg, ids[:1], "1 health/fitness learn 0.9"); err == nil {
		t.Error("an unknown area must reject the batch, not invent a topic")
	}
}

func TestCacheRoundtripBasis(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	want := LLMVerdict{Topic: "dev/talks", Mode: "learn", Confidence: 0.8, Model: "m", Basis: BasisFull}
	if err := c.Write("test123456", want); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Read("test123456")
	if !ok {
		t.Fatal("read failed")
	}
	if got.Basis != BasisFull {
		t.Errorf("Basis roundtrip: got %q, want %q", got.Basis, BasisFull)
	}
	if got != want {
		t.Errorf("full roundtrip: got %+v, want %+v", got, want)
	}
}

func TestPromptStatesAFixedArea(t *testing.T) {
	// Where the YouTube category already decided the area, the prompt says so
	// and drops the category line: the same fact spelled two ways is what
	// makes a small model hesitate, and the model is only asked for the sub.
	cfg := testConfig()
	item := Item{Input: rules.Input{
		Title: "Rust solo base tour", Categories: []string{"Gaming"}, Tags: []string{"rust"},
	}, Area: "gaming"}

	for name, system := range map[string]string{
		"single": first(BuildPrompt(cfg, item, nil)),
		"batch":  first(BuildBatchPrompt(cfg, []Item{item}, nil)),
	} {
		if !strings.Contains(system, "(fixed)") {
			t.Errorf("%s: system prompt never explains a fixed area", name)
		}
	}

	// The failure this guards against: with the area fixed, the model wrote
	// it twice — once as the topic, once where the mode belongs. The mode
	// vocabulary is therefore fenced off explicitly.
	batchSystem := first(BuildBatchPrompt(cfg, []Item{item}, nil))
	for _, want := range []string{"never a genre", "never an area"} {
		if !strings.Contains(batchSystem, want) {
			t.Errorf("batch prompt no longer fences the mode field off (%q missing)", want)
		}
	}

	_, user := BuildPrompt(cfg, item, nil)
	if !strings.Contains(user, "area: gaming (fixed)") {
		t.Errorf("user prompt does not state the fixed area:\n%s", user)
	}
	if strings.Contains(user, "youtube category") {
		t.Errorf("category line should give way to the fixed area:\n%s", user)
	}

	// Without a fixed area (tombstoned, or not yet enriched) the category is
	// still the best hint the model gets.
	noArea := item
	noArea.Area = ""
	if _, user := BuildPrompt(cfg, noArea, nil); !strings.Contains(user, "youtube category: Gaming") {
		t.Errorf("without a fixed area the category must stay:\n%s", user)
	}
}

func first(a, _ string) string { return a }

// TestParseBatchAcceptsObservedFieldLayouts pins the layouts Qwen3.6-35B-A3B
// actually produced on real batches. Each is decidable from the values in the
// line, so accepting it is not a guess — and refusing it costs one request
// per video in the single-request fallback, which is what stretched a
// 200-video probe from 2 to 10 minutes.
func TestParseBatchAcceptsObservedFieldLayouts(t *testing.T) {
	cfg := testConfig()
	ids := []string{"a", "b", "c"}
	reply := strings.Join([]string{
		"1 gaming rust consume 0.9", // area and sub split on a space
		"2 dev/talks 0.8",           // mode left out entirely
		"3 unclear 0.1",             // the older short form, same shape
	}, "\n")

	got, err := ParseBatchVerdicts(cfg, ids, reply)
	if err != nil {
		t.Fatalf("all three layouts are unambiguous: %v", err)
	}
	if v := got["a"]; v.Topic != "gaming/rust" || v.Mode != "consume" || v.Confidence != 0.9 {
		t.Errorf("space-split topic = %+v", v)
	}
	if v := got["b"]; v.Topic != "dev/talks" || v.Mode != "" {
		t.Errorf("missing mode = %+v, want the topic kept and no mode", v)
	}
	if v := got["c"]; v.Topic != "unclear" || v.Mode != "" {
		t.Errorf("short unclear form = %+v", v)
	}
}

func TestParseBatchStillRefusesAmbiguousLines(t *testing.T) {
	cfg := testConfig()
	// Five fields, but position 4 is not a mode — there is no single reading,
	// so it must fail rather than be repaired into one.
	if _, err := ParseBatchVerdicts(cfg, []string{"a"}, "1 gaming rust something 0.9"); err == nil {
		t.Error("ambiguous five-field line was accepted")
	}
	// Three fields whose second is not a topic: prose, not a verdict.
	if _, err := ParseBatchVerdicts(cfg, []string{"a"}, "1 rust 0.9"); err == nil {
		t.Error("three-field line with an unknown area was accepted")
	}
	// A duplicate line number is a real model error: it may mean another
	// line was dropped and everything after it shifted, so the mapping is
	// never repaired.
	if _, err := ParseBatchVerdicts(cfg, []string{"a", "b"},
		"1 gaming/rust consume 0.9\n1 dev/talks learn 0.8"); err == nil {
		t.Error("duplicate line number was accepted")
	}
}

func TestParseBatchReadsADroppedModeAsASub(t *testing.T) {
	// "1 education dev 0.9": the mode is left out and the sub split off on a
	// space, so a dropped mode looks like a broken one. Observed on a batch
	// that dropped the mode on every single line.
	cfg := testConfig()
	got, err := ParseBatchVerdicts(cfg, []string{"a"}, "1 gaming rust 0.9")
	if err != nil {
		t.Fatalf("decidable layout refused: %v", err)
	}
	if v := got["a"]; v.Topic != "gaming/rust" || v.Mode != "" {
		t.Errorf("got %+v, want gaming/rust with no mode", v)
	}

	// A sub that only repeats its area collapses back to the bare area, so
	// "1 gaming gaming 0.9" says exactly what it knows and nothing more.
	got, err = ParseBatchVerdicts(cfg, []string{"a"}, "1 gaming gaming 0.9")
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if v := got["a"]; v.Topic != "gaming" || v.Mode != "" {
		t.Errorf("got %+v, want the bare area with no mode", v)
	}

	// A topic that already HAS a sub cannot gain another one: "dev/talks
	// binge" is a broken mode, not a dropped one.
	if _, err := ParseBatchVerdicts(cfg, []string{"a"}, "1 dev/talks binge 0.9"); err == nil {
		t.Error("a broken mode on an already-subbed topic was accepted")
	}

	// A well-formed line is never rewritten.
	got, err = ParseBatchVerdicts(cfg, []string{"a"}, "1 gaming/rust consume 0.9")
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if v := got["a"]; v.Topic != "gaming/rust" || v.Mode != "consume" {
		t.Errorf("got %+v, want the line left alone", v)
	}
}
