// SPDX-License-Identifier: GPL-3.0-or-later

package report

import (
	"fmt"
	"strings"
)

// RenderText is the compact terminal summary of the same numbers. With
// showNames=false every channel/subscription name is omitted — aggregates
// and topic slugs only, safe to share or paste.
func RenderText(st *Stats, showNames bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d views, %d unique videos, ≤ %.0f h (upper bound), %s … %s\n",
		st.Views, st.UniqueVideos, st.HoursUpper, fmtDate(st.From), fmtDate(st.To))
	fmt.Fprintf(&b, "classified: %d via rules, %d via llm, %d open\n\n",
		st.Sources["rule"], st.Sources["llm"], st.Sources["unclassified"])

	maxViews := 1
	if len(st.Topics) > 0 {
		maxViews = st.Topics[0].Views
	}
	b.WriteString("topics (by views):\n")
	for i, t := range st.Topics {
		if i >= 12 {
			fmt.Fprintf(&b, "  … %d more\n", len(st.Topics)-i)
			break
		}
		bar := strings.Repeat("█", 1+t.Views*24/maxViews)
		fmt.Fprintf(&b, "  %-18s %-8s %5d views  ≤%7.1f h  %s\n", t.Topic, t.Mode, t.Views, t.Hours, bar)
	}

	modeViews := map[string]int{}
	for _, m := range st.Months {
		for mode, v := range m.ModeViews {
			modeViews[mode] += v
		}
	}
	b.WriteString("\nmode split: ")
	for _, mode := range ModeOrder {
		if modeViews[mode] > 0 {
			fmt.Fprintf(&b, "%s %.0f%%  ", mode, float64(modeViews[mode])/float64(st.Views)*100)
		}
	}
	b.WriteString("\n")

	if st.HasSubs {
		fmt.Fprintf(&b, "\nsubscriptions: %d total, %d never watched; %.0f%% of views / %.0f%% of hours are subscribed channels\n",
			len(st.Subs), st.DeadSubs,
			pctI(st.SubbedViews, st.Views), pctF(st.SubbedHours, st.HoursUpper))
		for i, s := range st.Subs {
			if !showNames || i >= 5 || s.Views == 0 {
				break
			}
			fmt.Fprintf(&b, "  %-30s %-16s %4d views  ≤%6.1f h\n", clip(s.Title, 30), s.TopTopic, s.Views, s.Hours)
		}
	}

	if len(st.UnclearNames) > 0 && showNames {
		fmt.Fprintf(&b, "\nunclear — top channels to add rules for: %s\n",
			strings.Join(st.UnclearNames[:min(5, len(st.UnclearNames))], ", "))
	}
	return b.String()
}

func fmtDate(t interface{ Format(string) string }) string { return t.Format("2006-01-02") }

func pctI(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

func pctF(part, total float64) float64 {
	if total == 0 {
		return 0
	}
	return part / total * 100
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
