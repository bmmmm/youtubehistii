// SPDX-License-Identifier: GPL-3.0-or-later

package rules

import (
	"os"
	"testing"
)

// TestLocalConfigLoads guards the config this machine actually runs with. The
// example proves the format; this proves the real file still parses and still
// covers the categories that showed up in the data. Skipped where there is
// none (CI, a fresh clone) — config/rules.yaml is gitignored.
func TestLocalConfigLoads(t *testing.T) {
	const path = "../../config/rules.yaml"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("no local config/rules.yaml")
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// Every category observed across a full library enrich — read off a real
	// meta cache rather than copied from documentation, which is what makes
	// the list trustworthy. Each one has to resolve to an area, or those
	// videos silently fall back to the LLM for a decision already made.
	for _, c := range []string{
		"Music", "Science & Technology", "Entertainment", "Sports", "People & Blogs",
		"Gaming", "News & Politics", "Education", "Howto & Style", "Film & Animation",
		"Autos & Vehicles", "Comedy", "Travel & Events", "Nonprofits & Activism",
		"Pets & Animals", "Trailers",
	} {
		if area, ok := cfg.AreaForCategory(c); !ok {
			t.Errorf("category %q maps to no area (got %q)", c, area)
		}
	}
}
