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
youtubehistii taxonomy                            → config/taxonomy.yaml (optional)
youtubehistii report                              → data/out/report.csv + terminal summary
youtubehistii watchpath                           → data/out/watchpath.html (incl. #/report)
youtubehistii watchpath -label-holes 150           name the deepest rabbit holes (cached)

youtubehistii run      all of enrich + classify + report + page in one go, overlapped
                       (takes -taxonomy and -label-holes, same as the stages above)
youtubehistii inspect  what the metadata cache holds — category distribution and
                       creator tags, to decide the taxonomy from the data
                       (read-only, never asks a model)
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

Videos that are gone are tombstoned — skipped on later runs but kept and
counted in the report. Each tombstone records **why**, because "gone" covers
two different things: `private`, `removed`, `terminated` and `unavailable` are
gone for everyone, while `age` and `members` are gone only for a caller
without the credential. The run prints the split:

```
done: 2 fetched, 3499 gone for good (tombstoned, kept in the report), 7 failed
  gone by reason: private 2109 · unavailable 676 · age 340 · terminated 168 · removed 156 · members 50
```

`-retry-gone` reopens tombstones that a credential might lift: `locked`
(age + members), `unknown` (written before reasons were recorded), `all`, or a
comma-separated list of reasons. Without it a tombstone is final, which is the
point of writing one.

Enrich fetches anonymously. `-cookies-from-browser auto` (or a browser name,
with the full `BROWSER[+KEYRING][:PROFILE]` syntax) hands your browser cookies
to yt-dlp, which can recover age-restricted videos — but only if that browser
is actually signed in to YouTube, and at the price of making every request
attributable to that Google account instead of just your IP. On a browser that
is merely installed it buys nothing: cookies extract fine and the age wall
stays up — measured twice, 220 cookies out of Chrome and a 200-video sample
with Firefox cookies active, both recovering exactly zero extra videos. A
cookie jar is not an account. Hence off by default. If a cookie source cannot be opened, enrich
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
enrich finishes, a final pass classifies the rest, the terminal summary
prints (`-no-names`) and the page is written — `run` ends where `watchpath`
would. It takes `-taxonomy` and `-label-holes N` and hands both to that last
step, so one command covers the whole pipeline. For a long unattended run on
macOS: `caffeinate -is ./youtubehistii run`.

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
  runs rules-only and says so). `taxonomy` additionally wants an *embedding*
  model on the same server (`bge-m3-mlx-fp16` by default, multilingual so
  that `chess` and `schach` meet); `taxonomy -probe` says whether both models
  are there and what they cost, without changing anything.

## Configuration

Copy `config/rules.example.yaml` to `config/rules.yaml` and adapt the rules —
the file documents the format. Out of the box there is no taxonomy to write:
the areas are YouTube's own categories. `OMLX_URL` / `OMLX_API_KEY`
environment variables override the LLM endpoint settings.

### Topics have two levels, from two different places

A topic is `<area>` or `<area>/<sub>`, and the two levels do not come from the
same source:

- **areas** are YouTube's own categories, slugified (`science-technology`,
  `news-politics`, `gaming`, …). Every enriched video already carries one —
  the uploader picked it in the upload form — so the area costs neither a rule
  nor a model call. It also holds when the LLM is off or unreachable; such rows
  are reported as *area-only from the category*.
- **subs** are free. The model invents one per video — `gaming/factorio`
  appears without anyone configuring it — and rules may name one directly
  (`topic: gaming/cs2`). Each request seeds the model with the subs already in
  use, so the vocabulary converges instead of fanning out.

That leaves the LLM exactly the two questions no metadata answers: which
specific subject, and consume vs. learn. It picks an area only for videos that
have no category at all — deleted, age-restricted or members-only ones, and
anything `enrich` has not reached yet.

Two of YouTube's categories, **Entertainment** and **People & Blogs**, are
catch-alls: uploaders pick them when nothing fits, so they hold anything. They
stay as areas rather than being second-guessed at classification time — the
free sub level is what makes them readable.

Rules are worth writing for two things now: contradicting YouTube's category,
and pinning a subject's spelling. Restating a category (`Gaming` → `gaming`) is
default behaviour. Note that a rule ends the classification — it sets topic and
mode and the video never reaches the LLM, so a video it catches gets no sub
beyond what the rule names and no mode judged from its content.

You can replace the areas wholesale by uncommenting `topics:`. It is a
replacement, not an addition: with an own taxonomy the YouTube categories stop
mapping and the LLM decides every area again, so `category_any` rules become
the way to place them.

Every cached verdict records a fingerprint of the areas it was judged under.
**Editing `topics:` therefore re-classifies every cached verdict** — the run
says so before it starts (`taxonomy changed (… → …): re-asking N cached
verdicts`). Use `-keep-verdicts` when you only reworded a `desc` and do not
want to pay for a full re-ask. Enriched metadata is never affected.

If the model spells one subject two ways, fold them with `sub_aliases:` — those
are applied when verdicts are read back, so folding costs no re-classification.

### Re-asking a verdict by its defect

`-retry` re-asks cached verdicts matched by what is WRONG with them, never by
age — the fingerprint is untouched, so nothing that already answers cleanly is
re-asked, and each defect is asked at most once per video:

```
youtubehistii classify -retry no-sub  -llm-batch 20   # an area with no subject
youtubehistii classify -retry no-mode -llm-batch 40   # a topic with no mode
youtubehistii classify -retry unclear -llm-batch 10   # only the ones with usable text
```

Each round has its own prompt and patches only its own field: a mode round
leaves the topic, the confidence and the basis exactly as they were. That
matters twice over — the model answers at temperature 0, so an unchanged
prompt would return the unchanged answer and cost a request for nothing.

`-retry unclear` refuses most of what it could ask. On a nine-year export
around 97 % of the `unclear` videos are tombstones whose Takeout "title" is
their own URL and whose channel line Takeout never wrote: there is no signal
to re-read, and guessing from the neighbours was measured at 35.5 % area
accuracy against a 24.4 % baseline for guessing the most common area every
time. The run says how many it refused. What it does ask are the ones that
still carry a real title, and those get the channel's other topics as a prior
(91 % area accuracy, leave-one-out) — shown to the model to weigh, never
written in as an answer.

## A taxonomy derived from the corpus

`sub_aliases:` fixes spellings one pair at a time. `taxonomy` does the same
job at the scale the free sub level actually produces: it reads the classified
corpus and derives a two-level tree from it, instead of asking anyone to write
one.

Every observed `area/sub` label is embedded together with its context — the
channels it came from, its tags, its titles — and the labels are clustered
twice over cosine distance: once tightly into **subjects**, once loosely into
the **top levels** that hold them. A local chat model names each cluster, and
refinement rounds then split subjects that are too wide to be one thing, merge
pairs that sit closer than the threshold, and fold the small tail into its
nearest neighbour. The run stops when a round changes none of its metrics.

```
youtubehistii taxonomy            → config/taxonomy.yaml
youtubehistii report -taxonomy    apply it
youtubehistii watchpath -taxonomy apply it
```

**The output is a read-side projection, nothing more.** `config/taxonomy.yaml`
maps `old-area/old-sub → top/subject`; the verdicts in `data/classified.jsonl`
are never touched, and neither is the classify fingerprint. So a taxonomy you
dislike costs a rerun, never a re-classification, and hand edits survive until
the next run.

**That file is also the most private thing here**, and it is gitignored for
that reason: the subject names in it are what somebody actually watched. They
belong in no comment, no fixture and no commit message — `classify.go` states
the rule, and the repo has broken it before. `scripts/gen-leak-patterns.sh`
turns the file into patterns that both git hooks read — the pre-commit guard
and the pre-push mirror gate — derived rather than listed by hand so the
subject nobody thought of is covered too:

```
scripts/gen-leak-patterns.sh    # after the taxonomy changes
```

Two gates, and the earlier one is the one that saves work: the push gate fires
when the value is already history, and history toward a public mirror is only
healed by a rewrite plus rebuilding the public repo. The commit guard fires
while the fix is still one `git restore --staged` away. That is not
hypothetical here — it is what the last incident cost.

If you forget to regenerate, the gates say so rather than scanning with
patterns the taxonomy has outgrown: the generated file records the source it
came from, and a source newer than the patterns blocks the commit until it is
regenerated. A stale file is the one failure this mechanism cannot survive
quietly — it loads, it matches, it reports success, and everything added since
is unprotected.

Generic words that are also somebody's subject ("chess", "jobs", "watchpath")
live in `scripts/leak-allow.txt` — that list is safe to commit precisely
because it holds nothing identifying. The generated patterns land inside the
git dir, a path that cannot be committed.

Steering happens through `config/taxonomy-control.yaml`, which is re-read
between rounds — `pin` moves one label, `merge` joins subjects, `split` forces
one apart, `keep` protects a name from the small-tail fold, `stop` ends the
loop early. What the run did lands in `data/out/taxonomy-run.jsonl`, one JSON
line per event, for `tail -f`.

Embeddings and names are both cached per label under `data/cache/`, so the
intended way to steer is to edit the control file and run the same command
again: a rerun that changes nothing asks the model nothing. `-probe` measures
what the server costs before a first run commits to it.

Two knobs are worth knowing. `-min-videos` and `-min-top-videos` are size bars
— without one, a subject whose centroid sits far from everything else becomes
a section of its own no matter how few videos it holds. And `-name-batch`
trades naming quality for speed: raising it names several subjects per
request, which is markedly faster on a cold run and *changes the names*, and
through them which clusters merge. It is meant for calibrating `-fine` and
`-coarse`, where only the shape of the tree is being read — not for a taxonomy
you mean to keep. Top levels are never batched.

Like `config/rules.yaml`, both taxonomy files are gitignored: a list of
someone's real subjects is a profile of a person.

## The watch path

`watchpath` renders the same classified views along the time axis instead of
aggregating them: `data/out/watchpath.html`. It is one self-contained page
whose every view is a real address, so the browser's back button walks them
backwards — `#/`, `#/day/2026-05-04`, `#/session/1187`,
`#/session/1187/chain/2`, `#/topics/gaming/factorio`, `#/list/gaming`,
`#/holes/depth`, `#/days/night`, `#/algo`, `#/report`.

**The overview** opens on one card per view, each holding a real miniature of
your own data — the topic tree packed by the same code the full view uses, the
busiest day on its hours, the longest sitting as a path, the list as a stack —
with a small motion that says what that view does. A view reachable only
through a word in the corner may as well not exist, so the front page shows
what it has. The motion honours `prefers-reduced-motion` and each card still
reads as a still picture without it.

Below the cards, a calendar of the whole span, one cell per day: the
hue is the area most of that day's main-lane views belonged to, and what the
opacity MEANS is yours to pick: views, time spent inside a chain, night share,
retention, first contacts, or how unusual the day was. The hue never changes
with it — what a day was about does not depend on the question. Beside it the areas sit
on a ring, and every arc is one video of an area following another *inside*
one sitting — a self-loop is a run that stayed on its topic. Click an area and
the calendar keeps only the days it dominated. Above both, the headline
numbers: sittings, longest sitting, deepest chain, the share of views
suspected of overlapping, the busiest day.

**A day** is 24 hours of axis with one bar per video — x is the start, the
width is the video's *full length*, not watch time. What looks like background
runs in a second lane below, so you can see what lay inside what, and the
sittings are brackets above the lanes. The axis grows past 24 rather than
clipping, because an evening running into the small hours is exactly what this
view is for.

**A sitting** is drawn as the path it was: video after video, each edge
labelled with what the gap says, overlap views branching off sideways to the
video that was still running, and a coloured bracket where four or more videos
of one area ran back to back. Every bracket is a link to that chain
(`#/session/<i>/chain/<k>`, counted from the top of the diagram), which opens
a panel saying how deep it went, how long it held, how many channels fed it,
which topic it was entered from and what ended it. The sitting also carries
its neighbours, so the deepest view of the page is not a dead end. The same
sitting follows underneath as cards.

**The rabbit holes** (`#/holes`) rank every chain — all of them, virtualised
rather than capped — by depth, span, length, channels, retention or date, with
an area filter. The sort key rides in the hash, so re-sorting is a step the
back button undoes and a sorted list is a link worth sharing. Each row says
where the run was entered from and what let you out of it. With
`watchpath -label-holes N` the chat model gives the N deepest a short name of
their own; replies are cached under their prompt, so a rerun asks nothing, and
the page reads exactly the same without a single label.

**The days** (`#/days`) rank the 2374 days that carried a sitting by views,
chain depth, night share, length, spread, retention, new channels, date — or
by "most unusual", which is the day's STRONGEST percentile rank across four of
those, never a weighted blend: a weight is an invented number that looks
exactly like the measured ones beside it. A row therefore says "top 0.4 % by
chain depth", which can be checked against the distribution. A day itself
gained its neighbours, measurement tiles that open what they name, and the
three weeks around it as bars.

**The algorithm, backwards** (`#/algo`) turns three signals that were only
decorative around. The edge label is the reward signal: a scatter of share of
your main lane against share watched through, where the bottom right is what
kept being served and kept being clicked away. The position in a sitting is
the drift: you chose the first video, the sidebar chose the seventh, and the
difference between those columns is the recommendation seen from the other
side. A first sighting is an introduction, ranked by what it turned into, each
row opening the sitting it began in. Every panel carries the same caveat:
Takeout records what was STARTED, never what was offered, so all of it is
inference about the response.

**The topic tree** is the other cut through the same views: areas holding
subjects holding channels, drawn as a cluster of nested circles. Clicking a
circle makes it the root, and the level below it grows big enough to read —
that step is an address too (`#/topics/gaming`, `#/topics/gaming/factorio`),
so the back button walks the drill-down back out. Circles nest, which means a
leaf's area is proportional to its views while a parent is only as big as its
children need; the page says so instead of letting the drawing imply a
precision it does not have. Circles under 14 px are not opened — their
children would be texture, not information — and the page states how many
stayed shut rather than quietly dropping them. A view classified to the bare
area still gets a subject node called `(no subject)`, because leaving it out
would make every circle above it a little wrong.

Unlike the calendar and the transition graph, the tree counts the views they
set aside as background — see the note on *overlap suspected* below.

**All views as one list** is still there, reachable from every level — it is
just no longer the front door.

A gap of more than 30 minutes starts a new sitting; within one, the gap to the
next start is compared against the video's length to say whether it was
watched through, mostly, or left early. Runs of four or more videos on one
area, less than 15 minutes apart, are marked as a chain.

Times are wall clock. Takeout stores every timestamp as UTC, and `watchpath`
reads them in the local zone of the machine that generates the page: a video
started at 01:20 belongs to that night, not to the previous UTC day. A sitting
counts on the day it BEGAN, so an evening that runs past midnight stays whole
on that evening instead of being cut in two.

The same export limit shapes this view as the one below, only harder: Takeout
records when a video was STARTED and nothing else — no end, no watch time, no
device. So a short gap after a long video has two readings that the data
cannot separate: the video was abandoned, or it kept running while something
else was started. Where a video from another area starts inside one of 20
minutes or more, the page sets it aside in a second lane labeled *overlap
suspected* — a marking, never a claim. Below that length the nearer reading is
simply "clicked away", and calling that a parallel stream would turn a guess
into a statement. Videos whose length is unknown (deleted, or not yet
enriched) get no label at all rather than a guessed one.

Those set-aside views are then treated differently depending on what is being
asked. The calendar's day colour and the transition graph both step over them —
one asks what a day was *about*, the other what followed what on the main
lane, and background must not vote in either. The topic tree counts them,
because it asks what was watched.

## Honesty note on "time spent"

The Takeout export has no per-view watch duration. Duration-weighted numbers
in the report use the full video length and are labeled as an upper bound
("up to X hours"); view counts are shown alongside as the exact metric.

A long history also has holes, and they are not a shortcoming of the
classifier. Over the years a noticeable share of any watch history points at
videos that no longer exist — on the corpus this was developed against, about
one view in eight. For those, Takeout keeps no title and no channel, just the
bare URL, so nothing can place them in a topic. The report does not hide them
under a silent "unclear"; it says what happened instead:

```
gone (4198 views, 12% — no metadata survives them, so no topic): made private 2531 ·
  unavailable 821 · age-restricted 440 · channel terminated 168 · removed by the uploader 167 ·
  members-only 65
  505 of those are locked rather than deleted (age-restricted or members-only)
```

The largest group by far is videos their uploader made private, which no
amount of fetching recovers. Three signals were measured for a way to guess a
topic for them anyway — the channel from Takeout, the surrounding session, and
the rewatch rate — and none carries. The rewatch rate is the interesting one:
if the vanished videos were secretly a music block (plausible, music videos
disappear constantly) they would be rewatched like music, roughly 1.5 times
each. They are rewatched 1.1 times, mid-table with the talk-shaped
categories — a cross-section, not a hidden category.

## License

GPL-3.0-or-later — see [LICENSE](LICENSE).

## Support

If this is useful to you: [ko-fi.com/bmabma](https://ko-fi.com/bmabma)
