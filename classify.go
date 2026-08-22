// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/bmmmm/youtubehistii/internal/classify"
	"github.com/bmmmm/youtubehistii/internal/enrich"
	"github.com/bmmmm/youtubehistii/internal/omlx"
	"github.com/bmmmm/youtubehistii/internal/rules"
	"github.com/bmmmm/youtubehistii/internal/takeout"
)

func cmdClassify(args []string) error {
	fs, dataDir := newFlagSet("classify")
	rulesPath := fs.String("rules", "", "rules file (default: config/rules.yaml, falling back to config/rules.example.yaml)")
	noLLM := fs.Bool("no-llm", false, "skip the LLM stage, rules only")
	llmLimit := fs.Int("llm-limit", 0, "ask the LLM about at most N videos this run (0 = all)")
	fs.Parse(args)
	p := paths{dataDir: *dataDir}

	cfg, err := loadRules(*rulesPath)
	if err != nil {
		return err
	}
	views, err := readJSONL[takeout.View](p.historyJSONL())
	if err != nil {
		return fmt.Errorf("read history (run \"import\" first): %w", err)
	}
	metas, err := enrich.Cache{Dir: p.metaCacheDir()}.ReadAll()
	if err != nil {
		return err
	}
	if len(metas) == 0 {
		fmt.Fprintln(os.Stderr, "note: metadata cache is empty — run \"enrich\" first for tags/categories/durations")
	}

	// Per unique video: build the matcher input (canonical metadata wins,
	// the takeout row fills the gaps) and run stage 1.
	inputs := map[string]rules.Input{}
	for _, v := range views {
		if v.VideoID == "" {
			continue
		}
		if _, done := inputs[v.VideoID]; done {
			continue
		}
		in := rules.Input{Title: v.Title, Channel: v.Channel}
		if m, ok := metas[v.VideoID]; ok && !m.Unavailable {
			if m.Title != "" {
				in.Title = m.Title
			}
			if m.Channel != "" {
				in.Channel = m.Channel
			}
			in.Tags = m.Tags
			in.Categories = m.Categories
		}
		inputs[v.VideoID] = in
	}

	type videoVerdict struct {
		topic, mode, source string
		confidence          float64
	}
	verdicts := map[string]videoVerdict{}
	var needLLM []string
	for id, in := range inputs {
		if topic, mode, ruleID, ok := cfg.Match(in); ok {
			verdicts[id] = videoVerdict{topic: topic, mode: mode, source: "rule:" + ruleID}
		} else {
			needLLM = append(needLLM, id)
		}
	}
	sort.Strings(needLLM)
	fmt.Printf("%d unique videos: %d matched by rules, %d for the LLM\n",
		len(inputs), len(verdicts), len(needLLM))

	// Stage 2 — cached LLM verdicts first, then live calls.
	llmCache := classify.Cache{Dir: p.classifyCache()}
	var live []string
	for _, id := range needLLM {
		if v, ok := llmCache.Read(id); ok {
			verdicts[id] = videoVerdict{topic: v.Topic, mode: v.Mode, source: "llm:" + v.Model, confidence: v.Confidence}
		} else {
			live = append(live, id)
		}
	}
	fmt.Printf("LLM: %d cached verdicts, %d to ask\n", len(needLLM)-len(live), len(live))

	llmDown := *noLLM
	if *llmLimit > 0 && len(live) > *llmLimit {
		live = live[:*llmLimit]
		fmt.Printf("limiting LLM calls to %d this run\n", len(live))
	}
	if !llmDown && len(live) > 0 {
		client := omlx.New(cfg.LLM.Model, cfg.LLM.BaseURL)
		// Discovery doubles as health check: bail out early with the real
		// model list instead of failing per-video.
		models, err := client.Models()
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "warning: %v\nwarning: continuing rules-only — %d videos stay unclassified this run\n", err, len(live))
			llmDown = true
		case !slices.Contains(models, client.Model):
			return fmt.Errorf("model %q not on the oMLX server — available: %s", client.Model, strings.Join(models, ", "))
		}
		if !llmDown {
			fmt.Printf("asking %s (model %s)\n", client.BaseURL, client.Model)
		}
		for i := 0; !llmDown && i < len(live); i++ {
			id := live[i]
			verdict, err := askLLM(client, cfg, inputs[id])
			if err != nil {
				if isConnErr(err) {
					fmt.Fprintf(os.Stderr, "warning: %v\nwarning: continuing rules-only — the remaining %d videos stay unclassified this run\n", err, len(live)-i)
					llmDown = true
					break
				}
				// Bad single reply: leave this video unclassified, keep going.
				fmt.Fprintf(os.Stderr, "warning: %s: %v\n", id, err)
				continue
			}
			if err := llmCache.Write(id, verdict); err != nil {
				return err
			}
			verdicts[id] = videoVerdict{topic: verdict.Topic, mode: verdict.Mode, source: "llm:" + verdict.Model, confidence: verdict.Confidence}
			if (i+1)%25 == 0 || i+1 == len(live) {
				fmt.Printf("  %d/%d\n", i+1, len(live))
			}
		}
	}

	// Join verdicts back onto every watch event.
	var out []classify.Verdict
	for _, v := range views {
		row := classify.Verdict{
			VideoID:   v.VideoID,
			Title:     v.Title,
			Channel:   v.Channel,
			ChannelID: takeout.ChannelIDFromURL(v.ChannelURL),
			WatchedAt: v.WatchedAt,
			Topic:     "unclear",
			Source:    "unclassified",
		}
		if m, ok := metas[v.VideoID]; ok {
			row.DurationS = m.Duration
			row.Unavailable = m.Unavailable
			if m.Title != "" {
				row.Title = m.Title
			}
			if m.Channel != "" {
				row.Channel = m.Channel
			}
			if m.ChannelID != "" {
				row.ChannelID = m.ChannelID
			}
		}
		if v.VideoID == "" {
			// No video ID (deleted/private): still give the rules a shot at
			// the takeout title/channel.
			if topic, mode, ruleID, ok := cfg.Match(rules.Input{Title: v.Title, Channel: v.Channel}); ok {
				row.Topic, row.Mode, row.Source = topic, mode, "rule:"+ruleID
			}
		} else if vv, ok := verdicts[v.VideoID]; ok {
			row.Topic, row.Mode, row.Source, row.Confidence = vv.topic, vv.mode, vv.source, vv.confidence
		}
		out = append(out, row)
	}
	if err := writeJSONL(p.classifiedJSONL(), out); err != nil {
		return err
	}

	bySource := map[string]int{}
	for _, r := range out {
		switch {
		case strings.HasPrefix(r.Source, "rule:"):
			bySource["rule"]++
		case strings.HasPrefix(r.Source, "llm:"):
			bySource["llm"]++
		default:
			bySource["unclassified"]++
		}
	}
	fmt.Printf("wrote %s: %d views (%d via rules, %d via llm, %d unclassified)\n",
		p.classifiedJSONL(), len(out), bySource["rule"], bySource["llm"], bySource["unclassified"])
	if llmDown && bySource["unclassified"] > 0 && !*noLLM {
		fmt.Println("rerun \"classify\" once oMLX is up to fill the gap — verdicts are cached.")
	}
	return nil
}

func loadRules(path string) (*rules.Config, error) {
	if path != "" {
		return rules.Load(path)
	}
	cfg, err := rules.Load("config/rules.yaml")
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "note: config/rules.yaml not found, using config/rules.example.yaml — copy and adapt it")
		return rules.Load("config/rules.example.yaml")
	}
	return cfg, err
}

func askLLM(client *omlx.Client, cfg *rules.Config, in rules.Input) (classify.LLMVerdict, error) {
	system, user := classify.BuildPrompt(cfg, in)
	reply, err := client.Chat(system, user)
	if err != nil {
		return classify.LLMVerdict{}, err
	}
	v, err := classify.ParseVerdict(cfg, reply)
	if err != nil {
		return classify.LLMVerdict{}, err
	}
	v.Model = client.Model
	return v, nil
}

func isConnErr(err error) bool {
	return strings.Contains(err.Error(), "unreachable") || strings.Contains(err.Error(), "401")
}
