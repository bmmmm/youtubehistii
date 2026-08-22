// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

func cmdEnrich(args []string) error {
	fs, dataDir := newFlagSet("enrich")
	limit := fs.Int("limit", 0, "fetch at most N videos this run (0 = all missing)")
	chunk := fs.Int("chunk", 100, "videos per yt-dlp invocation")
	workers := fs.Int("workers", 3, "parallel yt-dlp processes (keep low — YouTube rate limits by IP)")
	sleep := fs.Float64("sleep", 1.0, "seconds yt-dlp sleeps between requests, per worker (effective rate ≈ workers/sleep)")
	fs.Parse(args)
	p := paths{dataDir: *dataDir}
	if *workers < 1 {
		*workers = 1
	}

	views, err := readJSONL[takeout.View](p.historyJSONL())
	if err != nil {
		return fmt.Errorf("read history (run \"import\" first): %w", err)
	}

	cache := enrich.Cache{Dir: p.metaCacheDir()}
	missing, uniqueTotal := missingByPriority(views, cache)
	fmt.Printf("%d unique videos, %d cached, %d to fetch\n", uniqueTotal, uniqueTotal-len(missing), len(missing))
	if *limit > 0 && len(missing) > *limit {
		missing = missing[:*limit]
		fmt.Printf("limiting this run to the %d most-watched missing videos\n", len(missing))
	}
	if len(missing) == 0 {
		fmt.Println("nothing to do")
		return nil
	}

	// Chunks are independent and the cache is resumable, so workers just pull
	// chunks off a channel; the first error stops the intake.
	type totals struct {
		fetched, tombstoned, failed, done int
		err                               error
	}
	var (
		mu sync.Mutex
		t  totals
	)
	start := time.Now()
	jobs := make(chan []string)
	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ids := range jobs {
				res, err := enrich.FetchChunk(ids, *sleep)
				mu.Lock()
				if err != nil {
					if t.err == nil {
						t.err = err
					}
					mu.Unlock()
					return
				}
				for _, m := range res.Fetched {
					if werr := cache.Write(m); werr != nil && t.err == nil {
						t.err = werr
					}
				}
				for _, id := range res.Unavailable {
					if werr := cache.Write(enrich.Meta{ID: id, Unavailable: true}); werr != nil && t.err == nil {
						t.err = werr
					}
				}
				t.fetched += len(res.Fetched)
				t.tombstoned += len(res.Unavailable)
				t.failed += len(res.Failed)
				t.done += len(ids)
				elapsed := time.Since(start)
				eta := time.Duration(float64(elapsed) / float64(t.done) * float64(len(missing)-t.done)).Round(time.Second)
				fmt.Printf("  %d/%d (fetched %d, gone %d, failed %d) ETA %s\n",
					t.done, len(missing), t.fetched, t.tombstoned, t.failed, eta)
				mu.Unlock()
			}
		}()
	}
	for off := 0; off < len(missing); off += *chunk {
		mu.Lock()
		stop := t.err != nil
		mu.Unlock()
		if stop {
			break
		}
		jobs <- missing[off:min(off+*chunk, len(missing))]
	}
	close(jobs)
	wg.Wait()

	if t.err != nil {
		return fmt.Errorf("after %d fetched (progress is cached, rerun to resume): %w", t.fetched, t.err)
	}
	fmt.Printf("done: %d fetched, %d gone for good (tombstoned, kept in the report), %d failed\n",
		t.fetched, t.tombstoned, t.failed)
	if t.failed > 0 {
		fmt.Println("failed = transient (network/rate limit) — rerun \"enrich\" to retry just those")
		if t.failed*5 > t.fetched && t.failed > 20 {
			fmt.Fprintln(os.Stderr, "warning: high failure rate — YouTube may be rate limiting this IP; wait a while, raise -sleep or lower -workers")
		}
	}
	return nil
}

// missingByPriority returns uncached video IDs, most-watched first — so a
// -limit test batch covers the largest possible share of actual views.
func missingByPriority(views []takeout.View, cache enrich.Cache) (missing []string, uniqueTotal int) {
	counts := map[string]int{}
	for _, v := range views {
		if v.VideoID != "" {
			counts[v.VideoID]++
		}
	}
	for id := range counts {
		if !cache.Has(id) {
			missing = append(missing, id)
		}
	}
	sort.Slice(missing, func(i, j int) bool {
		ci, cj := counts[missing[i]], counts[missing[j]]
		if ci != cj {
			return ci > cj
		}
		return missing[i] < missing[j]
	})
	return missing, len(counts)
}
