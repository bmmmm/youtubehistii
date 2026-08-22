// SPDX-License-Identifier: GPL-3.0-or-later

package takeout

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"
)

// Subscription is one subscribed channel from the Takeout subscriptions CSV.
type Subscription struct {
	ChannelID  string `json:"channelID"`
	ChannelURL string `json:"channelURL,omitempty"`
	Title      string `json:"title"`
}

// ParseSubscriptions reads the Takeout subscriptions CSV. Header names are
// locale-dependent ("Channel Id" / "Kanal-ID", …), so columns are resolved by
// keyword with a positional fallback (id, url, title).
func ParseSubscriptions(r io.Reader) ([]Subscription, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse subscriptions CSV: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("subscriptions CSV is empty")
	}

	idCol, urlCol, titleCol := 0, 1, 2
	header := rows[0]
	for i, h := range header {
		hl := strings.ToLower(h)
		switch {
		case strings.Contains(hl, "id"):
			idCol = i
		case strings.Contains(hl, "url"):
			urlCol = i
		case strings.Contains(hl, "title") || strings.Contains(hl, "titel"):
			titleCol = i
		}
	}

	var subs []Subscription
	for _, row := range rows[1:] {
		if len(row) <= idCol || strings.TrimSpace(row[idCol]) == "" {
			continue
		}
		s := Subscription{ChannelID: strings.TrimSpace(row[idCol])}
		if len(row) > urlCol {
			s.ChannelURL = strings.TrimSpace(row[urlCol])
		}
		if len(row) > titleCol {
			s.Title = strings.TrimSpace(row[titleCol])
		}
		subs = append(subs, s)
	}
	return subs, nil
}

// ChannelIDFromURL extracts the UC… channel ID from a channel URL, or "".
func ChannelIDFromURL(rawURL string) string {
	if rest, ok := strings.CutPrefix(rawURL, "https://www.youtube.com/channel/"); ok {
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			rest = rest[:i]
		}
		return rest
	}
	return ""
}
