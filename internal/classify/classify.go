// SPDX-License-Identifier: GPL-3.0-or-later

// Package classify assigns topic and mode per video: deterministic rules
// first, a local LLM for the remainder. Every verdict names its source.
package classify

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bmmmm/youtubehistii/internal/fscache"
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
	// GoneReason carries enrich's tombstone reason through to the report, so
	// a view that cannot have a topic can at least say what happened to it:
	// "private", "removed", "age", "members", "terminated", "unavailable".
	// Empty on everything that still exists, and on tombstones written before
	// the reason was recorded.
	GoneReason string `json:"goneReason,omitempty"`
}

// Item is one video as the LLM sees it: the matcher input plus the area its
// YouTube category already decided. An empty Area means no category was
// available (tombstoned or not yet enriched) and the model picks one.
type Item struct {
	rules.Input
	Area string
	// Context is what the video's own metadata does not say: topics already
	// assigned to OTHER videos of the same channel. A prior, not a verdict —
	// it goes into the prompt so the model can weigh it against the title,
	// never into the answer directly. Measured at 91 % area accuracy
	// leave-one-out, which is why it is worth showing; available for only 86
	// of 3254 unclear tombstones, which is why it is a targeted retry and
	// not a campaign. Only ever set for that retry set: the normal prompt
	// must stay byte-identical or verdicts drift without a fingerprint bump.
	Context []string
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
	// Model is the model behind the LAST COMPLETE judgement, not behind every
	// field. A verdict that went through a "-retry no-sub" round under a
	// different model carries area and mode from the first judge and sub and
	// confidence from the second, and one string cannot say that — so the
	// retry rounds deliberately leave this field alone (retry_test.go pins
	// that). Read it as provenance for the whole answer only when no retry
	// marker is set.
	Model    string `json:"model"`
	Basis    string `json:"basis,omitempty"`    // legacy entries without it count as title-only
	Taxonomy string `json:"taxonomy,omitempty"` // rules.Config.Fingerprint at judgement time
	// Retried names the targeted re-asks already run for this video ("sub",
	// "mode", "context"), so a -retry pass is idempotent: a video the model
	// STILL could not give a sub or a mode is not asked the same narrow
	// question on every run. It is deliberately not part of Stale — a marker
	// records what was asked, it does not expire; the ~30k existing cache
	// files simply read as nil (everything selectable once).
	Retried []string `json:"retried,omitempty"`
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
	if len(item.Context) > 0 {
		fmt.Fprintf(u, "%sother videos on this channel: %s\n", indent, strings.Join(item.Context, ", "))
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

// BuildModePrompt asks ONLY the mode, for videos whose topic is settled but
// whose mode a batch reply left out (normalizeFields' "3 fields" case turned
// the omission into "cannot tell"). One word per line is the shortest
// constrained reply there is, so this round batches wider than the full one —
// the cost sits in the request, not in the tokens it generates.
//
// "unclear" is deliberately not offered: consume/learn/mixed is a total
// partition of WHY something was watched, and "mixed" is the honest hedge —
// an escape hatch would buy nothing "mixed" does not already say. That is
// the opposite of the topic level, where refusing IS an answer.
//
// topics runs parallel to items: the settled topic is shown per video, so
// the model judges "why watched" with the "what" already fixed.
func BuildModePrompt(items []Item, topics []string) (system, user string) {
	var b strings.Builder
	b.WriteString("You judge WHY a video was watched, from its metadata.\n")
	fmt.Fprintf(&b, "You get %d numbered videos. Reply with EXACTLY one line per video, in the same order:\n", len(items))
	b.WriteString("<n> <mode>\n")
	b.WriteString("mode is exactly one of consume, learn or mixed — one word, nothing else:\n")
	b.WriteString("  consume = watched for entertainment (let's plays, esports, concerts, memes)\n")
	b.WriteString("  learn   = watched to learn (talks, tutorials, documentaries)\n")
	b.WriteString("  mixed   = genuinely both\n")
	b.WriteString("Every video has one of the three. If it is a toss-up, answer mixed.\n")
	b.WriteString("No prose, no code fences, no JSON.\n")
	b.WriteString("Example: 2 consume")

	var u strings.Builder
	for i, item := range items {
		fmt.Fprintf(&u, "%d.\n", i+1)
		writeInputFields(&u, item, "   ")
		fmt.Fprintf(&u, "   topic: %s\n", topics[i])
	}
	return b.String(), u.String()
}

// ParseBatchModes parses the one-word-per-line reply of a mode prompt; the
// mode on line n belongs to ids[n-1]. STRICT on the mapping, exactly like
// ParseBatchVerdicts. An "unclear" reply is ACCEPTED and maps to the empty
// mode — the model saying "cannot tell" is an answer, and refusing it would
// turn one legitimate hedge into a single request per video of the batch.
// A topic where the mode belongs is an error: the model answered the wrong
// question, and the single-request fallback should re-ask it properly.
func ParseBatchModes(ids []string, reply string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	seen := make(map[int]bool, len(ids))
	for _, line := range strings.Split(reply, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue // prose, fences, blank lines — completeness is checked below
		}
		m := lineNumberRe.FindStringSubmatch(fields[0])
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		mode, modeOK := parseMode(fields[1])
		if n < 1 || n > len(ids) {
			if modeOK {
				return nil, fmt.Errorf("mode for line %d of %d", n, len(ids))
			}
			continue
		}
		if seen[n] {
			return nil, fmt.Errorf("duplicate mode for line %d", n)
		}
		seen[n] = true
		if !modeOK {
			return nil, fmt.Errorf("line %d: invalid mode %q", n, fields[1])
		}
		out[ids[n-1]] = mode
	}
	if len(out) != len(ids) {
		for n := 1; n <= len(ids); n++ {
			if !seen[n] {
				return nil, fmt.Errorf("reply misses %d of %d modes (first missing: line %d)", len(ids)-len(out), len(ids), n)
			}
		}
	}
	return out, nil
}

// BuildSubPrompt asks ONLY the sub level, for videos whose area is settled
// but whose verdict left the sub off. One area per batch on purpose: the
// prompt then carries only that area's seeds, and "which subject within THIS
// area" is a sharper question than the generic one — the model left these
// subs off when the full prompt made them optional, so here the sub is the
// whole answer.
func BuildSubPrompt(area string, seeds []string, items []Item) (system, user string) {
	var b strings.Builder
	b.WriteString("You name the SUBJECT of a YouTube video: the game, the band, the show, " +
		"the language, the team — the specific thing it is about.\n")
	fmt.Fprintf(&b, "All %d videos below are in the area %q. The area is already decided; "+
		"you supply the second level only.\n", len(items), area)
	b.WriteString("Reply with EXACTLY one line per video, in the same order:\n")
	b.WriteString("<n> <sub> <confidence>\n")
	b.WriteString("<sub> is ONE short lowercase slug (a-z, 0-9, dashes) — never the area " +
		"again, never a mode, never a sentence, never just the channel.\n")
	if len(seeds) > 0 {
		b.WriteString("Subs already in use in this area — reuse one whenever it fits, invent a new one only if none does:\n")
		fmt.Fprintf(&b, "  %s\n", strings.Join(seeds, ", "))
	}
	b.WriteString("Answer \"?\" as the sub only if the metadata names no subject at all.\n")
	b.WriteString("No prose, no code fences, no JSON.\n")
	b.WriteString("Example: 2 late-night-show 0.8")

	var u strings.Builder
	for i, item := range items {
		fmt.Fprintf(&u, "%d.\n", i+1)
		writeInputFields(&u, item, "   ")
	}
	return b.String(), u.String()
}

// SubAnswer is one line of a sub-prompt reply: the sub (empty when the model
// answered "?" or a slug the taxonomy folds to nothing) and its confidence.
type SubAnswer struct {
	Sub        string
	Confidence float64
}

// ParseBatchSubs parses the sub-prompt reply; the answer on line n belongs
// to ids[n-1]. STRICT like ParseBatchVerdicts. Two decidable rewrites, both
// observed shapes of "the model answered more than asked": a sub prefixed
// with the batch's OWN area is stripped (the area was fixed, repeating it
// adds nothing), while any other "x/y" is an error — a different area is a
// different answer, and rewriting it would be a guess. The final sub goes
// through NormalizeTopic, so empty-sub words ("other", "misc") and length
// caps apply exactly as they do on the full path.
func ParseBatchSubs(cfg *rules.Config, area string, ids []string, reply string) (map[string]SubAnswer, error) {
	out := make(map[string]SubAnswer, len(ids))
	seen := make(map[int]bool, len(ids))
	for _, line := range strings.Split(reply, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue // prose, fences, blank lines — completeness is checked below
		}
		m := lineNumberRe.FindStringSubmatch(fields[0])
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		rawSub, confStr := fields[1], fields[2]
		conf, confErr := strconv.ParseFloat(confStr, 64)
		if n < 1 || n > len(ids) {
			if confErr == nil {
				return nil, fmt.Errorf("sub for line %d of %d", n, len(ids))
			}
			continue
		}
		if seen[n] {
			return nil, fmt.Errorf("duplicate sub for line %d", n)
		}
		seen[n] = true
		if confErr != nil || conf < 0 || conf > 1 {
			return nil, fmt.Errorf("line %d: bad confidence %q", n, confStr)
		}
		if slash := strings.Index(rawSub, "/"); slash >= 0 {
			if rawSub[:slash] != area {
				return nil, fmt.Errorf("line %d: %q names an area other than the fixed %q", n, rawSub, area)
			}
			rawSub = rawSub[slash+1:]
		}
		if rawSub == "?" {
			out[ids[n-1]] = SubAnswer{Confidence: conf}
			continue
		}
		topic, ok := cfg.NormalizeTopic(area + "/" + rawSub)
		if !ok {
			return nil, fmt.Errorf("line %d: %q is not a usable sub", n, rawSub)
		}
		_, sub := rules.SplitTopic(topic)
		out[ids[n-1]] = SubAnswer{Sub: sub, Confidence: conf}
	}
	if len(out) != len(ids) {
		for n := 1; n <= len(ids); n++ {
			if !seen[n] {
				return nil, fmt.Errorf("reply misses %d of %d subs (first missing: line %d)", len(ids)-len(out), len(ids), n)
			}
		}
	}
	return out, nil
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
		// "1 gaming/factorio-consume 0.9" — the mode glued onto the sub with a
		// dash instead of a space, the fourth observed layout. Decidable: the
		// piece after the LAST dash is exactly a mode word and what remains is
		// still "area/sub". It must run before the whole-topic reading below,
		// which would happily accept the glued slug — that is how the mode
		// label ended up inside 24 sub names.
		if base, mode, ok := splitGluedMode(cfg, fields[1]); ok && isConfidence(fields[2]) {
			return []string{fields[0], base, mode, fields[2]}
		}
		// Covers the older observed short form "1 unclear 0.1" as the special
		// case it always was: "unclear" is a topic like any other.
		if _, ok := cfg.NormalizeTopic(fields[1]); ok && isConfidence(fields[2]) {
			return []string{fields[0], fields[1], "unclear", fields[2]}
		}
	}
	return fields
}

// splitGluedMode splits "area/sub-consume" into "area/sub" and "consume".
// Only a dash INSIDE the sub qualifies: a bare "gaming-consume" has no sub
// the mode could have glued onto, and reading its dash as a separator would
// be a guess. "unclear" never splits — parseMode maps it to the empty mode,
// and stripping it would silently invent a subject.
func splitGluedMode(cfg *rules.Config, topic string) (base, mode string, ok bool) {
	slash := strings.Index(topic, "/")
	dash := strings.LastIndex(topic, "-")
	if slash < 0 || dash < slash+2 {
		return "", "", false
	}
	m, modeOK := parseMode(topic[dash+1:])
	if !modeOK || m == "" {
		return "", "", false
	}
	if _, topicOK := cfg.NormalizeTopic(topic[:dash]); !topicOK {
		return "", "", false
	}
	return topic[:dash], m, true
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
	return fscache.ReadJSON[LLMVerdict](p)
}

// Write stores one verdict atomically (fscache carries the why). This cache
// used to write in place, and only never got hurt by it because a crashed
// classify run was always rerun by hand before anything read the cache.
func (c Cache) Write(id string, v LLMVerdict) error {
	p, err := c.path(id)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return fscache.WriteFile(p, append(b, '\n'))
}
