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

// commands is the dispatch table AND the list usage_main_test.go walks. A
// command added here without a `youtubehistii <name>` block in usageText
// fails the test, which is the only thing that has ever kept the two in step.
var commands = map[string]func([]string) error{
	"import":    cmdImport,
	"enrich":    cmdEnrich,
	"classify":  cmdClassify,
	"run":       cmdRun,
	"inspect":   cmdInspect,
	"taxonomy":  cmdTaxonomy,
	"abtest":    cmdABTest,
	"report":    cmdReport,
	"watchpath": cmdWatchPath,
}

// usageText is a const so a test can cut it into per-command segments and
// hold each one against the flags that command actually registers.
const usageText = `youtubehistii — analyze your YouTube watch history locally

Usage:
  youtubehistii import [path]     parse Takeout JSON -> data/history.jsonl
                                  (default path: data/watch-history.json;
                                  also picks up data/subscriptions.csv, or -subs <csv>)
  youtubehistii enrich            fetch video metadata via yt-dlp -> data/cache/meta/
                                  (flags: -limit N, -workers 3, -sleep 0.25, -chunk 100,
                                  -cookies-from-browser <browser> for age-gated videos,
                                  -retry-gone <spec> to reconsider tombstoned ids;
                                  most-watched videos are fetched first, safe to interrupt)
  youtubehistii classify          rules first, then local LLM (oMLX) -> data/classified.jsonl
                                  (flags: -rules <file>, -llm-batch 10, -llm-workers 1,
                                  -llm-limit N, -no-llm, -include-unenriched,
                                  -keep-verdicts, -retry <defect> to re-ask only the
                                  videos that came back incomplete; only enriched or
                                  tombstoned videos are sent to the LLM.
                                  Topics have two levels: the AREA is YouTube's own
                                  category, taken straight from the metadata without
                                  asking a model, and the SUB is free — the LLM picks
                                  it per video, along with the mode. Videos without a
                                  category keep their area open to the LLM too)
  youtubehistii run               enrich and classify together: metadata fetching runs
                                  continuously, classification catches up in waves
                                  (every 60s or 200 new videos), final report at the
                                  end — Ctrl-C safe, rerun to resume. Takes the union
                                  of the enrich, classify and watchpath flags:
                                  -limit N, -workers 3, -sleep 0.25, -chunk 100,
                                  -cookies-from-browser <browser>, -retry-gone <spec>,
                                  -rules <file>, -llm-batch 10, -llm-workers 1,
                                  -llm-limit N, -no-llm, -keep-verdicts, -retry <defect>,
                                  -include-unenriched (applied to the final sweep
                                  only, when enrich has had its whole run),
                                  -taxonomy, -label-holes N
  youtubehistii inspect           what the metadata cache holds: youtube category
                                  distribution and creator tags, to decide the
                                  taxonomy from the data (flags: -tags,
                                  -tags-per-category 15, -channels; read-only, no LLM)
  youtubehistii taxonomy          derive a data-driven taxonomy from the classified corpus:
                                  embed every observed area/sub label (with channel/tag
                                  context), cluster at two thresholds, name the clusters,
                                  refine in rounds -> config/taxonomy.yaml. Verdicts stay
                                  untouched; report/watchpath apply it on read via
                                  -taxonomy. Steer a running loop through
                                  config/taxonomy-control.yaml, watch it in
                                  data/out/taxonomy-run.jsonl. Flags: -rules <file>,
                                  -embed-model bge-m3-mlx-fp16, -fine 0.70, -coarse 0.85,
                                  -min-videos 3, -min-top-videos 25, -max-radius 0.50,
                                  -rounds 10, -tail-n 1, -center=false (skip
                                  mean-centering), -no-llm, -name-batch 1 (raise to name
                                  several subjects per request: 2.4x faster on a cold
                                  run, but it changes the names — for threshold
                                  calibration, not for a taxonomy you mean to keep),
                                  -probe (measure server latency, change nothing)
  youtubehistii abtest            would ANOTHER model on the server classify better? A
                                  verdict's cache key carries the taxonomy fingerprint,
                                  not the model, so switching models invalidates nothing
                                  and silently mixes two judges into one corpus — the
                                  consistent way out is re-asking ~28k videos, five hours.
                                  This asks both models the byte-identical production
                                  prompt on a deterministic sample and, with -judge, lets
                                  a third model decide the disagreements. Writes nothing.
                                  Flags: -model <candidate> (required), -baseline <model>,
                                  -judge <model>, -n 200, -batch 20, -rules <file>
  youtubehistii report            render data/out/report.csv + terminal summary; the same
                                  numbers are a view of the watchpath page, at #/report
                                  (flags: -taxonomy to project topics through
                                  config/taxonomy.yaml, -no-names for a run that names
                                  no channel)
  youtubehistii watchpath         render data/out/watchpath.html: the same views along the
                                  time axis — sittings split by a 30 min gap, newest first,
                                  with what the gap to the next start suggests about each
                                  video. Takeout logs only when a video was STARTED, so
                                  every label there is a reading of that gap, never a fact
                                  (flags: -rules <file>, -taxonomy, -label-holes N to
                                  list the N biggest unlabelled gaps)
  youtubehistii version           print version

Global flag (each subcommand): -data <dir>  data directory (default "data")

Everything stays on this machine. The only network access is "enrich",
which talks to YouTube itself via yt-dlp and caches results locally.
`

func usage() { fmt.Fprint(os.Stderr, usageText) }

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "version", "--version", "-v":
		fmt.Println(resolveVersion())
		return
	case "help", "--help", "-h":
		usage()
		return
	}
	run, ok := commands[cmd]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err := run(args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
