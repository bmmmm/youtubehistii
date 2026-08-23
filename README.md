# youtubehistii

Analyze your YouTube watch history **entirely on-device**: which topics does
your time actually go to — pure consumption (let's plays, esports) or learning
(conference talks, tutorials)?

Input is the Google Takeout export of your watch history. No cloud, no
third-party service: the only network access is the optional `enrich` stage,
which asks YouTube itself (via `yt-dlp`) for per-video metadata — tags,
category, duration — and caches it locally. Classification runs as
deterministic rules first, with a **local** LLM (an [oMLX](https://github.com/jundot/omlx)
server on `127.0.0.1`) as fallback. Every verdict records *why* it was made.

## Pipeline

```
youtubehistii import   data/watch-history.json    → data/history.jsonl
youtubehistii enrich                              → data/cache/meta/<videoID>.json
youtubehistii classify                            → data/classified.jsonl
youtubehistii report                              → data/out/report.html + report.csv

youtubehistii run      all of enrich + classify + report in one go, overlapped
```

Each stage writes plain, inspectable files and can be re-run independently.
`enrich` is resumable — interrupt it any time, it skips what is already cached.
It fetches most-watched videos first (so a `-limit` test batch covers the
largest share of your actual views) and runs a small worker pool
(`-workers 3 -sleep 0.25` by default). YouTube rate-limits by IP: when it
starts pushing back, enrich doubles its request sleep and pauses instead of
burning the rest of the run marking everything failed, then eases back off
once chunks come through cleanly. The progress line says so when it happens,
so a throttled run does not look like a hung one.

Videos that are gone for good are tombstoned — skipped on retries but kept and
counted in the report. That includes the ones no anonymous request will ever
reach: deleted, private, age-restricted and members-only.

Enrich fetches anonymously. `-cookies-from-browser auto` (or a browser name,
with the full `BROWSER[+KEYRING][:PROFILE]` syntax) hands your browser cookies
to yt-dlp, which can recover age-restricted videos — but only if that browser
is actually signed in to YouTube, and at the price of making every request
attributable to that Google account instead of just your IP. On a browser that
is merely installed it buys nothing: cookies extract fine and the age wall
stays up. Hence off by default. If a cookie source cannot be opened, enrich
warns once and carries on without it rather than failing the run.

### run: the wave pipeline

A full enrich over tens of thousands of videos takes hours; `run` hides the
LLM classification entirely behind it. Enrich runs continuously while the
classifier catches up in *waves* — every 60 seconds, or as soon as 200 new
videos have metadata — and each wave prints one line:

```
wave 4: +512 classified (llm 213, rules 299), 18432 waiting
```

The LLM is only asked about videos whose metadata has arrived (verdict basis
"full") or that are tombstoned (title-only is the best they will ever get);
everything else waits for enrich instead of wasting a title-only guess that
would need re-asking later. Videos are sent in batches of `-llm-batch` (10 by
default) as one prompt with one compact verdict line per video — about half
the output tokens of JSON, which roughly doubles throughput on a local
server. A reply that does not account for exactly the requested videos is
discarded and retried as single requests; mappings are verified, never
guessed. `-llm-workers` sends batches concurrently (only worth raising if
your server actually decodes requests in parallel — measure first).

`run` is Ctrl-C safe and resumable: all progress lives in the per-video
caches, and a restart picks up where it stopped. An oMLX outage only skips
the current wave — enrich keeps running and the next wave retries. When
enrich finishes, a final pass classifies the rest and the report renders
(with the `-no-names` terminal summary). For a long unattended run on macOS:
`caffeinate -is ./youtubehistii run`.

## Getting your data

[takeout.google.com](https://takeout.google.com) → select only *YouTube and
YouTube Music* → under format options choose **JSON** for history (the default
is HTML), and include **subscriptions** in the content selection. Place the
files at `data/watch-history.json` and `data/subscriptions.csv`.

The `data/` directory is gitignored: your history never leaves this machine.

## Subscriptions

With the subscriptions CSV in place, the report links your subscriptions to
your actual watching: each subscription gets the dominant topic of its watched
videos, dead subscriptions (never watched in the export) are called out, and
the share of views/hours spent on subscribed vs. unsubscribed channels is
shown. Top-channel tables mark subscribed channels.

## Requirements

- Go ≥ 1.26 (build), `yt-dlp` (enrich stage)
- an oMLX server for LLM classification (optional — without it, `classify`
  runs rules-only and says so)

## Configuration

Copy `config/rules.example.yaml` to `config/rules.yaml` and adapt the
taxonomy and rules — the file documents the format. `OMLX_URL` / `OMLX_API_KEY`
environment variables override the LLM endpoint settings.

## Honesty note on "time spent"

The Takeout export has no per-view watch duration. Duration-weighted numbers
in the report use the full video length and are labeled as an upper bound
("up to X hours"); view counts are shown alongside as the exact metric.

## License

GPL-3.0-or-later — see [LICENSE](LICENSE).

## Support

If this is useful to you: [ko-fi.com/bmabma](https://ko-fi.com/bmabma)
