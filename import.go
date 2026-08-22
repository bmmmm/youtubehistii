// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"time"

	"github.com/bmmmm/youtubehistii/internal/takeout"
)

func cmdImport(args []string) error {
	fs, dataDir := newFlagSet("import")
	subsPath := fs.String("subs", "", "subscriptions CSV from Takeout (default: data/subscriptions.csv if present)")
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
	if err := writeJSONL(p.historyJSONL(), views); err != nil {
		return err
	}

	unique := map[string]bool{}
	var oldest, newest time.Time
	for _, v := range views {
		if v.VideoID != "" {
			unique[v.VideoID] = true
		}
		if v.WatchedAt.IsZero() {
			continue
		}
		if oldest.IsZero() || v.WatchedAt.Before(oldest) {
			oldest = v.WatchedAt
		}
		if v.WatchedAt.After(newest) {
			newest = v.WatchedAt
		}
	}

	fmt.Printf("imported %s -> %s\n", in, p.historyJSONL())
	fmt.Printf("  entries:        %d\n", st.Total)
	fmt.Printf("  views kept:     %d (%d unique videos)\n", st.Views, len(unique))
	fmt.Printf("  ads skipped:    %d\n", st.Ads)
	fmt.Printf("  no video URL:   %d (deleted/private, kept but not enrichable)\n", st.NoURL)
	fmt.Printf("  youtube music:  %d\n", st.Music)
	if st.BadTime > 0 {
		fmt.Printf("  bad timestamps: %d\n", st.BadTime)
	}
	if !oldest.IsZero() {
		fmt.Printf("  range:          %s .. %s\n", oldest.Format("2006-01-02"), newest.Format("2006-01-02"))
	}
	return importSubscriptions(p, *subsPath)
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
	h, s := takeout.FindExport(root)
	if h == "" {
		return "", "", fmt.Errorf("no watch-history JSON found under %s — expected watch-history.json or Wiedergabeverlauf.json (Takeout: choose JSON format for history)", root)
	}
	return h, s, nil
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
