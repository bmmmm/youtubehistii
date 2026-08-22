// SPDX-License-Identifier: GPL-3.0-or-later

// Package takeout parses the Google Takeout watch-history JSON export.
package takeout

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// rawEntry mirrors one element of the Takeout watch-history array. Only the
// fields we consume are declared; everything else is ignored on purpose.
type rawEntry struct {
	Header    string `json:"header"`
	Title     string `json:"title"`
	TitleURL  string `json:"titleUrl"`
	Subtitles []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"subtitles"`
	Details []struct {
		Name string `json:"name"`
	} `json:"details"`
	Time     string   `json:"time"`
	Products []string `json:"products"`
}

// View is one normalized watch event. VideoID is empty when the entry has no
// titleUrl (deleted/private video) — such views are kept, not dropped.
type View struct {
	VideoID    string    `json:"videoID"`
	Title      string    `json:"title"`
	Channel    string    `json:"channel,omitempty"`
	ChannelURL string    `json:"channelURL,omitempty"`
	WatchedAt  time.Time `json:"watchedAt"`
}

// Stats counts what happened during parsing, so nothing disappears silently.
type Stats struct {
	Total   int // entries in the export
	Views   int // normalized views kept (incl. NoURL)
	Ads     int // ad entries skipped
	NoURL   int // kept views without a video ID (deleted/private)
	Music   int // kept views that came from YouTube Music
	BadTime int // kept views whose timestamp failed to parse (zero time)
}

// adMarkers identify sponsored entries; locale-dependent (EN + DE observed
// values, verify against a real German export on first run).
var adMarkers = []string{"From Google Ads", "Von Google Anzeigen"}

// titlePrefixes/titleSuffixes wrap the video title depending on export locale.
var (
	titlePrefixes = []string{"Watched ", "Angesehen: "}
	titleSuffixes = []string{" angesehen"}
)

// Parse reads a Takeout watch-history JSON array and returns normalized views.
func Parse(r io.Reader) ([]View, Stats, error) {
	var entries []rawEntry
	dec := json.NewDecoder(r)
	if err := dec.Decode(&entries); err != nil {
		return nil, Stats{}, fmt.Errorf("decode takeout JSON: %w", err)
	}

	var views []View
	var st Stats
	st.Total = len(entries)
	for _, e := range entries {
		if isAd(e) {
			st.Ads++
			continue
		}
		v := View{
			VideoID: VideoID(e.TitleURL),
			Title:   cleanTitle(e.Title),
		}
		if len(e.Subtitles) > 0 {
			v.Channel = e.Subtitles[0].Name
			v.ChannelURL = e.Subtitles[0].URL
		}
		if t, err := time.Parse(time.RFC3339, e.Time); err == nil {
			v.WatchedAt = t
		} else {
			st.BadTime++
		}
		if v.VideoID == "" {
			st.NoURL++
		}
		if isMusic(e) {
			st.Music++
		}
		views = append(views, v)
	}
	st.Views = len(views)
	return views, st, nil
}

func isAd(e rawEntry) bool {
	for _, d := range e.Details {
		for _, m := range adMarkers {
			if d.Name == m {
				return true
			}
		}
	}
	return false
}

func isMusic(e rawEntry) bool {
	for _, p := range e.Products {
		if p == "YouTube Music" {
			return true
		}
	}
	return strings.Contains(e.TitleURL, "music.youtube.com")
}

func cleanTitle(title string) string {
	for _, p := range titlePrefixes {
		if strings.HasPrefix(title, p) {
			return strings.TrimPrefix(title, p)
		}
	}
	for _, s := range titleSuffixes {
		if strings.HasSuffix(title, s) {
			return strings.TrimSuffix(title, s)
		}
	}
	return title
}

// VideoID extracts the 11-char YouTube video ID from a watch URL. Returns ""
// when the URL is absent or not a video link (channel pages, posts, …).
func VideoID(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.TrimPrefix(u.Hostname(), "www.")
	switch {
	case host == "youtu.be":
		return strings.Trim(u.Path, "/")
	case strings.HasSuffix(host, "youtube.com"):
		if id := u.Query().Get("v"); id != "" {
			return id
		}
		for _, prefix := range []string{"/shorts/", "/live/", "/embed/"} {
			if rest, ok := strings.CutPrefix(u.Path, prefix); ok {
				if i := strings.IndexByte(rest, '/'); i >= 0 {
					rest = rest[:i]
				}
				return rest
			}
		}
	}
	return ""
}
