// SPDX-License-Identifier: GPL-3.0-or-later
//
// youtubehistii analyzes a Google Takeout export of your YouTube watch
// history entirely on-device: parse, enrich with yt-dlp metadata, classify
// by topic and mode (consume vs. learn), and render a local report.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
)

var version = "dev"

func resolveVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

func usage() {
	fmt.Fprint(os.Stderr, `youtubehistii — analyze your YouTube watch history locally

Usage:
  youtubehistii import [path]     parse Takeout JSON -> data/history.jsonl
                                  (default path: data/watch-history.json)
  youtubehistii enrich            fetch video metadata via yt-dlp -> data/cache/meta/
  youtubehistii classify          rules first, then local LLM (oMLX) -> data/classified.jsonl
  youtubehistii report            render data/out/report.html + report.csv + terminal summary
  youtubehistii version           print version

Global flag (each subcommand): -data <dir>  data directory (default "data")

Everything stays on this machine. The only network access is "enrich",
which talks to YouTube itself via yt-dlp and caches results locally.
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "import":
		err = cmdImport(args)
	case "enrich":
		err = cmdEnrich(args)
	case "classify":
		err = cmdClassify(args)
	case "report":
		err = cmdReport(args)
	case "version", "--version", "-v":
		fmt.Println(resolveVersion())
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
