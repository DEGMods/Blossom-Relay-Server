package server

import (
	"encoding/json"
	"os"
	"sync"
)

// blobOwners maps a blob hash → the pubkey that uploaded it (taken from the
// kind-24242 upload authorization). The storage layer is content-addressed and
// keeps no per-object uploader, so this is recorded alongside it here.
//
// Persisted as JSON, same shape as the other aux stores (whitelist, deletions,
// …). Blobs uploaded before this existed have no entry — callers treat a missing
// owner as "unknown" rather than an error. Used by the admin dashboard now, and
// available for reconciling blobs against the mods that reference them later.
type blobOwners struct {
	mu     sync.RWMutex
	saveMu sync.Mutex
	path   string
	m      map[string]string // hash (hex) -> uploader pubkey (hex)
}

func loadBlobOwners(path string) *blobOwners {
	b := &blobOwners{path: path, m: map[string]string{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &b.m)
	}
	return b
}

func (b *blobOwners) get(hash string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.m[hash]
}

// set records the uploader for a hash — first writer wins. The store is
// content-addressed, so a "re-upload" of the same bytes is the same object; the
// first uploader is the one who actually introduced it, and later identical
// uploads (rare — clients HEAD-check and skip) must not erase that credit, or a
// blob could silently vanish from the original uploader's "my uploads" view.
func (b *blobOwners) set(hash, pubkey string) {
	if hash == "" || pubkey == "" {
		return
	}
	b.mu.Lock()
	if b.m[hash] != "" { // already attributed — keep the original uploader
		b.mu.Unlock()
		return
	}
	b.m[hash] = pubkey
	b.mu.Unlock()
	b.save()
}

// remove drops a hash's owner (e.g. after the blob is deleted), so the map
// doesn't accumulate entries for blobs that no longer exist.
func (b *blobOwners) remove(hash string) {
	b.mu.Lock()
	if _, ok := b.m[hash]; !ok {
		b.mu.Unlock()
		return
	}
	delete(b.m, hash)
	b.mu.Unlock()
	b.save()
}

// save snapshots under the read lock, then writes under a dedicated mutex so
// concurrent uploads can't interleave a half-written file. Compact JSON — this
// map can grow to one entry per blob.
func (b *blobOwners) save() {
	b.mu.RLock()
	data, _ := json.Marshal(b.m)
	b.mu.RUnlock()
	b.saveMu.Lock()
	_ = os.WriteFile(b.path, data, 0o600)
	b.saveMu.Unlock()
}
