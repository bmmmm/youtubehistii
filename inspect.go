// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"

	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/inspect"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

// cmdInspect reports what the metadata cache holds — YouTube's categories and
// the creator tags — so the taxonomy can be decided from the data. Read-only:
// no network, no LLM, no writes.
func cmdInspect(args []string) error {
	fs, dataDir := newFlagSet("inspect")
	tagsPerCategory := fs.Int("tags-per-category", 15, "creator tags to list per category")
	showTags := fs.Bool("tags", false, "list creator tags (they often contain channel and personal names)")
	fs.Parse(args)
	p := paths{dataDir: *dataDir}

	views, err := readJSONL[takeout.View](p.historyJSONL())
	if err != nil {
		return fmt.Errorf("read history (run \"import\" first): %w", err)
	}
	metas, err := enrich.Cache{Dir: p.metaCacheDir()}.ReadAll()
	if err != nil {
		return err
	}
	fmt.Print(inspect.Render(inspect.Aggregate(views, metas, *tagsPerCategory), *showTags))
	return nil
}
