// SPDX-License-Identifier: GPL-3.0-or-later

package classify

import (
	"strings"
	"testing"

	"github.com/bmmmm/youtubehistii/internal/rules"
)

func testConfig() *rules.Config {
	return &rules.Config{
		Topics: []rules.Topic{
			{ID: "gaming/rust", Desc: "rust gameplay"},
			{ID: "dev/talks", Desc: "conference talks"},
			{ID: "unclear", Desc: "cannot tell"},
		},
	}
}

func TestBuildPromptContainsTaxonomyAndMetadata(t *testing.T) {
	system, user := BuildPrompt(testConfig(), rules.Input{
		Title: "GopherCon keynote", Channel: "Gopher Academy",
		Tags: []string{"go", "conference"}, Categories: []string{"Science & Technology"},
	})
	for _, want := range []string{"gaming/rust", "dev/talks", "unclear"} {
		if !strings.Contains(system, want) {
			t.Errorf("system prompt misses topic %q", want)
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
	items := []rules.Input{
		{Title: "First Video", Channel: "Channel A"},
		{Title: "Second Video", Channel: "Channel B"},
	}

	system, user := BuildBatchPrompt(cfg, items)

	// Check system prompt contains all topics
	for _, topic := range cfg.Topics {
		if !strings.Contains(system, topic.ID) {
			t.Errorf("system prompt missing topic %q", topic.ID)
		}
	}

	// Check system prompt contains format line
	if !strings.Contains(system, "<n> <topic-id> <mode> <confidence>") {
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
	tests := []struct {
		name        string
		basis       string
		hasMeta     bool
		unavailable bool
		wantStale   bool
	}{
		{
			name:        "full basis never stale",
			basis:       BasisFull,
			hasMeta:     true,
			unavailable: false,
			wantStale:   false,
		},
		{
			name:        "title-only with meta goes stale",
			basis:       BasisTitleOnly,
			hasMeta:     true,
			unavailable: false,
			wantStale:   true,
		},
		{
			name:        "title-only no meta not stale",
			basis:       BasisTitleOnly,
			hasMeta:     false,
			unavailable: false,
			wantStale:   false,
		},
		{
			name:        "unavailable never stale",
			basis:       BasisTitleOnly,
			hasMeta:     true,
			unavailable: true,
			wantStale:   false,
		},
		{
			name:        "empty basis (legacy) with meta goes stale",
			basis:       "",
			hasMeta:     true,
			unavailable: false,
			wantStale:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := LLMVerdict{Basis: tc.basis, Topic: "dev/talks", Mode: "learn", Confidence: 0.9}
			got := v.Stale(tc.hasMeta, tc.unavailable)
			if got != tc.wantStale {
				t.Errorf("Stale() = %v, want %v", got, tc.wantStale)
			}
		})
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
