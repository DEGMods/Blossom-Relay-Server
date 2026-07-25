/**
 * Admin API client for the node.
 *
 * The dashboard is served by the node itself at /dashboard, so every call is
 * same-origin — paths are relative and there's no CORS to negotiate. Each
 * request carries a NIP-98 Authorization header signed by the extension; the
 * node's verifyAdmin rejects anything not signed by the configured admin key,
 * so authorization is the node's decision, not ours.
 */
import { nip98Header } from './nostr'

/** A mod (or other stored event) that references a blob. */
export interface ModRef {
  /** Addressable coordinate "<kind>:<pubkey>:<d>", or the event id. */
  coord: string
  /** The mod's title, or "" if it has none. */
  title: string
}
export interface AdminBlob {
  hash: string
  ext: string
  size: number
  url: string
  added: number
  /** Uploader pubkey (hex). Empty for blobs uploaded before this was tracked. */
  pubkey?: string
  /** Mods that reference this blob. Empty/absent = no stored mod references it. */
  refs?: ModRef[]
}
export interface BlobsPage {
  total: number
  page: number
  per: number
  pages: number
  types: string[]
  blobs: AdminBlob[]
}
export type BlobSort = 'hash' | 'size' | 'date'
export interface WhitelistEntry {
  pubkey: string
  note?: string
}
export interface WhitelistInfo {
  limit_mb: number
  whitelisted_mb: number
  entries: WhitelistEntry[]
}

/** X-Reason carries the node's human-readable failure; fall back to status. */
async function reason(res: Response): Promise<string> {
  return res.headers.get('X-Reason') || `${res.status} ${res.statusText}`
}

async function adminFetch(path: string, method: string, body?: unknown): Promise<Response> {
  // Absolute URL for the NIP-98 `u` tag — the node matches it against the
  // request path exactly, so it has to be the real origin, not a relative path.
  const url = new URL(path, window.location.origin).toString()
  const headers: Record<string, string> = { Authorization: await nip98Header(url, method) }
  const init: RequestInit = { method, headers }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    init.body = JSON.stringify(body)
  }
  return fetch(url, init)
}

// ─── Blobs ──────────────────────────────────────────────────────────

export async function listBlobs(opts: {
  search?: string
  ext?: string[]
  sort?: BlobSort
  dir?: 'asc' | 'desc'
  page?: number
  per?: number
} = {}): Promise<BlobsPage> {
  const q = new URLSearchParams()
  if (opts.search) q.set('search', opts.search)
  if (opts.ext?.length) q.set('ext', opts.ext.join(','))
  if (opts.sort) q.set('sort', opts.sort)
  if (opts.dir) q.set('dir', opts.dir)
  if (opts.page) q.set('page', String(opts.page))
  if (opts.per) q.set('per', String(opts.per))
  const res = await adminFetch(`/admin/blobs?${q.toString()}`, 'GET')
  if (!res.ok) throw new Error(await reason(res))
  return res.json()
}

/**
 * Download a blob through the admin route, which bypasses the public
 * download gate (PoW/ads). Fetched with the NIP-98 header, then handed to the
 * browser as a save — a plain <a download> can't carry the auth header.
 */
export async function downloadBlob(hash: string, ext: string): Promise<void> {
  const name = ext ? `${hash}.${ext}` : hash
  const url = new URL(`/admin/blob/${name}`, window.location.origin).toString()
  const res = await fetch(url, { headers: { Authorization: await nip98Header(url, 'GET') } })
  if (!res.ok) throw new Error(await reason(res))
  const objUrl = URL.createObjectURL(await res.blob())
  const a = document.createElement('a')
  a.href = objUrl
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(objUrl)
}

/** Delete a blob by hash (admin-authed DELETE /<hash>). */
export async function deleteBlob(hash: string): Promise<void> {
  const url = new URL(`/${hash}`, window.location.origin).toString()
  const res = await fetch(url, {
    method: 'DELETE',
    headers: { Authorization: await nip98Header(url, 'DELETE') },
  })
  if (!res.ok) throw new Error(await reason(res))
}

// ─── Upload-size whitelist ──────────────────────────────────────────

export async function getWhitelist(): Promise<WhitelistInfo> {
  const res = await adminFetch('/admin/whitelist', 'GET')
  if (!res.ok) throw new Error(await reason(res))
  return res.json()
}

export async function addWhitelist(pubkey: string, note?: string): Promise<void> {
  const res = await adminFetch('/admin/whitelist', 'POST', { pubkey, note })
  if (!res.ok) throw new Error(await reason(res))
}

export async function removeWhitelist(pubkey: string): Promise<void> {
  const res = await adminFetch('/admin/whitelist', 'DELETE', { pubkey })
  if (!res.ok) throw new Error(await reason(res))
}

/** Update the per-upload size caps (MB). Whitelisted must be ≥ the normal cap. */
export async function setUploadCaps(limitMb: number, whitelistedMb: number): Promise<void> {
  const res = await adminFetch('/admin/upload-caps', 'PUT', {
    limit_mb: limitMb,
    whitelisted_mb: whitelistedMb,
  })
  if (!res.ok) throw new Error(await reason(res))
}

// ─── helpers ────────────────────────────────────────────────────────

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`
}
