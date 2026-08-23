// SPDX-License-Identifier: GPL-3.0-or-later

// Package enrich fetches per-video metadata (tags, category, duration) via
// yt-dlp and maintains the local cache — the only stage that touches the
// network, and it talks to YouTube only.
package enrich

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Meta is the pruned per-video metadata we keep — not the full yt-dlp dump.
type Meta struct {
	ID          string   `json:"id"`
	Title       string   `json:"title,omitempty"`
	Channel     string   `json:"channel,omitempty"`
	ChannelID   string   `json:"channel_id,omitempty"`
	Duration    int      `json:"duration,omitempty"` // seconds
	Categories  []string `json:"categories,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	UploadDate  string   `json:"upload_date,omitempty"`
	Unavailable bool     `json:"unavailable,omitempty"` // tombstone: video gone, don't retry
}

// rawMeta tolerates yt-dlp's field types (duration may be a float).
type rawMeta struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Channel    string   `json:"channel"`
	ChannelID  string   `json:"channel_id"`
	Duration   float64  `json:"duration"`
	Categories []string `json:"categories"`
	Tags       []string `json:"tags"`
	UploadDate string   `json:"upload_date"`
}

// Prune reduces one yt-dlp JSON document to the fields we keep.
func Prune(raw []byte) (Meta, error) {
	var r rawMeta
	if err := json.Unmarshal(raw, &r); err != nil {
		return Meta{}, err
	}
	if r.ID == "" {
		return Meta{}, fmt.Errorf("yt-dlp record without id")
	}
	return Meta{
		ID:         r.ID,
		Title:      r.Title,
		Channel:    r.Channel,
		ChannelID:  r.ChannelID,
		Duration:   int(r.Duration),
		Categories: r.Categories,
		Tags:       r.Tags,
		UploadDate: r.UploadDate,
	}, nil
}

var videoIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,20}$`)

// Cache stores one pruned JSON file per video ID.
type Cache struct{ Dir string }

func (c Cache) path(id string) (string, error) {
	if !videoIDRe.MatchString(id) {
		return "", fmt.Errorf("refusing suspicious video id %q", id)
	}
	return filepath.Join(c.Dir, id+".json"), nil
}

func (c Cache) Has(id string) bool {
	p, err := c.path(id)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// Write stores one entry atomically (temp file + rename). A plain write would
// leave truncated JSON behind on a crash, and that is worse than no entry at
// all: Has() would report the video as cached so it never gets refetched, and
// ReadAll would fail the WHOLE cache on the one unparseable file.
func (c Cache) Write(m Meta) error {
	p, err := c.path(m.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.Dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// IDs returns every cached video ID from a single directory read. Callers that
// need to test thousands of IDs use this instead of Has, which costs one
// os.Stat each.
func (c Cache) IDs() (map[string]bool, error) {
	out := map[string]bool{}
	entries, err := os.ReadDir(c.Dir)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		out[strings.TrimSuffix(name, ".json")] = true
	}
	return out, nil
}

// ReadAll loads the whole cache into memory (a few KB per video).
func (c Cache) ReadAll() (map[string]Meta, error) {
	out := map[string]Meta{}
	entries, err := os.ReadDir(c.Dir)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(c.Dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var m Meta
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		out[m.ID] = m
	}
	return out, nil
}

// ChunkResult reports what one yt-dlp invocation produced.
type ChunkResult struct {
	Fetched     []Meta
	Unavailable []string // gone for good -> tombstone
	Failed      []string // transient (network, rate limit) -> retry next run

	// RateLimited means YouTube pushed back on the request rate rather than
	// on any individual video. The caller should slow down instead of
	// burning through the remaining chunks failing every one of them.
	RateLimited bool
	// CookiesFailed means --cookies-from-browser could not be honoured at
	// all (locked profile, denied keychain, unknown browser). The caller
	// should retry this chunk without cookies and stop passing them.
	CookiesFailed bool
}

// errLineRe matches yt-dlp error lines, e.g.
// "ERROR: [youtube] dQw4w9WgXcQ: Video unavailable. ..."
var errLineRe = regexp.MustCompile(`ERROR: \[[^\]]+\] ([A-Za-z0-9_-]{6,20}): (.*)`)

// goneMarkers in an error message mean the video will never come back.
// "confirm your age" is deliberately narrow — it only matches the
// age-verification wall (permanent without --cookies), never the separate
// "confirm you're not a bot" message, which signals IP-level rate limiting
// and is transient (retry later, don't tombstone).
// "members" catches the members-only wall ("available to this channel's
// members on level: ...", "Join this channel to get access to members-only
// content"). Like age restriction it is permanent for anyone without the
// credential, and it used to be misfiled as transient — so every run retried
// the same paywalled videos forever, at full request cost.
var goneMarkers = []string{
	"unavailable", "private", "removed", "terminated", "not available",
	"confirm your age", "members",
}

// rateLimitMarkers mean YouTube is throttling this IP or account — nothing is
// wrong with the video, we are simply going too fast. Matched on "not a bot"
// rather than the full sentence because yt-dlp emits it with a typographic
// apostrophe ("you’re"), which is easy to miss with an ASCII literal.
var rateLimitMarkers = []string{"not a bot", "too many requests", "http error 429"}

// cookieErrorMarkers appear on plain ERROR lines (no per-video prefix) when
// --cookies-from-browser cannot be honoured at all.
var cookieErrorMarkers = []string{
	"cookies database", "cookie database", "unable to decrypt", "failed to decrypt",
	"unsupported browser", "could not find", "no such profile",
}

// isRateLimit reports whether a lowercased yt-dlp message is rate limiting.
func isRateLimit(msg string) bool {
	for _, marker := range rateLimitMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// cookiesUnusable reports whether stderr shows yt-dlp failing on the cookie
// source itself. Deliberately scoped to lines that also mention cookies, so a
// generic "could not find" about something else never disables cookies.
func cookiesUnusable(stderr string) bool {
	for _, line := range strings.Split(strings.ToLower(stderr), "\n") {
		if !strings.Contains(line, "cookie") {
			continue
		}
		for _, marker := range cookieErrorMarkers {
			if strings.Contains(line, marker) {
				return true
			}
		}
	}
	return false
}

// ClassifyErrors splits the IDs missing from stdout into gone vs. transient,
// based on yt-dlp's stderr.
func ClassifyErrors(stderr string, missing []string) (gone, failed []string) {
	reasons := map[string]string{}
	for _, m := range errLineRe.FindAllStringSubmatch(stderr, -1) {
		reasons[m[1]] = strings.ToLower(m[2])
	}
	for _, id := range missing {
		msg, ok := reasons[id]
		if !ok {
			failed = append(failed, id)
			continue
		}
		isGone := false
		for _, marker := range goneMarkers {
			if strings.Contains(msg, marker) {
				isGone = true
				break
			}
		}
		if isGone {
			gone = append(gone, id)
		} else {
			failed = append(failed, id)
		}
	}
	return gone, failed
}

// ytDLPBin is the binary FetchChunk shells out to. A variable so tests can
// point it at a fake that reproduces yt-dlp's stdout/stderr/exit-code shape
// without touching the network.
var ytDLPBin = "yt-dlp"

// FetchOpts tunes one yt-dlp invocation.
//
// There is deliberately no player_client knob. Pinning a single client was
// tried and measured: on a realistic 20-video chunk it made no difference to
// wall clock (65.0 s versus 65.8 s), because the run is bound by
// --sleep-requests and network latency, not by client count. It halved yt-dlp
// CPU, but buying that meant a second fallback pass to cover the videos a
// single client misses — complexity out of proportion to a saving nobody
// waits on. yt-dlp's own client selection stays.
type FetchOpts struct {
	// Sleep is passed to --sleep-requests: seconds between HTTP requests.
	Sleep float64
	// Cookies is passed to --cookies-from-browser. Empty means no cookies.
	Cookies string
}

// FetchChunk asks yt-dlp for metadata of the given IDs in one invocation.
func FetchChunk(ids []string, opts FetchOpts) (ChunkResult, error) {
	batch, err := os.CreateTemp("", "youtubehistii-batch-*.txt")
	if err != nil {
		return ChunkResult{}, err
	}
	defer os.Remove(batch.Name())
	for _, id := range ids {
		if !videoIDRe.MatchString(id) {
			return ChunkResult{}, fmt.Errorf("refusing suspicious video id %q", id)
		}
		fmt.Fprintf(batch, "https://www.youtube.com/watch?v=%s\n", id)
	}
	if err := batch.Close(); err != nil {
		return ChunkResult{}, err
	}

	args := []string{
		"-j", "--ignore-errors", "--no-warnings", "--no-progress",
		"--sleep-requests", fmt.Sprintf("%g", opts.Sleep),
	}
	if opts.Cookies != "" {
		args = append(args, "--cookies-from-browser", opts.Cookies)
	}
	args = append(args, "-a", batch.Name())

	cmd := exec.Command(ytDLPBin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// --ignore-errors still yields a non-zero exit when any video failed;
	// per-ID triage below decides what that means, so the error itself is
	// only fatal when yt-dlp produced nothing at all.
	runErr := cmd.Run()

	var res ChunkResult
	got := map[string]bool{}
	dec := json.NewDecoder(&stdout)
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return res, fmt.Errorf("parse yt-dlp output: %w", err)
		}
		m, err := Prune(raw)
		if err != nil {
			return res, err
		}
		res.Fetched = append(res.Fetched, m)
		got[m.ID] = true
	}
	var missing []string
	for _, id := range ids {
		if !got[id] {
			missing = append(missing, id)
		}
	}
	// Fatal only when yt-dlp explained NONE of the missing IDs — that means
	// it never got to per-video processing at all (binary missing, auth
	// wall, network down for the whole run). A chunk that just happens to be
	// all-private/all-deleted still gets a per-ID reason for every ID and
	// must not kill the rest of a long run — that's what ClassifyErrors below
	// is for.
	errText := stderr.String()

	// A cookie source yt-dlp cannot open is a configuration problem, not a
	// fetch failure: the caller recovers by retrying the chunk without
	// cookies. So it must neither be fatal nor tombstone anything.
	if opts.Cookies != "" && cookiesUnusable(errText) {
		res.Failed = missing
		res.CookiesFailed = true
		return res, nil
	}

	if len(res.Fetched) == 0 && runErr != nil && !errLineRe.MatchString(errText) {
		return ChunkResult{}, fmt.Errorf("yt-dlp: %w\n%s", runErr, lastLines(errText, 5))
	}

	res.Unavailable, res.Failed = ClassifyErrors(errText, missing)
	res.RateLimited = isRateLimit(strings.ToLower(errText))
	return res, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
