package server

import (
	"context"
	"log/slog"

	"github.com/nbd-wtf/go-nostr"
)

// RunIngest subscribes to the given relays for mod events (current 31142 + legacy
// 30402 GameMod) and stores every one that is validly signed and passes this
// relay's own policy. This turns the node into a comprehensive mirror/DB of all
// mods on the network, so clients can query one place and reliably get everything.
// It blocks until ctx is cancelled; the pool reconnects on drops.
func (s *Server) RunIngest(ctx context.Context, relays []string) {
	if len(relays) == 0 {
		return
	}
	pool := nostr.NewSimplePool(ctx)
	filters := nostr.Filters{
		{Kinds: []int{currentModKind}},
		{Kinds: []int{legacyModKind}, Tags: nostr.TagMap{"t": []string{"GameMod"}}},
	}
	slog.Info("ingest started", "relays", len(relays))

	var stored, seen int
	for re := range pool.SubMany(ctx, relays, filters) {
		evt := re.Event
		if evt == nil {
			continue
		}
		seen++
		// Verify the signature ourselves — khatru does this for direct clients, but
		// ingested events come straight from possibly-untrusted relays.
		if ok, err := evt.CheckSignature(); err != nil || !ok {
			continue
		}
		if reject, _ := s.rejectEvent(ctx, evt); reject {
			continue
		}
		// Both mod kinds are addressable → ReplaceEvent keeps the latest per coordinate.
		if err := s.store.ReplaceEvent(ctx, evt); err != nil {
			continue
		}
		stored++
		if stored%200 == 0 {
			slog.Info("ingest progress", "stored", stored, "seen", seen)
		}
	}
	slog.Info("ingest stopped", "stored", stored, "seen", seen)
}
