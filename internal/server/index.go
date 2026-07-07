package server

import (
	"context"

	"github.com/DEG-Mods/blossom-relay-server/internal/storage"
	"github.com/fiatjaf/khatru/blossom"
	"github.com/nbd-wtf/go-nostr"
)

// r2Index is a minimal blossom.BlobIndex backed by the storage layer. Phase 0
// does not track ownership (the existing bucket's blobs predate this node), so
// Keep/Delete are no-ops and List is empty. Get reflects real object existence so
// GET/HEAD work for the files already in the bucket. Phase 2 replaces this with a
// persistent, ownership-tracking index (khatru's EventStoreBlobIndexWrapper).
type r2Index struct {
	st        storage.Storage
	publicURL string
}

func (i *r2Index) Keep(ctx context.Context, blob blossom.BlobDescriptor, pubkey string) error {
	return nil
}

func (i *r2Index) Delete(ctx context.Context, sha256, pubkey string) error {
	return nil
}

func (i *r2Index) List(ctx context.Context, pubkey string) (chan blossom.BlobDescriptor, error) {
	ch := make(chan blossom.BlobDescriptor)
	close(ch)
	return ch, nil
}

func (i *r2Index) Get(ctx context.Context, sha256 string) (*blossom.BlobDescriptor, error) {
	info, err := i.st.Stat(ctx, sha256, "")
	if err == storage.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &blossom.BlobDescriptor{
		URL:      i.publicURL + "/" + info.Key,
		SHA256:   sha256,
		Size:     int(info.Size),
		Type:     info.ContentType,
		Uploaded: nostr.Now(),
	}, nil
}
