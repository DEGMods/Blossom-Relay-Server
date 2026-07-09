package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DEG-Mods/blossom-relay-server/internal/config"
	"github.com/nbd-wtf/go-nostr"
)

func TestAds_NormalizeAndBuild(t *testing.T) {
	ads := normalizeAds([]adItem{
		{ID: "a", Media: "hash1", Weight: 0}, // weight defaults to 1
		{ID: "", Media: "hash2"},             // dropped: no id
		{ID: "b", Media: ""},                 // dropped: no media
		{ID: "c", Media: "hash3", Weight: 5},
	})
	if len(ads) != 2 {
		t.Fatalf("want 2 valid ads, got %d", len(ads))
	}
	if ads[0].Weight != 1 {
		t.Fatalf("weight should default to 1, got %d", ads[0].Weight)
	}
	if ads[1].Weight != 5 {
		t.Fatalf("explicit weight should survive, got %d", ads[1].Weight)
	}

	sk := nostr.GeneratePrivateKey()
	pk, _ := nostr.GetPublicKey(sk)
	ev, err := buildAdInventoryEvent(ads, sk, nostr.Now())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != 30078 {
		t.Fatalf("kind: want 30078, got %d", ev.Kind)
	}
	if ev.Tags.GetD() != adInventoryDTag {
		t.Fatalf("d tag: want %q, got %q", adInventoryDTag, ev.Tags.GetD())
	}
	if ev.PubKey != pk {
		t.Fatal("event must be signed by the node key")
	}
	if ok, _ := ev.CheckSignature(); !ok {
		t.Fatal("signature invalid")
	}
	var content adInventoryContent
	if err := json.Unmarshal([]byte(ev.Content), &content); err != nil {
		t.Fatal(err)
	}
	if len(content.Ads) != 2 {
		t.Fatalf("content should hold 2 ads, got %d", len(content.Ads))
	}
}

func TestAds_AdminPutThenGet(t *testing.T) {
	nodeSK := nostr.GeneratePrivateKey()
	nodePK, _ := nostr.GetPublicKey(nodeSK)
	adminSK := nostr.GeneratePrivateKey()
	adminPK, _ := nostr.GetPublicKey(adminSK)

	cfg := &config.Config{PublicURL: "https://test.example", DataDir: tempDataDir(t)}
	cfg.Upload.MaxSizeMB = 500
	cfg.Upload.MaxConcurrent = 4
	cfg.Relay.AdminNpub = adminPK
	srv, err := New(cfg, &fakeStorage{stored: map[string][]byte{}}, nodeSK, nodePK)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	// PUT the inventory as the admin (no publish relays configured → 0 broadcast).
	body := `{"ads":[{"id":"a","media":"hash1","link":"example.com","weight":2}]}`
	req := httptest.NewRequest(http.MethodPut, "/admin/ads", strings.NewReader(body))
	req.Header.Set("Authorization", nip98(t, adminSK, "PUT", "https://test.example/admin/ads", nostr.Now()))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: want 200, got %d (%s)", w.Code, w.Body.String())
	}

	// A non-admin PUT is rejected.
	req2 := httptest.NewRequest(http.MethodPut, "/admin/ads", strings.NewReader(body))
	req2.Header.Set("Authorization", nip98(t, nostr.GeneratePrivateKey(), "PUT", "https://test.example/admin/ads", nostr.Now()))
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("non-admin PUT: want 401, got %d", w2.Code)
	}

	// GET echoes the stored inventory + node-signed event.
	req3 := httptest.NewRequest(http.MethodGet, "/admin/ads", nil)
	req3.Header.Set("Authorization", nip98(t, adminSK, "GET", "https://test.example/admin/ads", nostr.Now()))
	w3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("GET: want 200, got %d", w3.Code)
	}
	var resp struct {
		Ref   string       `json:"ref"`
		Ads   []adItem     `json:"ads"`
		Event *nostr.Event `json:"event"`
	}
	if err := json.Unmarshal(w3.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Ref != "30078:"+nodePK+":"+adInventoryDTag {
		t.Fatalf("ref mismatch: %s", resp.Ref)
	}
	if len(resp.Ads) != 1 || resp.Ads[0].ID != "a" || resp.Ads[0].Weight != 2 {
		t.Fatalf("stored ads unexpected: %+v", resp.Ads)
	}
	if resp.Event == nil || resp.Event.PubKey != nodePK {
		t.Fatal("GET should return the node-signed inventory event")
	}

	// The inventory must be queryable from brs's own store (the gate client reads
	// it here), and rejectFilter must permit that kind.
	if rej, _ := srv.rejectFilter(context.Background(), nostr.Filter{Kinds: []int{adInventoryKind}}); rej {
		t.Fatal("rejectFilter must allow reading the ad inventory kind")
	}
	ch, err := srv.store.QueryEvents(context.Background(), nostr.Filter{
		Kinds:   []int{adInventoryKind},
		Authors: []string{nodePK},
		Tags:    nostr.TagMap{"d": []string{adInventoryDTag}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored := <-ch; stored == nil {
		t.Fatal("inventory event must be stored in brs so the relay can serve it")
	}
}

func TestAds_RejectFilterStillMostlyModOnly(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	// A random non-mod, non-inventory kind stays rejected.
	if rej, _ := srv.rejectFilter(context.Background(), nostr.Filter{Kinds: []int{1}}); !rej {
		t.Fatal("non-mod, non-inventory kinds must still be rejected")
	}
}
