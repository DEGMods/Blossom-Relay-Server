// Package server wires the khatru relay + Blossom handler to the storage backend.
//
//   - Blossom: GET/HEAD/list/delete from R2 via khatru; a streaming PUT/HEAD
//     /upload handler sits in front of the relay (khatru's buffered upload is
//     unusable for 500 MB files).
//   - Relay: mod-scoped (kinds 31142 + legacy 30402), persisted in an embedded
//     badger event store, with NIP-86 admin.
package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/DEG-Mods/blossom-relay-server/internal/config"
	"github.com/DEG-Mods/blossom-relay-server/internal/storage"
	"github.com/fiatjaf/eventstore/badger"
	"github.com/fiatjaf/khatru"
	"github.com/fiatjaf/khatru/blossom"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

type Server struct {
	relay   *khatru.Relay
	bl      *blossom.BlossomServer
	handler http.Handler
	store   *badger.BadgerBackend

	storage        storage.Storage
	publicURL      string
	tempDir        string
	maxUploadBytes int64
	minFreeDiskMB  int64
	minUploadPoW   int
	minEventPoW    int
	limiter        *uploadLimiter
	block          *blocklist
	adminPubkey    string // hex; the only key allowed to delete blobs (moderation)

	// download gates (BUD-POW + BUD-Ads)
	powDifficulty   int
	challengeTTL    time.Duration
	trustedIPHeader string
	gateKey         []byte
	adGate          bool
	adMinMs         int
	adRef           string
	metrics         *adMetrics
	metricsStop     chan struct{}
}

// New builds the node's HTTP handler (streaming upload + blossom + mod relay).
// gateSecret seeds the stateless challenge HMAC key (the node's secret key);
// nodePubkey (hex) is the operator identity used for the ad inventory reference.
func New(cfg *config.Config, st storage.Storage, gateSecret, nodePubkey string) (*Server, error) {
	relay := khatru.NewRelay()

	bl := blossom.New(relay, cfg.PublicURL)
	bl.Store = &r2Index{st: st, publicURL: cfg.PublicURL}
	bl.LoadBlob = append(bl.LoadBlob, func(ctx context.Context, sha256, ext string) (io.ReadSeeker, error) {
		rc, err := st.Load(ctx, sha256, ext)
		if err != nil {
			return nil, err
		}
		return rc, nil // *minio.Object is also an io.Closer; khatru closes ReadSeekers that implement it
	})
	bl.DeleteBlob = append(bl.DeleteBlob, func(ctx context.Context, sha256, ext string) error {
		return st.Delete(ctx, sha256, ext)
	})
	// Disable khatru's own (buffered) upload — we serve /upload ourselves.
	bl.RejectUpload = append(bl.RejectUpload, func(ctx context.Context, auth *nostr.Event, size int, ext string) (bool, string, int) {
		return true, "use this server's streaming upload endpoint", http.StatusServiceUnavailable
	})

	// Embedded persistent event store for the mod relay.
	store := &badger.BadgerBackend{Path: filepath.Join(cfg.DataDir, "events")}
	if err := store.Init(); err != nil {
		return nil, fmt.Errorf("event store: %w", err)
	}

	s := &Server{
		relay:          relay,
		bl:             bl,
		store:          store,
		storage:        st,
		publicURL:      cfg.PublicURL,
		tempDir:        cfg.Upload.TempDir,
		maxUploadBytes: int64(cfg.Upload.MaxSizeMB) * 1024 * 1024,
		minFreeDiskMB:  cfg.Upload.MinFreeDiskMB,
		minUploadPoW:   cfg.Upload.MinPoW,
		minEventPoW:    cfg.Relay.MinEventPoW,
		limiter:        newUploadLimiter(cfg.Upload.MaxConcurrent),
		block:          loadBlocklist(filepath.Join(cfg.DataDir, "blocklist.json")),
		adminPubkey:    resolvePubkey(cfg.Relay.AdminNpub),

		powDifficulty:   cfg.Download.PoWDifficulty,
		challengeTTL:    time.Duration(cfg.Download.ChallengeTTL) * time.Second,
		trustedIPHeader: cfg.Download.TrustedIPHeader,
		gateKey:         deriveGateKey(gateSecret),
		adGate:          cfg.Download.AdGate,
		adMinMs:         cfg.Download.AdMinMs,
	}
	if s.adGate {
		s.adRef = "30078:" + nodePubkey + ":manual-blossom-ads"
		s.metrics = newAdMetrics(filepath.Join(cfg.DataDir, "ad_stats.json"))
		s.metricsStop = make(chan struct{})
		go s.metricsSaver()
	}

	s.setupRelay(store, s.adminPubkey)

	// Streaming /upload in front; everything else (blossom GET, relay WS, NIP-11,
	// NIP-86) → the relay.
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /upload", s.handleUploadPut)
	mux.HandleFunc("HEAD /upload", s.handleUploadHead)
	mux.HandleFunc("OPTIONS /upload", func(w http.ResponseWriter, r *http.Request) {
		setUploadCORS(w)
		w.WriteHeader(http.StatusNoContent)
	})
	if s.adGate {
		mux.HandleFunc("GET /ads/stats", s.handleAdStats)
		mux.HandleFunc("POST /ads/click", s.handleAdClick)
		mux.HandleFunc("OPTIONS /ads/click", func(w http.ResponseWriter, r *http.Request) {
			setGateCORS(w)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	// Admin-only blob deletion (our streaming upload bypasses khatru's blob index,
	// so we handle DELETE ourselves rather than via the relay's blossom handler).
	mux.HandleFunc("DELETE /{hash}", s.handleDelete)
	mux.Handle("/", s.gate(relay)) // BUD-POW/BUD-Ads gate on blob GET; pass-through otherwise
	s.handler = mux

	return s, nil
}

func (s *Server) metricsSaver() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			_ = s.metrics.save()
		case <-s.metricsStop:
			return
		}
	}
}

func deriveGateKey(secret string) []byte {
	sum := sha256.Sum256([]byte("deg-mods-gate|" + secret))
	return sum[:]
}

// Handler returns the node's combined HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

// Close stops the metrics saver and releases the event store.
func (s *Server) Close() {
	if s.metricsStop != nil {
		close(s.metricsStop)
		s.metricsStop = nil
		_ = s.metrics.save()
	}
	if s.store != nil {
		s.store.Close()
	}
}

func (s *Server) blocked(pubkey string) bool { return s.block.has(pubkey) }

// resolvePubkey accepts an npub or a hex pubkey and returns hex ("" if empty/bad).
func resolvePubkey(s string) string {
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "npub1") {
		if _, v, err := nip19.Decode(s); err == nil {
			if hex, ok := v.(string); ok {
				return hex
			}
		}
		return ""
	}
	return s
}
