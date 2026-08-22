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
	"strconv"
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

// Basis values: what metadata the LLM saw when it judged the video.
const (
	BasisFull      = "full"       // tags/categories from the meta cache
	BasisTitleOnly = "title-only" // takeout title/channel only
)

// LLMVerdict is the cached per-video LLM answer.
type LLMVerdict struct {
	Topic      string  `json:"topic"`
	Mode       string  `json:"mode"`
	Confidence float64 `json:"confidence"`
	Model      string  `json:"model"`
	Basis      string  `json:"basis,omitempty"` // legacy entries without it count as title-only
}

// Stale reports whether a cached verdict should be re-asked given the current
// metadata state: a title-only verdict goes stale once real metadata exists.
// Tombstoned videos never get more than the title, so they are never re-asked.
func (v LLMVerdict) Stale(hasMeta, unavailable bool) bool {
	return v.Basis != BasisFull && hasMeta && !unavailable
}

// BuildPrompt renders the system+user messages for one video. The taxonomy
// is inlined so the model can only pick from it.
func BuildPrompt(cfg *rules.Config, in rules.Input) (system, user string) {
	var b strings.Builder
	b.WriteString("You classify YouTube videos from watch-history metadata.\n")
	b.WriteString("Reply with EXACTLY one JSON object, no prose, no code fence:\n")
	b.WriteString(`{"topic": "<topic-id>", "mode": "consume|learn|mixed|unclear", "confidence": <0..1>}` + "\n")
	b.WriteString("mode: consume = watched for entertainment (let's plays, esports, music, memes); ")
	b.WriteString("learn = watched to learn (talks, tutorials, documentaries); mixed = genuinely both.\n")
	b.WriteString("topic MUST be one of:\n")
	for _, t := range cfg.Topics {
		fmt.Fprintf(&b, "  %s — %s\n", t.ID, t.Desc)
	}
	b.WriteString("If the metadata is not enough, use topic \"unclear\" with low confidence.")

	var u strings.Builder
	writeInputFields(&u, in, "")
	return b.String(), u.String()
}

// writeInputFields renders one video's metadata block, shared between the
// single and the batch prompt.
func writeInputFields(u *strings.Builder, in rules.Input, indent string) {
	fmt.Fprintf(u, "%stitle: %s\n", indent, in.Title)
	if in.Channel != "" {
		fmt.Fprintf(u, "%schannel: %s\n", indent, in.Channel)
	}
	if len(in.Categories) > 0 {
		fmt.Fprintf(u, "%syoutube category: %s\n", indent, strings.Join(in.Categories, ", "))
	}
	if len(in.Tags) > 0 {
		tags := in.Tags
		if len(tags) > 15 {
			tags = tags[:15]
		}
		fmt.Fprintf(u, "%screator tags: %s\n", indent, strings.Join(tags, ", "))
	}
}

// BuildBatchPrompt renders one prompt for many videos. The reply format is
// one LINE per video, not JSON: generation dominates local-LLM latency, and
// `<n> <topic> <mode> <confidence>` is roughly half the output tokens of a
// JSON object while being easier for small models to emit correctly. The
// line number, not the video ID, is the reply key — models mistranscribe
// the high-entropy IDs (observed with Qwen3.8-27B), while 1..N is copyable
// and gives the same exactly-once mapping guarantee.
func BuildBatchPrompt(cfg *rules.Config, items []rules.Input) (system, user string) {
	var b strings.Builder
	b.WriteString("You classify YouTube videos from watch-history metadata.\n")
	fmt.Fprintf(&b, "You get %d numbered videos. Reply with EXACTLY one line per video, in the same order:\n", len(items))
	b.WriteString("<n> <topic-id> <mode> <confidence>\n")
	fmt.Fprintf(&b, "Example: 2 %s consume 0.9\n", cfg.Topics[0].ID)
	b.WriteString("No prose, no code fences, no JSON — one line per video.\n")
	b.WriteString("mode: consume = watched for entertainment (let's plays, esports, music, memes); ")
	b.WriteString("learn = watched to learn (talks, tutorials, documentaries); mixed = genuinely both.\n")
	b.WriteString("topic MUST be one of:\n")
	for _, t := range cfg.Topics {
		fmt.Fprintf(&b, "  %s — %s\n", t.ID, t.Desc)
	}
	b.WriteString("If the metadata is not enough, use topic \"unclear\" and mode \"unclear\" ")
	b.WriteString("with low confidence — still four fields: <n> unclear unclear 0.1")

	var u strings.Builder
	for i, in := range items {
		fmt.Fprintf(&u, "%d.\n", i+1)
		writeInputFields(&u, in, "   ")
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
	mode, ok := parseMode(v.Mode)
	if !ok {
		return LLMVerdict{}, fmt.Errorf("verdict has invalid mode %q", v.Mode)
	}
	v.Mode = mode
	if v.Confidence < 0 || v.Confidence > 1 {
		return LLMVerdict{}, fmt.Errorf("confidence %v out of range", v.Confidence)
	}
	return v, nil
}

// parseMode normalizes an LLM mode: the explicit "unclear" (and, in JSON
// replies, an omitted mode) becomes the empty string used everywhere else
// for "cannot tell". Anything else is rejected.
func parseMode(m string) (string, bool) {
	switch m {
	case "consume", "learn", "mixed":
		return m, true
	case "unclear", "":
		return "", true
	}
	return "", false
}

// lineNumberRe matches the reply key "1" / "2." / "3)" at the start of a line.
var lineNumberRe = regexp.MustCompile(`^(\d+)[.):]?$`)

// ParseBatchVerdicts parses the line-per-video reply of a batch prompt; the
// verdict for line number n belongs to ids[n-1]. STRICT on the mapping:
// every line number 1..len(ids) must appear exactly once with a valid
// topic/mode/confidence, and no verdict may name a number outside that
// range — any violation is an error and the caller falls back to single
// requests. Surrounding prose and code fences are skipped; verdicts are
// never guessed.
func ParseBatchVerdicts(cfg *rules.Config, ids []string, reply string) (map[string]LLMVerdict, error) {
	out := make(map[string]LLMVerdict, len(ids))
	seen := make(map[int]bool, len(ids))
	for _, line := range strings.Split(reply, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[1] == "unclear" {
			// Models tend to collapse "unclear unclear" into one field —
			// accept the observed short form "<n> unclear <confidence>".
			fields = []string{fields[0], "unclear", "unclear", fields[2]}
		}
		if len(fields) != 4 {
			continue // prose, fences, blank lines — completeness is checked below
		}
		m := lineNumberRe.FindStringSubmatch(fields[0])
		if m == nil {
			continue // no line number — prose
		}
		n, _ := strconv.Atoi(m[1])
		topic, modeStr, confStr := fields[1], fields[2], fields[3]
		conf, confErr := strconv.ParseFloat(confStr, 64)
		mode, modeOK := parseMode(modeStr)
		if n < 1 || n > len(ids) {
			if cfg.HasTopic(topic) && modeOK && confErr == nil {
				return nil, fmt.Errorf("verdict for line %d of %d", n, len(ids))
			}
			continue // not verdict-shaped either — prose
		}
		if seen[n] {
			return nil, fmt.Errorf("duplicate verdict for line %d", n)
		}
		seen[n] = true
		if !cfg.HasTopic(topic) {
			return nil, fmt.Errorf("line %d: unknown topic %q", n, topic)
		}
		if !modeOK {
			return nil, fmt.Errorf("line %d: invalid mode %q", n, modeStr)
		}
		if confErr != nil || conf < 0 || conf > 1 {
			return nil, fmt.Errorf("line %d: bad confidence %q", n, confStr)
		}
		out[ids[n-1]] = LLMVerdict{Topic: topic, Mode: mode, Confidence: conf}
	}
	if len(out) != len(ids) {
		for n := 1; n <= len(ids); n++ {
			if !seen[n] {
				return nil, fmt.Errorf("reply misses %d of %d verdicts (first missing: line %d)", len(ids)-len(out), len(ids), n)
			}
		}
	}
	return out, nil
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
