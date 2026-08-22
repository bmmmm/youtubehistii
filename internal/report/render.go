// SPDX-License-Identifier: GPL-3.0-or-later

package report

import (
	"bytes"
	"fmt"
	"html/template"
	"time"
)

// RenderHTML produces the self-contained report page: system fonts, no
// external assets, light/dark via prefers-color-scheme.
func RenderHTML(st *Stats, generated time.Time) ([]byte, error) {
	funcs := template.FuncMap{
		"pct": func(part, total int) float64 {
			if total == 0 {
				return 0
			}
			return float64(part) / float64(total) * 100
		},
		"pctf": func(part, total float64) float64 {
			if total == 0 {
				return 0
			}
			return part / total * 100
		},
		"f1": func(v float64) string { return fmt.Sprintf("%.1f", v) },
		"f0": func(v float64) string { return fmt.Sprintf("%.0f", v) },
		"date": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.Format("2006-01-02")
		},
	}
	tpl, err := template.New("report").Funcs(funcs).Parse(pageTpl)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	err = tpl.Execute(&buf, map[string]any{
		"S":         st,
		"Generated": generated.Format("2006-01-02 15:04"),
		"Months":    monthBars(st),
		"TopChans":  capChannels(st.Channels, 25),
		"MaxTopic":  maxTopicViews(st),
	})
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func maxTopicViews(st *Stats) int {
	max := 1
	for _, t := range st.Topics {
		if t.Views > max {
			max = t.Views
		}
	}
	return max
}

func capChannels(cs []ChannelAgg, n int) []ChannelAgg {
	if len(cs) > n {
		return cs[:n]
	}
	return cs
}

type monthSeg struct {
	Mode      string
	Views     int
	HeightPct float64
}

type monthBar struct {
	Month string
	Total int
	Segs  []monthSeg
}

func monthBars(st *Stats) []monthBar {
	maxViews := 1
	for _, m := range st.Months {
		total := 0
		for _, v := range m.ModeViews {
			total += v
		}
		if total > maxViews {
			maxViews = total
		}
	}
	var bars []monthBar
	for _, m := range st.Months {
		b := monthBar{Month: m.Month}
		for _, mode := range ModeOrder {
			v := m.ModeViews[mode]
			b.Total += v
			if v > 0 {
				b.Segs = append(b.Segs, monthSeg{
					Mode:      mode,
					Views:     v,
					HeightPct: float64(v) / float64(maxViews) * 100,
				})
			}
		}
		bars = append(bars, b)
	}
	return bars
}

const pageTpl = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>youtubehistii report</title>
<style>
:root {
  --bg: #ffffff; --fg: #1a1a1a; --muted: #666; --line: #e2e2e2;
  --bar: #4a7dbd; --card: #f6f6f6;
  --consume: #d98a3d; --learn: #4c9e6b; --mixed: #8a6fb8; --unclear: #9a9a9a;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #16181c; --fg: #e6e6e6; --muted: #9a9a9a; --line: #33363c;
    --bar: #6b9fd8; --card: #1f2228;
    --consume: #e0a468; --learn: #6dbb8a; --mixed: #a68fd0; --unclear: #7a7a7a;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0 auto; padding: 2rem 1rem 4rem; max-width: 62rem;
  background: var(--bg); color: var(--fg);
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  line-height: 1.5;
}
h1 { font-size: 1.6rem; margin-bottom: .2rem; }
h2 { font-size: 1.15rem; margin: 2.2rem 0 .6rem; border-bottom: 1px solid var(--line); padding-bottom: .3rem; }
.muted { color: var(--muted); font-size: .85rem; }
.cards { display: flex; flex-wrap: wrap; gap: .8rem; margin: 1rem 0; }
.card { background: var(--card); border-radius: .5rem; padding: .7rem 1rem; min-width: 8.5rem; }
.card .n { font-size: 1.35rem; font-weight: 600; }
table { border-collapse: collapse; width: 100%; font-size: .9rem; }
th, td { text-align: left; padding: .35rem .6rem .35rem 0; border-bottom: 1px solid var(--line); vertical-align: top; }
th { color: var(--muted); font-weight: 500; }
td.num, th.num { text-align: right; }
.scroll { overflow-x: auto; }
.barbox { background: var(--line); border-radius: .2rem; height: .7rem; min-width: 8rem; }
.barfill { background: var(--bar); border-radius: .2rem; height: 100%; }
.mode { display: inline-block; padding: 0 .45rem; border-radius: .6rem; font-size: .75rem; color: #fff; }
.mode.consume { background: var(--consume); } .mode.learn { background: var(--learn); }
.mode.mixed { background: var(--mixed); } .mode.unclear { background: var(--unclear); }
.months { display: flex; align-items: flex-end; gap: .3rem; height: 11rem; padding-top: 1rem; overflow-x: auto; }
.month { display: flex; flex-direction: column-reverse; width: 1.6rem; flex-shrink: 0; }
.month .seg.consume { background: var(--consume); } .month .seg.learn { background: var(--learn); }
.month .seg.mixed { background: var(--mixed); } .month .seg.unclear { background: var(--unclear); }
.mlabels { display: flex; gap: .3rem; overflow-x: hidden; }
.mlabels span { width: 1.6rem; flex-shrink: 0; font-size: .6rem; color: var(--muted);
  writing-mode: vertical-rl; height: 3.2rem; }
.legend { margin: .5rem 0; font-size: .8rem; }
.ok { color: var(--learn); }
.note { background: var(--card); border-left: 3px solid var(--bar); padding: .6rem .9rem; border-radius: .3rem; font-size: .85rem; }
</style>
</head>
<body>
<h1>youtubehistii report</h1>
<p class="muted">generated {{.Generated}} · {{date .S.From}} … {{date .S.To}} · everything below was computed on this machine</p>

<div class="cards">
  <div class="card"><div class="n">{{.S.Views}}</div><div class="muted">views</div></div>
  <div class="card"><div class="n">{{.S.UniqueVideos}}</div><div class="muted">unique videos</div></div>
  <div class="card"><div class="n">≤ {{f0 .S.HoursUpper}} h</div><div class="muted">watch time (upper bound)</div></div>
  <div class="card"><div class="n">{{index .S.Sources "rule"}} / {{index .S.Sources "llm"}} / {{index .S.Sources "unclassified"}}</div><div class="muted">via rules / LLM / open</div></div>
</div>

<p class="note">Takeout has no per-view watch duration, so hour figures assume
each video was watched in full — an upper bound. View counts are exact.
{{if .S.NoID}}{{.S.NoID}} views had no video link (deleted/private).{{end}}
{{if .S.Unavailable}}{{.S.Unavailable}} views point at videos that are gone from YouTube.{{end}}</p>

<h2>Topics</h2>
<div class="scroll"><table>
<tr><th>topic</th><th>mode</th><th class="num">views</th><th class="num">≤ hours</th><th></th></tr>
{{range .S.Topics}}
<tr>
  <td>{{.Topic}}</td>
  <td><span class="mode {{.Mode}}">{{.Mode}}</span></td>
  <td class="num">{{.Views}}</td>
  <td class="num">{{f1 .Hours}}</td>
  <td><div class="barbox"><div class="barfill" style="width: {{f1 (pct .Views $.MaxTopic)}}%"></div></div></td>
</tr>
{{end}}
</table></div>

<h2>Consume vs. learn, per month</h2>
<div class="legend">
  <span class="mode consume">consume</span> <span class="mode learn">learn</span>
  <span class="mode mixed">mixed</span> <span class="mode unclear">unclear</span>
</div>
<div class="months">
{{range .Months}}<div class="month" title="{{.Month}}: {{.Total}} views">
{{range .Segs}}<div class="seg {{.Mode}}" style="height: {{f1 .HeightPct}}%" title="{{.Mode}}: {{.Views}}"></div>{{end}}
</div>{{end}}
</div>
<div class="mlabels">{{range .Months}}<span>{{.Month}}</span>{{end}}</div>

<h2>Top channels</h2>
<div class="scroll"><table>
<tr><th>channel</th><th>topic</th><th class="num">views</th><th class="num">≤ hours</th>{{if .S.HasSubs}}<th>subscribed</th>{{end}}</tr>
{{range .TopChans}}
<tr>
  <td>{{.Name}}</td>
  <td>{{.TopTopic}}</td>
  <td class="num">{{.Views}}</td>
  <td class="num">{{f1 .Hours}}</td>
  {{if $.S.HasSubs}}<td>{{if .Subscribed}}<span class="ok">✓</span>{{end}}</td>{{end}}
</tr>
{{end}}
</table></div>

{{if .S.HasSubs}}
<h2>Subscriptions</h2>
<div class="cards">
  <div class="card"><div class="n">{{len .S.Subs}}</div><div class="muted">subscriptions</div></div>
  <div class="card"><div class="n">{{f1 (pct .S.SubbedViews .S.Views)}} %</div><div class="muted">of views on subscribed channels</div></div>
  <div class="card"><div class="n">{{f1 (pctf .S.SubbedHours .S.HoursUpper)}} %</div><div class="muted">of hours on subscribed channels</div></div>
  <div class="card"><div class="n">{{.S.DeadSubs}}</div><div class="muted">never watched in this export</div></div>
</div>
<div class="scroll"><table>
<tr><th>subscription</th><th>topic (from watched videos)</th><th class="num">views</th><th class="num">≤ hours</th><th>last watched</th></tr>
{{range .S.Subs}}
<tr>
  <td>{{.Title}}</td>
  <td>{{if .TopTopic}}{{.TopTopic}}{{else}}<span class="muted">— never watched</span>{{end}}</td>
  <td class="num">{{.Views}}</td>
  <td class="num">{{f1 .Hours}}</td>
  <td>{{date .LastWatched}}</td>
</tr>
{{end}}
</table></div>
{{end}}

{{if .S.UnclearNames}}
<h2>Unclear — feed these into your rules</h2>
<p class="muted">Channels with the most unclassified/unclear views. Add channel_any rules for them in config/rules.yaml, then rerun classify.</p>
<ul>{{range .S.UnclearNames}}<li>{{.}}</li>{{end}}</ul>
{{end}}

</body>
</html>
`
