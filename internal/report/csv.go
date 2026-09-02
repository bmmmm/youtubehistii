// SPDX-License-Identifier: GPL-3.0-or-later

package report

import (
	"encoding/csv"
	"fmt"
	"io"
	"time"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/rules"
)

// WriteCSV exports every classified view as one flat row.
//
// provenance identifies the taxonomy the topics were folded through, and goes
// out as a leading "# " comment. Without it the file says nothing about which
// projection produced its topics: 103 of 189 topics in this repo's own CSV did
// not match the page rendered beside it, and neither file admitted why. Both
// pandas (comment="#") and `grep -v '^#'` skip the line, so nothing that reads
// the CSV today has to learn about it. An empty provenance writes "none" —
// there is no such thing as a CSV that declines to say.
func WriteCSV(w io.Writer, rows []classify.Verdict, subscribed map[string]bool, provenance string) error {
	if provenance == "" {
		provenance = "none"
	}
	if _, err := fmt.Fprintf(w, "# taxonomy: %s\n", provenance); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	// "topic" is the full "<area>/<sub>"; "area" repeats just the fixed level
	// so a pivot can group on it without splitting strings.
	header := []string{"videoID", "title", "channel", "channelID", "watchedAt",
		"topic", "area", "mode", "source", "confidence", "durationS", "subscribed", "unavailable"}
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, r := range rows {
		area, _ := rules.SplitTopic(r.Topic)
		rec := []string{
			r.VideoID, r.Title, r.Channel, r.ChannelID,
			fmtTime(r.WatchedAt),
			r.Topic, area, r.Mode, r.Source,
			fmtConf(r.Confidence),
			fmt.Sprintf("%d", r.DurationS),
			fmt.Sprintf("%t", r.ChannelID != "" && subscribed[r.ChannelID]),
			fmt.Sprintf("%t", r.Unavailable),
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func fmtConf(c float64) string {
	if c == 0 {
		return ""
	}
	return fmt.Sprintf("%.2f", c)
}
