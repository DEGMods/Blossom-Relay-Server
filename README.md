# blossom-relay-server

A self-hostable, federated **Blossom server + Nostr relay** node for
[DEG Mods](https://degmods.com) — a decentralized game-mods platform. It serves
mod files (content-addressed by SHA-256) from pluggable storage and runs a
mod-scoped Nostr relay, with anti-abuse **proof-of-work** and ad-view **download
gates** instead of payments or logins.

Licensed **MIT** — anyone can run a node.

## Features

- **Blossom blob server** — BUD-01/02/04/05/06 GET/HEAD/upload/delete, blobs
  addressed by SHA-256.
- **Streaming uploads** up to 500 MB — hashed on the fly (a lying `Content-Length`
  can't overflow), `.zip`-only via magic bytes, kind-24242 auth, a global
  concurrency cap plus one in-flight upload per npub, and a free-disk check.
- **Mod-scoped Nostr relay** — an embedded [badger](https://github.com/dgraph-io/badger)
  event store, accepts only mod events, with NIP-86 admin (ban/allow).
- **Pluggable storage** — Cloudflare R2, self-hosted S3 (MinIO / Garage / Ceph),
  or local disk. Cloud is one option, not a requirement.
- **Download gates** — [BUD-POW](docs/specs/BUD-POW.md) (proof-of-work, anti-abuse)
  and [BUD-Ads](docs/specs/BUD-Ads.md) (ad view, funds the operator). Both use
  HTTP `428` and are deliberately **not** a payment flow, so payment clients fail
  over cleanly instead of misreading them.
- **Federation** — nodes announce their capabilities over Nostr; because blobs are
  content-addressed, any discovered node is a drop-in source for any hash.
- **Resilience** — an S3 circuit breaker fails fast during an outage, structured
  (`slog`) logging, and a fully static single binary.

## How it works

The node wraps [khatru](https://github.com/fiatjaf/khatru) (fiatjaf's Go relay +
Blossom framework) as a library rather than forking it. A streaming `/upload`
handler sits in front of the relay (khatru's built-in upload buffers the whole
file, unusable at 500 MB), and a gate middleware wraps blob `GET`/`HEAD` to
enforce the PoW/ad challenges. Storage is abstracted behind a small `Storage`
interface with R2/S3 and local-disk implementations.

## Quick start (Docker + Caddy)

The [`deploy/`](deploy/) directory has a Compose stack (node + Caddy for automatic
HTTPS):

```sh
cd deploy
cp .env.example .env                 # R2/S3 credentials
cp config.yml.example config.yml     # endpoint, public URL, admin npub
docker compose up -d
```

Full walkthrough — from a bare VPS to cutover — in the
[deployment guide](docs/DEPLOYMENT.md).

## Configuration

All options are documented in [`config.example.yml`](config.example.yml). Secrets
(R2/S3 keys) are read from the environment (`R2_ACCESS_KEY` / `R2_SECRET_KEY` or
`S3_ACCESS_KEY` / `S3_SECRET_KEY`) so they stay out of the config file. Key knobs:

| Section | What it controls |
|---|---|
| `backend` | `r2`, `s3`, or `disk` |
| `r2` / `s3` / `disk` | the selected backend's settings |
| `upload` | size cap, concurrency, min free disk, optional NIP-13 PoW |
| `relay` | admin npub (NIP-86), min event PoW |
| `download` | PoW difficulty, ad gate, trusted client-IP header |
| `announce` | opt-in Nostr capability announcement (federation) |
| `log` | level + `text`/`json` format |

On first run the node generates its identity and prints its `npub`; the `nsec` is
saved to `data/identity.key` (used to sign the node's ad inventory and its
federation announcement).

## Download gates

Gates are off by default. When enabled, an ungated blob request gets a `428` with
one `X-Blossom-Gate-*` challenge per unmet gate; the client satisfies them (mines
the PoW, shows the ad) and retries with the matching proof headers.

- **PoW** ([BUD-POW](docs/specs/BUD-POW.md)) is the enforceable anti-abuse gate:
  a stateless HMAC challenge bound to the blob + client IP + expiry.
- **Ads** ([BUD-Ads](docs/specs/BUD-Ads.md)) is a cooperative funding gate. The ad
  inventory is the operator's own NIP-78 event (`kind 30078`, `d=manual-blossom-ads`).
  Impression/view/click metrics are aggregate-only with ephemeral, salted in-window
  dedup — **no raw IPs are stored** — and advertisers reconcile against their own
  analytics (there is no central authority).

## Storage & retention

Blobs are keyed `<sha256>.<ext>`. Mod files are meant to **persist** — there is no
automatic pruning or expiry. Deletion is manual only: the authenticated Blossom
`DELETE /<hash>` path and the NIP-86 admin ban/allow API.

## Build & run from source

Requires Go 1.25+.

```sh
cp config.example.yml config.yml     # edit; set backend + endpoint
go run ./cmd/degnode -config config.yml
go test ./...                        # unit tests (no cloud needed)
```

## Project layout

```
cmd/degnode        entrypoint (config load, storage factory, announce wiring)
internal/config    YAML config (+ env overrides for secrets)
internal/identity  node Nostr key (generated on first run)
internal/storage   Storage interface + backends: r2.go (R2/S3), disk.go (local), breaker.go
internal/server    khatru relay + Blossom handler, streaming upload, PoW/ad gate
internal/announce  Nostr capability announcement (federation discovery)
docs/              deployment guide + BUD specs
deploy/            Docker Compose + Caddy
```

## License

[MIT](LICENSE).
