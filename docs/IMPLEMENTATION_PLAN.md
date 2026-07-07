# DEG Mods Node — Implementation Plan

A single Go binary that is **both** a mod-scoped Nostr relay (the mod *events*) and
a Blossom server (the mod *files*), built on **khatru** + **go-nostr**, with a
swappable storage backend (Cloudflare R2 today; local disk / MinIO / Garage
later), gated downloads (two standalone specs, [BUD-POW](./specs/BUD-POW.md) +
[BUD-Ads](./specs/BUD-Ads.md)), and per-operator ad funding.

Repo layout: this doc + specs live in `Blossom and Relay/docs/`; the Go project
(khatru-based) lives in `Blossom and Relay/node/`.

This node is **v2** — it does **not** replace `bs.degmods.com` in place. It runs
alongside the current server; because blobs are content-addressed, clients don't
care which node serves a hash, so cutover is gradual (see Phase 6).

## Ground truth (from the existing R2 bucket)

- Bucket: `deg-blossom-storage`, region **WEUR**, **private** (no public/dev URL,
  no custom domain, no CORS). Downloads therefore **proxy through the server** —
  the node must stay in the byte path (required anyway to enforce the gates).
- Object key = **`<sha256>.<ext>`** (e.g. `<hash>.zip`, `<hash>.jpg`), uniform
  across zips and images. No custom object metadata; `Content-Type` is set.
- Mixed content already present (images + zips). v2 **reads everything**; its
  upload policy accepts **`.zip` only** going forward.
- Config secrets (NOT in repo): S3 API endpoint + Access Key ID + Secret Access
  Key. Bucket stays private; do not enable a public URL for gated blobs.

## Stack

- Language: **Go** (single static binary; approachable for other operators).
- Framework: **khatru** (relay + embedded Blossom handler) + **go-nostr**.
- Event store: embedded (**SQLite** / LMDB / Badger) — no external DB.
- Object storage: S3 API via **minio-go** (R2 today), behind a `Storage` interface.
- Node metadata (quotas, blocklists, ad metrics): embedded SQLite (or reuse the
  event store's DB).

---

## Phase 0 — Scaffold + storage parity (read the existing bucket)

Goal: a Go node that **serves the files already in R2**, no gates yet.

- New Go module + config loader (YAML/env): S3 endpoint/bucket/keys, limits, and
  the node **identity key** — **generated on first run** if absent, stored in the
  config/data dir, and printed once (and/or exposed via a CLI flag) so the operator
  can extract the `nsec` to publish their `manual-blossom-ads` inventory elsewhere.
- Implement the `Storage` interface against R2 (minio-go):
  - `Load(sha256, ext) -> io.ReadSeeker | redirect` → object key `<sha256>.<ext>`.
    If `ext` is empty (bare-hash request), **`ListObjects(prefix=<sha256>)`** to
    resolve the actual key. **Do not redirect** (private bucket + gate requires
    proxying); stream the bytes.
  - `Store(sha256, ext, reader)` → `PutObject` `<sha256>.<ext>` with correct
    `Content-Type`.
  - `Delete`, `Has`, `Stat`.
- Wire khatru's Blossom handler for `GET`/`HEAD`/`list`/`delete` + kind-24242 auth.
- **Exit check:** `GET /<known-hash>.zip` streams the correct bytes from R2.

## Phase 1 — Streaming upload + guards (replace khatru's buffered upload)

Goal: 500 MB `.zip` uploads with **bounded memory** and spam guards. khatru's
`StoreBlob([]byte)` buffers the whole file — **bypass it** with our own streaming
`PUT /upload` handler; keep khatru for the rest.

- Streaming handler: request body → temp file, `io.TeeReader` into `sha256.New()`
  → on completion verify vs. `X-Sha256` → gates → `Store` to R2 → remove temp.
- Upload guards (pre-body where possible):
  - Required **kind-24242 auth** (verify signature/expiration; optional **NIP-13
    PoW** on the auth event, checked from headers before reading the body).
  - **`.zip` only** via magic-byte sniff (not just MIME/extension); reject media.
  - **Size cap** (500 MB) + reject encrypted zips.
  - **Per-npub upload queue** (1 concurrent, position-based), per-npub/IP rate +
    rolling quota, **global concurrency cap**, temp-disk headroom check.
  - npub/blob **blocklist**.
- Port the concepts (queue, tiers, disk check) from the current Koa server.
- **Exit check:** two concurrent 500 MB uploads succeed without RAM blowup; a
  3rd is queued; a non-zip/oversized/no-auth upload is rejected. No AV (removed).

## Phase 2 — Mod-scoped relay + admin

Goal: the node also stores/serves the mod **events**.

- Enable khatru's relay with an embedded event store.
- `RejectEvent` / `RejectFilter` restricted to **mod kinds**:
  - **`31142`** — current mods (the primary, permanent kind).
  - **`30402`** — legacy mods, accepted **only** under the same rule the client
    uses: the event MUST carry `["t","GameMod"]` **and** have `created_at` before
    the cutoff `2026-08-01T00:00:00Z`. `// LEGACY: temporary exception — mirrors the
    client's isLegacyModEvent(); expected to be removed in a future version.`
  - Reactions (`7`) and comments (`1111`) are **not** accepted for now.
  - Keep the accepted set config-driven so it can be widened later.
- Min **NIP-13 PoW** on accepted events (legacy `30402` is **PoW-exempt**, same as
  the client); structural validation (required tags); event blocklist.
- **NIP-86** management API (ban/allow pubkeys, moderate events, metadata) +
  custom RPC methods for Blossom ops (block blob hash, quotas) — one
  npub-authenticated admin surface. No admin GUI for now.
- **Exit check:** node accepts a valid mod event, rejects other kinds and
  low-PoW events; NIP-86 ban/allow works.

## Phase 3 — Download PoW gate (BUD-POW)

Goal: anti-abuse gate on retrieval.

- **Gate middleware in front of the Blossom `GET`**: no/invalid proof → `428` +
  `X-Blossom-Gate: pow` + `X-Blossom-Gate-Pow` (stateless HMAC challenge bound to
  blob+IP+exp). Valid proof → stream bytes.
- Read client IP from `CF-Connecting-IP` (configurable trusted header).
- Optional adaptive difficulty by recent per-IP volume.
- **Exit check:** a cold `GET` returns `428`; solving + retrying serves the file;
  a stale/mismatched proof is rejected.

## Phase 4 — Ad gate + metrics (BUD-Ads) + client integration

Goal: funding overlay, the "PoW + ad together" flow.

### Phase 4a — node ad gate + metrics — **done**

- Gate emits `X-Blossom-Gate-Ad` (ref to the node's `manual-blossom-ads`, `min`,
  HMAC challenge) alongside `X-Blossom-Gate-Pow`; accepts `X-Blossom-Gate-Ad-Proof`.
  A single `428` carries every unmet challenge (`checkGates`).
- Operator publishes their own `manual-blossom-ads` (node nsec).
- **Metrics** (`data/ad_stats.json`): impression/view/click, ephemeral in-window
  salted-hash IP dedup (rotating per-24h salt, **no raw IPs**), aggregates persisted
  every 30s + on shutdown, `GET /ads/stats` for reconciliation, `POST /ads/click`.
- Unit-tested (`ad_test.go`); builds for linux/amd64.

### Phase 4b — client integration (DEG Mods frontend) — **done**

- `src/lib/blossom/gate.ts`: framework-agnostic 428 parsing, PoW miner
  (`SHA-256("<c>:<nonce>")` via batched `crypto.subtle`, async/non-blocking),
  NIP-78 ad-inventory fetch + author check, weighted shuffle-bag rotation
  (mirrors `SidebarAd`, no consecutive repeats), ad-click beacon.
- `downloadFileWithProgress` gained an optional `resolveGate` hook: on `428` it
  parses the challenges, awaits the resolver for proof headers, and retries the
  identical GET once. Absent/failed resolver → fails over (unchanged behavior).
- `GateDownloadModal.tsx`: mines PoW **and** shows the ad simultaneously; the ad
  timer counts only while visible + tab-focused (IntersectionObserver + Page
  Visibility); refuses to send a proof if no ad can be shown (honest, fails over).
  Wired into `ModDownloads` → `downloadWithFailoverProgress`.
- Typechecks (`tsc -b`) + production build clean.
- **Exit check (pending live node):** clicking download shows the modal, mines PoW
  while the ad shows ≥1s, then the file downloads; metrics increment; a
  non-gate-aware client fails over cleanly (never mis-enters payment).

## Phase 5 — Hardening + portability + federation

- **done — portability:** `backend:` selector (`r2` | `s3` | `disk`) behind the
  same `Storage` interface. New local-disk backend (content-addressed, sharded,
  atomic writes — no cloud dependency) + generalized S3 adapter (path-style for
  self-hosted MinIO/Garage/Ceph). R2 is one option, not a requirement.
  Unit-tested (`storage/disk_test.go`).
- **done — federation:** Nostr **capability announcement** (`internal/announce`,
  opt-in `announce:` config) — a signed NIP-78 event (`kind 30078 d=degmods-node`)
  advertising URL, upload cap, accepted kinds, and required gates; republished on
  an interval. Clients/other nodes discover peers by `#d:degmods-node`;
  content-addressing makes any node a drop-in source. Unit-tested.
- **done — packaging:** multi-stage `Dockerfile` (static `CGO_ENABLED=0` binary on
  Alpine, non-root, `/app/data` volume) + `.dockerignore`; self-hoster docs in the
  node README.
- **done — resilience:** S3/R2 **circuit breaker** (5 consecutive failures → 60s
  fail-fast; per-op + write timeouts; 404 counts as healthy) ported from the legacy
  server; unit-tested. **Structured logging** via `log/slog` (`log.level`/`log.format`
  text|json), with a warn on circuit-open.
- **no pruning (by design):** unlike the legacy *media cache*, this node hosts mod
  files that must persist — there is no automatic retention/expiry. Deletion is
  manual only (authenticated Blossom `DELETE /<hash>` + NIP-86 admin ban/allow).

## Phase 6 — Cutover

- Run v2 alongside `bs.degmods.com`, pointed at the **same R2 bucket** (read
  parity from Phase 0).
- Move traffic gradually; content-addressing means links keep working regardless
  of which node serves a hash. Decommission the old Koa server when v2 is at
  parity + gates verified.

---

## Cross-cutting decisions already settled

- No cryptocurrency payment (BUD-07) — normie audience. Gates = **PoW (anti-abuse,
  enforced) + Ad (funding, cooperative)**, each a standalone BUD (BUD-POW,
  BUD-Ads) using `428`, **not** BUD-07's `402`.
- No AV scanning (removed).
- Ads = self-hosted NIP-78 `manual-blossom-ads`, per-operator (node nsec),
  self-reported metrics reconciled by advertisers. No central metrics authority.
- Bucket stays **private / node-proxied**; never expose a public R2 URL for gated
  blobs (would bypass the gate).
- Do **not** hard-block adblock users; degrade gracefully (server lists which
  methods it requires).

## Open items → see the questions in the handoff message
