package server

import (
	"context"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func mkEvent(kind int, ts int64, tags nostr.Tags) *nostr.Event {
	return &nostr.Event{Kind: kind, CreatedAt: nostr.Timestamp(ts), Tags: tags}
}

func TestRejectEvent(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	ctx := context.Background()
	now := int64(nostr.Now())

	cases := []struct {
		name   string
		evt    *nostr.Event
		reject bool
	}{
		{"current mod with d", mkEvent(currentModKind, now, nostr.Tags{{"d", "abc"}, {"title", "x"}}), false},
		{"current mod missing d", mkEvent(currentModKind, now, nostr.Tags{{"title", "x"}}), true},
		{"legacy GameMod before cutoff", mkEvent(legacyModKind, legacyCutoff-1000, nostr.Tags{{"t", "GameMod"}}), false},
		{"legacy without GameMod", mkEvent(legacyModKind, legacyCutoff-1000, nostr.Tags{{"t", "other"}}), true},
		{"legacy after cutoff", mkEvent(legacyModKind, legacyCutoff+1000, nostr.Tags{{"t", "GameMod"}}), true},
		{"other kind", mkEvent(1, now, nil), true},
	}
	for _, c := range cases {
		if rej, msg := srv.rejectEvent(ctx, c.evt); rej != c.reject {
			t.Errorf("%s: reject=%v want %v (msg=%q)", c.name, rej, c.reject, msg)
		}
	}

	// Blocklisted pubkey is rejected even for a valid mod event.
	srv.block.ban("deadbeef", "test")
	blocked := mkEvent(currentModKind, now, nostr.Tags{{"d", "abc"}})
	blocked.PubKey = "deadbeef"
	if rej, _ := srv.rejectEvent(ctx, blocked); !rej {
		t.Error("blocked pubkey should be rejected")
	}
}

func TestRejectFilter(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	ctx := context.Background()

	if rej, _ := srv.rejectFilter(ctx, nostr.Filter{Kinds: []int{currentModKind}}); rej {
		t.Error("mod-kind filter should be allowed")
	}
	if rej, _ := srv.rejectFilter(ctx, nostr.Filter{Kinds: []int{1}}); !rej {
		t.Error("non-mod-kind filter should be rejected")
	}
	if rej, _ := srv.rejectFilter(ctx, nostr.Filter{}); rej {
		t.Error("empty filter should be allowed")
	}
}
