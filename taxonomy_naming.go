// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/bmmmm/youtubehistii/internal/omlx"
	"github.com/bmmmm/youtubehistii/internal/rules"
	"github.com/bmmmm/youtubehistii/internal/taxonomy"
)

// defaultNameBatch is 1 — one cluster per naming request — and that default
// is a measurement, not caution.
//
// Batching works, and it is fast: naming is latency-bound rather than
// token-bound, so twelve clusters in one prompt turned a cold run's 335
// requests into 44 and 518.6 s into 216.0 s, 2.4x. What it also did was
// change the names, and through them the taxonomy: the same corpus that
// settles at 188 subjects under 9 tops named one at a time came back as 198
// under 11, and its biggest section — 64 subjects, 12751 views, music
// included — was named after one narrow part of itself. That is precisely
// the failure taxonomy.GroupPrompt was written to fix. Asking the tops
// singly did not save it (the same name came back), because the damage is
// upstream: batched subject names differ, MergeSameNames then merges
// different clusters, and the coarse groups it feeds are no longer the same
// groups.
//
// So the speed is real and the cost is the output. Raise -name-batch when
// the names do not matter and the run does — calibrating -fine/-coarse,
// where only the shape of the tree is being read — and leave it at 1 when
// the taxonomy is meant to be kept. A warm rerun asks nothing either way.
const defaultNameBatch = 1

// newNamer returns the cluster-naming function: it takes the clusters that
// need a name and returns one name each, in order, falling back to the
// strongest member's sub on any model trouble. kind is "subject" or "top"
// and only changes the prompt's altitude. The returned NamingStats fills in
// as the closure runs; read it only after the run is done with it.
//
// Replies are cached on disk under the prompt, because the intended way to
// steer a run is to edit the control file and run the same command again —
// and naming is the expensive half: a real corpus clusters into ~770
// subjects, one request each. The cache key carries the chat model, so
// swapping the model in rules.yaml (the answer to unusable names) bypasses it
// on its own. To throw the names away deliberately, delete the directory.
//
// There is deliberately no worker knob here, the way -llm-workers exists for
// classification. It was measured against the real server before anything
// was written: eight naming requests took 737 ms each in sequence, and
// 986 ms each across two workers, 967 ms across four — concurrency made the
// run 25% SLOWER, because oMLX decodes one chat request at a time and the
// extra workers only add queueing.
//
// So the lever is fewer requests, not more at once, and that is what
// nameBatch is: the clusters a round cannot serve from cache are named a
// dozen per request. The counters say what it bought — a cold run used to
// send 388 requests (286 subjects, 102 top levels, one apiece), a warm one
// still sends none.
func newNamer(client *omlx.Client, noLLM bool, log *runLog, cacheDir string, nameBatch int) (func(cs []taxonomy.Cluster, kind string) []string, *taxonomy.NamingStats) {
	warned := false
	stats := &taxonomy.NamingStats{}
	// One mkdir up front: a cache that cannot exist costs speed, not the run.
	if !noLLM {
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "warning: name cache unusable (%v) — every rerun will pay the model again\n", err)
			cacheDir = ""
		}
	}
	// slugOr turns a model's raw answer into a name, keeping the cluster's
	// strongest member as the floor. Slugging stays a code decision that a
	// later change may revise; the cached reply is the model's words.
	slugOr := func(reply string, c taxonomy.Cluster) string {
		if slug := rules.SlugifySub(reply); slug != "" {
			return slug
		}
		return taxonomy.FallbackName(c)
	}

	// askOne is the single-cluster request: the path for a lone leftover, and
	// the answer to a batch the model got wrong.
	askOne := func(c taxonomy.Cluster, kind string, ks *taxonomy.KindStats) string {
		system, user := taxonomy.NameSinglePrompt(c, kind)
		t0 := time.Now()
		reply, err := client.ChatMax(system, user, 24)
		atomic.AddInt64(&ks.ReqNanos, int64(time.Since(t0)))
		atomic.AddInt64(&ks.Requests, 1)
		if err != nil {
			atomic.AddInt64(&ks.Fallbacks, 1)
			if !warned {
				fmt.Fprintf(os.Stderr, "warning: naming via LLM failed (%v) — falling back to member names\n", err)
				warned = true
			}
			log.event("name-fallback", map[string]any{"cluster": taxonomy.FallbackName(c), "error": err.Error()})
			return taxonomy.FallbackName(c)
		}
		writeNameCache(cacheDir, client.Model, system, user, reply)
		return slugOr(reply, c)
	}

	namer := func(cs []taxonomy.Cluster, kind string) []string {
		out := make([]string, len(cs))
		if noLLM {
			// -no-llm never asks the model, so it never touches stats: the
			// naming line simply reads all zeros, which is the true count.
			for i, c := range cs {
				out[i] = taxonomy.FallbackName(c)
			}
			return out
		}
		ks := stats.ForKind(kind)

		// The cache is consulted per cluster, under the single-cluster
		// prompt, whatever shape the request that filled it had. That is what
		// makes batching free to adopt: a run after this change still finds
		// every name an earlier run paid for.
		var miss []int
		for i, c := range cs {
			system, user := taxonomy.NameSinglePrompt(c, kind)
			reply, cached := readNameCache(cacheDir, client.Model, system, user)
			if !cached {
				miss = append(miss, i)
				continue
			}
			atomic.AddInt64(&ks.Hits, 1)
			out[i] = slugOr(reply, c)
		}
		atomic.AddInt64(&ks.Misses, int64(len(miss)))

		// Top levels are always asked one at a time, whatever -name-batch
		// says. There are only ever a handful of them — five of a cold run's
		// 44 requests — so batching them buys almost nothing, and a batched
		// run named the 12751-view section, 64 subjects including music,
		// after one narrow part of it. A section name is the most visible
		// name the run produces; it gets the prompt's whole attention.
		batchSize := 1
		if kind != "top" {
			batchSize = max(nameBatch, 1)
		}
		for off := 0; off < len(miss); off += batchSize {
			chunk := miss[off:min(off+batchSize, len(miss))]
			if len(chunk) == 1 {
				// A batch prompt for one cluster is the single prompt with
				// extra ceremony, and its reply is not cache-compatible.
				out[chunk[0]] = askOne(cs[chunk[0]], kind, ks)
				continue
			}
			batch := make([]taxonomy.Cluster, len(chunk))
			for n, i := range chunk {
				batch[n] = cs[i]
			}
			system, user := taxonomy.NameBatchPrompt(batch, kind)
			t0 := time.Now()
			// max_tokens scales with the batch: a reply cut off mid-list
			// loses the clusters at its end, and those are real names.
			reply, err := client.ChatMax(system, user, 24*len(chunk))
			atomic.AddInt64(&ks.ReqNanos, int64(time.Since(t0)))
			atomic.AddInt64(&ks.Requests, 1)
			var names []string
			if err == nil {
				names, err = taxonomy.ParseNameBatch(reply, len(chunk))
			}
			if err != nil {
				// One unusable batch costs len(chunk) requests, never a name
				// on the wrong cluster — a misplaced name is invisible later.
				log.event("name-batch-retry", map[string]any{
					"kind": kind, "clusters": len(chunk), "error": err.Error(),
				})
				for _, i := range chunk {
					out[i] = askOne(cs[i], kind, ks)
				}
				continue
			}
			for n, i := range chunk {
				out[i] = slugOr(names[n], cs[i])
				// Stored under the SINGLE prompt, so the next run finds it
				// however this one happened to ask.
				s, u := taxonomy.NameSinglePrompt(cs[i], kind)
				writeNameCache(cacheDir, client.Model, s, u, names[n])
			}
		}
		return out
	}
	return namer, stats
}

// nameSubjects names clusters: all of them on the first pass, only unnamed
// pieces (fresh splits) afterwards — merges keep their names. The ones that
// need a name go to the namer together, so a round of fresh splits costs a
// handful of requests instead of one per piece.
func nameSubjects(cs []taxonomy.Cluster, namer func([]taxonomy.Cluster, string) []string, all bool) {
	var idx []int
	for i := range cs {
		if all || cs[i].Name == "" {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return
	}
	batch := make([]taxonomy.Cluster, len(idx))
	for n, i := range idx {
		batch[n] = cs[i]
	}
	names := namer(batch, "subject")
	for n, i := range idx {
		cs[i].Name = names[n]
	}
}

// assignParents groups subjects into top levels and names each group. Two
// groups landing on the same name simply ARE one top level — nothing to
// dedupe. Groups under the minTopVideos bar are folded into their nearest
// neighbour first, so a far-out subject does not become a section of one.
func assignParents(cs []taxonomy.Cluster, coarse float64, minTopVideos int, keep map[string]bool, namer func([]taxonomy.Cluster, string) []string) {
	groups := taxonomy.FoldSmallGroups(cs, taxonomy.Coarse(cs, coarse), minTopVideos, keep)
	// The WHOLE group carries the naming prompt, one label per subject:
	// naming it after its strongest subject alone reduced a top level of
	// 31 music subjects, and one of 29 sport subjects, to the name of that
	// single subject. TopChannels then sums across the group too.
	//
	// All groups go in one call: a corpus has around ten top levels, which
	// is under nameBatch, so a round that used to cost ten requests now
	// costs one.
	var prompts []taxonomy.Cluster
	var named [][]int
	for _, group := range groups {
		prompt := taxonomy.GroupPrompt(cs, group)
		if len(prompt.Members) == 0 {
			continue
		}
		prompts = append(prompts, prompt)
		named = append(named, group)
	}
	if len(prompts) == 0 {
		return
	}
	tops := namer(prompts, "top")
	for n, group := range named {
		for _, i := range group {
			cs[i].Parent = tops[n]
		}
	}
}
