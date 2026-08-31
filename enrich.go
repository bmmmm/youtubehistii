// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bmmmm/youtubehistii/internal/counts"
	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

// enrichFlags are the metadata-fetch flags shared by "enrich" and "run".
type enrichFlags struct {
	limit, chunk, workers *int
	sleep                 *float64
	cookies               *string
	retryGone             *string
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
		// Off by default. Measured on this machine: yt-dlp extracted 220
		// cookies from Chrome without trouble, and recovered exactly zero
		// extra videos — the profile carries no YouTube login, so the
		// age-restricted wall stayed up either way. Cookies only pay off for
		// a browser that is actually signed in, and the price is that every
		// request becomes attributable to that account instead of just an IP.
		// So it is opt-in: "auto" picks the first installed browser, or name
		// one directly (the full BROWSER[+KEYRING][:PROFILE] syntax works).
		cookies: fs.String("cookies-from-browser", "",
			`take YouTube cookies from a browser ("auto" picks the first installed); off by default`),
		// A tombstone normally means "never ask again", which is the whole
		// point of writing one. But two of the reasons are only permanent
		// for a caller without the credential, so a run WITH one needs a way
		// to reconsider exactly those — otherwise the tombstone that says
		// "locked" is as final as the one that says "deleted".
		retryGone: fs.String("retry-gone", "",
			`re-ask tombstoned videos: "locked" (age+members), "unknown" (written before reasons existed), "all", or a comma-separated list of reasons`),
	}
}

func (ef enrichFlags) opts() enrichOpts {
	return enrichOpts{
		limit: *ef.limit, chunk: *ef.chunk, workers: *ef.workers,
		sleep: *ef.sleep, cookies: *ef.cookies, retryGone: *ef.retryGone,
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
	retryGone             string          // -retry-gone, see matchesGoneReason
	stop                  <-chan struct{} // optional: stop feeding new chunks (in-flight ones finish)

	// fetch replaces the yt-dlp call. nil means enrich.FetchChunk; tests set
	// it to drive the worker pool without a network or a binary.
	fetch fetchFunc
	// pauseUnit shortens the rate-limit pause in tests. 0 means the real one.
	pauseUnit time.Duration
	// state lets a test seed and then inspect the run's backoff. nil means a
	// fresh one, which is what every real caller gets.
	state *runState
}

func (o enrichOpts) fetcher() fetchFunc {
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
	backoff      int    // effective sleep = configured sleep * 2^backoff
	cookies      string // emptied for good once yt-dlp cannot open the source
	cookieWarned bool
	pauseUnit    time.Duration // pause per backoff step; see penalise
}

func (s *runState) snapshot() (backoff int, cookies string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backoff, s.cookies
}

// penalise doubles the request sleep and reports the pause the caller should
// take before pulling the next chunk, plus the level it reached.
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

// fetchOne fetches one chunk at the run's current pace. Transient failures
// are left for the next run, which is what makes the cache resumable — the
// only in-run retry is for a cookie source yt-dlp could not open, because
// that is a configuration problem the run can fix for itself.
func fetchOne(fetch fetchFunc, ids []string, sleep float64, st *runState) (enrich.ChunkResult, error) {
	backoff, cookies := st.snapshot()
	sleep *= float64(int(1) << backoff)

	res, err := fetch(ids, enrich.FetchOpts{Sleep: sleep, Cookies: cookies})
	if err != nil || !res.CookiesFailed {
		return res, err
	}
	if st.dropCookies() {
		fmt.Fprintln(os.Stderr, "warning: browser cookies unusable — continuing without them (age-restricted videos will be tombstoned)")
	}
	return fetch(ids, enrich.FetchOpts{Sleep: sleep})
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
	// -retry-gone lifts selected tombstones out of the cached set, so the
	// normal "what is missing" path picks them up again. Reading the whole
	// cache costs a directory of small files, so it only happens when the
	// flag is actually set.
	retrying := map[string]bool{}
	if opts.retryGone != "" {
		metas, rerr := cache.ReadAll()
		if rerr != nil {
			return fmt.Errorf("read meta cache for -retry-gone: %w", rerr)
		}
		byReason := map[string]int{}
		for id, m := range metas {
			if !m.Unavailable || !matchesGoneReason(opts.retryGone, m.GoneReason) {
				continue
			}
			delete(cached, id)
			retrying[id] = true
			r := m.GoneReason
			if r == "" {
				r = "unknown"
			}
			byReason[r]++
		}
		if len(retrying) == 0 {
			fmt.Printf("-retry-gone %q matched no tombstone\n", opts.retryGone)
		} else {
			fmt.Printf("-retry-gone %q: reconsidering %d tombstones (%s)\n",
				opts.retryGone, len(retrying), formatCounts(byReason))
		}
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
		// goneBy counts tombstones per reason. Without it the reason exists
		// only inside the cache, and the run that produced it says nothing
		// about what it found — which is the whole question when deciding
		// whether a retry with credentials is worth it.
		goneBy map[string]int
		err                               error
	}
	var (
		mu sync.Mutex
		t  totals
	)
	start := time.Now()
	fetch := opts.fetcher()
	st := opts.state
	if st == nil {
		st = &runState{cookies: resolveCookieSource(opts.cookies)}
	}
	st.pauseUnit = opts.pauseUnit
	if st.pauseUnit <= 0 {
		st.pauseUnit = 10 * time.Second
	}
	if st.cookies != "" {
		fmt.Printf("using %s cookies — requests are authenticated, so this run is attributable to that account\n", st.cookies)
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
					// A retry candidate IS cached — that is the point — so
					// the freshness check must not filter it back out.
					if !cache.Has(id) || retrying[id] {
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
				for _, g := range res.Unavailable {
					if werr := cache.Write(enrich.Meta{ID: g.ID, Unavailable: true, GoneReason: g.Reason}); werr != nil && t.err == nil {
						t.err = werr
					}
				}
				t.fetched += len(res.Fetched)
				t.tombstoned += len(res.Unavailable)
				for _, g := range res.Unavailable {
					if t.goneBy == nil {
						t.goneBy = map[string]int{}
					}
					t.goneBy[g.Reason]++
				}
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
				} else {
					// Anything that came back without pushback counts as
					// clean, including an all-tombstone chunk: it fetched
					// nothing but YouTube answered every ID, so there is no
					// reason to keep the run throttled.
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
	if len(t.goneBy) > 0 {
		fmt.Printf("  gone by reason: %s\n", formatCounts(t.goneBy))
		locked := t.goneBy["age"] + t.goneBy["members"]
		if locked > 0 {
			fmt.Printf("  %d of those are locked, not dead — retry with \"-cookies-from-browser\" and an account that has access\n", locked)
		}
	}
	if t.failed > 0 {
		fmt.Println("failed = transient (network/rate limit) — rerun \"enrich\" to retry just those")
		if t.failed*5 > t.fetched && t.failed > 20 {
			fmt.Fprintln(os.Stderr, "warning: high failure rate — YouTube may be rate limiting this IP; wait a while, raise -sleep or lower -workers")
		}
	}
	return nil
}

// matchesGoneReason reports whether a tombstone's reason is covered by a
// -retry-gone spec. "locked" is the one worth naming: age and members are the
// only reasons a credential can actually lift, so it is the difference
// between re-asking 200 videos and re-asking 3554.
func matchesGoneReason(spec, reason string) bool {
	for _, want := range strings.Split(spec, ",") {
		switch strings.TrimSpace(want) {
		case "":
			continue
		case "all":
			return true
		case "locked":
			if reason == "age" || reason == "members" {
				return true
			}
		case "unknown":
			if reason == "" {
				return true
			}
		default:
			if strings.TrimSpace(want) == reason {
				return true
			}
		}
	}
	return false
}

// formatCounts renders a count map as "age 12 · members 3", biggest first.
func formatCounts(m map[string]int) string {
	keys := counts.Keys(m)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, " · ")
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
