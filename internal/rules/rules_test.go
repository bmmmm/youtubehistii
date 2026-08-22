// SPDX-License-Identifier: GPL-3.0-or-later

package rules

import (
	"os"
	"path/filepath"
	"testing"
)

func loadExample(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load("../../config/rules.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestLoadExampleConfig(t *testing.T) {
	cfg := loadExample(t)
	if len(cfg.Topics) == 0 || len(cfg.Rules) == 0 {
		t.Fatalf("example config empty: %d topics, %d rules", len(cfg.Topics), len(cfg.Rules))
	}
	if cfg.LLM.Model == "" {
		t.Error("example config has no llm.model")
	}
}

func TestMatchOrder(t *testing.T) {
	cfg := loadExample(t)

	// Channel rule beats the later category fallback.
	topic, mode, ruleID, ok := cfg.Match(Input{
		Title: "Some talk", Channel: "media.ccc.de", Categories: []string{"Gaming"},
	})
	if !ok || topic != "dev/talks" || mode != "learn" || ruleID != "ccc-talks" {
		t.Errorf("got %s/%s via %s ok=%v", topic, mode, ruleID, ok)
	}

	// text_any matches inside a tag.
	topic, _, _, ok = cfg.Match(Input{Title: "Monday stream", Tags: []string{"rust base tour"}})
	if !ok || topic != "gaming/rust" {
		t.Errorf("tag text match: got %s ok=%v", topic, ok)
	}

	// Category fallback fires when nothing specific matched.
	topic, mode, _, ok = cfg.Match(Input{Title: "chill vibes mix", Categories: []string{"Music"}})
	if !ok || topic != "music" || mode != "consume" {
		t.Errorf("category fallback: got %s/%s ok=%v", topic, mode, ok)
	}

	// Nothing matches -> not ok.
	if _, _, _, ok := cfg.Match(Input{Title: "completely opaque"}); ok {
		t.Error("expected no match")
	}
}

func TestMatchIsCaseInsensitive(t *testing.T) {
	cfg := loadExample(t)
	topic, _, _, ok := cfg.Match(Input{Title: "AGE OF EMPIRES iv grand final"})
	if !ok || topic != "gaming/aoe" {
		t.Errorf("got %s ok=%v", topic, ok)
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadRejectsBadConfigs(t *testing.T) {
	base := `
topics:
  - {id: a, desc: x}
  - {id: unclear, desc: fallback}
rules:
`
	cases := map[string]string{
		"unknown topic": base + `  - {id: r1, title_any: [x], topic: nope, mode: learn}`,
		"invalid mode":  base + `  - {id: r1, title_any: [x], topic: a, mode: banana}`,
		"no matchers":   base + `  - {id: r1, topic: a, mode: learn}`,
		"duplicate ids": base + "  - {id: r1, title_any: [x], topic: a, mode: learn}\n  - {id: r1, title_any: [y], topic: a, mode: learn}",
		"missing unclear": `
topics:
  - {id: a, desc: x}
rules: []`,
	}
	for name, body := range cases {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Errorf("%s: want error, got nil", name)
		}
	}
}
