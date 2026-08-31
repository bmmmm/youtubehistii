// SPDX-License-Identifier: GPL-3.0-or-later

package classify

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bmmmm/youtubehistii/internal/rules"
)

// TestBuildModePromptOffersNoEscape pins the design decision: the mode
// partition is total and "mixed" is the honest hedge, so the prompt must
// offer the three modes and never "unclear" — unlike the topic level, where
// refusing is an answer.
func TestBuildModePromptOffersNoEscape(t *testing.T) {
	system, user := BuildModePrompt(
		[]Item{{Input: rules.Input{Title: "some talk", Channel: "some channel"}}},
		[]string{"dev/talks"},
	)
	if strings.Contains(system, "unclear") {
		t.Errorf("mode prompt offers an escape hatch:\n%s", system)
	}
	for _, want := range []string{"consume", "learn", "mixed"} {
		if !strings.Contains(system, want) {
			t.Errorf("mode prompt misses %q", want)
		}
	}
	if !strings.Contains(user, "topic: dev/talks") {
		t.Errorf("user prompt misses the settled topic:\n%s", user)
	}
}

func TestParseBatchModes(t *testing.T) {
	ids := []string{"a", "b", "c"}
	for _, tc := range []struct {
		name  string
		reply string
		want  map[string]string // nil = expect an error
	}{
		{"clean", "1 consume\n2 learn\n3 mixed", map[string]string{"a": "consume", "b": "learn", "c": "mixed"}},
		{"out of order", "3 mixed\n1 learn\n2 consume", map[string]string{"a": "learn", "b": "consume", "c": "mixed"}},
		{"prose around it", "Sure!\n1 consume\n2 learn\n3 mixed\nDone.", map[string]string{"a": "consume", "b": "learn", "c": "mixed"}},
		// "cannot tell" twice is an answer: empty mode, no error — refusing
		// it would turn one honest hedge into a request per video.
		{"unclear accepted as empty", "1 consume\n2 unclear\n3 learn", map[string]string{"a": "consume", "b": "", "c": "learn"}},
		{"missing one", "1 consume\n2 learn", nil},
		{"duplicate number", "1 consume\n1 learn\n3 mixed", nil},
		{"number out of range", "1 consume\n2 learn\n7 mixed", nil},
		// A topic where the mode belongs means the model answered the wrong
		// question — the fallback should re-ask, not guess.
		{"topic instead of mode", "1 consume\n2 dev/talks\n3 learn", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseBatchModes(ids, tc.reply)
			if tc.want == nil {
				if err == nil {
					t.Fatalf("parsed %q into %v, want an error", tc.reply, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseBatchModes(%q) = %v", tc.reply, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBuildSubPromptPinsOneArea: the batch carries exactly one area and only
// that area's seeds — the sharper question is the whole point of grouping.
func TestBuildSubPromptPinsOneArea(t *testing.T) {
	system, user := BuildSubPrompt("gaming", []string{"rust", "aoe"}, []Item{
		{Input: rules.Input{Title: "some video"}},
	})
	if !strings.Contains(system, `"gaming"`) {
		t.Errorf("sub prompt does not pin the area:\n%s", system)
	}
	if !strings.Contains(system, "rust, aoe") {
		t.Errorf("sub prompt misses the area's seeds:\n%s", system)
	}
	if strings.Contains(system, "dev") {
		t.Errorf("sub prompt leaks another area:\n%s", system)
	}
	if !strings.Contains(user, "some video") {
		t.Errorf("user prompt misses the video:\n%s", user)
	}
}

func TestParseBatchSubs(t *testing.T) {
	cfg := testConfig()
	ids := []string{"a", "b"}
	for _, tc := range []struct {
		name  string
		reply string
		want  map[string]SubAnswer // nil = expect an error
	}{
		{"clean", "1 factorio 0.9\n2 rust 0.8",
			map[string]SubAnswer{"a": {Sub: "factorio", Confidence: 0.9}, "b": {Sub: "rust", Confidence: 0.8}}},
		// The model repeating the fixed area is decidable and stripped;
		// any OTHER area is a different answer and must fail.
		{"own area stripped", "1 gaming/factorio 0.9\n2 rust 0.8",
			map[string]SubAnswer{"a": {Sub: "factorio", Confidence: 0.9}, "b": {Sub: "rust", Confidence: 0.8}}},
		{"foreign area is an error", "1 dev/factorio 0.9\n2 rust 0.8", nil},
		// "?" is the honest refusal: empty sub, confidence kept.
		{"question mark", "1 ? 0.2\n2 rust 0.8",
			map[string]SubAnswer{"a": {Confidence: 0.2}, "b": {Sub: "rust", Confidence: 0.8}}},
		// emptySubs fold through NormalizeTopic exactly like the full path.
		{"other folds to empty", "1 other 0.9\n2 rust 0.8",
			map[string]SubAnswer{"a": {Confidence: 0.9}, "b": {Sub: "rust", Confidence: 0.8}}},
		{"missing one", "1 factorio 0.9", nil},
		{"bad confidence", "1 factorio 1.5\n2 rust 0.8", nil},
		{"duplicate", "1 factorio 0.9\n1 rust 0.8", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseBatchSubs(cfg, "gaming", ids, tc.reply)
			if tc.want == nil {
				if err == nil {
					t.Fatalf("parsed %q into %v, want an error", tc.reply, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseBatchSubs(%q) = %v", tc.reply, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestContextLineOnlyWhenSet: the channel-prior line must appear exactly for
// items that carry Context — an unconditional line would change the normal
// prompt, and verdicts would drift without a fingerprint bump to show for it.
func TestContextLineOnlyWhenSet(t *testing.T) {
	cfg := testConfig()
	seeds := map[string][]string{}
	base := Item{Input: rules.Input{Title: "t", Channel: "c"}}
	_, without := BuildPrompt(cfg, base, seeds)
	if strings.Contains(without, "other videos on this channel") {
		t.Errorf("context line appears without context:\n%s", without)
	}
	withCtx := base
	withCtx.Context = []string{"music/jazz", "music/blues"}
	_, with := BuildPrompt(cfg, withCtx, seeds)
	if !strings.Contains(with, "other videos on this channel: music/jazz, music/blues") {
		t.Errorf("context line missing:\n%s", with)
	}
}
