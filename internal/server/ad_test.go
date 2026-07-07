package server

import (
	"strings"
	"testing"
)

func TestAd_ChallengeVerify(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	hash := strings.Repeat("a", 64)
	ip := "1.2.3.4"

	c := srv.adChallenge(hash, ip)
	proof := c + "; ad=banner-1"

	id, ok := srv.verifyAd(proof, hash, ip)
	if !ok || id != "banner-1" {
		t.Fatalf("valid ad proof: ok=%v id=%q", ok, id)
	}
	if _, ok := srv.verifyAd(proof, hash, "9.9.9.9"); ok {
		t.Fatal("ad proof bound to a different IP must fail")
	}
	if _, ok := srv.verifyAd(proof, strings.Repeat("b", 64), ip); ok {
		t.Fatal("ad proof bound to a different blob must fail")
	}
	if _, ok := srv.verifyAd(c+"x; ad=banner-1", hash, ip); ok {
		t.Fatal("tampered ad challenge must fail")
	}
}

func TestAdMetrics_UniqueDedup(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	m := newAdMetrics(t.TempDir() + "/ad_stats.json")
	_ = srv

	m.view("banner-1", "1.2.3.4")
	m.view("banner-1", "1.2.3.4") // same IP+ad in window → not double counted
	m.view("banner-1", "5.6.7.8") // different IP → counted
	m.view("banner-2", "1.2.3.4")
	m.click("banner-1", "1.2.3.4")
	m.click("banner-1", "1.2.3.4") // deduped

	s := m.snapshot()
	if s.Views["banner-1"] != 2 {
		t.Fatalf("banner-1 unique views = %d, want 2", s.Views["banner-1"])
	}
	if s.Views["banner-2"] != 1 {
		t.Fatalf("banner-2 views = %d, want 1", s.Views["banner-2"])
	}
	if s.Clicks["banner-1"] != 1 {
		t.Fatalf("banner-1 clicks = %d, want 1", s.Clicks["banner-1"])
	}
}
