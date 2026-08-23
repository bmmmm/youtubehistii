// SPDX-License-Identifier: GPL-3.0-or-later

package rules

import "strings"

// youtubeCategories are the categories a YouTube uploader can pick in the
// upload form, in YouTube's own order. They are the DEFAULT areas: the
// category is already attached to every enriched video, so taking it as the
// area costs nothing and asks no model. It is the uploader's click, not an
// automatic classification — which is why the two catch-alls below say so in
// their descriptions instead of pretending to be topics.
//
// The list is fixed on purpose. Deriving the areas from whatever categories
// happen to be in the meta cache would move the taxonomy fingerprint every
// time enrich discovers a new one, invalidating every cached verdict.
//
// Descriptions are only ever shown to the LLM, and only for videos that have
// NO category (tombstoned or not yet enriched) — everything else never
// reaches the model's area decision at all.
var youtubeCategories = []Topic{
	{ID: "film-animation", Desc: "film, animation, and the documentaries creators file here"},
	{ID: "autos-vehicles", Desc: "cars, motorcycles, driving, vehicle builds"},
	{ID: "music", Desc: "music videos, live sets, concerts, mixes"},
	{ID: "pets-animals", Desc: "pets, wildlife, animal keeping"},
	{ID: "sports", Desc: "sports broadcasts, highlights, training"},
	{ID: "short-movies", Desc: "short films"},
	{ID: "travel-events", Desc: "travel, trips, events filmed on location"},
	{ID: "gaming", Desc: "video games — gameplay, let's plays, esports, casts"},
	{ID: "videoblogging", Desc: "video blogs"},
	{ID: "people-blogs", Desc: "vlogs and personal channels — one of YouTube's two catch-alls, so expect anything here"},
	{ID: "comedy", Desc: "comedy, sketches, satire"},
	{ID: "entertainment", Desc: "YouTube's other catch-all — shows, celebrity, pop culture, and whatever a creator did not file elsewhere"},
	{ID: "news-politics", Desc: "news, politics, commentary"},
	{ID: "howto-style", Desc: "how-to, DIY, style, product walkthroughs"},
	{ID: "education", Desc: "lessons, courses, explainers"},
	{ID: "science-technology", Desc: "software, engineering, IT security, science"},
	{ID: "nonprofits-activism", Desc: "activism, campaigns, non-profit work"},
	{ID: "shorts", Desc: "short-form vertical videos"},
	{ID: "shows", Desc: "episodic show content"},
	{ID: "trailers", Desc: "trailers and teasers"},
	{ID: "unclear", Desc: "cannot tell from the available metadata"},
}

// YouTubeAreas returns the default areas — a copy, so a caller editing the
// slice cannot reach the package-level list.
func YouTubeAreas() []Topic {
	out := make([]Topic, len(youtubeCategories))
	copy(out, youtubeCategories)
	return out
}

// AreaForCategory maps a YouTube category name as yt-dlp reports it
// ("Science & Technology") to the area id it stands for, and reports whether
// that area exists in THIS config. The check against the configured areas is
// what keeps a hand-written `topics:` list working: with an own taxonomy the
// categories simply stop matching and the LLM decides the area again, rather
// than areas appearing that the prompt never mentioned.
func (c *Config) AreaForCategory(category string) (string, bool) {
	slug := slugify(category, 0)
	if slug == "" {
		return "", false
	}
	return c.canonicalArea(slug)
}

// FirstCategory picks the category YouTube shows: yt-dlp reports a list, and
// the first entry is the one on the watch page.
func FirstCategory(categories []string) string {
	for _, c := range categories {
		if strings.TrimSpace(c) != "" {
			return c
		}
	}
	return ""
}
