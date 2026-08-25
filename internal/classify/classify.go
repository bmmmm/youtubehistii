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
	"sort"
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

// Item is one video as the LLM sees it: the matcher input plus the area its
// YouTube category already decided. An empty Area means no category was
// available (tombstoned or not yet enriched) and the model picks one.
type Item struct {
	rules.Input
	Area string
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
	Basis      string  `json:"basis,omitempty"`    // legacy entries without it count as title-only
	Taxonomy   string  `json:"taxonomy,omitempty"` // rules.Config.Fingerprint at judgement time
}

// Stale reports whether a cached verdict should be re-asked.
//
// Two independent reasons. A taxonomy change outranks everything, tombstones
// included: the verdict names areas from a taxonomy that no longer exists, so
// keeping it would mix dead topic ids into the report — a tombstoned video's
// topic is exactly as outdated as any other. Legacy verdicts carry no
// fingerprint and so are always stale on the first run after the upgrade.
// Otherwise the metadata rule applies: a title-only verdict goes stale once
// real metadata lands, and a tombstoned video never gets more than the title.
func (v LLMVerdict) Stale(taxonomy string, hasMeta, unavailable bool) bool {
	if v.Taxonomy != taxonomy {
		return true
	}
	return v.Basis != BasisFull && hasMeta && !unavailable
}

// writeTaxonomy renders the shared taxonomy block: the fixed areas, the free
// sub level, and the subs already in use.
//
// Seeding the existing subs is what makes the free level workable — without
// it every batch invents its own spelling for the same subject and the
// vocabulary fans out instead of converging. The caller bounds the list, so
// this block does not grow with the corpus.
func writeTaxonomy(b *strings.Builder, cfg *rules.Config, seeds map[string][]string) {
	first := cfg.Topics[0].ID
	b.WriteString("A topic is either \"<area>\" or \"<area>/<sub>\".\n")
	b.WriteString("area MUST be one of:\n")
	for _, t := range cfg.Topics {
		fmt.Fprintf(b, "  %s — %s\n", t.ID, t.Desc)
	}
	fmt.Fprintf(b, "sub is a short lowercase slug YOU choose for the specific subject — the game, "+
		"the language, the show, the band (a-z, 0-9 and dashes only). Name it whenever the "+
		"metadata lets you: prefer %q over a bare %q. Leave the sub off only when you cannot "+
		"name the subject, and never put a sub on \"unclear\".\n", first+"/<the-specific-thing>", first)

	b.WriteString("Most videos come with \"area: <x> (fixed)\": their area is already decided, so the " +
		"topic is that exact area plus your sub — \"<x>/<your-sub>\". The area belongs in the topic and " +
		"nowhere else.\n")

	areas := make([]string, 0, len(seeds))
	for area, subs := range seeds {
		if len(subs) > 0 {
			areas = append(areas, area)
		}
	}
	if len(areas) == 0 {
		return
	}
	sort.Strings(areas)
	b.WriteString("Subs already in use — reuse one whenever it fits, invent a new one only if none does:\n")
	for _, area := range areas {
		fmt.Fprintf(b, "  %s: %s\n", area, strings.Join(seeds[area], ", "))
	}
}

// BuildPrompt renders the system+user messages for one video. The taxonomy
// is inlined so the model can only pick an area from it.
func BuildPrompt(cfg *rules.Config, item Item, seeds map[string][]string) (system, user string) {
	var b strings.Builder
	b.WriteString("You classify YouTube videos from watch-history metadata.\n")
	b.WriteString("Reply with EXACTLY one JSON object, no prose, no code fence:\n")
	b.WriteString(`{"topic": "<area>/<sub>", "mode": "consume|learn|mixed|unclear", "confidence": <0..1>}` + "\n")
	b.WriteString("mode is one of consume, learn or mixed — never a genre, never an area: ")
	b.WriteString("consume = watched for entertainment (let's plays, esports, concerts, memes); ")
	b.WriteString("learn = watched to learn (talks, tutorials, documentaries); mixed = genuinely both.\n")
	writeTaxonomy(&b, cfg, seeds)
	b.WriteString("If the metadata is not enough, use topic \"unclear\" with low confidence.")

	var u strings.Builder
	writeInputFields(&u, item, "")
	return b.String(), u.String()
}

// writeInputFields renders one video's metadata block, shared between the
// single and the batch prompt.
func writeInputFields(u *strings.Builder, item Item, indent string) {
	in := item.Input
	fmt.Fprintf(u, "%stitle: %s\n", indent, in.Title)
	if in.Channel != "" {
		fmt.Fprintf(u, "%schannel: %s\n", indent, in.Channel)
	}
	// The fixed area REPLACES the category line rather than joining it: it
	// carries the same information already translated into the taxonomy, and
	// two spellings of one fact is what makes small models hesitate.
	if item.Area != "" {
		fmt.Fprintf(u, "%sarea: %s (fixed)\n", indent, item.Area)
	} else if len(in.Categories) > 0 {
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
func BuildBatchPrompt(cfg *rules.Config, items []Item, seeds map[string][]string) (system, user string) {
	var b strings.Builder
	b.WriteString("You classify YouTube videos from watch-history metadata.\n")
	fmt.Fprintf(&b, "You get %d numbered videos. Reply with EXACTLY one line per video, in the same order:\n", len(items))
	b.WriteString("<n> <topic> <mode> <confidence>\n")
	b.WriteString("Always those four fields in that order, whatever the video is, separated by single spaces. " +
		"<topic> is ONE field: if it has a sub, the slash joins it — \"area/sub\", never \"area sub\".\n")
	b.WriteString("No prose, no code fences, no JSON.\n")
	fmt.Fprintf(&b, "Example: 2 %s/<sub> consume 0.9\n", cfg.Topics[0].ID)
	// Spelled out because a fixed area invites exactly this mistake: the
	// model writes the area twice, once as the topic and once where the mode
	// belongs ("2 music music 0.9"). Observed on 3 of 6 batches.
	b.WriteString("mode is one of consume, learn or mixed — never a genre, never an area, never the sub:\n")
	b.WriteString("  consume = watched for entertainment (let's plays, esports, concerts, memes)\n")
	b.WriteString("  learn   = watched to learn (talks, tutorials, documentaries)\n")
	b.WriteString("  mixed   = genuinely both\n")
	writeTaxonomy(&b, cfg, seeds)
	b.WriteString("If the metadata is not enough, use topic \"unclear\" and mode \"unclear\" ")
	b.WriteString("with low confidence — still four fields: <n> unclear unclear 0.1")

	var u strings.Builder
	for i, item := range items {
		fmt.Fprintf(&u, "%d.\n", i+1)
		writeInputFields(&u, item, "   ")
	}
	return b.String(), u.String()
}

// normalizeFields maps the field layouts models actually produce onto the
// canonical four. Every rewrite has to be DECIDABLE from the values in the
// line — never guessed: the line number stays the reply key, and a layout
// that could mean two things is left alone to fail into the single-request
// fallback. Observed against Qwen3.6-35B-A3B on real batches:
//
// The layouts below were observed; the topics in them are illustrations, not
// transcripts — a real batch is somebody's watch history, and its subjects do
// not belong in a public comment.
//
//	5 fields "1 gaming factorio consume 0.9" — area and sub split on a
//	  space instead of a slash. Telling the model the topic has two parts is
//	  what invites this, and refusing the line costs a request per video.
//	3 fields "1 science-technology/talks 0.9" — the mode is simply left out.
//	  An absent mode already means "cannot tell" everywhere else.
func normalizeFields(cfg *rules.Config, fields []string) []string {
	switch len(fields) {
	case 5:
		_, areaOK := cfg.NormalizeTopic(fields[1])
		_, modeOK := parseMode(fields[3])
		if areaOK && modeOK && isConfidence(fields[4]) {
			return []string{fields[0], fields[1] + "/" + fields[2], fields[3], fields[4]}
		}
	case 4:
		// "1 education dev 0.9" — both quirks at once: mode left out AND the
		// sub split off on a space, which makes a dropped mode look like a
		// broken one. Only rewritten when position 3 is NOT a valid mode: a
		// well-formed line is never touched, and the resulting verdict claims
		// less (no mode) rather than more.
		// The bare-area check is what keeps this narrow: "1 dev/talks binge
		// 0.9" already HAS a sub, so "binge" cannot be one — that line is a
		// broken mode and stays an error.
		if _, modeOK := parseMode(fields[2]); !modeOK && !strings.Contains(fields[1], "/") {
			if _, areaOK := cfg.NormalizeTopic(fields[1]); areaOK && isConfidence(fields[3]) {
				return []string{fields[0], fields[1] + "/" + fields[2], "unclear", fields[3]}
			}
		}
	case 3:
		// Covers the older observed short form "1 unclear 0.1" as the special
		// case it always was: "unclear" is a topic like any other.
		if _, ok := cfg.NormalizeTopic(fields[1]); ok && isConfidence(fields[2]) {
			return []string{fields[0], fields[1], "unclear", fields[2]}
		}
	}
	return fields
}

func isConfidence(s string) bool {
	c, err := strconv.ParseFloat(s, 64)
	return err == nil && c >= 0 && c <= 1
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
	topic, ok := cfg.NormalizeTopic(v.Topic)
	if !ok {
		return LLMVerdict{}, fmt.Errorf("verdict names unknown area in topic %q", v.Topic)
	}
	v.Topic = topic
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
		fields := normalizeFields(cfg, strings.Fields(line))
		if len(fields) != 4 {
			continue // prose, fences, blank lines — completeness is checked below
		}
		m := lineNumberRe.FindStringSubmatch(fields[0])
		if m == nil {
			continue // no line number — prose
		}
		n, _ := strconv.Atoi(m[1])
		rawTopic, modeStr, confStr := fields[1], fields[2], fields[3]
		conf, confErr := strconv.ParseFloat(confStr, 64)
		mode, modeOK := parseMode(modeStr)
		topic, topicOK := cfg.NormalizeTopic(rawTopic)
		if n < 1 || n > len(ids) {
			if topicOK && modeOK && confErr == nil {
				return nil, fmt.Errorf("verdict for line %d of %d", n, len(ids))
			}
			continue // not verdict-shaped either — prose
		}
		if seen[n] {
			return nil, fmt.Errorf("duplicate verdict for line %d", n)
		}
		seen[n] = true
		if !topicOK {
			return nil, fmt.Errorf("line %d: topic %q names no known area", n, rawTopic)
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
