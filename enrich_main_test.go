// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"strings"
	"testing"

	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

func TestMissingByPriority(t *testing.T) {
	views := []takeout.View{
		{VideoID: "aaaaaaaaaa1"},                                                     // 1 view
		{VideoID: "bbbbbbbbbb2"}, {VideoID: "bbbbbbbbbb2"}, {VideoID: "bbbbbbbbbb2"}, // 3 views
		{VideoID: "cccccccccc3"}, {VideoID: "cccccccccc3"}, // 2 views, cached below
		{VideoID: ""}, // no-URL views never enter the fetch list
	}
	cache := enrich.Cache{Dir: t.TempDir()}
	if err := cache.Write(enrich.Meta{ID: "cccccccccc3", Title: "cached"}); err != nil {
		t.Fatal(err)
	}

	missing, uniqueTotal := missingByPriority(views, cache)
	if uniqueTotal != 3 {
		t.Errorf("uniqueTotal = %d, want 3", uniqueTotal)
	}
	if strings.Join(missing, ",") != "bbbbbbbbbb2,aaaaaaaaaa1" {
		t.Errorf("missing = %v, want most-watched first without cached", missing)
	}
}

func TestParseRejectsHTMLExport(t *testing.T) {
	_, _, err := takeout.Parse(strings.NewReader("<!DOCTYPE html><html>…</html>"))
	if err == nil || !strings.Contains(err.Error(), "HTML export") {
		t.Errorf("err = %v, want HTML-export hint", err)
	}
}
