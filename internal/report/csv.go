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
func WriteCSV(w io.Writer, rows []classify.Verdict, subscribed map[string]bool) error {
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
