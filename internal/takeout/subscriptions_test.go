// SPDX-License-Identifier: GPL-3.0-or-later

package takeout

import (
	"os"
	"strings"
	"testing"
)

func TestParseSubscriptions(t *testing.T) {
	f, err := os.Open("../../testdata/subscriptions.sample.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	subs, err := ParseSubscriptions(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 3 {
		t.Fatalf("got %d subscriptions, want 3", len(subs))
	}
	if subs[0].ChannelID != "UCgopher0000000000000001" || subs[0].Title != "Gopher Academy" {
		t.Errorf("subs[0] = %+v", subs[0])
	}
}

func TestParseSubscriptionsGermanHeader(t *testing.T) {
	csv := "Kanal-ID,Kanal-URL,Kanal-Titel\nUCx,http://youtube.com/channel/UCx,Ein Kanal\n"
	subs, err := ParseSubscriptions(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].ChannelID != "UCx" || subs[0].Title != "Ein Kanal" {
		t.Errorf("subs = %+v", subs)
	}
}

func TestChannelIDFromURL(t *testing.T) {
	cases := map[string]string{
		"https://www.youtube.com/channel/UCabc":       "UCabc",
		"https://www.youtube.com/channel/UCabc/about": "UCabc",
		"https://www.youtube.com/@handle":             "",
		"":                                            "",
	}
	for in, want := range cases {
		if got := ChannelIDFromURL(in); got != want {
			t.Errorf("ChannelIDFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}
