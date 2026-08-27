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
                                  (default path: data/watch-history.json;
                                  also picks up data/subscriptions.csv, or -subs <csv>)
  youtubehistii enrich            fetch video metadata via yt-dlp -> data/cache/meta/
                                  (flags: -limit N, -workers 3, -sleep 1.0, -chunk 100;
                                  most-watched videos are fetched first, safe to interrupt)
  youtubehistii classify          rules first, then local LLM (oMLX) -> data/classified.jsonl
                                  (flags: -llm-batch 10, -llm-workers 1, -llm-limit N,
                                  -no-llm, -include-unenriched, -keep-verdicts; only
                                  enriched or tombstoned videos are sent to the LLM.
                                  Topics have two levels: the AREA is YouTube's own
                                  category, taken straight from the metadata without
                                  asking a model, and the SUB is free — the LLM picks
                                  it per video, along with the mode. Videos without a
                                  category keep their area open to the LLM too)
  youtubehistii run               enrich and classify together: metadata fetching runs
                                  continuously, classification catches up in waves
                                  (every 60s or 200 new videos), final report at the
                                  end — takes both commands' flags, Ctrl-C safe,
                                  rerun to resume
  youtubehistii inspect           what the metadata cache holds: youtube category
                                  distribution and creator tags, to decide the
                                  taxonomy from the data (flags: -tags,
                                  -tags-per-category 15; read-only, no LLM)
  youtubehistii taxonomy          derive a data-driven taxonomy from the classified corpus:
                                  embed every observed area/sub label (with channel/tag
                                  context), cluster at two thresholds, name the clusters,
                                  refine in rounds -> config/taxonomy.yaml. Verdicts stay
                                  untouched; report/watchpath apply it on read via
                                  -taxonomy. Steer a running loop through
                                  config/taxonomy-control.yaml, watch it in
                                  data/out/taxonomy-run.jsonl. Flags: -embed-model
                                  bge-m3-mlx-fp16, -fine 0.70, -coarse 0.85, -min-videos 3,
                                  -rounds 5, -center=false (skip mean-centering), -no-llm,
                                  -probe (measure server latency, change nothing)
  youtubehistii report            render data/out/report.html + report.csv + terminal summary
                                  (-taxonomy: project topics through config/taxonomy.yaml)
  youtubehistii watchpath         render data/out/watchpath.html: the same views along the
                                  time axis — sittings split by a 30 min gap, newest first,
                                  with what the gap to the next start suggests about each
                                  video. Takeout logs only when a video was STARTED, so
                                  every label there is a reading of that gap, never a fact
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
	case "run":
		err = cmdRun(args)
	case "inspect":
		err = cmdInspect(args)
	case "taxonomy":
		err = cmdTaxonomy(args)
	case "report":
		err = cmdReport(args)
	case "watchpath":
		err = cmdWatchPath(args)
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
