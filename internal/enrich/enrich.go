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
	return os.WriteFile(p, append(b, '\n'), 0o644)
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
}

// errLineRe matches yt-dlp error lines, e.g.
// "ERROR: [youtube] dQw4w9WgXcQ: Video unavailable. ..."
var errLineRe = regexp.MustCompile(`ERROR: \[[^\]]+\] ([A-Za-z0-9_-]{6,20}): (.*)`)

// goneMarkers in an error message mean the video will never come back.
var goneMarkers = []string{"unavailable", "private", "removed", "terminated", "not available"}

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

// FetchChunk asks yt-dlp for metadata of the given IDs in one invocation.
func FetchChunk(ids []string, sleepSeconds float64) (ChunkResult, error) {
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

	cmd := exec.Command("yt-dlp",
		"-j", "--ignore-errors", "--no-warnings", "--no-progress",
		"--sleep-requests", fmt.Sprintf("%g", sleepSeconds),
		"-a", batch.Name())
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
	if len(res.Fetched) == 0 && runErr != nil {
		return res, fmt.Errorf("yt-dlp: %w\n%s", runErr, lastLines(stderr.String(), 5))
	}

	var missing []string
	for _, id := range ids {
		if !got[id] {
			missing = append(missing, id)
		}
	}
	res.Unavailable, res.Failed = ClassifyErrors(stderr.String(), missing)
	return res, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
