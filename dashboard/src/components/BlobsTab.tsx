import { useCallback, useEffect, useState } from 'react'
import {
  Loader2, Search, Trash2, Download, Copy, Check, ChevronLeft, ChevronRight,
  ArrowUp, ArrowDown, AlertTriangle,
} from 'lucide-react'
import { listBlobs, deleteBlob, downloadBlob, formatBytes, type BlobsPage, type BlobSort } from '../api'
import { npubEncode } from '../nostr'
import { cn, truncateMiddle, formatDate } from '../lib'
import { toast } from '../toast'

const PER_PAGE = 50

export function BlobsTab() {
  const [data, setData] = useState<BlobsPage | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [search, setSearch] = useState('')
  const [debounced, setDebounced] = useState('')
  const [exts, setExts] = useState<string[]>([])
  const [sort, setSort] = useState<BlobSort>('date')
  const [dir, setDir] = useState<'asc' | 'desc'>('desc')
  const [page, setPage] = useState(1)
  const [confirm, setConfirm] = useState<string | null>(null)

  useEffect(() => {
    const t = setTimeout(() => setDebounced(search.trim()), 300)
    return () => clearTimeout(t)
  }, [search])
  useEffect(() => setPage(1), [debounced, exts, sort, dir])

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setData(await listBlobs({ search: debounced, ext: exts, sort, dir, page, per: PER_PAGE }))
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load files')
    } finally {
      setLoading(false)
    }
  }, [debounced, exts, sort, dir, page])

  useEffect(() => {
    void load()
  }, [load])

  const toggleSort = (field: BlobSort) => {
    if (sort === field) setDir((d) => (d === 'asc' ? 'desc' : 'asc'))
    else {
      setSort(field)
      setDir(field === 'hash' ? 'asc' : 'desc')
    }
  }
  const toggleExt = (e: string) =>
    setExts((prev) => (prev.includes(e) ? prev.filter((x) => x !== e) : [...prev, e]))

  return (
    <div className="space-y-4">
      {/* Search + type filters */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-[220px] flex-1">
          <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search by hash…"
            className="w-full rounded-lg border border-border bg-[#212121] py-2 pl-9 pr-3 text-sm text-white placeholder:text-muted-foreground focus:border-[#404040] focus:outline-none"
          />
        </div>
        {data && data.types.length > 0 && (
          <div className="flex flex-wrap gap-1">
            {data.types.map((t) => (
              <button
                key={t}
                onClick={() => toggleExt(t)}
                className={cn(
                  'rounded-md border px-2 py-1 text-xs font-medium transition-colors',
                  exts.includes(t)
                    ? 'border-primary/40 bg-primary/10 text-primary'
                    : 'border-border text-neutral-400 hover:border-[#404040]',
                )}
              >
                .{t}
              </button>
            ))}
          </div>
        )}
      </div>

      <div className="rounded-lg border border-border bg-card">
        <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
          <h3 className="text-sm font-semibold text-neutral-200">
            Stored files{data ? ` (${data.total.toLocaleString()})` : ''}
          </h3>
          {loading && <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />}
        </div>

        {error ? (
          <div className="flex items-center gap-2 px-4 py-8 text-sm text-destructive">
            <AlertTriangle className="h-4 w-4" /> {error}
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border text-xs text-muted-foreground">
                  <SortTh label="Hash" field="hash" sort={sort} dir={dir} onSort={toggleSort} />
                  <th className="px-3 py-2 text-left font-medium">Type</th>
                  <th className="px-3 py-2 text-left font-medium">Uploader</th>
                  <SortTh label="Size" field="size" sort={sort} dir={dir} onSort={toggleSort} align="right" />
                  <SortTh label="Added" field="date" sort={sort} dir={dir} onSort={toggleSort} align="right" />
                  <th className="px-3 py-2 text-left font-medium">State</th>
                  <th className="px-3 py-2 text-left font-medium">Mod</th>
                  <th className="px-3 py-2 text-right font-medium">Actions</th>
                </tr>
              </thead>
              <tbody>
                {data?.blobs.map((b) => (
                  <tr key={b.hash} className="border-b border-border/60 last:border-0 hover:bg-[#212121]/40">
                    <td className="px-3 py-2 font-mono text-xs text-neutral-300">
                      <HashCell hash={b.hash} />
                    </td>
                    <td className="px-3 py-2 text-neutral-400">{b.ext ? `.${b.ext}` : '—'}</td>
                    <td className="px-3 py-2 font-mono text-xs">
                      {b.pubkey ? (
                        <span className="text-neutral-300" title={npubEncode(b.pubkey)}>
                          {truncateMiddle(npubEncode(b.pubkey), 10, 6)}
                        </span>
                      ) : (
                        <span className="text-neutral-600" title="Uploaded before uploaders were tracked">—</span>
                      )}
                    </td>
                    <td className="px-3 py-2 text-right tabular-nums text-neutral-300">{formatBytes(b.size)}</td>
                    <td className="px-3 py-2 text-right tabular-nums text-neutral-400">{formatDate(b.added)}</td>
                    {/* Placeholder — reference tracking / expiry (the "Fix C" work) isn't built. */}
                    <td className="px-3 py-2" title="Reference tracking not enabled yet">
                      <span className="rounded-full bg-secondary px-2 py-0.5 text-[10px] font-medium text-neutral-400">
                        Permanent
                      </span>
                    </td>
                    <td className="px-3 py-2 text-neutral-600" title="Not tracked yet">—</td>
                    <td className="px-3 py-2">
                      <div className="flex items-center justify-end gap-1">
                        <DownloadButton hash={b.hash} ext={b.ext} />
                        <button
                          onClick={() => setConfirm(b.hash)}
                          title="Delete"
                          className="rounded-md p-1.5 text-neutral-500 transition-colors hover:bg-destructive/15 hover:text-destructive"
                        >
                          <Trash2 className="h-4 w-4" />
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
                {data && data.blobs.length === 0 && !loading && (
                  <tr>
                    <td colSpan={8} className="px-4 py-10 text-center text-sm text-muted-foreground">
                      No files match.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        )}

        {data && data.pages > 1 && (
          <div className="flex items-center justify-between border-t border-border px-4 py-2.5 text-xs text-muted-foreground">
            <span>Page {data.page} of {data.pages}</span>
            <div className="flex gap-1">
              <button
                disabled={data.page <= 1}
                onClick={() => setPage((p) => p - 1)}
                className="rounded-md border border-border p-1 disabled:opacity-40 enabled:hover:border-[#404040]"
              >
                <ChevronLeft className="h-4 w-4" />
              </button>
              <button
                disabled={data.page >= data.pages}
                onClick={() => setPage((p) => p + 1)}
                className="rounded-md border border-border p-1 disabled:opacity-40 enabled:hover:border-[#404040]"
              >
                <ChevronRight className="h-4 w-4" />
              </button>
            </div>
          </div>
        )}
      </div>

      {confirm && (
        <DeleteConfirm
          hash={confirm}
          onClose={() => setConfirm(null)}
          onDeleted={() => {
            setConfirm(null)
            void load()
          }}
        />
      )}
    </div>
  )
}

function DownloadButton({ hash, ext }: { hash: string; ext: string }) {
  const [busy, setBusy] = useState(false)
  const go = async () => {
    setBusy(true)
    try {
      await downloadBlob(hash, ext)
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Download failed', 'error')
    } finally {
      setBusy(false)
    }
  }
  return (
    <button
      onClick={go}
      disabled={busy}
      title="Download"
      className="rounded-md p-1.5 text-neutral-500 transition-colors hover:bg-secondary hover:text-white disabled:opacity-60"
    >
      {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Download className="h-4 w-4" />}
    </button>
  )
}

function HashCell({ hash }: { hash: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      onClick={() => {
        navigator.clipboard.writeText(hash)
        setCopied(true)
        setTimeout(() => setCopied(false), 1500)
      }}
      className="inline-flex items-center gap-1.5 hover:text-white"
      title={hash}
    >
      {truncateMiddle(hash, 12, 8)}
      {copied ? <Check className="h-3 w-3 text-success" /> : <Copy className="h-3 w-3 text-neutral-600" />}
    </button>
  )
}

function SortTh({
  label, field, sort, dir, onSort, align = 'left',
}: {
  label: string
  field: BlobSort
  sort: BlobSort
  dir: 'asc' | 'desc'
  onSort: (f: BlobSort) => void
  align?: 'left' | 'right'
}) {
  const active = sort === field
  return (
    <th className={cn('px-3 py-2 font-medium', align === 'right' ? 'text-right' : 'text-left')}>
      <button
        onClick={() => onSort(field)}
        className={cn('inline-flex items-center gap-1 hover:text-neutral-200', active && 'text-neutral-200')}
      >
        {label}
        {active && (dir === 'asc' ? <ArrowUp className="h-3 w-3" /> : <ArrowDown className="h-3 w-3" />)}
      </button>
    </th>
  )
}

function DeleteConfirm({ hash, onClose, onDeleted }: { hash: string; onClose: () => void; onDeleted: () => void }) {
  const [busy, setBusy] = useState(false)
  const del = async () => {
    setBusy(true)
    try {
      await deleteBlob(hash)
      toast('File deleted')
      onDeleted()
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Delete failed', 'error')
      setBusy(false)
    }
  }
  return (
    <div className="fixed inset-0 z-40 flex items-center justify-center bg-black/60 px-4" onClick={onClose}>
      <div
        className="w-full max-w-sm space-y-4 rounded-xl border border-border bg-card p-5"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-start gap-3">
          <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-destructive/15">
            <Trash2 className="h-4 w-4 text-destructive" />
          </div>
          <div className="space-y-1">
            <h3 className="text-sm font-semibold text-neutral-100">Delete this file?</h3>
            <p className="break-all font-mono text-xs text-muted-foreground">{truncateMiddle(hash, 16, 10)}</p>
            <p className="text-xs text-neutral-500">
              This removes the blob from storage permanently. Any mod referencing it will lose the file.
            </p>
          </div>
        </div>
        <div className="flex justify-end gap-2">
          <button
            onClick={onClose}
            disabled={busy}
            className="rounded-lg border border-border px-3 py-1.5 text-xs text-neutral-300 hover:border-[#404040]"
          >
            Cancel
          </button>
          <button
            onClick={del}
            disabled={busy}
            className="inline-flex items-center gap-1.5 rounded-lg bg-destructive px-3 py-1.5 text-xs font-medium text-destructive-foreground hover:opacity-90 disabled:opacity-60"
          >
            {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Trash2 className="h-3.5 w-3.5" />}
            Delete
          </button>
        </div>
      </div>
    </div>
  )
}
