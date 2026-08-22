// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"sort"
	"time"

	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

func cmdEnrich(args []string) error {
	fs, dataDir := newFlagSet("enrich")
	limit := fs.Int("limit", 0, "fetch at most N videos this run (0 = all missing)")
	chunk := fs.Int("chunk", 100, "videos per yt-dlp invocation")
	sleep := fs.Float64("sleep", 0.5, "seconds yt-dlp sleeps between requests")
	fs.Parse(args)
	p := paths{dataDir: *dataDir}

	views, err := readJSONL[takeout.View](p.historyJSONL())
	if err != nil {
		return fmt.Errorf("read history (run \"import\" first): %w", err)
	}

	cache := enrich.Cache{Dir: p.metaCacheDir()}
	uniq := map[string]bool{}
	for _, v := range views {
		if v.VideoID != "" {
			uniq[v.VideoID] = true
		}
	}
	var missing []string
	for id := range uniq {
		if !cache.Has(id) {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	fmt.Printf("%d unique videos, %d cached, %d to fetch\n", len(uniq), len(uniq)-len(missing), len(missing))
	if *limit > 0 && len(missing) > *limit {
		missing = missing[:*limit]
		fmt.Printf("limiting this run to %d\n", len(missing))
	}
	if len(missing) == 0 {
		fmt.Println("nothing to do")
		return nil
	}

	var fetched, tombstoned, failed int
	start := time.Now()
	for off := 0; off < len(missing); off += *chunk {
		ids := missing[off:min(off+*chunk, len(missing))]
		res, err := enrich.FetchChunk(ids, *sleep)
		if err != nil {
			return fmt.Errorf("after %d fetched (progress is cached, rerun to resume): %w", fetched, err)
		}
		for _, m := range res.Fetched {
			if err := cache.Write(m); err != nil {
				return err
			}
			fetched++
		}
		for _, id := range res.Unavailable {
			if err := cache.Write(enrich.Meta{ID: id, Unavailable: true}); err != nil {
				return err
			}
			tombstoned++
		}
		failed += len(res.Failed)

		done := off + len(ids)
		elapsed := time.Since(start)
		eta := time.Duration(float64(elapsed) / float64(done) * float64(len(missing)-done)).Round(time.Second)
		fmt.Printf("  %d/%d (fetched %d, gone %d, failed %d) ETA %s\n",
			done, len(missing), fetched, tombstoned, failed, eta)
	}

	fmt.Printf("done: %d fetched, %d tombstoned, %d failed (transient — rerun \"enrich\" to retry)\n",
		fetched, tombstoned, failed)
	return nil
}
