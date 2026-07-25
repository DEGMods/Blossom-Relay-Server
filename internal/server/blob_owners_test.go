package server

import (
	"path/filepath"
	"testing"
)

func TestBlobOwnersRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blob_owners.json")
	o := loadBlobOwners(path)

	if got := o.get("abc"); got != "" {
		t.Fatalf("empty store returned %q", got)
	}

	o.set("hash1", "pk1")
	o.set("hash2", "pk2")
	o.set("", "pkX")     // ignored — no hash
	o.set("hash3", "")   // ignored — no pubkey
	o.set("hash1", "pk1") // no-op (unchanged)

	if got := o.get("hash1"); got != "pk1" {
		t.Fatalf("hash1 = %q, want pk1", got)
	}
	if got := o.get("hash3"); got != "" {
		t.Fatalf("hash3 should be absent, got %q", got)
	}

	// Persisted and reloadable.
	reloaded := loadBlobOwners(path)
	if got := reloaded.get("hash2"); got != "pk2" {
		t.Fatalf("after reload hash2 = %q, want pk2", got)
	}

	// Removal persists too.
	o.remove("hash1")
	if got := loadBlobOwners(path).get("hash1"); got != "" {
		t.Fatalf("hash1 not removed after reload, got %q", got)
	}
	if got := loadBlobOwners(path).get("hash2"); got != "pk2" {
		t.Fatalf("removal clobbered hash2, got %q", got)
	}
}
