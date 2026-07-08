package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DEG-Mods/blossom-relay-server/internal/config"
	"github.com/nbd-wtf/go-nostr"
)

func deleteAuth(t *testing.T, sk, hash, verb string, exp int64) string {
	t.Helper()
	evt := nostr.Event{
		Kind:      24242,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"t", verb}, {"x", hash}, {"expiration", strconv.FormatInt(exp, 10)}},
		Content:   verb,
	}
	if err := evt.Sign(sk); err != nil {
		t.Fatal(err)
	}
	j, _ := json.Marshal(evt)
	return "Nostr " + base64.StdEncoding.EncodeToString(j)
}

func TestDelete_AdminOnly(t *testing.T) {
	adminSK := nostr.GeneratePrivateKey()
	adminPK, _ := nostr.GetPublicKey(adminSK)

	fs := &fakeStorage{stored: map[string][]byte{}}
	cfg := &config.Config{PublicURL: "https://test.example", DataDir: tempDataDir(t)}
	cfg.Upload.MaxSizeMB = 500
	cfg.Upload.MaxConcurrent = 4
	cfg.Relay.AdminNpub = adminPK // hex pubkey; resolvePubkey passes it through
	srv, err := New(cfg, fs, "gate-secret", "nodepubkeyhex000000000000000000000000000000000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	hash := strings.Repeat("a", 64)
	future := time.Now().Add(time.Hour).Unix()

	del := func(auth string) int {
		req := httptest.NewRequest(http.MethodDelete, "/"+hash, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w.Code
	}

	if code := del(deleteAuth(t, adminSK, hash, "delete", future)); code != http.StatusOK {
		t.Fatalf("admin delete: want 200, got %d", code)
	}
	if code := del(deleteAuth(t, nostr.GeneratePrivateKey(), hash, "delete", future)); code != http.StatusUnauthorized {
		t.Fatalf("non-admin delete: want 401, got %d", code)
	}
	if code := del(deleteAuth(t, adminSK, hash, "upload", future)); code != http.StatusUnauthorized {
		t.Fatalf("wrong verb: want 401, got %d", code)
	}
	if code := del(deleteAuth(t, adminSK, hash, "delete", time.Now().Add(-time.Hour).Unix())); code != http.StatusUnauthorized {
		t.Fatalf("expired: want 401, got %d", code)
	}
	if code := del(deleteAuth(t, adminSK, strings.Repeat("b", 64), "delete", future)); code != http.StatusUnauthorized {
		t.Fatalf("x-mismatch (auth for a different blob): want 401, got %d", code)
	}
	if code := del(""); code != http.StatusUnauthorized {
		t.Fatalf("no auth: want 401, got %d", code)
	}
}
