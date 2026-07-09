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
	if s.bannedEv.has(moderationKey(evt)) {
		return true, "blocked: this event was removed by a moderator"
	}
	// An author's NIP-09 delete is persistent: reject re-stores (incl. re-ingest)
	// of the deleted coordinate up to the deletion's timestamp. A newer revision at
	// the same coordinate is still accepted (created_at moves past the record).
	if dt := s.deletions.deletedAt(moderationKey(evt)); dt > 0 && int64(evt.CreatedAt) <= dt {
		return true, "blocked: this event was deleted by its author"
	}
	switch evt.Kind {
	case 5:
		// NIP-09 deletion request. khatru applies its effect before this policy
		// runs (see onDeletionOutcome); accept it so the author gets an OK and the
		// deletion is retained (briefly — pruned by RunDeletionSweep).
		return false, ""
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
	// Persist author-initiated NIP-09 deletions so re-ingest can't resurrect them.
	r.OverwriteDeletionOutcome = append(r.OverwriteDeletionOutcome, s.onDeletionOutcome)

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
	// Event takedowns are handled by the admin API (persistent, address-based, with
	// auto-reject on re-publish), not one-shot NIP-86 banevent.
}
