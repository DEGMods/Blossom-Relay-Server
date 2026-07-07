package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BUD-Ads gate. Cooperative (not enforceable) — it funds the operator; PoW is the
// anti-abuse gate. The ad challenge is the same stateless HMAC token as PoW but
// without work; the proof echoes the token plus the shown ad id, which is recorded
// for best-effort, privacy-preserving metrics (advertisers reconcile their own).

func (s *Server) adChallenge(hash, ip string) string {
	exp := strconv.FormatInt(time.Now().Add(s.challengeTTL).Unix(), 10)
	rnd := make([]byte, 8)
	_, _ = rand.Read(rnd)
	return s.signToken("ad", hash, ip, exp, hex.EncodeToString(rnd))
}

// verifyAd validates "X-Blossom-Gate-Ad-Proof: <c>; ad=<id>" and returns the ad id.
func (s *Server) verifyAd(proofHeader, hash, ip string) (adID string, ok bool) {
	if proofHeader == "" {
		return "", false
	}
	segs := strings.SplitN(proofHeader, ";", 2)
	c := strings.TrimSpace(segs[0])
	if len(segs) == 2 {
		if kv := strings.TrimSpace(segs[1]); strings.HasPrefix(kv, "ad=") {
			adID = strings.TrimSpace(kv[3:])
		}
	}
	parts, valid := s.parseToken(c)
	if !valid || len(parts) != 5 || parts[0] != "ad" || parts[1] != hash || parts[2] != ip {
		return "", false
	}
	if exp, e := strconv.ParseInt(parts[3], 10, 64); e != nil || exp < time.Now().Unix() {
		return "", false
	}
	return adID, true
}

// ── metrics ──────────────────────────────────────────────────────────────────

// adStats is the persisted cumulative aggregate (no raw IPs, ever).
type adStats struct {
	Views  map[string]int64 `json:"views"`
	Clicks map[string]int64 `json:"clicks"`
}

// adMetrics counts unique views/clicks per ad id. Uniqueness is ephemeral: a
// per-window set of salted-hashed IPs (salt rotates per window, so no cross-window
// correlation and no raw IPs stored); only the cumulative counts are persisted.
type adMetrics struct {
	mu          sync.Mutex
	path        string
	window      time.Duration
	windowStart time.Time
	salt        []byte
	seenView    map[string]struct{}
	seenClick   map[string]struct{}
	stats       adStats
}

func newAdMetrics(path string) *adMetrics {
	m := &adMetrics{
		path:      path,
		window:    24 * time.Hour,
		seenView:  map[string]struct{}{},
		seenClick: map[string]struct{}{},
		stats:     adStats{Views: map[string]int64{}, Clicks: map[string]int64{}},
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &m.stats)
	}
	if m.stats.Views == nil {
		m.stats.Views = map[string]int64{}
	}
	if m.stats.Clicks == nil {
		m.stats.Clicks = map[string]int64{}
	}
	m.salt = make([]byte, 16)
	_, _ = rand.Read(m.salt)
	m.windowStart = time.Now()
	return m
}

// maybeRotate resets the unique-tracking window (caller holds the lock).
func (m *adMetrics) maybeRotate() {
	if time.Since(m.windowStart) > m.window {
		m.salt = make([]byte, 16)
		_, _ = rand.Read(m.salt)
		m.windowStart = time.Now()
		m.seenView = map[string]struct{}{}
		m.seenClick = map[string]struct{}{}
	}
}

func (m *adMetrics) ipHash(ip string) string {
	h := sha256.Sum256(append(append([]byte{}, m.salt...), ip...))
	return hex.EncodeToString(h[:8])
}

func (m *adMetrics) view(adID, ip string) {
	if adID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maybeRotate()
	k := adID + "|" + m.ipHash(ip)
	if _, ok := m.seenView[k]; ok {
		return
	}
	m.seenView[k] = struct{}{}
	m.stats.Views[adID]++
}

func (m *adMetrics) click(adID, ip string) {
	if adID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maybeRotate()
	k := adID + "|" + m.ipHash(ip)
	if _, ok := m.seenClick[k]; ok {
		return
	}
	m.seenClick[k] = struct{}{}
	m.stats.Clicks[adID]++
}

func (m *adMetrics) snapshot() adStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := adStats{Views: map[string]int64{}, Clicks: map[string]int64{}}
	for k, v := range m.stats.Views {
		out.Views[k] = v
	}
	for k, v := range m.stats.Clicks {
		out.Clicks[k] = v
	}
	return out
}

func (m *adMetrics) save() error {
	m.mu.Lock()
	data, _ := json.MarshalIndent(m.stats, "", "  ")
	m.mu.Unlock()
	return os.WriteFile(m.path, data, 0o600)
}

// ── handlers ─────────────────────────────────────────────────────────────────

func (s *Server) handleAdStats(w http.ResponseWriter, r *http.Request) {
	setGateCORS(w)
	writeJSON(w, http.StatusOK, s.metrics.snapshot())
}

func (s *Server) handleAdClick(w http.ResponseWriter, r *http.Request) {
	setGateCORS(w)
	var body struct {
		Ad string `json:"ad"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Ad == "" {
		httpErr(w, http.StatusBadRequest, "missing ad id")
		return
	}
	s.metrics.click(body.Ad, s.clientIP(r))
	w.WriteHeader(http.StatusNoContent)
}
