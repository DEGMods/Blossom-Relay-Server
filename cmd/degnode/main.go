// Command degnode runs a DEG Mods node: a Blossom blob server + mod-scoped Nostr
// relay, with pluggable storage and optional PoW/ad download gates.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/DEG-Mods/blossom-relay-server/internal/announce"
	"github.com/DEG-Mods/blossom-relay-server/internal/config"
	"github.com/DEG-Mods/blossom-relay-server/internal/identity"
	"github.com/DEG-Mods/blossom-relay-server/internal/server"
	"github.com/DEG-Mods/blossom-relay-server/internal/storage"
)

func main() {
	cfgPath := flag.String("config", "config.yml", "path to the config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}
	setupLogging(cfg)

	id, err := identity.Load(cfg.DataDir)
	if err != nil {
		slog.Error("identity load failed", "err", err)
		os.Exit(1)
	}
	if id.Created {
		fmt.Printf("\n  ► generated node identity\n    npub: %s\n    nsec saved to %s/identity.key — use it to publish manual-blossom-ads\n\n", id.Npub, cfg.DataDir)
	} else {
		fmt.Printf("  node identity npub: %s\n", id.Npub)
	}

	st, err := newStorage(cfg)
	if err != nil {
		slog.Error("storage init failed", "backend", cfg.Backend, "err", err)
		os.Exit(1)
	}

	srv, err := server.New(cfg, st, id.SecretKey, id.PubKey)
	if err != nil {
		slog.Error("server init failed", "err", err)
		os.Exit(1)
	}
	defer srv.Close()

	// Federation: announce this node's capabilities over Nostr (opt-in).
	if cfg.Announce.Enabled && len(cfg.Announce.Relays) > 0 {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pub := announce.New(announce.Node{
			URL:         cfg.PublicURL,
			Blossom:     true,
			Relay:       true,
			MaxUploadMB: cfg.Upload.MaxSizeMB,
			Kinds:       server.AcceptedModKinds(),
			Gates:       announce.Gates{Pow: cfg.Download.PoWDifficulty, Ad: cfg.Download.AdGate},
			Version:     1,
		}, id.SecretKey, cfg.Announce.Relays, time.Duration(cfg.Announce.IntervalHours)*time.Hour)
		go pub.Run(ctx)
		slog.Info("announce enabled", "relays", len(cfg.Announce.Relays), "interval_hours", cfg.Announce.IntervalHours)
	}

	slog.Info("node listening",
		"addr", cfg.Listen, "storage", cfg.Backend, "public_url", cfg.PublicURL,
		"pow_bits", cfg.Download.PoWDifficulty, "ad_gate", cfg.Download.AdGate)
	if err := http.ListenAndServe(cfg.Listen, srv.Handler()); err != nil {
		slog.Error("http server stopped", "err", err)
		os.Exit(1)
	}
}

// setupLogging installs the process-wide structured logger from config.
func setupLogging(c *config.Config) {
	var level slog.Level
	switch strings.ToLower(c.Log.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.ToLower(c.Log.Format) == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
}

// newStorage builds the blob backend selected by cfg.Backend.
func newStorage(cfg *config.Config) (storage.Storage, error) {
	switch cfg.Backend {
	case "s3":
		return storage.NewR2(storage.R2Config{
			Endpoint:  cfg.S3.Endpoint,
			Region:    cfg.S3.Region,
			Bucket:    cfg.S3.Bucket,
			AccessKey: cfg.S3.AccessKey,
			SecretKey: cfg.S3.SecretKey,
			UseSSL:    cfg.S3.UseSSL,
			PathStyle: cfg.S3.PathStyle,
		})
	case "disk":
		return storage.NewDisk(cfg.Disk.Path)
	default: // "r2"
		return storage.NewR2(storage.R2Config{
			Endpoint:  cfg.R2.Endpoint,
			Region:    cfg.R2.Region,
			Bucket:    cfg.R2.Bucket,
			AccessKey: cfg.R2.AccessKey,
			SecretKey: cfg.R2.SecretKey,
			UseSSL:    cfg.R2.UseSSL,
		})
	}
}
