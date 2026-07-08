package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DEG-Mods/blossom-relay-server/internal/storage"
)

// Admin API — moderation/management endpoints authenticated via NIP-98 (a signed
// kind-27235 event) whose pubkey must equal the configured admin. The admin signs
// in their browser (NIP-07), so no raw nsec is ever handled on the server/CLI.

const blobCacheTTL = 30 * time.Second

// verifyAdmin authenticates a NIP-98 request signed by the admin key.
func (s *Server) verifyAdmin(r *http.Request) error {
	if s.adminPubkey == "" {
		return errors.New("admin API disabled (set relay.admin_npub)")
	}
	evt, err := parseNostrAuth(r)
	if err != nil {
		return err
	}
	if evt.Kind != 27235 {
		return errors.New("auth: expected NIP-98 (kind 27235)")
	}
	if d := time.Now().Unix() - int64(evt.CreatedAt); d < -60 || d > 60 {
		return errors.New("auth: timestamp too far from now")
	}
	if !strings.EqualFold(tagValue(evt, "method"), r.Method) {
		return errors.New("auth: method mismatch")
	}
	if pu, e := url.Parse(tagValue(evt, "u")); e != nil || pu.Path != r.URL.Path {
		return errors.New("auth: url mismatch")
	}
	if ok, e := evt.CheckSignature(); e != nil || !ok {
		return errors.New("auth: bad signature")
	}
	if evt.PubKey != s.adminPubkey {
		return errors.New("auth: not the admin key")
	}
	return nil
}

func setAdminCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	h.Set("Access-Control-Expose-Headers", "X-Reason")
}

// listBlobsCached returns the full blob listing, refreshed at most every
// blobCacheTTL so paging/searching doesn't re-scan storage on every click.
func (s *Server) listBlobsCached(ctx context.Context) ([]storage.BlobInfo, error) {
	s.blobCacheMu.Lock()
	defer s.blobCacheMu.Unlock()
	if s.blobCache != nil && time.Since(s.blobCacheAt) < blobCacheTTL {
		return s.blobCache, nil
	}
	list, err := s.storage.List(ctx)
	if err != nil {
		return nil, err
	}
	s.blobCache = list
	s.blobCacheAt = time.Now()
	return list, nil
}

func (s *Server) invalidateBlobCache() {
	s.blobCacheMu.Lock()
	s.blobCache = nil
	s.blobCacheMu.Unlock()
}

// handleAdminBlobs lists stored blobs (name + size), filtered by an optional
// substring `search` on the hash, with numbered pagination.
func (s *Server) handleAdminBlobs(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	all, err := s.listBlobsCached(r.Context())
	if err != nil {
		httpErr(w, http.StatusBadGateway, "storage list failed")
		return
	}

	q := r.URL.Query()
	search := strings.ToLower(strings.TrimSpace(q.Get("search")))
	filtered := make([]storage.BlobInfo, 0, len(all))
	for _, b := range all {
		if search == "" || strings.Contains(strings.ToLower(b.Hash), search) {
			filtered = append(filtered, b)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Hash < filtered[j].Hash })

	per := clampInt(atoiOr(q.Get("per"), 50), 1, 200)
	total := len(filtered)
	pages := (total + per - 1) / per
	page := clampInt(atoiOr(q.Get("page"), 1), 1, maxInt(pages, 1))
	start := (page - 1) * per
	if start > total {
		start = total
	}
	end := minInt(start+per, total)

	type blobDTO struct {
		Hash string `json:"hash"`
		Ext  string `json:"ext"`
		Size int64  `json:"size"`
		URL  string `json:"url"`
	}
	items := make([]blobDTO, 0, end-start)
	for _, b := range filtered[start:end] {
		items = append(items, blobDTO{Hash: b.Hash, Ext: b.Ext, Size: b.Size, URL: s.publicURL + "/" + b.Key})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total": total, "page": page, "per": per, "pages": pages, "blobs": items,
	})
}

// handleAdminWhitelistList returns the upload-size whitelist + the two size caps.
func (s *Server) handleAdminWhitelistList(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	mb := s.maxUploadBytes / (1024 * 1024)
	writeJSON(w, http.StatusOK, map[string]any{
		"limit_mb":       mb,
		"whitelisted_mb": mb * 5,
		"entries":        s.white.list(),
	})
}

func (s *Server) handleAdminWhitelistAdd(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var body struct {
		Pubkey string `json:"pubkey"`
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	pk := resolvePubkey(strings.TrimSpace(body.Pubkey))
	if !isSHA256Hex(pk) {
		httpErr(w, http.StatusBadRequest, "invalid pubkey/npub")
		return
	}
	if err := s.white.add(pk, strings.TrimSpace(body.Note)); err != nil {
		httpErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminWhitelistRemove(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var body struct {
		Pubkey string `json:"pubkey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.white.remove(resolvePubkey(strings.TrimSpace(body.Pubkey))); err != nil {
		httpErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── small helpers ─────────────────────────────────────────────────────────────

func atoiOr(s string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return v
	}
	return def
}

func clampInt(v, lo, hi int) int { return maxInt(lo, minInt(v, hi)) }

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
