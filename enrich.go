// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
	cookies               *string
}

func addEnrichFlags(fs *flag.FlagSet) enrichFlags {
	return enrichFlags{
		limit:   fs.Int("limit", 0, "fetch at most N videos this run (0 = all missing)"),
		chunk:   fs.Int("chunk", 100, "videos per yt-dlp invocation"),
		workers: fs.Int("workers", 3, "parallel yt-dlp processes (keep low — YouTube rate limits by IP)"),
		// Measured on a 60-video sample: at 1.0 about two thirds of the run
		// is pure waiting (~2.1 sleeping requests per video). 0.25 halved the
		// wall clock with byte-identical metadata and no bot check, and the
		// rate-limit backoff below is what makes it safe to start there.
		sleep: fs.Float64("sleep", 0.25, "seconds yt-dlp sleeps between requests, per worker (raised automatically when YouTube pushes back)"),
		cookies: fs.String("cookies-from-browser", "auto",
			`browser to take YouTube cookies from ("auto" picks the first installed, "" fetches anonymously)`),
	}
}

func (ef enrichFlags) opts() enrichOpts {
	return enrichOpts{
		limit: *ef.limit, chunk: *ef.chunk, workers: *ef.workers,
		sleep: *ef.sleep, cookies: *ef.cookies,
	}
}

// cookieSources are the browsers "auto" considers, best-supported first, with
// the file whose mere existence marks the browser as installed. Only the
// existence is checked here — the database itself is opened by yt-dlp alone.
var cookieSources = []struct{ browser, probe string }{
	{"chrome", "Library/Application Support/Google/Chrome/Default/Cookies"},
	{"brave", "Library/Application Support/BraveSoftware/Brave-Browser/Default/Cookies"},
	{"edge", "Library/Application Support/Microsoft Edge/Default/Cookies"},
	{"firefox", "Library/Application Support/Firefox/Profiles"},
	{"safari", "Library/Containers/com.apple.Safari/Data/Library/Cookies/Cookies.binarycookies"},
	// Linux fallbacks, same order.
	{"chrome", ".config/google-chrome/Default/Cookies"},
	{"brave", ".config/BraveSoftware/Brave-Browser/Default/Cookies"},
	{"firefox", ".mozilla/firefox"},
}

// resolveCookieSource turns the flag value into what yt-dlp gets. Anything
// other than "auto"/"none" is passed through verbatim, so the full
// BROWSER[+KEYRING][:PROFILE] syntax stays available.
func resolveCookieSource(spec string) string {
	if spec == "" || spec == "none" {
		return ""
	}
	if spec != "auto" {
		return spec
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, src := range cookieSources {
		if _, err := os.Stat(filepath.Join(home, src.probe)); err == nil {
			return src.browser
		}
	}
	return ""
}

type enrichOpts struct {
	limit, chunk, workers int
	sleep                 float64
	cookies               string          // -cookies-from-browser, see resolveCookieSource
	stop                  <-chan struct{} // optional: stop feeding new chunks (in-flight ones finish)

	// fetch replaces the yt-dlp call. nil means enrich.FetchChunk; tests set
	// it to drive the worker pool without a network or a binary.
	fetch func(ids []string, o enrich.FetchOpts) (enrich.ChunkResult, error)
	// pauseUnit shortens the rate-limit pause in tests. 0 means the real one.
	pauseUnit time.Duration
}

func (o enrichOpts) fetcher() func([]string, enrich.FetchOpts) (enrich.ChunkResult, error) {
	if o.fetch != nil {
		return o.fetch
	}
	return enrich.FetchChunk
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

type fetchFunc func([]string, enrich.FetchOpts) (enrich.ChunkResult, error)

// maxBackoff caps the rate-limit response at 2^3 = 8x the configured sleep.
// Beyond that the run is not going to finish today anyway and the user is
// better served by stopping and coming back later.
const maxBackoff = 3

// runState is the knowledge the workers build up about YouTube's mood during
// one run: how hard it is currently pushing back, and whether the cookie
// source still works. Both start optimistic and only degrade.
type runState struct {
	mu           sync.Mutex
	backoff      int           // effective sleep = configured sleep * 2^backoff
	cookies      string        // emptied for good once yt-dlp cannot open the source
	cookieWarned bool
	pauseUnit    time.Duration // pause per backoff step; see penalise
}

func (s *runState) snapshot() (backoff int, cookies string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backoff, s.cookies
}

// penalise doubles the request sleep and reports the pause the caller should
// take before pulling the next chunk. ok is false once the cap is reached.
func (s *runState) penalise() (pause time.Duration, level int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backoff < maxBackoff {
		s.backoff++
	}
	// Doubles with every step: long enough for a bot check to age out, short
	// enough that the run still visibly progresses.
	return time.Duration(1<<s.backoff) * s.pauseUnit, s.backoff
}

// recover walks the backoff back down after a clean chunk, so one bad patch
// does not slow the rest of a long run to a crawl.
func (s *runState) recover() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backoff > 0 {
		s.backoff--
	}
}

// dropCookies disables cookies for the rest of the run and reports whether
// this was the first time, so the warning is printed exactly once.
func (s *runState) dropCookies() (first bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	first = !s.cookieWarned
	s.cookieWarned = true
	s.cookies = ""
	return first
}

// fetchOne runs the single-client pass and, only for what it could not
// resolve, a second pass on yt-dlp's default clients. The fallback keeps the
// coverage of the two-client default for the videos that need it, and doubles
// as an in-run retry for transient failures that would otherwise cost the
// user a whole rerun.
//
// Its cost is bounded by the transient-failure rate, which is why correct
// classification matters so much: while members-only videos were misfiled as
// transient, this pass retried a permanent paywall on every chunk and made
// the run measurably SLOWER than no fallback at all.
func fetchOne(fetch fetchFunc, ids []string, sleep float64, st *runState) (enrich.ChunkResult, error) {
	backoff, cookies := st.snapshot()
	sleep *= float64(int(1) << backoff)

	res, err := fetch(ids, enrich.FetchOpts{Sleep: sleep, Client: enrich.FastClient, Cookies: cookies})
	if err != nil {
		return res, err
	}
	if res.CookiesFailed {
		if st.dropCookies() {
			fmt.Fprintln(os.Stderr, "warning: browser cookies unusable — continuing without them (age-restricted videos will be tombstoned)")
		}
		cookies = ""
		res, err = fetch(ids, enrich.FetchOpts{Sleep: sleep, Client: enrich.FastClient})
		if err != nil {
			return res, err
		}
	}
	// Retrying into an active bot check only digs the hole deeper — let the
	// caller back off and leave these for the next run.
	if len(res.Failed) == 0 || res.RateLimited {
		return res, nil
	}

	retry, err := fetch(res.Failed, enrich.FetchOpts{Sleep: sleep, Cookies: cookies})
	if err != nil {
		// The fast pass's results are already good; a broken fallback just
		// means these IDs wait for the next run.
		return res, nil
	}
	merged := enrich.ChunkResult{
		Fetched:     append(res.Fetched, retry.Fetched...),
		Unavailable: append(res.Unavailable, retry.Unavailable...),
		Failed:      retry.Failed,
		RateLimited: retry.RateLimited,
	}
	return merged, nil
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
	cached, err := cache.IDs()
	if err != nil {
		return fmt.Errorf("read meta cache: %w", err)
	}
	missing, uniqueTotal := missingByPriority(views, cached)
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
	fetch := opts.fetcher()
	st := &runState{cookies: resolveCookieSource(opts.cookies), pauseUnit: opts.pauseUnit}
	if st.pauseUnit <= 0 {
		st.pauseUnit = 10 * time.Second
	}
	if st.cookies != "" {
		fmt.Printf("using %s cookies (authenticated — set -cookies-from-browser \"\" to fetch anonymously)\n", st.cookies)
	}
	jobs := make(chan []string)
	var wg sync.WaitGroup
	for w := 0; w < opts.workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for all := range jobs {
				// Once the run is aborting, keep draining instead of
				// returning. A worker that leaves this loop early strands
				// the feeder on an unbuffered send with no receiver left —
				// that hung the whole command whenever every chunk failed
				// (missing yt-dlp binary, dead network).
				mu.Lock()
				aborting := t.err != nil
				mu.Unlock()
				if aborting {
					continue
				}

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
					res, err = fetchOne(fetch, ids, opts.sleep, st)
				}
				mu.Lock()
				if err != nil {
					if t.err == nil {
						t.err = err
					}
					mu.Unlock()
					continue
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
				line := fmt.Sprintf("  %d/%d (fetched %d, gone %d, failed %d) ETA %s",
					t.done, len(missing), t.fetched, t.tombstoned, t.failed, eta)
				mu.Unlock()

				// Adapt to YouTube's pushback outside the lock: a throttled
				// run must look throttled, not hung, so the pause is named
				// in the progress line before it is taken.
				var pause time.Duration
				if res.RateLimited {
					var level int
					pause, level = st.penalise()
					line += fmt.Sprintf(" — rate limited, backoff x%d, pausing %s", 1<<level, pause)
				} else if len(res.Fetched) > 0 {
					st.recover()
				}
				fmt.Println(line)
				if pause > 0 {
					select {
					case <-opts.stop:
					case <-time.After(pause):
					}
				}
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
// -limit test batch covers the largest possible share of actual views. cached
// comes from Cache.IDs(): one directory read instead of an os.Stat per video,
// which matters at tens of thousands of IDs.
func missingByPriority(views []takeout.View, cached map[string]bool) (missing []string, uniqueTotal int) {
	counts := map[string]int{}
	for _, v := range views {
		if v.VideoID != "" {
			counts[v.VideoID]++
		}
	}
	for id := range counts {
		if !cached[id] {
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
