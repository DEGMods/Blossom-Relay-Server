/**
 * Nostr auth for the dashboard — browser extension (NIP-07) only.
 *
 * There is deliberately no paste-your-nsec path: the private key never touches
 * this page. `window.nostr` (Alby, nos2x, …) holds the key and signs on demand;
 * we only ever see the pubkey and finished signatures.
 */

export interface NostrEvent {
  id: string
  pubkey: string
  created_at: number
  kind: number
  tags: string[][]
  content: string
  sig: string
}

interface Nip07 {
  getPublicKey(): Promise<string>
  signEvent(e: {
    kind: number
    created_at: number
    tags: string[][]
    content: string
  }): Promise<NostrEvent>
}

declare global {
  interface Window {
    nostr?: Nip07
  }
}

/** Whether a signing extension is present. */
export function hasExtension(): boolean {
  return typeof window !== 'undefined' && !!window.nostr
}

/** Ask the extension for the logged-in pubkey (hex). Throws if none / declined. */
export async function connect(): Promise<string> {
  if (!window.nostr) throw new Error('No Nostr extension found')
  return window.nostr.getPublicKey()
}

/**
 * Build a NIP-98 Authorization header for (url, method), signed by the
 * extension. Matches what the node's verifyAdmin expects: kind 27235, a `u`
 * tag with the exact URL and a `method` tag, created within ±60s.
 */
export async function nip98Header(url: string, method: string): Promise<string> {
  if (!window.nostr) throw new Error('No Nostr extension found')
  const evt = await window.nostr.signEvent({
    kind: 27235,
    created_at: Math.floor(Date.now() / 1000),
    tags: [
      ['u', url],
      ['method', method.toUpperCase()],
    ],
    content: '',
  })
  return `Nostr ${btoa(JSON.stringify(evt))}`
}

/** hex pubkey → npub (bech32). Minimal, dependency-free. */
export function npubEncode(hex: string): string {
  try {
    return bech32Encode('npub', hexToBytes(hex))
  } catch {
    return hex
  }
}

// ─── minimal bech32 (npub display only) ─────────────────────────────
const CHARSET = 'qpzry9x8gf2tvdw0s3jn54khce6mua7l'

function hexToBytes(hex: string): number[] {
  const out: number[] = []
  for (let i = 0; i < hex.length; i += 2) out.push(parseInt(hex.slice(i, i + 2), 16))
  return out
}

function polymod(values: number[]): number {
  const GEN = [0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3]
  let chk = 1
  for (const v of values) {
    const top = chk >> 25
    chk = ((chk & 0x1ffffff) << 5) ^ v
    for (let i = 0; i < 5; i++) if ((top >> i) & 1) chk ^= GEN[i]
  }
  return chk
}

function hrpExpand(hrp: string): number[] {
  const out: number[] = []
  for (let i = 0; i < hrp.length; i++) out.push(hrp.charCodeAt(i) >> 5)
  out.push(0)
  for (let i = 0; i < hrp.length; i++) out.push(hrp.charCodeAt(i) & 31)
  return out
}

function convertBits(data: number[], from: number, to: number): number[] {
  let acc = 0
  let bits = 0
  const out: number[] = []
  const maxv = (1 << to) - 1
  for (const value of data) {
    acc = (acc << from) | value
    bits += from
    while (bits >= to) {
      bits -= to
      out.push((acc >> bits) & maxv)
    }
  }
  if (bits > 0) out.push((acc << (to - bits)) & maxv)
  return out
}

function bech32Encode(hrp: string, data: number[]): string {
  const words = convertBits(data, 8, 5)
  const values = hrpExpand(hrp).concat(words)
  const mod = polymod(values.concat([0, 0, 0, 0, 0, 0])) ^ 1
  const checksum: number[] = []
  for (let i = 0; i < 6; i++) checksum.push((mod >> (5 * (5 - i))) & 31)
  let out = hrp + '1'
  for (const w of words.concat(checksum)) out += CHARSET[w]
  return out
}
