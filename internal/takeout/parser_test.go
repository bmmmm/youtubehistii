// SPDX-License-Identifier: GPL-3.0-or-later

package takeout

import (
	"os"
	"testing"
)

func parseFixture(t *testing.T) ([]View, Stats) {
	t.Helper()
	f, err := os.Open("../../testdata/watch-history.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	views, st, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return views, st
}

func TestParseStats(t *testing.T) {
	_, st := parseFixture(t)
	if st.Total != 8 {
		t.Errorf("Total = %d, want 8", st.Total)
	}
	if st.Ads != 1 {
		t.Errorf("Ads = %d, want 1", st.Ads)
	}
	if st.Views != 7 {
		t.Errorf("Views = %d, want 7 (8 entries - 1 ad)", st.Views)
	}
	if st.NoURL != 1 {
		t.Errorf("NoURL = %d, want 1 (deleted video)", st.NoURL)
	}
	if st.Music != 1 {
		t.Errorf("Music = %d, want 1", st.Music)
	}
	if st.BadTime != 1 {
		t.Errorf("BadTime = %d, want 1", st.BadTime)
	}
}

func TestParseTitleCleaning(t *testing.T) {
	views, _ := parseFixture(t)
	byID := map[string]View{}
	for _, v := range views {
		byID[v.VideoID] = v
	}

	// EN prefix "Watched " stripped.
	if got := byID["abc123DEF45"].Title; got != "Rust Base Building Guide 2026" {
		t.Errorf("EN title = %q", got)
	}
	// DE suffix " angesehen" stripped.
	if got := byID["def456GHI78"].Title; got != "GopherCon 2025: Profile-Guided Optimization in Practice" {
		t.Errorf("DE suffix title = %q", got)
	}
	// DE prefix "Angesehen: " stripped.
	if got := byID["pqr678STU90"].Title; got != "Vortrag über eBPF im Kernel" {
		t.Errorf("DE prefix title = %q", got)
	}
}

func TestParseChannel(t *testing.T) {
	views, _ := parseFixture(t)
	for _, v := range views {
		if v.VideoID == "abc123DEF45" && v.Channel != "RustLetsPlayGuy" {
			t.Errorf("channel = %q, want RustLetsPlayGuy", v.Channel)
		}
	}
}

func TestChannelKey(t *testing.T) {
	cases := []struct {
		name string
		view View
		want string
	}{
		{"the id wins where the export has one",
			View{Channel: "Rust Lets Play Guy", ChannelURL: "https://www.youtube.com/channel/UCabc"}, "UCabc"},
		{"the name carries a handle-only or renamed channel",
			View{Channel: "Rust Lets Play Guy"}, "rust lets play guy"},
		{"a handle URL is not an id",
			View{Channel: "Guy", ChannelURL: "https://www.youtube.com/@guy"}, "guy"},
		{"nothing to group by", View{}, ""},
	}
	for _, c := range cases {
		if got := c.view.ChannelKey(); got != c.want {
			t.Errorf("%s: ChannelKey() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestVideoID(t *testing.T) {
	cases := map[string]string{
		"https://www.youtube.com/watch?v=abc123DEF45":       "abc123DEF45",
		"https://youtube.com/watch?v=abc123DEF45&t=120":     "abc123DEF45",
		"https://music.youtube.com/watch?v=mno345PQR67":     "mno345PQR67",
		"https://youtu.be/ghi789JKL01":                      "ghi789JKL01",
		"https://www.youtube.com/shorts/jkl012MNO34":        "jkl012MNO34",
		"https://www.youtube.com/live/xyz987WVU65":          "xyz987WVU65",
		"https://www.youtube.com/embed/emb111EMB22/related": "emb111EMB22",
		"https://www.youtube.com/channel/UCsomething":       "",
		"https://www.youtube.com/post/Ugkxsomething":        "",
		"": "",
	}
	for in, want := range cases {
		if got := VideoID(in); got != want {
			t.Errorf("VideoID(%q) = %q, want %q", in, got, want)
		}
	}
}
