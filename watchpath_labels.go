// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/bmmmm/youtubehistii/internal/omlx"
	"github.com/bmmmm/youtubehistii/internal/report"
)

// labelHoles asks the chat model for a short name per rabbit hole — the
// deepest n of them — and returns what it got, keyed by chain index.
//
// One cluster per request, never a batch. That is the measurement from the
// taxonomy namer, not caution: batching unconstrained names is 2.4x faster
// and CHANGES the names, because a name is exactly the thing being invented
// and the model reads its neighbours as context. Here the cost of a wrong
// name is smaller, but so is the saving — a warm run asks nothing at all,
// since every reply is cached under its own prompt.
//
// It never fails the render. A model that is down, slow or missing costs
// labels, and the page falls back to "chain of N · area" for every one it
// did not get — decoration, never structure.
func labelHoles(client *omlx.Client, p paths, path *report.Path, n int) map[int]string {
	if n <= 0 || len(path.Chains) == 0 {
		return nil
	}
	// Deepest first, then the longest-held, then the newest: deterministic,
	// or the cache would be keyed to an order that changes every run.
	idx := make([]int, len(path.Chains))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		ca, cb := path.Chains[a], path.Chains[b]
		if ca.Len != cb.Len {
			return cb.Len - ca.Len
		}
		if ca.Span != cb.Span {
			return int(cb.Span - ca.Span)
		}
		return a - b // chains run newest first, so the lower index is newer
	})
	idx = idx[:min(n, len(idx))]

	cacheDir := p.holeLabelCacheDir()
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: hole-label cache unusable (%v) — every run will pay the model again\n", err)
		cacheDir = ""
	}
	cache := hashCache[nameEntry]{dir: cacheDir}

	out := map[int]string{}
	asked, cached, failed := 0, 0, 0
	t0 := time.Now()
	warned := false
	for _, ci := range idx {
		system, user := holePrompt(path, ci)
		if e, ok := cache.read(client.Model, system, user); ok && e.Reply != "" {
			out[ci] = e.Reply
			cached++
			continue
		}
		reply, err := client.ChatMax(system, user, 24)
		asked++
		if err != nil {
			failed++
			if !warned {
				fmt.Fprintf(os.Stderr, "warning: naming rabbit holes failed (%v) — they keep their plain names\n", err)
				warned = true
			}
			// One failure is enough: the server is not going to answer the
			// next 149 either, and a run should not spend two minutes
			// finding that out.
			break
		}
		name := cleanHoleLabel(reply)
		if name == "" {
			failed++
			continue
		}
		out[ci] = name
		_ = cache.write(nameEntry{Reply: name}, client.Model, system, user)
	}
	fmt.Printf("hole labels: %d named (%d from cache, %d asked, %d unusable) in %s\n",
		len(out), cached, asked, failed, time.Since(t0).Round(time.Millisecond))
	return out
}

// holeLabelMembers bounds what a naming prompt sees. Twelve titles is what
// the taxonomy namer settled on: a namer reads the strongest few and the
// tail only costs tokens.
const holeLabelMembers = 12

// holePrompt renders one chain for naming. The prompt is the CACHE KEY, so
// its wording must not drift casually — a reworded prompt is a cold cache.
func holePrompt(path *report.Path, ci int) (system, user string) {
	c := path.Chains[ci]
	system = "You name a run of YouTube videos that were watched back to back.\n" +
		"Reply with a short noun phrase naming what they have in common — at most six words, " +
		"lowercase, no quotes, no trailing period. Name the SUBJECT, not the act of watching: " +
		"\"berlin drill\", not \"a series of music videos\".\nNo prose."
	var b strings.Builder
	fmt.Fprintf(&b, "area: %s\n", c.Area)
	fmt.Fprintf(&b, "%d videos", c.Len)
	if c.Span > 0 {
		fmt.Fprintf(&b, " over %s", c.Span.Round(time.Minute))
	}
	b.WriteString("\ntitles:\n")
	vs := path.Sessions[c.Session].Views[c.First : c.Last+1]
	shown := 0
	chans := map[string]bool{}
	for _, v := range vs {
		if v.Overlap {
			continue // background is not part of the run being named
		}
		if v.Channel != "" {
			chans[v.Channel] = true
		}
		if shown < holeLabelMembers {
			fmt.Fprintf(&b, "  %s\n", truncateTitle(v.Title, 80))
			shown++
		}
	}
	if len(chans) > 0 {
		names := make([]string, 0, len(chans))
		for ch := range chans {
			names = append(names, ch)
		}
		slices.Sort(names) // deterministic, so the prompt is a stable cache key
		fmt.Fprintf(&b, "channels: %s\n", strings.Join(names, ", "))
	}
	return system, b.String()
}

// truncateTitle keeps a title inside the prompt's budget without cutting a
// UTF-8 sequence in half.
func truncateTitle(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// cleanHoleLabel takes the model's words down to something a row can show:
// one line, no quotes, no trailing punctuation, and short enough that it
// cannot push a table column off the screen. An answer that survives none of
// that is dropped, and the chain keeps its plain name.
func cleanHoleLabel(reply string) string {
	s := strings.TrimSpace(reply)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.Trim(s, " \t\"'`.:;—-")
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	if s == "" || len([]rune(s)) > 48 || len(strings.Fields(s)) > 8 {
		return ""
	}
	return s
}
