package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// Blob deletion is admin-only (content moderation). Because our streaming /upload
// bypasses khatru's blossom blob index, khatru's own DELETE (which authorizes via
// that index) can't work here — so we verify a kind-24242 "delete" authorization
// signed by the configured admin key and remove the object from storage directly.
// There is no automatic pruning; this is the sole removal path.

// verifyDeleteAuth validates a kind-24242 delete authorization for the given blob,
// requiring it to be signed by the node admin.
func (s *Server) verifyDeleteAuth(evt *nostr.Event, hash string) error {
	if evt.Kind != 24242 {
		return errors.New("auth: wrong kind (want 24242)")
	}
	if tagValue(evt, "t") != "delete" {
		return errors.New("auth: not a 'delete' authorization")
	}
	exp := tagValue(evt, "expiration")
	if ts, e := strconv.ParseInt(exp, 10, 64); e != nil || ts < time.Now().Unix() {
		return errors.New("auth: missing or expired 'expiration'")
	}
	if tagValue(evt, "x") != hash {
		return errors.New("auth: 'x' does not match the blob")
	}
	if ok, e := evt.CheckSignature(); e != nil || !ok {
		return errors.New("auth: bad signature")
	}
	if s.adminPubkey == "" || evt.PubKey != s.adminPubkey {
		return errors.New("auth: deletion is restricted to the node admin")
	}
	return nil
}

// handleDelete removes a blob after verifying an admin delete authorization.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	setGateCORS(w)
	hash, ok := blobHashFromPath(r.URL.Path)
	if !ok {
		httpErr(w, http.StatusBadRequest, "invalid blob path")
		return
	}
	evt, err := parseNostrAuth(r)
	if err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err := s.verifyDeleteAuth(evt, hash); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	// ext "" lets the storage layer resolve the actual object by hash prefix.
	if err := s.storage.Delete(r.Context(), hash, ""); err != nil {
		httpErr(w, http.StatusBadGateway, "storage delete failed")
		return
	}
	slog.Info("blob deleted", "hash", hash, "admin", evt.PubKey)
	w.WriteHeader(http.StatusOK)
}
