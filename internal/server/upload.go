package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip13"
)

// ── auth ─────────────────────────────────────────────────────────────────────

// parseNostrAuth decodes "Authorization: Nostr <base64-json-event>".
func parseNostrAuth(r *http.Request) (*nostr.Event, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return nil, errors.New("missing Authorization header")
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Nostr") {
		return nil, errors.New("expected 'Authorization: Nostr <base64>'")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[1]))
	if err != nil {
		return nil, errors.New("invalid base64 in Authorization")
	}
	var evt nostr.Event
	if err := json.Unmarshal(raw, &evt); err != nil {
		return nil, errors.New("invalid auth event JSON")
	}
	return &evt, nil
}

func tagValue(evt *nostr.Event, name string) string {
	for _, t := range evt.Tags {
		if len(t) >= 2 && t[0] == name {
			return t[1]
		}
	}
	return ""
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// verifyUploadAuth validates a kind-24242 upload authorization and returns the
// claimed blob hash (its "x" tag).
func (s *Server) verifyUploadAuth(evt *nostr.Event) (hash string, err error) {
	if evt.Kind != 24242 {
		return "", errors.New("auth: wrong kind (want 24242)")
	}
	if tagValue(evt, "t") != "upload" {
		return "", errors.New("auth: not an 'upload' authorization")
	}
	exp := tagValue(evt, "expiration")
	if ts, e := strconv.ParseInt(exp, 10, 64); e != nil || ts < time.Now().Unix() {
		return "", errors.New("auth: missing or expired 'expiration'")
	}
	hash = tagValue(evt, "x")
	if !isSHA256Hex(hash) {
		return "", errors.New("auth: missing/invalid 'x' (blob sha256)")
	}
	if s.minUploadPoW > 0 && nip13.Difficulty(evt.ID) < s.minUploadPoW {
		return "", errors.New("auth: insufficient proof-of-work")
	}
	if ok, e := evt.CheckSignature(); e != nil || !ok {
		return "", errors.New("auth: bad signature")
	}
	return hash, nil
}

// ── zip validation ───────────────────────────────────────────────────────────

// verifyZip confirms the spooled file is an unencrypted ZIP (magic bytes), so we
// reject media and password-protected archives.
func verifyZip(f *os.File) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var hdr [8]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return errors.New("not a valid zip file")
	}
	// Local file header must start with PK\x03\x04.
	if !(hdr[0] == 'P' && hdr[1] == 'K' && hdr[2] == 0x03 && hdr[3] == 0x04) {
		return errors.New("only .zip files are accepted")
	}
	// General-purpose bit flag at offset 6 (little-endian); bit 0 = encrypted.
	if (uint16(hdr[6])|uint16(hdr[7])<<8)&0x0001 != 0 {
		return errors.New("encrypted zip archives are not accepted")
	}
	return nil
}

// ── http helpers ─────────────────────────────────────────────────────────────

func setUploadCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "PUT, HEAD, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, X-Sha256, X-SHA-256, X-Content-Type, X-Content-Length, Content-Type")
	h.Set("Access-Control-Expose-Headers", "X-Reason")
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("X-Reason", msg)
	http.Error(w, msg, code)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

var (
	errUploadTooLarge = errors.New("upload exceeds size limit")
	errUploadStalled  = errors.New("upload stalled")
)

// uploadLimitFor returns the max upload size for a pubkey — 5× the normal cap for
// whitelisted keys.
func (s *Server) uploadLimitFor(pubkey string) int64 {
	if s.white.has(pubkey) {
		return s.maxUploadBytes * 5
	}
	return s.maxUploadBytes
}

// streamBody copies the request body to dst, enforcing the size cap and an idle
// timeout that resets on every chunk. Slow-but-steady uploads are fine (each chunk
// resets the deadline); a stalled or trickle connection is aborted so it can't hold
// a concurrency slot + temp file indefinitely.
func (s *Server) streamBody(w http.ResponseWriter, r *http.Request, dst io.Writer, limit int64) (int64, error) {
	rc := http.NewResponseController(w)
	buf := make([]byte, 128*1024)
	var total int64
	for {
		if s.uploadIdleTimeout > 0 {
			// Best-effort: some ResponseWriters (e.g. httptest) don't support deadlines.
			_ = rc.SetReadDeadline(time.Now().Add(s.uploadIdleTimeout))
		}
		nr, er := r.Body.Read(buf)
		if nr > 0 {
			total += int64(nr)
			if total > limit {
				return total, errUploadTooLarge
			}
			if _, ew := dst.Write(buf[:nr]); ew != nil {
				return total, ew
			}
		}
		if er == io.EOF {
			return total, nil
		}
		if er != nil {
			if os.IsTimeout(er) {
				return total, errUploadStalled
			}
			return total, er
		}
	}
}

// ── handlers ─────────────────────────────────────────────────────────────────

// handleUploadHead is the BUD-06 pre-flight: validate auth + declared size so the
// client learns whether a 500 MB PUT will be accepted before sending it.
func (s *Server) handleUploadHead(w http.ResponseWriter, r *http.Request) {
	setUploadCORS(w)
	evt, err := parseNostrAuth(r)
	if err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if _, err := s.verifyUploadAuth(evt); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if s.blocked(evt.PubKey) {
		httpErr(w, http.StatusForbidden, "blocked")
		return
	}
	if sz := r.Header.Get("X-Content-Length"); sz != "" {
		if v, e := strconv.ParseInt(sz, 10, 64); e == nil && v > s.uploadLimitFor(evt.PubKey) {
			httpErr(w, http.StatusRequestEntityTooLarge, "file exceeds the size limit")
			return
		}
	}
	if free, ok := freeDiskMB(s.tempDir); ok && free < s.minFreeDiskMB {
		httpErr(w, http.StatusInsufficientStorage, "server is low on disk")
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleUploadPut streams the body to a temp file, hashing on the fly, verifies
// the hash against the auth, enforces zip-only + size + concurrency, then stores
// to R2. Memory stays flat regardless of file size.
func (s *Server) handleUploadPut(w http.ResponseWriter, r *http.Request) {
	setUploadCORS(w)
	ctx := r.Context()

	evt, err := parseNostrAuth(r)
	if err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	claimed, err := s.verifyUploadAuth(evt)
	if err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if s.blocked(evt.PubKey) {
		httpErr(w, http.StatusForbidden, "blocked")
		return
	}
	limit := s.uploadLimitFor(evt.PubKey)
	if r.ContentLength > limit {
		httpErr(w, http.StatusRequestEntityTooLarge, "file exceeds the size limit")
		return
	}
	if free, ok := freeDiskMB(s.tempDir); ok && free < s.minFreeDiskMB {
		httpErr(w, http.StatusInsufficientStorage, "server is low on disk")
		return
	}

	release, err := s.limiter.acquire(evt.PubKey)
	if err != nil {
		httpErr(w, http.StatusTooManyRequests, err.Error())
		return
	}
	defer release()

	tmp, err := os.CreateTemp(s.tempDir, "deg-up-*.tmp")
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "could not open temp file")
		return
	}
	tmpName := tmp.Name()
	defer func() { tmp.Close(); os.Remove(tmpName) }()

	// Stream → temp file + hasher, hard-capped in case Content-Length lied, with an
	// idle timeout so a stalled or trickle upload can't hold a slot/temp file forever.
	h := sha256.New()
	n, err := s.streamBody(w, r, io.MultiWriter(tmp, h), limit)
	if err == errUploadTooLarge {
		httpErr(w, http.StatusRequestEntityTooLarge, "file exceeds the size limit")
		return
	}
	if err == errUploadStalled {
		httpErr(w, http.StatusRequestTimeout, "upload stalled (no data received) — aborted")
		return
	}
	if err != nil {
		httpErr(w, http.StatusBadRequest, "error reading upload")
		return
	}

	sum := hex.EncodeToString(h.Sum(nil))
	if sum != claimed {
		httpErr(w, http.StatusBadRequest, "sha256 does not match the authorization")
		return
	}
	if err := verifyZip(tmp); err != nil {
		httpErr(w, http.StatusUnsupportedMediaType, err.Error())
		return
	}

	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		httpErr(w, http.StatusInternalServerError, "temp seek failed")
		return
	}
	if err := s.storage.Store(ctx, sum, "zip", "application/zip", tmp, n); err != nil {
		httpErr(w, http.StatusBadGateway, "storage write failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"url":      s.publicURL + "/" + sum + ".zip",
		"sha256":   sum,
		"size":     n,
		"type":     "application/zip",
		"uploaded": time.Now().Unix(),
	})
}
