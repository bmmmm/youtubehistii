// SPDX-License-Identifier: GPL-3.0-or-later

// Package classify assigns topic and mode per video: deterministic rules
// first, a local LLM for the remainder. Every verdict names its source.
package classify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bmmmm/youtubehistii/internal/rules"
)

// Verdict is one row of classified.jsonl — one per watch event, flat and
// CSV-friendly on purpose.
type Verdict struct {
	VideoID     string    `json:"videoID"`
	Title       string    `json:"title"`
	Channel     string    `json:"channel,omitempty"`
	ChannelID   string    `json:"channelID,omitempty"`
	WatchedAt   time.Time `json:"watchedAt"`
	Topic       string    `json:"topic"`
	Mode        string    `json:"mode,omitempty"`
	Source      string    `json:"source"` // "rule:<id>" | "llm:<model>" | "unclassified"
	Confidence  float64   `json:"confidence,omitempty"`
	DurationS   int       `json:"durationS,omitempty"`
	Unavailable bool      `json:"unavailable,omitempty"`
}

// LLMVerdict is the cached per-video LLM answer.
type LLMVerdict struct {
	Topic      string  `json:"topic"`
	Mode       string  `json:"mode"`
	Confidence float64 `json:"confidence"`
	Model      string  `json:"model"`
}

// BuildPrompt renders the system+user messages for one video. The taxonomy
// is inlined so the model can only pick from it.
func BuildPrompt(cfg *rules.Config, in rules.Input) (system, user string) {
	var b strings.Builder
	b.WriteString("You classify YouTube videos from watch-history metadata.\n")
	b.WriteString("Reply with EXACTLY one JSON object, no prose, no code fence:\n")
	b.WriteString(`{"topic": "<topic-id>", "mode": "consume|learn|mixed", "confidence": <0..1>}` + "\n")
	b.WriteString("mode: consume = watched for entertainment (let's plays, esports, music, memes); ")
	b.WriteString("learn = watched to learn (talks, tutorials, documentaries); mixed = genuinely both.\n")
	b.WriteString("topic MUST be one of:\n")
	for _, t := range cfg.Topics {
		fmt.Fprintf(&b, "  %s — %s\n", t.ID, t.Desc)
	}
	b.WriteString("If the metadata is not enough, use topic \"unclear\" with low confidence.")

	var u strings.Builder
	fmt.Fprintf(&u, "title: %s\n", in.Title)
	if in.Channel != "" {
		fmt.Fprintf(&u, "channel: %s\n", in.Channel)
	}
	if len(in.Categories) > 0 {
		fmt.Fprintf(&u, "youtube category: %s\n", strings.Join(in.Categories, ", "))
	}
	if len(in.Tags) > 0 {
		tags := in.Tags
		if len(tags) > 15 {
			tags = tags[:15]
		}
		fmt.Fprintf(&u, "creator tags: %s\n", strings.Join(tags, ", "))
	}
	return b.String(), u.String()
}

var jsonObjRe = regexp.MustCompile(`(?s)\{.*?\}`)

// ParseVerdict extracts the JSON verdict from an LLM reply, tolerating code
// fences and surrounding prose. The topic is validated against the taxonomy.
func ParseVerdict(cfg *rules.Config, reply string) (LLMVerdict, error) {
	raw := jsonObjRe.FindString(reply)
	if raw == "" {
		return LLMVerdict{}, fmt.Errorf("no JSON object in reply %q", truncate(reply, 120))
	}
	var v LLMVerdict
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return LLMVerdict{}, fmt.Errorf("bad verdict JSON: %w", err)
	}
	if !cfg.HasTopic(v.Topic) {
		return LLMVerdict{}, fmt.Errorf("verdict names unknown topic %q", v.Topic)
	}
	switch v.Mode {
	case "consume", "learn", "mixed":
	default:
		return LLMVerdict{}, fmt.Errorf("verdict has invalid mode %q", v.Mode)
	}
	if v.Confidence < 0 || v.Confidence > 1 {
		return LLMVerdict{}, fmt.Errorf("confidence %v out of range", v.Confidence)
	}
	return v, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var cacheKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,20}$`)

// Cache stores one LLM verdict per video ID, so reruns are free and stable.
type Cache struct{ Dir string }

func (c Cache) path(id string) (string, error) {
	if !cacheKeyRe.MatchString(id) {
		return "", fmt.Errorf("refusing suspicious video id %q", id)
	}
	return filepath.Join(c.Dir, id+".json"), nil
}

func (c Cache) Read(id string) (LLMVerdict, bool) {
	p, err := c.path(id)
	if err != nil {
		return LLMVerdict{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return LLMVerdict{}, false
	}
	var v LLMVerdict
	if err := json.Unmarshal(b, &v); err != nil {
		return LLMVerdict{}, false
	}
	return v, true
}

func (c Cache) Write(id string, v LLMVerdict) error {
	p, err := c.path(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o644)
}
