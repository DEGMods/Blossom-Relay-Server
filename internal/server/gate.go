package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/bits"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// BUD-POW download gate. When enabled, blob GET/HEAD requires a proof-of-work
// bound to (blob, client IP, expiry). Challenges are stateless (HMAC), so no
// per-challenge server state is kept; a proof is reusable until it expires, which
// covers ranged/resumed downloads.

// blobHashFromPath extracts the sha256 from "/<sha256>[.ext]".
func blobHashFromPath(p string) (string, bool) {
	p = strings.TrimPrefix(p, "/")
	if dot := strings.IndexByte(p, '.'); dot >= 0 {
		p = p[:dot]
	}
	if isSHA256Hex(p) {
		return p, true
	}
	return "", false
}

// clientIP resolves the caller IP, honoring a trusted forwarded header if set.
func (s *Server) clientIP(r *http.Request) string {
	if s.trustedIPHeader != "" {
		if v := r.Header.Get(s.trustedIPHeader); v != "" {
			return strings.TrimSpace(strings.SplitN(v, ",", 2)[0])
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func leadingZeroBits(b []byte) int {
	n := 0
	for _, x := range b {
		if x == 0 {
			n += 8
			continue
		}
		n += bits.LeadingZeros8(x)
		break
	}
	return n
}

// powChallenge builds a stateless challenge token c bound to (hash, ip, exp, d).
func (s *Server) powChallenge(hash, ip string) string {
	exp := time.Now().Add(s.challengeTTL).Unix()
	rnd := make([]byte, 8)
	_, _ = rand.Read(rnd)
	payload := fmt.Sprintf("%s|%s|%d|%d|%s", hash, ip, exp, s.powDifficulty, hex.EncodeToString(rnd))
	mac := hmac.New(sha256.New, s.gateKey)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyPow validates "<c>:<nonce>" for the given blob + IP.
func (s *Server) verifyPow(proof, hash, ip string) bool {
	sep := strings.LastIndexByte(proof, ':')
	if sep < 0 {
		return false
	}
	c := proof[:sep]

	dot := strings.IndexByte(c, '.')
	if dot < 0 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(c[:dot])
	if err != nil {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(c[dot+1:])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, s.gateKey)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return false
	}

	parts := strings.Split(string(payload), "|")
	if len(parts) != 5 {
		return false
	}
	if parts[0] != hash || parts[1] != ip {
		return false
	}
	if exp, e := strconv.ParseInt(parts[2], 10, 64); e != nil || exp < time.Now().Unix() {
		return false
	}
	d, e := strconv.Atoi(parts[3])
	if e != nil {
		return false
	}
	sum := sha256.Sum256([]byte(proof)) // proof == c ":" nonce
	return leadingZeroBits(sum[:]) >= d
}

// signToken builds "<base64url(payload)>.<base64url(hmac)>" over "|"-joined parts.
func (s *Server) signToken(parts ...string) string {
	payload := strings.Join(parts, "|")
	mac := hmac.New(sha256.New, s.gateKey)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// parseToken verifies the HMAC and returns the payload fields.
func (s *Server) parseToken(c string) ([]string, bool) {
	dot := strings.IndexByte(c, '.')
	if dot < 0 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(c[:dot])
	if err != nil {
		return nil, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(c[dot+1:])
	if err != nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, s.gateKey)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, false
	}
	return strings.Split(string(payload), "|"), true
}

func setGateCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "X-Blossom-Gate-Pow-Proof, X-Blossom-Gate-Ad-Proof, Range")
	h.Set("Access-Control-Expose-Headers", "X-Blossom-Gate-Pow, X-Blossom-Gate-Ad, X-Reason, Accept-Ranges, Content-Length")
}

// gate wraps the blob handler with the download gates (PoW + ad). No-op when both
// are off. Both can be required together (the "two-birds" flow); a 428 carries a
// challenge header per unsatisfied gate.
func (s *Server) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hash, isBlob := blobHashFromPath(r.URL.Path)
		if !isBlob {
			next.ServeHTTP(w, r)
			return
		}
		setGateCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && (s.powDifficulty > 0 || s.adGate) {
			if !s.checkGates(w, r, hash) {
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// checkGates enforces the enabled gates; returns false (and writes 428) if unmet.
func (s *Server) checkGates(w http.ResponseWriter, r *http.Request, hash string) bool {
	ip := s.clientIP(r)
	challenged := false

	if s.powDifficulty > 0 {
		if !s.verifyPow(r.Header.Get("X-Blossom-Gate-Pow-Proof"), hash, ip) {
			w.Header().Set("X-Blossom-Gate-Pow",
				fmt.Sprintf("v=1; d=%d; c=%s", s.powDifficulty, s.powChallenge(hash, ip)))
			challenged = true
		}
	}
	if s.adGate {
		if id, ok := s.verifyAd(r.Header.Get("X-Blossom-Gate-Ad-Proof"), hash, ip); ok {
			s.metrics.view(id, ip)
		} else {
			w.Header().Set("X-Blossom-Gate-Ad",
				fmt.Sprintf("v=1; ref=%s; min=%d; c=%s", s.adRef, s.adMinMs, s.adChallenge(hash, ip)))
			challenged = true
		}
	}
	if challenged {
		httpErr(w, http.StatusPreconditionRequired, "gate requirements not satisfied")
		return false
	}
	return true
}
