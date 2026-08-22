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
```

Each stage writes plain, inspectable files and can be re-run independently.
`enrich` is resumable — interrupt it any time, it skips what is already cached.

## Getting your data

[takeout.google.com](https://takeout.google.com) → select only *YouTube and
YouTube Music* → under format options choose **JSON** for history (the default
is HTML). Place the resulting `watch-history.json` at `data/watch-history.json`.

The `data/` directory is gitignored: your history never leaves this machine.

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
