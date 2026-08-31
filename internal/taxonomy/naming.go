// SPDX-License-Identifier: GPL-3.0-or-later

package taxonomy

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bmmmm/youtubehistii/internal/rules"
)

// KindStats counts one altitude's ("subject" or "top") naming outcomes over
// a run: answers the disk cache served, clusters it did not cover, how many
// chat requests those cost (fewer than the clusters, since a request names
// up to nameBatch of them), how many names fell back to the cluster's
// strongest member, and the wall-clock time spent inside ChatMax.
//
// misses and requests are separate on purpose: misses is the work the cache
// could not save, requests is what that work cost the server, and the ratio
// between them is the whole point of batching. Fields are only ever touched
// through atomic.AddInt64 — see NamingStats.
type KindStats struct {
	Hits, Misses, Requests, Fallbacks int64
	ReqNanos                          int64 // sum of time.Since around each ChatMax call
}

// NamingStats is the answer to "how many of the 86s run and its 380ms/warm-
// request estimate are real requests, and how many the cache already
// covers" — a question nobody could answer before this existed. One
// KindStats per altitude, because "subject" and "top" go through the same
// closure but at very different counts (a real corpus names ~770 subjects
// and a couple dozen tops).
//
// Atomic counters rather than a mutex: newNamer's closure is called
// serially today, once per cluster, but making it concurrent is the next
// planned change to this run — and atomics cost nothing extra to have
// right, whereas a mutex added after the fact is easy to forget on one of
// the four fields.
type NamingStats struct {
	Subject, Top KindStats
}

func (s *NamingStats) ForKind(kind string) *KindStats {
	if kind == "top" {
		return &s.Top
	}
	return &s.Subject
}

// Detail is what log.event("naming", ...) writes: one nested object per
// altitude, in the same plain-map style as the "embed" and "write" events.
func (s *NamingStats) Detail() map[string]any {
	kindDetail := func(k KindStats) map[string]any {
		return map[string]any{
			"cached": k.Hits, "uncached": k.Misses, "requests": k.Requests,
			"fallback": k.Fallbacks, "request_ms": time.Duration(k.ReqNanos).Milliseconds(),
		}
	}
	return map[string]any{"subject": kindDetail(s.Subject), "top": kindDetail(s.Top)}
}

// Line renders the same counts for the terminal, in metricsLine's
// pipe-separated style.
func (s *NamingStats) Line() string {
	part := func(kind string, k KindStats) string {
		avg := time.Duration(0)
		if k.Requests > 0 {
			avg = time.Duration(k.ReqNanos / k.Requests)
		}
		return fmt.Sprintf("%s %d cached %d uncached in %d req %d fallback (%s, %s/req)",
			kind, k.Hits, k.Misses, k.Requests, k.Fallbacks,
			time.Duration(k.ReqNanos).Round(time.Millisecond), avg.Round(time.Millisecond))
	}
	return part("subject", s.Subject) + " | " + part("top", s.Top)
}

// nameAltitude is the one sentence that separates the two naming jobs.
func nameAltitude(kind string) string {
	if kind == "top" {
		return "the broad top-level category the members share, like a site section"
	}
	return "the cluster's one shared subject, as specific as the members allow"
}

// writeClusterBody renders the members and channels a namer reads. prefix is
// "" for the single-cluster prompt and an indent for the batch, where several
// clusters sit under numbers.
//
// Twelve members at most: a namer reads the strongest ones and the tail only
// costs tokens.
func writeClusterBody(b *strings.Builder, c Cluster, prefix string) {
	fmt.Fprintf(b, "%smembers:\n", prefix)
	for i, l := range c.Members {
		if i == 12 {
			fmt.Fprintf(b, "%s  … and %d more\n", prefix, len(c.Members)-i)
			break
		}
		fmt.Fprintf(b, "%s  %s (%d views)\n", prefix, l.Topic(), l.Views)
	}
	if ch := c.TopChannels(5); len(ch) > 0 {
		fmt.Fprintf(b, "%schannels: %s\n", prefix, strings.Join(ch, ", "))
	}
}

// NameSinglePrompt is the one-cluster prompt — and, just as importantly, the
// CACHE KEY for that cluster's name whether the answer arrived alone or
// inside a batch. Keying on the single prompt is what lets batching change
// how names are fetched without invalidating a single cached name: the
// wording here must not drift, or every existing entry goes cold at once.
func NameSinglePrompt(c Cluster, kind string) (system, user string) {
	system = "You name clusters of YouTube watch-history topic labels.\n" +
		"Reply with EXACTLY one short lowercase slug (a-z, 0-9, dashes; at most three words " +
		"joined by dashes) naming " + nameAltitude(kind) + ".\n" +
		"Prefer reusing a member label's name when it already covers the whole cluster. No prose."
	var u strings.Builder
	writeClusterBody(&u, c, "")
	return system, u.String()
}

// NameBatchPrompt renders one prompt for several clusters. Same shape as
// classification's batch prompt, and for the same reason: one LINE per
// cluster keyed by its NUMBER, never by its name. A name is exactly the
// thing the model is being asked to invent, so it cannot also be the key —
// and 1..N is copyable, which the high-entropy alternatives are not.
func NameBatchPrompt(cs []Cluster, kind string) (system, user string) {
	var b strings.Builder
	b.WriteString("You name clusters of YouTube watch-history topic labels.\n")
	fmt.Fprintf(&b, "You get %d numbered clusters. Reply with EXACTLY one line per cluster, in the same order:\n", len(cs))
	b.WriteString("<n> <slug>\n")
	fmt.Fprintf(&b, "<slug> is ONE short lowercase slug (a-z, 0-9, dashes; at most three words joined by dashes) naming %s.\n",
		nameAltitude(kind))
	b.WriteString("Prefer reusing a member label's name when it already covers the whole cluster.\n")
	b.WriteString("No prose, no code fences, no JSON, no blank lines.\n")
	b.WriteString("Example: 2 indie-rock")

	var u strings.Builder
	for i, c := range cs {
		fmt.Fprintf(&u, "%d.\n", i+1)
		writeClusterBody(&u, c, "   ")
	}
	return b.String(), u.String()
}

// nameLineRe matches the reply key "1" / "2." / "3)" plus the slug after it.
var nameLineRe = regexp.MustCompile(`^(\d+)[.):]?\s+(\S.*)$`)

// ParseNameBatch maps a batch reply back onto the clusters that were asked
// about; the name on line n belongs to cs[n-1]. STRICT, exactly like
// ParseBatchVerdicts: every number 1..n must appear once with a slug that
// survives slugging, nothing may name a number outside the range, and any
// violation is an error the caller answers with single requests. A name that
// landed on the wrong cluster is invisible afterwards — it just reads as a
// badly named subject — so the mapping is verified, never guessed.
func ParseNameBatch(reply string, n int) ([]string, error) {
	out := make([]string, n)
	for _, line := range strings.Split(reply, "\n") {
		m := nameLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue // prose, fences, blank lines — completeness is checked below
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil || idx < 1 || idx > n {
			return nil, fmt.Errorf("name for line %s of %d", m[1], n)
		}
		if out[idx-1] != "" {
			return nil, fmt.Errorf("duplicate name for line %d", idx)
		}
		if rules.SlugifySub(m[2]) == "" {
			return nil, fmt.Errorf("line %d: %q is not a usable slug", idx, m[2])
		}
		// The RAW answer is what comes back, not the slug — the single path
		// caches the model's words and slugs them afterwards, and a batched
		// name has to be stored the same way or the two disagree on rerun.
		out[idx-1] = m[2]
	}
	for i, s := range out {
		if s == "" {
			return nil, fmt.Errorf("reply misses a name for line %d of %d", i+1, n)
		}
	}
	return out, nil
}
