package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// Ad inventory management. The BUD-Ads gate points clients at a NIP-78 event
// (30078:<node-pubkey>:manual-blossom-ads) that lists the ads a node shows. That
// event must be signed by the NODE key (it lives under the node's pubkey), so the
// admin can't publish it via NIP-07 — instead they submit the ad list to this
// admin API and the node signs + publishes it with its own key.

const adInventoryDTag = "manual-blossom-ads"

// adItem is one ad in the inventory (mirrors the client's Ad shape in gate.ts).
type adItem struct {
	ID     string `json:"id"`
	Media  string `json:"media"`          // blossom hash or URL of the creative
	Link   string `json:"link,omitempty"` // click-through URL
	Alt    string `json:"alt,omitempty"`  // alt text
	Weight int    `json:"weight,omitempty"`
}

// adInventoryContent is the JSON serialized into the NIP-78 event content.
type adInventoryContent struct {
	Ads []adItem `json:"ads"`
}

// adInventory persists the last-published inventory (the ad list + the signed
// event) so a GET can echo it and a restart doesn't lose it.
type adInventory struct {
	mu    sync.RWMutex
	path  string
	Ads   []adItem     `json:"ads"`
	Event *nostr.Event `json:"event,omitempty"` // last signed 30078 event
}

func loadAdInventory(path string) *adInventory {
	inv := &adInventory{path: path, Ads: []adItem{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, inv)
	}
	if inv.Ads == nil {
		inv.Ads = []adItem{}
	}
	return inv
}

func (inv *adInventory) snapshot() ([]adItem, *nostr.Event) {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	return inv.Ads, inv.Event
}

func (inv *adInventory) store(ads []adItem, ev *nostr.Event) error {
	inv.mu.Lock()
	inv.Ads = ads
	inv.Event = ev
	data, _ := json.MarshalIndent(inv, "", "  ")
	inv.mu.Unlock()
	return os.WriteFile(inv.path, data, 0o600)
}

// normalizeAds validates and cleans a submitted ad list: an ad needs a non-empty
// id and media; weight defaults to 1 and is floored at 1.
func normalizeAds(in []adItem) []adItem {
	out := make([]adItem, 0, len(in))
	for _, a := range in {
		if a.ID == "" || a.Media == "" {
			continue
		}
		if a.Weight < 1 {
			a.Weight = 1
		}
		out = append(out, a)
	}
	return out
}

// buildAdInventoryEvent builds and signs the NIP-78 inventory event with the node
// key. createdAt is a parameter so the result is deterministic in tests.
func buildAdInventoryEvent(ads []adItem, secretKey string, createdAt nostr.Timestamp) (*nostr.Event, error) {
	content, err := json.Marshal(adInventoryContent{Ads: ads})
	if err != nil {
		return nil, err
	}
	ev := &nostr.Event{
		Kind:      30078,
		CreatedAt: createdAt,
		Tags:      nostr.Tags{{"d", adInventoryDTag}},
		Content:   string(content),
	}
	if err := ev.Sign(secretKey); err != nil {
		return nil, err
	}
	return ev, nil
}

// publishEvent sends a signed event to each relay (best-effort) and returns how
// many accepted it. Mirrors the announce publisher's per-relay connect/publish.
func publishEvent(ctx context.Context, ev *nostr.Event, relays []string) int {
	ok := 0
	for _, url := range relays {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		relay, err := nostr.RelayConnect(rctx, url)
		if err != nil {
			cancel()
			continue
		}
		err = relay.Publish(rctx, *ev)
		relay.Close()
		cancel()
		if err == nil {
			ok++
		}
	}
	return ok
}

// handleAdminAdsGet returns the current ad inventory (list + last signed event).
func (s *Server) handleAdminAdsGet(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	ads, ev := s.adInv.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"ref":            s.adRef,
		"publish_relays": s.adPublishRelays,
		"ads":            ads,
		"event":          ev,
	})
}

// handleAdminAdsPut accepts a new ad list, signs it as the node's NIP-78 inventory
// event, publishes it to the configured relays, and persists it.
func (s *Server) handleAdminAdsPut(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if s.nodeSeckey == "" {
		httpErr(w, http.StatusServiceUnavailable, "node signing key unavailable")
		return
	}
	var body struct {
		Ads []adItem `json:"ads"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	ads := normalizeAds(body.Ads)

	ev, err := buildAdInventoryEvent(ads, s.nodeSeckey, nostr.Now())
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "failed to sign inventory event")
		return
	}
	published := publishEvent(r.Context(), ev, s.adPublishRelays)
	if err := s.adInv.store(ads, ev); err != nil {
		httpErr(w, http.StatusInternalServerError, "failed to persist inventory")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ads":            ads,
		"event":          ev,
		"published_to":   published,
		"publish_relays": s.adPublishRelays,
	})
}
