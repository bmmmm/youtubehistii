// SPDX-License-Identifier: GPL-3.0-or-later

// Package rules loads the classification config and runs the deterministic
// stage-1 matcher: first matching rule wins, every verdict names its rule.
package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Topic is one AREA — the fixed, top level of the taxonomy. The second level
// (the sub) is not configured: the LLM invents it per video and
// NormalizeTopic slugifies it, so "gaming/factorio" appears without anyone
// adding it here.
type Topic struct {
	ID   string `yaml:"id"`
	Desc string `yaml:"desc"`
}

// Rule fires when ANY listed value matches its field (values are
// case-insensitive; see config/rules.example.yaml for field semantics).
type Rule struct {
	ID          string   `yaml:"id"`
	ChannelAny  []string `yaml:"channel_any"`
	TitleAny    []string `yaml:"title_any"`
	TagAny      []string `yaml:"tag_any"`
	TextAny     []string `yaml:"text_any"`
	CategoryAny []string `yaml:"category_any"`
	Topic       string   `yaml:"topic"`
	Mode        string   `yaml:"mode"`
}

type LLM struct {
	Model   string `yaml:"model"`
	BaseURL string `yaml:"base_url"`
}

type Config struct {
	LLM    LLM     `yaml:"llm"`
	Topics []Topic `yaml:"topics"`
	// SubAliases folds drifted sub slugs into one ("rust-game" -> "rust").
	// Applied on every NormalizeTopic call, including when the report reads
	// verdicts back, so folding two subs together costs no re-classification.
	SubAliases map[string]string `yaml:"sub_aliases"`
	Rules      []Rule            `yaml:"rules"`
}

var validModes = map[string]bool{"consume": true, "learn": true, "mixed": true}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// No `topics:` is the normal case: the areas then ARE YouTube's own
	// categories, which every enriched video already carries. Listing topics
	// replaces that default outright rather than adding to it — otherwise the
	// YouTube areas could never be got rid of, and the prompt would name areas
	// the config never mentioned.
	if len(c.Topics) == 0 {
		c.Topics = YouTubeAreas()
	}
	topicIDs := map[string]bool{}
	for _, t := range c.Topics {
		if t.ID == "" {
			return nil, fmt.Errorf("%s: topic without id", path)
		}
		// Caught explicitly because a pre-two-level config ("gaming/rust")
		// would otherwise fail later with a confusing message about rules.
		if strings.Contains(t.ID, "/") {
			area, _, _ := strings.Cut(t.ID, "/")
			return nil, fmt.Errorf("%s: topic %q contains \"/\" — topics are the top level (areas) now and the second level is free: list %q here and let rules or the LLM assign %q",
				path, t.ID, area, t.ID)
		}
		topicIDs[t.ID] = true
	}
	if !topicIDs["unclear"] {
		return nil, fmt.Errorf("%s: taxonomy must contain an %q topic", path, "unclear")
	}
	seen := map[string]bool{}
	for i, r := range c.Rules {
		if r.ID == "" {
			return nil, fmt.Errorf("%s: rule #%d has no id", path, i+1)
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("%s: duplicate rule id %q", path, r.ID)
		}
		seen[r.ID] = true
		// Rules may be as specific as they like ("gaming/rust"); only the
		// area has to exist. Canonicalizing here means Match returns the
		// same shape the LLM path produces.
		topic, ok := c.NormalizeTopic(r.Topic)
		if !ok {
			return nil, fmt.Errorf("%s: rule %q assigns topic %q whose area is not in the taxonomy", path, r.ID, r.Topic)
		}
		c.Rules[i].Topic = topic
		if !validModes[r.Mode] {
			return nil, fmt.Errorf("%s: rule %q has invalid mode %q (consume|learn|mixed)", path, r.ID, r.Mode)
		}
		if len(r.ChannelAny)+len(r.TitleAny)+len(r.TagAny)+len(r.TextAny)+len(r.CategoryAny) == 0 {
			return nil, fmt.Errorf("%s: rule %q has no matchers", path, r.ID)
		}
	}
	return &c, nil
}

// HasArea reports whether id names one of the configured areas.
func (c *Config) HasArea(id string) bool {
	_, ok := c.canonicalArea(id)
	return ok
}

// canonicalArea resolves an area case-insensitively to the spelling used in
// the config, so "Gaming" from an LLM lands in the same bucket as "gaming".
func (c *Config) canonicalArea(s string) (string, bool) {
	for _, t := range c.Topics {
		if strings.EqualFold(t.ID, s) {
			return t.ID, true
		}
	}
	return "", false
}

// subMaxLen caps a sub slug: long enough for "counter-strike", short enough
// that a hallucinated sentence cannot become a topic.
const subMaxLen = 24

// emptySubs name nothing. Told to leave the sub off when it cannot identify
// the subject, a model reaches for one of these instead — and "sports/other"
// claims a second level that is not there, while a bare "sports" says the
// same thing honestly. Folded to no sub at all, which is what they mean.
var emptySubs = map[string]bool{
	"other": true, "others": true, "misc": true, "miscellaneous": true,
	"general": true, "various": true, "unknown": true, "none": true,
	"unclear": true, "n-a": true,
}

// NormalizeTopic canonicalizes a topic to "<area>" or "<area>/<sub>" and
// reports whether it is usable. This is the single gate every topic passes
// through — rule config at load time and LLM replies at parse time — so both
// paths produce byte-identical topics.
//
// The area must be configured; an unknown one is rejected (a batch reply
// naming it is thrown away rather than guessed). The sub is free text by
// design, so it is slugified instead of validated, then folded through
// SubAliases. "unclear" never carries a sub — it means the model could not
// tell, and a sub would pretend otherwise.
func (c *Config) NormalizeTopic(s string) (string, bool) {
	rawArea, rawSub, _ := strings.Cut(strings.TrimSpace(s), "/")
	area, ok := c.canonicalArea(strings.TrimSpace(rawArea))
	if !ok {
		return "", false
	}
	sub := slugifySub(rawSub)
	if alias, ok := c.SubAliases[sub]; ok {
		sub = slugifySub(alias)
	}
	// After the alias, so a config can still map "other" onto a real subject.
	// A sub that merely repeats its area says nothing either — models write
	// "entertainment/entertainment" when they cannot name the subject.
	if emptySubs[sub] || strings.EqualFold(sub, area) {
		sub = ""
	}
	if sub == "" || area == "unclear" {
		return area, true
	}
	return area + "/" + sub, true
}

// SplitTopic splits a canonical topic into its two levels; a topic without a
// sub returns an empty sub. Lives next to NormalizeTopic so the report splits
// exactly the way the classifier joined.
func SplitTopic(topic string) (area, sub string) {
	area, sub, _ = strings.Cut(topic, "/")
	return area, sub
}

// ReplaceArea swaps the area of a canonical topic, keeping the sub. Used
// where the area is already known from the YouTube category and only the sub
// was asked of the model: the answer's area is then not a judgement to
// respect but a field that may have drifted, while its sub is the payload.
// An "unclear" sub is dropped, matching NormalizeTopic.
func (c *Config) ReplaceArea(topic, area string) string {
	_, sub := SplitTopic(topic)
	joined := area
	if sub != "" {
		joined = area + "/" + sub
	}
	if out, ok := c.NormalizeTopic(joined); ok {
		return out
	}
	return topic
}

// slugifySub reduces whatever the model wrote to a stable slug, capped at
// subMaxLen. Free text is the point of this level, so it is normalized
// rather than rejected.
func slugifySub(s string) string { return slugify(s, subMaxLen) }

// slugify lowercases to [a-z0-9-] with no repeated or edge dashes; anything
// else (spaces, punctuation, ampersands, slashes, non-ASCII) acts as a
// separator. maxLen 0 means no cap — area ids must never be truncated, only
// the free sub level is bounded.
func slugify(s string, maxLen int) string {
	var b strings.Builder
	sep := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if sep && b.Len() > 0 {
				b.WriteByte('-')
			}
			sep = false
			b.WriteRune(r)
			if maxLen > 0 && b.Len() >= maxLen {
				break
			}
			continue
		}
		sep = true
	}
	// The loop breaks one rune late — a separator is written without checking
	// the length — so the cap is enforced here, where it is plainly the cap.
	out := strings.Trim(b.String(), "-")
	if maxLen > 0 && len(out) > maxLen {
		out = strings.Trim(out[:maxLen], "-")
	}
	return out
}

// Fingerprint identifies the taxonomy a verdict was made under: the areas and
// their descriptions, both of which go into the prompt. A cached verdict
// carrying a different fingerprint is stale — see classify.LLMVerdict.Stale.
// Sub aliases are deliberately NOT part of it: they are applied on read, so
// folding subs together must not invalidate anything.
func (c *Config) Fingerprint() string {
	entries := make([]string, 0, len(c.Topics))
	for _, t := range c.Topics {
		entries = append(entries, t.ID+"\x00"+t.Desc)
	}
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\x1e")))
	return hex.EncodeToString(sum[:4])
}

// Input is everything the matcher may look at for one video.
type Input struct {
	Title      string
	Channel    string
	Tags       []string
	Categories []string
}

// Match runs the rules top to bottom; the first hit wins.
func (c *Config) Match(in Input) (topic, mode, ruleID string, ok bool) {
	title := strings.ToLower(in.Title)
	channel := strings.ToLower(in.Channel)
	tags := lowerAll(in.Tags)
	cats := lowerAll(in.Categories)

	for _, r := range c.Rules {
		if matchesRule(r, title, channel, tags, cats) {
			return r.Topic, r.Mode, r.ID, true
		}
	}
	return "", "", "", false
}

func matchesRule(r Rule, title, channel string, tags, cats []string) bool {
	for _, v := range r.ChannelAny {
		if channel == strings.ToLower(v) {
			return true
		}
	}
	for _, v := range r.TitleAny {
		if strings.Contains(title, strings.ToLower(v)) {
			return true
		}
	}
	for _, v := range r.TagAny {
		if containsExact(tags, strings.ToLower(v)) {
			return true
		}
	}
	for _, v := range r.TextAny {
		lv := strings.ToLower(v)
		if strings.Contains(title, lv) {
			return true
		}
		for _, tag := range tags {
			if strings.Contains(tag, lv) {
				return true
			}
		}
	}
	for _, v := range r.CategoryAny {
		if containsExact(cats, strings.ToLower(v)) {
			return true
		}
	}
	return false
}

func lowerAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	return out
}

func containsExact(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
