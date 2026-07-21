package server

import (
	"context"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

// Moderation tags (kind 30985) apply tags to *other people's* posts, so the
// write policy is a single-pubkey allowlist rather than the PoW spam floor the
// open kinds use.
func TestModerationTags_AdminOnly(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	adminPK, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	otherPK, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	ctx := context.Background()

	tagEvent := func(pk, d string) *nostr.Event {
		tags := nostr.Tags{}
		if d != "" {
			tags = append(tags, nostr.Tag{"d", d})
		}
		return &nostr.Event{Kind: moderationTagKind, PubKey: pk, CreatedAt: nostr.Now(), Tags: tags}
	}

	// With no admin configured there is nobody to trust, so the kind is closed.
	if rej, _ := srv.rejectEvent(ctx, tagEvent(adminPK, "31142:abc:my-mod")); !rej {
		t.Fatal("expected rejection while no relay admin is configured")
	}

	srv.adminPubkey = adminPK

	if rej, msg := srv.rejectEvent(ctx, tagEvent(adminPK, "31142:abc:my-mod")); rej {
		t.Fatalf("admin moderation tag rejected: %s", msg)
	}
	if rej, _ := srv.rejectEvent(ctx, tagEvent(otherPK, "31142:abc:my-mod")); !rej {
		t.Fatal("a non-admin must not be able to tag someone else's post")
	}
	if rej, _ := srv.rejectEvent(ctx, tagEvent(adminPK, "")); !rej {
		t.Fatal("expected rejection: addressable event with no d tag")
	}

	// PoW is deliberately not applied here — the pubkey check already does the
	// job, and a moderator shouldn't have to mine to hide something.
	srv.minEventPoW = 30
	if rej, msg := srv.rejectEvent(ctx, tagEvent(adminPK, "31142:abc:my-mod")); rej {
		t.Fatalf("moderation tag should be PoW-exempt, got: %s", msg)
	}
}

// Readers are unrestricted: anyone must be able to fetch the overlays, or the
// tags the admin applied would have no effect in clients.
func TestModerationTags_Readable(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	ctx := context.Background()

	if rej, msg := srv.rejectFilter(ctx, nostr.Filter{Kinds: []int{moderationTagKind}}); rej {
		t.Fatalf("moderation tag reads rejected: %s", msg)
	}
}
