// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"reflect"
	"strings"
	"testing"
)

// TestReadPicksAcceptsBothShapes nails the parser that threw away a working
// referee. gemma-4-26b answered 61 disagreements correctly and 54 of them
// were counted as unreadable, because its lines carry no index — the fallback
// exists for exactly that reply, and the guards exist so it never guesses.
func TestReadPicksAcceptsBothShapes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
		n     int
		want  map[int]int
	}{{
		name:  "numbered, the shape the prompt asks for",
		reply: "1 2\n2 0\n3 1",
		n:     3,
		want:  map[int]int{1: 2, 2: 0, 3: 1},
	}, {
		name:  "numbered out of order is still numbered",
		reply: "3 1\n1 2\n2 0",
		n:     3,
		want:  map[int]int{1: 2, 2: 0, 3: 1},
	}, {
		name:  "unnumbered: read positionally when the count matches exactly",
		reply: "1 2\n1 0\n2 1",
		n:     3,
		want:  map[int]int{1: 2, 2: 0, 3: 1},
	}, {
		// The guard that earns the fallback its place: one line short, and
		// positional reading would put every answer on the wrong video.
		name:  "unnumbered and one line short: report what parsed, guess nothing",
		reply: "1 2\n1 0",
		n:     3,
		want:  map[int]int{1: 2},
	}, {
		name:  "prose around the answer does not count as a line",
		reply: "Here you go:\n1 2\n2 0\n3 1\nHope that helps!",
		n:     3,
		want:  map[int]int{1: 2, 2: 0, 3: 1},
	}, {
		name:  "a pick outside 0..2 is not an answer",
		reply: "1 2\n2 7\n3 1",
		n:     3,
		want:  map[int]int{1: 2, 3: 1},
	}, {
		name:  "trailing dot on the number, as models like to write it",
		reply: "1. 2\n2. 0\n3. 1",
		n:     3,
		want:  map[int]int{1: 2, 2: 0, 3: 1},
	}, {
		name:  "nothing readable at all",
		reply: "I cannot decide between these labels.",
		n:     3,
		want:  map[int]int{},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := abReadPicks(tc.reply, tc.n)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("abReadPicks(%q, %d) = %v, want %v", tc.reply, tc.n, got, tc.want)
			}
		})
	}
}

// TestVerdictRefusesWhatItCannotSupport pins both refusals with the numbers
// that produced them. The first row is the run that shipped "the candidate
// wins 86 %" from 7 of 61 answers; the second is the run that called 20-vs-16
// a win.
func TestVerdictRefusesWhatItCannotSupport(t *testing.T) {
	for _, tc := range []struct {
		name          string
		wins          abWins
		disagreements int
		wantContains  string
	}{{
		name:          "the real 7-of-61 run: no quorum, no verdict",
		wins:          abWins{a: 1, b: 6, unparsed: 54},
		disagreements: 61,
		wantContains:  "NONE",
	}, {
		name:          "the real 20-vs-16 run: quorum met, margin is noise",
		wins:          abWins{a: 16, b: 20, neither: 7, unparsed: 18},
		disagreements: 61,
		wantContains:  "TOO CLOSE",
	}, {
		name:          "a margin that clears the band is a real win",
		wins:          abWins{a: 8, b: 40},
		disagreements: 50,
		wantContains:  "the candidate wins",
	}, {
		name:          "the same margin the other way is a real loss",
		wins:          abWins{a: 40, b: 8},
		disagreements: 50,
		wantContains:  "LOSES",
	}, {
		name:          "nothing decided at all",
		wins:          abWins{neither: 10, unparsed: 51},
		disagreements: 61,
		wantContains:  "decided nothing",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := abVerdict(tc.wins, tc.disagreements)
			if !strings.Contains(got, tc.wantContains) {
				t.Fatalf("abVerdict(%+v, %d) = %q, want it to contain %q",
					tc.wins, tc.disagreements, got, tc.wantContains)
			}
		})
	}
}

// TestSampleIsDeterministicAndSpread guards the property the whole comparison
// rests on: two invocations must compare the SAME videos, or the numbers are
// not comparable across days or across candidate models.
func TestSampleIsDeterministicAndSpread(t *testing.T) {
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = string(rune('a'+i/26)) + string(rune('a'+i%26))
	}

	first := abSampleIDs(ids, 10)
	second := abSampleIDs(append([]string(nil), ids...), 10)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("sample is not stable: %v vs %v", first, second)
	}
	if len(first) != 10 {
		t.Fatalf("wanted 10 sampled ids, got %d", len(first))
	}
	// Spread, not the first ten: a sample taken off the front measures
	// whatever the corpus happens to begin with.
	if first[len(first)-1] == ids[9] {
		t.Fatalf("sample looks like a prefix, not a stride: %v", first)
	}
}
