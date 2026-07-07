# blossom-relay-server

A self-hostable, federated **Blossom server + Nostr relay** node for
[DEG Mods](https://degmods.com) — a decentralized game-mods platform. It serves
mod files (content-addressed by SHA-256) from pluggable storage and runs a
mod-scoped Nostr relay, with anti-abuse **proof-of-work** and ad-view **download
gates** (BUD-POW / BUD-Ads) instead of payments or logins.

Anyone can run a node — see [Docker / self-hosting](#docker--self-hosting) and the
step-by-step [deployment guide](docs/DEPLOYMENT.md). Licensed **MIT**.

Design notes: [`docs/IMPLEMENTATION_PLAN.md`](docs/IMPLEMENTATION_PLAN.md); protocol
specs: [`docs/specs/`](docs/specs/).

## Fork vs. import — read this first

We most likely **do not fork khatru**. khatru is a Go *library* you import as a
dependency and wrap:

- `import "github.com/fiatjaf/khatru"` (relay) + its Blossom handler + `go-nostr`.
- We add our own code around it: the streaming upload handler (khatru's buffers a
  full `[]byte`, unusable for 500 MB — so we bypass it), the download gate
  middleware (BUD-POW / BUD-Ads), rate limits/quotas, the R2 storage adapter, and
  the mod-kind relay policies.

A **fork** is only warranted if we need to patch khatru's *internals* (e.g. make its
upload handler stream). Our plan avoids that by wrapping, not patching. If a patch
becomes necessary, we vendor/fork khatru into `./vendor-khatru/` and swap the module
`replace` directive — until then, keep it a normal dependency for easy upstream
updates.

## Layout

```
cmd/degnode/main.go        entrypoint (+ storage factory, announce wiring)
internal/config            YAML config (+ env overrides for R2/S3 secrets)
internal/identity          node Nostr key (generated on first run; prints nsec)
internal/storage           Storage interface + backends (keys "<sha256>.<ext>",
                           bare-hash prefix fallback): r2.go (R2/S3), disk.go (local)
internal/server            khatru relay + Blossom handler wired to storage;
                           streaming upload, limits, disk check, PoW/ad gate
internal/announce          Nostr capability announcement (federation discovery)
```

## Phase 0 — done (blossom read parity)

Serves the files already in the `deg-blossom-storage` bucket over Blossom
GET/HEAD/list/delete. Boot-verified (identity + listen + NIP-11) and the R2 read
path verified against the live bucket (correct bytes/size/type, range support).

## Phase 1 — done (streaming upload)

A streaming `PUT /upload` (+ BUD-06 `HEAD /upload` pre-flight) sits in front of the
relay, replacing khatru's buffered upload:

- body → temp file with **hash-on-the-fly** (`io.MultiWriter(temp, sha256)`),
  hard-capped so a lying `Content-Length` can't overflow;
- verifies the hash against the kind-24242 auth's `x` tag;
- **`.zip`-only via magic bytes** (rejects media and encrypted archives);
- **24242 auth** (kind / `t=upload` / expiration / signature; optional NIP-13 PoW);
- **global concurrency cap + one in-flight upload per npub** + free-disk check;
- streams the verified temp file to R2, returns a BUD-02 descriptor.

Verified by unit tests (`internal/server/upload_test.go`) with a fake backend — no
R2 needed: happy path, no-auth (401), hash mismatch (400), non-zip (415).

_Deferred to a later pass:_ rolling per-npub quotas / rate-tiers and a persistent
blocklist (the hooks exist; the current limit is one concurrent upload per npub).

### Run

```sh
cp config.example.yml config.yml      # then fill in R2 endpoint + keys
#   (or export R2_ACCESS_KEY / R2_SECRET_KEY instead of putting them in the file)
go run ./cmd/degnode -config config.yml
```

On first run it generates the node identity and prints the `npub` (the `nsec` is
saved to `data/identity.key` — use it later to publish `manual-blossom-ads`).

### Exit check

With a known blob hash from the bucket:

```sh
curl -sI http://localhost:3000/<sha256>.zip     # 200 + Content-Type/Length
curl -s  http://localhost:3000/<sha256>.zip -o out.zip
sha256sum out.zip                                # must equal <sha256>
```

Run the tests with `go test ./...`.

## Phase 2 — done (mod-scoped relay)

khatru relay with an embedded **badger** event store (`data/events/`), scoped to
mod events:

- **Accepted kinds:** `31142` (current) and `30402` (legacy, accepted only with a
  `GameMod` tag + before the `2026-08-01` cutoff, PoW-exempt — a temporary
  exception that mirrors the client and is meant to be removed).
- Current mods require a `d` tag and meet `relay.min_event_pow` (NIP-13);
  non-mod kinds are rejected on both write (`RejectEvent`) and read (`RejectFilter`).
- **NIP-86 admin** (config `relay.admin_npub`): ban/allow pubkeys + list, restricted
  to the admin key. Bans persist in `data/blocklist.json` and are shared with the
  upload handler.

Boot-verified (badger opens, relay serves NIP-11/86) and unit-tested
(`relay_test.go`: kind/legacy/cutoff/blocklist policy + filter scoping).

## Phase 3 — done (PoW download gate)

BUD-POW gate in front of blob `GET`/`HEAD` (`download.pow_difficulty` bits; `0` =
off): a `428` + `X-Blossom-Gate-Pow` challenge, **stateless** (HMAC over
`hash|ip|exp|d|rand`, key derived from the node's secret key), verified from the
`X-Blossom-Gate-Pow-Proof` header. A proof is reusable until it expires, covering
ranged/resumed downloads. Client IP comes from `download.trusted_ip_header`
(`CF-Connecting-IP` / `X-Forwarded-For`) or the socket. CORS handled for browser
downloads. Unit-tested (`gate_test.go`): challenge/verify incl. wrong-ip /
wrong-blob / wrong-nonce / tampered-sig, and the middleware (428 → mine → pass;
non-blob untouched).

## Phase 4a — done (ad download gate + metrics)

BUD-Ads gate composed alongside PoW on blob `GET`/`HEAD` (`download.ad_gate`; off by
default). When on, an ungated request gets a `428` carrying **both** unsatisfied
challenges: `X-Blossom-Gate-Pow` and `X-Blossom-Gate-Ad`
(`v=1; ref=30078:<node-pubkey>:manual-blossom-ads; min=<ms>; c=<challenge>`). The ad
challenge is the same stateless HMAC token as PoW but requires no work; the client
returns `X-Blossom-Gate-Ad-Proof: <c>; ad=<id>` naming the ad it showed.

- **Cooperative, not enforced** — PoW is the anti-abuse gate; the ad gate funds the
  operator. A determined client can spoof a view, so metrics are best-effort and
  advertisers reconcile against their own analytics (no central authority).
- **Inventory** is the operator's own NIP-78 event
  `30078:<node-pubkey>:manual-blossom-ads`, published with the node's nsec.
- **Metrics** (`data/ad_stats.json`, persisted every 30s + on shutdown): cumulative
  unique views/clicks per ad id. Uniqueness is ephemeral — a per-window set of
  salted-hashed IPs (salt rotates each 24 h window, so **no raw IPs are ever stored**
  and there's no cross-window correlation); only the aggregate counts persist.
- **Endpoints** (only when `ad_gate` is on): `GET /ads/stats` (aggregate counts) and
  `POST /ads/click` (`{"ad":"<id>"}`, deduped per window).

Unit-tested (`ad_test.go`): ad challenge/verify incl. wrong-ip / wrong-blob /
tampered-sig, and metrics unique-dedup for views and clicks.

## Phase 4b — done (client integration, in the DEG Mods frontend)

The generic `428` gate handler + the `PoW + ad` download modal live in the main
DEG Mods app (`src/lib/blossom/gate.ts`, `src/components/mod/GateDownloadModal.tsx`,
wired into `downloadWithFailoverProgress`). It mines the PoW while the ad is on
screen and retries with both proofs; a non-gate-aware client fails over cleanly.

## Phase 5 — storage portability + federation + hardening — done

**Pluggable storage** (`backend:` in config): the same `Storage` interface backs
three backends — `r2` (Cloudflare R2), `s3` (self-hosted MinIO/Garage/Ceph,
path-style), and `disk` (local filesystem, content-addressed + sharded, atomic
temp-and-rename writes — zero cloud dependency). R2 is one option, not a
requirement. Disk backend unit-tested (`storage/disk_test.go`); all backends
cross-compile for linux/amd64.

**Federation announce** (`announce:` in config, opt-in): publishes a NIP-78
capability event (`kind 30078`, `d=degmods-node`) signed by the node key,
advertising its URL, upload cap, accepted relay kinds, and required gates (PoW
bits / ad). Republished on an interval. Clients and other nodes discover peers
with `kind:30078 #d:degmods-node`; content-addressing makes any discovered node a
drop-in source for any hash. Event build/sign unit-tested (`announce/announce_test.go`).

**Resilience:** an S3/R2 **circuit breaker** (5 consecutive failures → 60s
fail-fast, per-op + write timeouts, 404 = healthy) so an outage fails fast instead
of hanging; unit-tested (`storage/breaker_test.go`). **Structured logging** via
`log/slog` (`log.level` / `log.format` text|json).

**No pruning, by design:** mod files must persist, so there is no automatic
retention/expiry. Deletion is manual only — the authenticated Blossom
`DELETE /<hash>` path and NIP-86 admin ban/allow.

Next: **Phase 6 cutover** — run alongside `bs.degmods.com` against the same R2
bucket, shift traffic gradually, retire the old Koa server.

## Docker / self-hosting

```sh
docker build -t degnode .
docker run -d --name degnode \
  -p 3000:3000 \
  -v "$PWD/data:/app/data" \
  -v "$PWD/config.yml:/app/config.yml:ro" \
  -e R2_ACCESS_KEY=… -e R2_SECRET_KEY=…   # or S3_ACCESS_KEY/S3_SECRET_KEY; omit for disk backend
  degnode
```

The image is a static (`CGO_ENABLED=0`) single binary on Alpine, runs as a
non-root user, and persists identity + event store (+ blobs on the disk backend)
in the `/app/data` volume. Put TLS + the real hostname on a reverse proxy (Caddy)
in front, and set `download.trusted_ip_header` so the gates see the real client IP.
