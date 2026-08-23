// SPDX-License-Identifier: GPL-3.0-or-later

package rules

import (
	"os"
	"path/filepath"
	"strings"
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

	// Channel rule beats the later title rule.
	topic, mode, ruleID, ok := cfg.Match(Input{
		Title: "Some talk", Channel: "Example Conference Channel", Categories: []string{"Gaming"},
	})
	if !ok || topic != "science-technology/talks" || mode != "learn" || ruleID != "conference-channel" {
		t.Errorf("got %s/%s via %s ok=%v", topic, mode, ruleID, ok)
	}

	// text_any matches inside a tag, not just the title.
	topic, _, ruleID, ok = cfg.Match(Input{Title: "Monday stream", Tags: []string{"counter-strike"}})
	if !ok || topic != "gaming/cs2" || ruleID != "cs2" {
		t.Errorf("tag text match: got %s via %s ok=%v", topic, ruleID, ok)
	}

	// category_any is for moving a whole category, and still works.
	topic, mode, _, ok = cfg.Match(Input{Title: "Teaser", Categories: []string{"Trailers"}})
	if !ok || topic != "film-animation" || mode != "consume" {
		t.Errorf("category rule: got %s/%s ok=%v", topic, mode, ok)
	}

	// The point of the whole redesign: a category the example does NOT
	// contradict matches no rule at all. Its area comes from
	// AreaForCategory, and the video goes on to the LLM for sub and mode
	// instead of being finished off with a hand-written fallback.
	if _, _, id, ok := cfg.Match(Input{Title: "chill vibes mix", Categories: []string{"Music"}}); ok {
		t.Errorf("plain %q must not match a rule any more, got %q", "Music", id)
	}
	if _, _, id, ok := cfg.Match(Input{Title: "boss fight", Categories: []string{"Gaming"}}); ok {
		t.Errorf("plain %q must not match a rule any more, got %q", "Gaming", id)
	}

	// Nothing matches -> not ok.
	if _, _, _, ok := cfg.Match(Input{Title: "completely opaque"}); ok {
		t.Error("expected no match")
	}
}

func TestMatchIsCaseInsensitive(t *testing.T) {
	cfg := loadExample(t)
	topic, _, _, ok := cfg.Match(Input{Title: "CS2 — final round"})
	if !ok || topic != "gaming/cs2" {
		t.Errorf("got %s ok=%v", topic, ok)
	}
}

func TestAreasDefaultToYouTubeCategories(t *testing.T) {
	// A config without `topics:` gets YouTube's categories as its areas —
	// that is the normal case, not a fallback.
	cfg, err := Load(writeConfig(t, `
rules:
  - id: only
    title_any: ["x"]
    topic: gaming
    mode: consume
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Topics) != len(YouTubeAreas()) {
		t.Fatalf("got %d areas, want the %d YouTube ones", len(cfg.Topics), len(YouTubeAreas()))
	}
	for _, want := range []string{"gaming", "music", "science-technology", "people-blogs", "unclear"} {
		if !cfg.HasArea(want) {
			t.Errorf("default areas miss %q", want)
		}
	}
}

func TestAreaForCategory(t *testing.T) {
	cfg, err := Load(writeConfig(t, "rules: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"Science & Technology":  "science-technology",
		"News & Politics":       "news-politics",
		"Nonprofits & Activism": "nonprofits-activism",
		"Gaming":                "gaming",
		"Howto & Style":         "howto-style",
	} {
		got, ok := cfg.AreaForCategory(in)
		if !ok || got != want {
			t.Errorf("AreaForCategory(%q) = %q,%v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "   ", "Made Up Category"} {
		if got, ok := cfg.AreaForCategory(in); ok {
			t.Errorf("AreaForCategory(%q) = %q, want no match", in, got)
		}
	}

	// With an own taxonomy the YouTube categories stop mapping, so the LLM
	// decides the area again instead of areas appearing the prompt never named.
	own, err := Load(writeConfig(t, `
topics:
  - id: gaming
    desc: games
  - id: unclear
    desc: cannot tell
rules: []
`))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := own.AreaForCategory("Gaming"); !ok || got != "gaming" {
		t.Errorf("an own area that happens to match a category should still map: %q,%v", got, ok)
	}
	if got, ok := own.AreaForCategory("Music"); ok {
		t.Errorf("category outside the own taxonomy mapped to %q", got)
	}
}

func TestSubsThatNameNothingAreDropped(t *testing.T) {
	cfg := &Config{Topics: []Topic{{ID: "sports"}, {ID: "gaming"}, {ID: "unclear"}}}
	// Seen in a real probe run: told to leave the sub off, the model wrote
	// "other" instead — under sports, gaming, entertainment and autos alike.
	for _, in := range []string{"sports/other", "sports/misc", "sports/N/A", "sports/Various"} {
		if got, _ := cfg.NormalizeTopic(in); got != "sports" {
			t.Errorf("NormalizeTopic(%q) = %q, want the bare area", in, got)
		}
	}
	// A sub that just repeats its area says nothing either.
	if got, _ := cfg.NormalizeTopic("sports/Sports"); got != "sports" {
		t.Errorf("got %q, want the bare area", got)
	}
	// A real sub is untouched.
	if got, _ := cfg.NormalizeTopic("sports/subject-e"); got != "sports/subject-e" {
		t.Errorf("got %q, want the sub kept", got)
	}

	// The alias runs FIRST, so a config that gives "other" a meaning still
	// wins over the drop list.
	aliased := &Config{
		Topics:     []Topic{{ID: "gaming"}, {ID: "unclear"}},
		SubAliases: map[string]string{"other": "esports"},
	}
	if got, _ := aliased.NormalizeTopic("gaming/other"); got != "gaming/esports" {
		t.Errorf("got %q, want the alias to win over the drop list", got)
	}
}

func TestSlugifyCapAppliesToSubsOnly(t *testing.T) {
	// The sub is bounded so a hallucinated sentence cannot become a topic;
	// an area id is a fixed vocabulary and must never be truncated, or two
	// long categories could collide into one area.
	const long = "Nonprofits & Activism and then some more words"
	if got := slugify(long, 0); len(got) <= subMaxLen {
		t.Errorf("uncapped slugify truncated: %q", got)
	}
	if got := slugifySub(long); len(got) > subMaxLen {
		t.Errorf("sub slug %q longer than the %d cap", got, subMaxLen)
	}
	if got := slugify("  Science & Technology  ", 0); got != "science-technology" {
		t.Errorf("got %q — ampersands and edges must collapse to single dashes", got)
	}
}

func TestReplaceArea(t *testing.T) {
	cfg := &Config{Topics: []Topic{{ID: "gaming"}, {ID: "music"}, {ID: "unclear"}}}
	for _, tc := range []struct {
		topic, area, want, comment string
	}{
		{"music/rap", "gaming", "gaming/rap", "the sub survives, the area is replaced"},
		{"gaming/rust", "gaming", "gaming/rust", "already right — unchanged"},
		{"unclear", "music", "music", "no sub to keep"},
		{"music", "gaming", "gaming", "bare area"},
		{"music/rap", "unclear", "unclear", `"unclear" never carries a sub`},
	} {
		if got := cfg.ReplaceArea(tc.topic, tc.area); got != tc.want {
			t.Errorf("%s: ReplaceArea(%q, %q) = %q, want %q", tc.comment, tc.topic, tc.area, got, tc.want)
		}
	}
	// An area that is not in the taxonomy must not silently produce one.
	if got := cfg.ReplaceArea("music/rap", "nonsense"); got != "music/rap" {
		t.Errorf("unknown area: got %q, want the topic left alone", got)
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

func twoLevelConfig(t *testing.T) *Config {
	t.Helper()
	return &Config{
		Topics:     []Topic{{ID: "gaming", Desc: "games"}, {ID: "tech", Desc: "software"}, {ID: "unclear", Desc: "cannot tell"}},
		SubAliases: map[string]string{"rust-game": "rust"},
	}
}

func TestNormalizeTopic(t *testing.T) {
	cfg := twoLevelConfig(t)
	cases := []struct {
		in      string
		want    string
		wantOK  bool
		comment string
	}{
		{in: "gaming", want: "gaming", wantOK: true, comment: "bare area"},
		{in: "gaming/rust", want: "gaming/rust", wantOK: true, comment: "area and sub"},
		{in: " Gaming/Rust ", want: "gaming/rust", wantOK: true, comment: "casing and padding"},
		{in: "gaming/counter strike", want: "gaming/counter-strike", wantOK: true, comment: "space becomes a dash"},
		{in: "gaming/counter_strike", want: "gaming/counter-strike", wantOK: true, comment: "underscore becomes a dash"},
		{in: "gaming/a/b", want: "gaming/a-b", wantOK: true, comment: "only the first slash splits"},
		{in: "gaming/", want: "gaming", wantOK: true, comment: "empty sub drops"},
		{in: "gaming/---", want: "gaming", wantOK: true, comment: "sub of pure separators drops"},
		{in: "gaming/rust-game", want: "gaming/rust", wantOK: true, comment: "alias folds the sub"},
		{in: "unclear/something", want: "unclear", wantOK: true, comment: "unclear never carries a sub"},
		{in: "health/fitness", wantOK: false, comment: "unknown area"},
		{in: "", wantOK: false, comment: "empty"},
		{
			in:      "gaming/an-extremely-long-subject-name-that-keeps-going",
			want:    "gaming/an-extremely-long-subjec", // 24 chars of sub
			wantOK:  true,
			comment: "sub capped at subMaxLen",
		},
	}
	for _, tc := range cases {
		got, ok := cfg.NormalizeTopic(tc.in)
		if ok != tc.wantOK {
			t.Errorf("%s: NormalizeTopic(%q) ok = %v, want %v", tc.comment, tc.in, ok, tc.wantOK)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("%s: NormalizeTopic(%q) = %q, want %q", tc.comment, tc.in, got, tc.want)
		}
	}
}

func TestSplitTopic(t *testing.T) {
	for in, want := range map[string][2]string{
		"gaming":      {"gaming", ""},
		"gaming/rust": {"gaming", "rust"},
		"":            {"", ""},
	} {
		area, sub := SplitTopic(in)
		if area != want[0] || sub != want[1] {
			t.Errorf("SplitTopic(%q) = %q, %q; want %q, %q", in, area, sub, want[0], want[1])
		}
	}
}

func TestFingerprint(t *testing.T) {
	base := &Config{Topics: []Topic{{ID: "gaming", Desc: "games"}, {ID: "unclear", Desc: "cannot tell"}}}
	reordered := &Config{Topics: []Topic{{ID: "unclear", Desc: "cannot tell"}, {ID: "gaming", Desc: "games"}}}
	if base.Fingerprint() != reordered.Fingerprint() {
		t.Error("fingerprint must not depend on the order topics are listed in")
	}

	// Both halves of a topic reach the model, so both must invalidate.
	for name, changed := range map[string]*Config{
		"desc reworded": {Topics: []Topic{{ID: "gaming", Desc: "video games"}, {ID: "unclear", Desc: "cannot tell"}}},
		"area added":    {Topics: []Topic{{ID: "gaming", Desc: "games"}, {ID: "tech", Desc: "software"}, {ID: "unclear", Desc: "cannot tell"}}},
		"area removed":  {Topics: []Topic{{ID: "unclear", Desc: "cannot tell"}}},
	} {
		if base.Fingerprint() == changed.Fingerprint() {
			t.Errorf("%s: fingerprint must change", name)
		}
	}

	// Aliases are applied on read, so folding subs must NOT invalidate.
	aliased := &Config{
		Topics:     []Topic{{ID: "gaming", Desc: "games"}, {ID: "unclear", Desc: "cannot tell"}},
		SubAliases: map[string]string{"rust-game": "rust"},
	}
	if base.Fingerprint() != aliased.Fingerprint() {
		t.Error("sub aliases must not invalidate cached verdicts")
	}
}

func TestLoadRejectsSlashInTopicID(t *testing.T) {
	body := `
topics:
  - {id: gaming/rust, desc: x}
  - {id: unclear, desc: fallback}
rules: []`
	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("a pre-two-level topic id must be rejected")
	}
	if !strings.Contains(err.Error(), "gaming") {
		t.Errorf("error should name the area to migrate to, got: %v", err)
	}
}

func TestLoadCanonicalizesRuleTopics(t *testing.T) {
	body := `
topics:
  - {id: gaming, desc: x}
  - {id: unclear, desc: fallback}
sub_aliases:
  rust-game: rust
rules:
  - {id: r1, title_any: [x], topic: "Gaming/Rust Game", mode: consume}`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Rules[0].Topic; got != "gaming/rust" {
		t.Errorf("rule topic = %q, want %q (canonicalized and aliased at load)", got, "gaming/rust")
	}
	if topic, _, _, ok := cfg.Match(Input{Title: "x"}); !ok || topic != "gaming/rust" {
		t.Errorf("Match returned %q ok=%v, want the canonical topic", topic, ok)
	}
}
