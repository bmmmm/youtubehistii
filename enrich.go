// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

// enrichFlags are the metadata-fetch flags shared by "enrich" and "run".
type enrichFlags struct {
	limit, chunk, workers *int
	sleep                 *float64
}

func addEnrichFlags(fs *flag.FlagSet) enrichFlags {
	return enrichFlags{
		limit:   fs.Int("limit", 0, "fetch at most N videos this run (0 = all missing)"),
		chunk:   fs.Int("chunk", 100, "videos per yt-dlp invocation"),
		workers: fs.Int("workers", 3, "parallel yt-dlp processes (keep low — YouTube rate limits by IP)"),
		sleep:   fs.Float64("sleep", 1.0, "seconds yt-dlp sleeps between requests, per worker (effective rate ≈ workers/sleep)"),
	}
}

func (ef enrichFlags) opts() enrichOpts {
	return enrichOpts{limit: *ef.limit, chunk: *ef.chunk, workers: *ef.workers, sleep: *ef.sleep}
}

type enrichOpts struct {
	limit, chunk, workers int
	sleep                 float64
	stop                  <-chan struct{} // optional: stop feeding new chunks (in-flight ones finish)
}

func cmdEnrich(args []string) error {
	fs, dataDir := newFlagSet("enrich")
	ef := addEnrichFlags(fs)
	fs.Parse(args)
	p := paths{dataDir: *dataDir}

	views, err := readJSONL[takeout.View](p.historyJSONL())
	if err != nil {
		return fmt.Errorf("read history (run \"import\" first): %w", err)
	}
	return enrichAll(p, views, ef.opts())
}

// enrichAll fetches metadata for every missing video. It coexists with
// another enrich running in parallel (the per-ID cache writes are
// independent): each chunk re-checks the cache right before fetching, so the
// worst case is one double fetch per chunk boundary.
func enrichAll(p paths, views []takeout.View, opts enrichOpts) error {
	if opts.workers < 1 {
		opts.workers = 1
	}
	if opts.chunk < 1 {
		opts.chunk = 100
	}
	cache := enrich.Cache{Dir: p.metaCacheDir()}
	missing, uniqueTotal := missingByPriority(views, cache)
	fmt.Printf("%d unique videos, %d cached, %d to fetch\n", uniqueTotal, uniqueTotal-len(missing), len(missing))
	if opts.limit > 0 && len(missing) > opts.limit {
		missing = missing[:opts.limit]
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
	for w := 0; w < opts.workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for all := range jobs {
				// Another enrich may have cached some of these since the
				// missing list was built — skip them, fetch only the rest.
				ids := make([]string, 0, len(all))
				for _, id := range all {
					if !cache.Has(id) {
						ids = append(ids, id)
					}
				}
				var res enrich.ChunkResult
				var err error
				if len(ids) > 0 {
					res, err = enrich.FetchChunk(ids, opts.sleep)
				}
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
				t.done += len(all)
				elapsed := time.Since(start)
				eta := time.Duration(float64(elapsed) / float64(t.done) * float64(len(missing)-t.done)).Round(time.Second)
				fmt.Printf("  %d/%d (fetched %d, gone %d, failed %d) ETA %s\n",
					t.done, len(missing), t.fetched, t.tombstoned, t.failed, eta)
				mu.Unlock()
			}
		}()
	}
feed:
	for off := 0; off < len(missing); off += opts.chunk {
		mu.Lock()
		abort := t.err != nil
		mu.Unlock()
		if abort {
			break
		}
		ids := missing[off:min(off+opts.chunk, len(missing))]
		if opts.stop != nil {
			select {
			case <-opts.stop:
				break feed
			case jobs <- ids:
			}
		} else {
			jobs <- ids
		}
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
