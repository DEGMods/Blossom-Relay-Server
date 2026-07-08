package server

import (
	"context"
	"time"

	"github.com/fiatjaf/eventstore/badger"
	"github.com/fiatjaf/khatru"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip13"
	"github.com/nbd-wtf/go-nostr/nip86"
)

const (
	currentModKind = 31142 // the permanent, current mod kind
	legacyModKind  = 30402 // LEGACY: old mods (temporary exception, see below)
)

// LEGACY: legacy mods (kind 30402) are accepted only with a "GameMod" t-tag and a
// created_at before this cutoff, and are PoW-exempt. Mirrors the client's
// isLegacyModEvent(). Expected to be removed in a future version.
var legacyCutoff = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Unix()

func hasTagValue(evt *nostr.Event, name, val string) bool {
	for _, t := range evt.Tags {
		if len(t) >= 2 && t[0] == name && t[1] == val {
			return true
		}
	}
	return false
}

func isLegacyMod(evt *nostr.Event) bool {
	return evt.Kind == legacyModKind &&
		int64(evt.CreatedAt) < legacyCutoff &&
		hasTagValue(evt, "t", "GameMod")
}

func acceptedKind(k int) bool { return k == currentModKind || k == legacyModKind }

// AcceptedModKinds are the relay event kinds this node stores/serves (for the
// discovery announcement).
func AcceptedModKinds() []int { return []int{currentModKind, legacyModKind} }

// rejectEvent enforces the mod-only write policy.
func (s *Server) rejectEvent(ctx context.Context, evt *nostr.Event) (bool, string) {
	if s.blocked(evt.PubKey) {
		return true, "blocked: pubkey is banned"
	}
	switch evt.Kind {
	case currentModKind:
		if tagValue(evt, "d") == "" {
			return true, "invalid: mod event is missing its d tag"
		}
		if s.minEventPoW > 0 && nip13.Difficulty(evt.ID) < s.minEventPoW {
			return true, "pow: insufficient proof-of-work"
		}
		return false, ""
	case legacyModKind:
		if !isLegacyMod(evt) { // LEGACY: PoW-exempt, but gated by tag + cutoff
			return true, "legacy mods require a GameMod tag and must predate the cutoff"
		}
		return false, ""
	default:
		return true, "blocked: this relay only accepts mod events"
	}
}

// rejectFilter keeps the relay mod-scoped for reads.
func (s *Server) rejectFilter(ctx context.Context, filter nostr.Filter) (bool, string) {
	if len(filter.Kinds) == 0 {
		return false, "" // querying everything → only mod events exist here
	}
	for _, k := range filter.Kinds {
		if acceptedKind(k) {
			return false, ""
		}
	}
	return true, "this relay only serves mod events"
}

// setupRelay wires the event store, mod policies, and NIP-86 admin onto the relay.
func (s *Server) setupRelay(store *badger.BadgerBackend, adminPubkey string) {
	r := s.relay
	r.StoreEvent = append(r.StoreEvent, store.SaveEvent)
	r.ReplaceEvent = append(r.ReplaceEvent, store.ReplaceEvent)
	r.QueryEvents = append(r.QueryEvents, store.QueryEvents)
	r.CountEvents = append(r.CountEvents, store.CountEvents)
	r.DeleteEvent = append(r.DeleteEvent, store.DeleteEvent)

	r.RejectEvent = append(r.RejectEvent, s.rejectEvent)
	r.RejectFilter = append(r.RejectFilter, s.rejectFilter)

	if adminPubkey == "" {
		return // NIP-86 disabled when no admin configured
	}
	m := &r.ManagementAPI
	m.RejectAPICall = append(m.RejectAPICall, func(ctx context.Context, mp nip86.MethodParams) (bool, string) {
		if khatru.GetAuthed(ctx) != adminPubkey {
			return true, "restricted to the relay admin"
		}
		return false, ""
	})
	m.BanPubKey = func(ctx context.Context, pubkey, reason string) error { return s.block.ban(pubkey, reason) }
	m.AllowPubKey = func(ctx context.Context, pubkey, reason string) error { return s.block.allow(pubkey) }
	m.ListBannedPubKeys = func(ctx context.Context) ([]nip86.PubKeyReason, error) { return s.block.list(), nil }
	// Event takedown: delete the event from the store. (A determined author could
	// re-publish; ban the pubkey for a persistent block.)
	m.BanEvent = func(ctx context.Context, id, reason string) error {
		ch, err := store.QueryEvents(ctx, nostr.Filter{IDs: []string{id}})
		if err != nil {
			return err
		}
		for evt := range ch {
			if derr := store.DeleteEvent(ctx, evt); derr != nil {
				return derr
			}
		}
		return nil
	}
}
