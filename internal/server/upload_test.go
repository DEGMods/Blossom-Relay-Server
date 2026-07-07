package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/DEG-Mods/blossom-relay-server/internal/config"
	"github.com/DEG-Mods/blossom-relay-server/internal/storage"
	"github.com/nbd-wtf/go-nostr"
)

// fakeStorage records Store calls; the rest is inert.
type fakeStorage struct {
	mu     sync.Mutex
	stored map[string][]byte
}

func (f *fakeStorage) Store(_ context.Context, sha, ext, _ string, r io.Reader, _ int64) error {
	b, _ := io.ReadAll(r)
	f.mu.Lock()
	f.stored[sha+"."+ext] = b
	f.mu.Unlock()
	return nil
}
func (f *fakeStorage) Load(context.Context, string, string) (io.ReadSeekCloser, error) {
	return nil, storage.ErrNotFound
}
func (f *fakeStorage) Delete(context.Context, string, string) error { return nil }
func (f *fakeStorage) Has(context.Context, string, string) (bool, error) {
	return false, nil
}
func (f *fakeStorage) Stat(context.Context, string, string) (storage.StatInfo, error) {
	return storage.StatInfo{}, storage.ErrNotFound
}

func testServer(t *testing.T, fs storage.Storage) *Server {
	t.Helper()
	cfg := &config.Config{PublicURL: "https://test.example", DataDir: t.TempDir()}
	cfg.Upload.MaxSizeMB = 500
	cfg.Upload.MaxConcurrent = 4
	cfg.Upload.TempDir = "" // handler uses os temp via CreateTemp(dir="")
	srv, err := New(cfg, fs, "test-gate-secret", "testnodepubkeyhex")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv
}

func makeZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("hello deg mods"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func authHeader(t *testing.T, sk, hash string) string {
	t.Helper()
	evt := nostr.Event{
		Kind:      24242,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"t", "upload"},
			{"x", hash},
			{"expiration", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)},
		},
		Content: "upload",
	}
	if err := evt.Sign(sk); err != nil {
		t.Fatal(err)
	}
	j, _ := json.Marshal(evt)
	return "Nostr " + base64.StdEncoding.EncodeToString(j)
}

func put(t *testing.T, srv *Server, body []byte, auth string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/upload", bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

func TestUpload_HappyPath(t *testing.T) {
	fs := &fakeStorage{stored: map[string][]byte{}}
	srv := testServer(t, fs)

	zipBytes := makeZip(t)
	sum := hex.EncodeToString(sha256Sum(zipBytes))
	sk := nostr.GeneratePrivateKey()

	w := put(t, srv, zipBytes, authHeader(t, sk, sum))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := fs.stored[sum+".zip"]; !bytes.Equal(got, zipBytes) {
		t.Fatalf("stored bytes mismatch (len %d vs %d)", len(got), len(zipBytes))
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["sha256"] != sum {
		t.Fatalf("response sha256 = %v, want %s", resp["sha256"], sum)
	}
}

func TestUpload_NoAuth(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	if w := put(t, srv, makeZip(t), ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestUpload_HashMismatch(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	zipBytes := makeZip(t)
	sk := nostr.GeneratePrivateKey()
	wrong := hex.EncodeToString(sha256Sum([]byte("different")))
	if w := put(t, srv, zipBytes, authHeader(t, sk, wrong)); w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpload_NotZip(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	body := []byte("i am not a zip file, i am plain text")
	sum := hex.EncodeToString(sha256Sum(body))
	sk := nostr.GeneratePrivateKey()
	if w := put(t, srv, body, authHeader(t, sk, sum)); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("want 415, got %d: %s", w.Code, w.Body.String())
	}
}

func sha256Sum(b []byte) []byte {
	h := sha256.New()
	h.Write(b)
	return h.Sum(nil)
}
