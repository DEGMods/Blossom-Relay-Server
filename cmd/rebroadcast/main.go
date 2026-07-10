// Command rebroadcast reads every legacy mod (kind 30402, t=GameMod) from a
// source relay (brs by default) and re-publishes them to one or more target
// relays, gently — each target is paced independently, publishes back off
// exponentially on error (rate limits included), and progress is checkpointed so
// a re-run resumes instead of re-sending. Re-publishing signed events needs no
// key (the signatures already exist), so this is safe to run anywhere.
//
// Usage:
//
//	go run ./cmd/rebroadcast \
//	  -source wss://brs.degmods.com \
//	  -targets wss://relay.damus.io,wss://relay.primal.net,wss://nos.lol,wss://relay.nostr.band \
//	  -delay 600
//
// It's a one-off maintenance tool, not part of the server.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

const (
	legacyModKind = 30402
	gameModTag    = "GameMod"
)

func main() {
	source := flag.String("source", "wss://brs.degmods.com", "relay to read the legacy mods from")
	targetsCSV := flag.String("targets", "wss://relay.damus.io,wss://relay.primal.net,wss://nos.lol,wss://relay.nostr.band", "comma-separated relays to rebroadcast to")
	delayMs := flag.Int("delay", 600, "milliseconds between publishes to a single relay (pacing)")
	checkpointPath := flag.String("checkpoint", "rebroadcast-checkpoint.json", "resumable progress file")
	pageSize := flag.Int("page", 500, "fetch page size when reading from the source")
	maxRetries := flag.Int("retries", 6, "max publish attempts per event before skipping it")
	flag.Parse()

	targets := splitCSV(*targetsCSV)
	if len(targets) == 0 {
		log.Fatal("no target relays given")
	}
	ctx := context.Background()

	// 1) Read all legacy mods from the source (paginate past the per-query cap).
	events, err := fetchLegacyMods(ctx, *source, *pageSize)
	if err != nil {
		log.Fatalf("fetch from %s: %v", *source, err)
	}
	log.Printf("fetched %d legacy mods from %s", len(events), *source)
	if len(events) == 0 {
		return
	}

	// 2) Resume from a prior run if any.
	cp := loadCheckpoint(*checkpointPath)

	// 3) Rebroadcast to each target concurrently, paced within each.
	var wg sync.WaitGroup
	for _, target := range targets {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()
			rebroadcast(ctx, target, events, cp, time.Duration(*delayMs)*time.Millisecond, *maxRetries)
		}(target)
	}
	wg.Wait()
	cp.save()
	log.Print("done")
}

// fetchLegacyMods reads the whole GameMod set from a relay, stepping `until`
// backward past the relay's single-query cap and de-duping by id.
func fetchLegacyMods(ctx context.Context, url string, pageSize int) ([]*nostr.Event, error) {
	relay, err := nostr.RelayConnect(ctx, url)
	if err != nil {
		return nil, err
	}
	defer relay.Close()

	byID := map[string]*nostr.Event{}
	var until *nostr.Timestamp
	for round := 0; round < 80; round++ {
		f := nostr.Filter{
			Kinds: []int{legacyModKind},
			Tags:  nostr.TagMap{"t": []string{gameModTag}},
			Limit: pageSize,
		}
		if until != nil {
			f.Until = until
		}
		qctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		evs, err := relay.QuerySync(qctx, f)
		cancel()
		if err != nil {
			return nil, err
		}
		if len(evs) == 0 {
			break
		}
		added := 0
		min := nostr.Timestamp(1 << 62)
		for _, e := range evs {
			if _, ok := byID[e.ID]; !ok {
				byID[e.ID] = e
				added++
			}
			if e.CreatedAt < min {
				min = e.CreatedAt
			}
		}
		if added == 0 { // relay returned only events we've already seen
			break
		}
		u := min - 1
		until = &u
	}

	out := make([]*nostr.Event, 0, len(byID))
	for _, e := range byID {
		out = append(out, e)
	}
	return out, nil
}

// rebroadcast publishes every event to one target relay, paced + backed off,
// recording progress so a re-run skips what already landed.
func rebroadcast(ctx context.Context, url string, events []*nostr.Event, cp *checkpoint, delay time.Duration, maxRetries int) {
	relay, err := nostr.RelayConnect(ctx, url)
	if err != nil {
		log.Printf("[%s] connect failed: %v", url, err)
		return
	}
	defer relay.Close()

	done, skipped := 0, 0
	for i, evt := range events {
		if cp.has(url, evt.ID) {
			done++
			continue
		}

		ok := false
		for attempt := 0; attempt <= maxRetries; attempt++ {
			pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			perr := relay.Publish(pctx, *evt)
			cancel()
			if perr == nil {
				ok = true
				break
			}
			// Reconnect a dropped socket, then back off (covers rate limits too).
			if r, e := nostr.RelayConnect(ctx, url); e == nil {
				relay.Close()
				relay = r
			}
			wait := time.Duration(1<<attempt) * time.Second
			if wait > 30*time.Second {
				wait = 30 * time.Second
			}
			log.Printf("[%s] %s attempt %d failed: %v (backoff %s)", url, short(evt.ID), attempt+1, perr, wait)
			time.Sleep(wait)
		}

		if ok {
			cp.mark(url, evt.ID)
			done++
		} else {
			skipped++
			log.Printf("[%s] giving up on %s", url, short(evt.ID))
		}

		if (i+1)%25 == 0 {
			cp.save()
			log.Printf("[%s] %d/%d (done=%d skipped=%d)", url, i+1, len(events), done, skipped)
		}
		time.Sleep(delay) // pace between publishes to stay under rate limits
	}
	cp.save()
	log.Printf("[%s] finished: done=%d skipped=%d", url, done, skipped)
}

// ── checkpoint (resumable "relay|eventid" set) ──────────────────────────────

type checkpoint struct {
	mu   sync.Mutex
	path string
	set  map[string]bool
}

func loadCheckpoint(path string) *checkpoint {
	c := &checkpoint{path: path, set: map[string]bool{}}
	if b, err := os.ReadFile(path); err == nil {
		var keys []string
		if json.Unmarshal(b, &keys) == nil {
			for _, k := range keys {
				c.set[k] = true
			}
		}
	}
	return c
}

func ckey(url, id string) string { return url + "|" + id }

func (c *checkpoint) has(url, id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.set[ckey(url, id)]
}

func (c *checkpoint) mark(url, id string) {
	c.mu.Lock()
	c.set[ckey(url, id)] = true
	c.mu.Unlock()
}

func (c *checkpoint) save() {
	c.mu.Lock()
	keys := make([]string, 0, len(c.set))
	for k := range c.set {
		keys = append(keys, k)
	}
	c.mu.Unlock()
	if b, err := json.MarshalIndent(keys, "", " "); err == nil {
		_ = os.WriteFile(c.path, b, 0o644)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func splitCSV(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func short(id string) string {
	if len(id) > 10 {
		return id[:10]
	}
	return id
}
