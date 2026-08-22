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
