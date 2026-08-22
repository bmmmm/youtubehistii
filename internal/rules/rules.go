// SPDX-License-Identifier: GPL-3.0-or-later

// Package rules loads the classification config and runs the deterministic
// stage-1 matcher: first matching rule wins, every verdict names its rule.
package rules

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

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
	Rules  []Rule  `yaml:"rules"`
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
	if len(c.Topics) == 0 {
		return nil, fmt.Errorf("%s: no topics defined", path)
	}
	topicIDs := map[string]bool{}
	for _, t := range c.Topics {
		if t.ID == "" {
			return nil, fmt.Errorf("%s: topic without id", path)
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
		if !topicIDs[r.Topic] {
			return nil, fmt.Errorf("%s: rule %q assigns unknown topic %q", path, r.ID, r.Topic)
		}
		if !validModes[r.Mode] {
			return nil, fmt.Errorf("%s: rule %q has invalid mode %q (consume|learn|mixed)", path, r.ID, r.Mode)
		}
		if len(r.ChannelAny)+len(r.TitleAny)+len(r.TagAny)+len(r.TextAny)+len(r.CategoryAny) == 0 {
			return nil, fmt.Errorf("%s: rule %q has no matchers", path, r.ID)
		}
	}
	return &c, nil
}

// HasTopic reports whether id is part of the taxonomy (used to validate LLM output).
func (c *Config) HasTopic(id string) bool {
	for _, t := range c.Topics {
		if t.ID == id {
			return true
		}
	}
	return false
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
