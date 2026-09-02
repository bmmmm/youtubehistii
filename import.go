// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bmmmm/youtubehistii/internal/takeout"
)

func cmdImport(args []string) error {
	fs, dataDir := newFlagSet("import")
	subsPath := fs.String("subs", "", "subscriptions CSV from Takeout (default: data/subscriptions.csv if present)")
	force := fs.Bool("force", false, "import even when the export carries less history than the file it replaces")
	fs.Parse(args)
	p := paths{dataDir: *dataDir}

	in, foundSubs, err := resolveExport(p, fs.Arg(0))
	if err != nil {
		return err
	}
	if *subsPath == "" {
		*subsPath = foundSubs
	}
	f, err := os.Open(in)
	if err != nil {
		return fmt.Errorf("open export: %w", err)
	}
	defer f.Close()

	views, st, err := takeout.Parse(f)
	if err != nil {
		return err
	}

	// Compared BEFORE the write: writeJSONL renames a temp file over the old
	// one, and once that rename has happened there is nothing left to compare
	// against — or to keep.
	fresh := historyStats(views)
	delta, err := compareWithExisting(p, fresh, *force)
	if err != nil {
		return err
	}
	if err := writeJSONL(p.historyJSONL(), views); err != nil {
		return err
	}

	fmt.Printf("imported %s -> %s\n", in, p.historyJSONL())
	fmt.Printf("  entries:        %d\n", st.Total)
	fmt.Printf("  views kept:     %d (%d unique videos)\n", st.Views, fresh.unique)
	fmt.Printf("  ads skipped:    %d\n", st.Ads)
	fmt.Printf("  no video URL:   %d (deleted/private, kept but not enrichable)\n", st.NoURL)
	fmt.Printf("  youtube music:  %d\n", st.Music)
	if st.BadTime > 0 {
		fmt.Printf("  bad timestamps: %d\n", st.BadTime)
	}
	if !fresh.oldest.IsZero() {
		fmt.Printf("  range:          %s\n", fresh.span())
	}
	if delta != "" {
		fmt.Println(delta)
	}
	return importSubscriptions(p, *subsPath)
}

// historySummary is what one history file amounts to, in the four numbers two
// imports can be held against each other by.
type historySummary struct {
	views  int
	unique int
	oldest time.Time
	newest time.Time
}

// historyStats reduces parsed views to that summary. Views without a
// timestamp (Stats.BadTime) carry no date and are counted but never dated —
// letting a zero time win the "oldest" comparison would make every export
// look like it reached back to year 1.
func historyStats(views []takeout.View) historySummary {
	s := historySummary{views: len(views)}
	unique := map[string]bool{}
	for _, v := range views {
		if v.VideoID != "" {
			unique[v.VideoID] = true
		}
		if v.WatchedAt.IsZero() {
			continue
		}
		if s.oldest.IsZero() || v.WatchedAt.Before(s.oldest) {
			s.oldest = v.WatchedAt
		}
		if v.WatchedAt.After(s.newest) {
			s.newest = v.WatchedAt
		}
	}
	s.unique = len(unique)
	return s
}

func (s historySummary) span() string {
	if s.oldest.IsZero() {
		return "no dated views"
	}
	return fmt.Sprintf("%s .. %s", s.oldest.Format("2006-01-02"), s.newest.Format("2006-01-02"))
}

// lostAgainst names what the export on the way in no longer has, or "" when
// nothing was lost.
//
// The second signal is the one that matters. Google deletes watch history 3,
// 18 or 36 months after the fact, depending on the account's auto-delete
// setting, and it deletes it from the OLD end: a fresh export of a truncated
// account still ends today and simply starts nine years later. Comparing
// NEWEST against newest sees none of that — both say "today" — which is why
// it is not the check here; the one case it would catch, re-importing a stale
// export, is a strict prefix of what is on disk and so already refused by the
// view count.
func (s historySummary) lostAgainst(fresh historySummary) string {
	switch {
	case fresh.views < s.views:
		return fmt.Sprintf("%d views instead of %d", fresh.views, s.views)
	case s.oldest.IsZero() || fresh.oldest.IsZero():
		return "" // nothing datable on one of the two sides
	case fresh.oldest.After(s.oldest):
		return fmt.Sprintf("it starts at %s, the file on disk at %s",
			fresh.oldest.Format("2006-01-02"), s.oldest.Format("2006-01-02"))
	}
	return ""
}

// compareWithExisting weighs the parsed export against the history file it is
// about to replace, and returns the one line the import prints about the
// difference — or an error that stops the write.
//
// Nine years of history are one `import` away from being three months of it,
// and the old command said nothing at all: it wrote whatever it had parsed.
func compareWithExisting(p paths, fresh historySummary, force bool) (string, error) {
	previous, err := readJSONL[takeout.View](p.historyJSONL())
	switch {
	case os.IsNotExist(err):
		return "", nil // first import — nothing to compare against
	case err != nil:
		// A corrupt or half-written history must not block a good export:
		// re-importing is the way OUT of that state, and refusing here would
		// close it.
		fmt.Fprintf(os.Stderr, "note: cannot read %s to compare against (%v) — importing without the comparison\n",
			p.historyJSONL(), err)
		return "", nil
	}

	prev := historyStats(previous)
	if reason := prev.lostAgainst(fresh); reason != "" && !force {
		return "", fmt.Errorf("%s", shrinkRefusal(p.historyJSONL(), prev, fresh, reason))
	}
	if fresh.views == prev.views && fresh.unique == prev.unique {
		return "since last: unchanged", nil
	}
	// "+3 unique videos", not "+3 new videos": under -force the numbers go
	// negative, and "-25289 new videos" is not a sentence.
	return fmt.Sprintf("since last: %+d views, %+d unique videos (was %s)",
		fresh.views-prev.views, fresh.unique-prev.unique, prev.span()), nil
}

// shrinkRefusal says what would have been lost, and why it can legitimately
// happen — the auto-delete setting is the reason a real export shrinks, and
// somebody who knows that about their own account needs one flag, not a
// debate with the tool.
func shrinkRefusal(path string, prev, fresh historySummary, reason string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "refusing to overwrite %s: the export carries less history — %s", path, reason)
	fmt.Fprintf(&b, "\n  on disk: %d views, %d videos, %s", prev.views, prev.unique, prev.span())
	fmt.Fprintf(&b, "\n  export:  %d views, %d videos, %s", fresh.views, fresh.unique, fresh.span())
	b.WriteString("\n  YouTube deletes watch history by itself after 3, 18 or 36 months, depending on the account's auto-delete setting, so a fresh export reaches back only that far")
	b.WriteString("\n  if that is what happened and the shorter export is the one you want: youtubehistii import -force <path>")
	return b.String()
}

// resolveExport turns the import argument into a watch-history file path.
// A directory argument — or none at all — is searched for known Takeout
// layouts (e.g. "YouTube und YouTube Music/Verlauf/Wiedergabeverlauf.json"),
// which also surfaces the subscriptions CSV when present.
func resolveExport(p paths, arg string) (historyPath, subsPath string, err error) {
	root := arg
	if root == "" {
		root = p.dataDir
	}
	st, statErr := os.Stat(root)
	switch {
	case statErr == nil && !st.IsDir():
		return root, "", nil // explicit file argument
	case statErr != nil && arg != "":
		return "", "", fmt.Errorf("open export: %w", statErr)
	case statErr != nil:
		return "", "", fmt.Errorf("no data directory at %s — place your Takeout export there (see README)", root)
	}
	h, s, html, ignored := takeout.FindExport(root)
	if h == "" {
		return "", "", fmt.Errorf("%s", noExportMessage(root, html))
	}
	// Naming the losers is the whole point of collecting them: the pick is by
	// mtime, and "which of my two Takeout folders did it just read" is not a
	// question the import should leave open.
	for _, path := range ignored {
		fmt.Fprintf(os.Stderr, "note: ignoring %s — a second Takeout export is unpacked here; %s is newer\n", path, h)
	}
	return h, s, nil
}

// noExportMessage explains what is actually in the data directory and what
// was expected, instead of a bare "not found".
func noExportMessage(root, htmlPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "no watch-history JSON found under %s", root)
	if htmlPath != "" {
		fmt.Fprintf(&b, "\n  found %s — that is Takeout's HTML export, which cannot be parsed.", htmlPath)
		b.WriteString("\n  re-export at takeout.google.com and switch the history format from HTML to JSON")
		return b.String()
	}
	entries, err := os.ReadDir(root)
	switch {
	case err != nil || len(entries) == 0:
		b.WriteString("\n  the directory is empty — copy your Takeout folder (e.g. \"YouTube und YouTube Music\") into it")
	default:
		b.WriteString("\n  it contains: ")
		for i, e := range entries {
			if i > 0 {
				b.WriteString(", ")
			}
			if i == 8 {
				fmt.Fprintf(&b, "… (%d more)", len(entries)-i)
				break
			}
			b.WriteString(e.Name())
			if e.IsDir() {
				b.WriteString("/")
			}
		}
		b.WriteString("\n  expected somewhere inside: Wiedergabeverlauf.json (DE) or watch-history.json (EN)")
	}
	b.WriteString("\n  (a different path can be passed directly: youtubehistii import <file-or-dir>)")
	return b.String()
}

// importSubscriptions is optional: without the CSV the report simply skips
// the subscription sections.
func importSubscriptions(p paths, path string) error {
	explicit := path != ""
	if !explicit {
		path = p.subscriptionsCSV()
	}
	f, err := os.Open(path)
	if err != nil {
		if !explicit && os.IsNotExist(err) {
			fmt.Printf("no subscriptions CSV at %s — skipping (export \"subscriptions\" in Takeout to include it)\n", path)
			return nil
		}
		return fmt.Errorf("open subscriptions: %w", err)
	}
	defer f.Close()
	subs, err := takeout.ParseSubscriptions(f)
	if err != nil {
		return err
	}
	if err := writeJSONL(p.subscriptionsJSONL(), subs); err != nil {
		return err
	}
	fmt.Printf("imported %s -> %s (%d subscriptions)\n", path, p.subscriptionsJSONL(), len(subs))
	return nil
}
