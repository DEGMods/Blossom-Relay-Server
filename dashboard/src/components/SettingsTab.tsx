import { useEffect, useState } from 'react'
import { Loader2, HardDrive, ShieldCheck, CreditCard, Info, Save } from 'lucide-react'
import { getWhitelist, setUploadCaps } from '../api'
import { toast } from '../toast'

/**
 * Editable per-upload size caps (normal + whitelisted), plus a coming-soon row.
 * Reads the live values via getWhitelist (which returns the current caps) and
 * writes them through /admin/upload-caps. The node validates and persists; the
 * change takes effect on the next upload.
 */
export function SettingsTab() {
  const [loading, setLoading] = useState(true)
  const [limitMb, setLimitMb] = useState('')
  const [whitelistedMb, setWhitelistedMb] = useState('')
  const [saving, setSaving] = useState(false)

  const load = () => {
    setLoading(true)
    getWhitelist()
      .then((info) => {
        setLimitMb(String(info.limit_mb))
        setWhitelistedMb(String(info.whitelisted_mb))
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }
  useEffect(load, [])

  const limit = Number(limitMb)
  const wl = Number(whitelistedMb)
  const valid =
    Number.isInteger(limit) && limit >= 1 && Number.isInteger(wl) && wl >= limit

  const save = async () => {
    if (!valid) return
    setSaving(true)
    try {
      await setUploadCaps(limit, wl)
      toast('Upload caps updated')
      load()
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Save failed', 'error')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-start gap-2 rounded-lg border border-border bg-card p-3 text-xs text-muted-foreground">
        <Info className="mt-0.5 h-4 w-4 shrink-0 text-neutral-500" />
        <p>
          Changes take effect on the next upload — an upload already in progress isn’t affected. The
          whitelisted cap must be at least the max upload size.
        </p>
      </div>

      <div className="space-y-3 rounded-lg border border-border bg-card p-4">
        <NumberRow
          icon={HardDrive}
          title="Max upload size"
          hint="Default cap for any uploader."
          value={limitMb}
          onChange={setLimitMb}
          loading={loading}
        />
        <NumberRow
          icon={ShieldCheck}
          title="Whitelisted cap"
          hint="Raised cap for keys on the whitelist."
          value={whitelistedMb}
          onChange={setWhitelistedMb}
          loading={loading}
        />
        <div className="flex items-center justify-between gap-3 border-t border-border/60 pt-3">
          {!valid && !loading ? (
            <span className="text-xs text-destructive">
              Whitelisted cap must be ≥ the max upload size, and both ≥ 1.
            </span>
          ) : (
            <span />
          )}
          <button
            onClick={save}
            disabled={saving || loading || !valid}
            className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground hover:bg-primary-hover disabled:opacity-60"
          >
            {saving ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />}
            Save
          </button>
        </div>
      </div>

      <Row
        icon={CreditCard}
        title="Subscriber tiers"
        value="Coming soon"
        hint="Per-subscriber limits — planned, not yet available."
        muted
      />
    </div>
  )
}

function NumberRow({
  icon: Icon, title, hint, value, onChange, loading,
}: {
  icon: typeof HardDrive
  title: string
  hint: string
  value: string
  onChange: (v: string) => void
  loading: boolean
}) {
  return (
    <div className="flex items-center justify-between gap-4">
      <div className="flex items-center gap-3">
        <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-secondary">
          <Icon className="h-4 w-4 text-neutral-400" />
        </div>
        <div>
          <div className="text-sm font-medium text-neutral-200">{title}</div>
          <div className="text-xs text-muted-foreground">{hint}</div>
        </div>
      </div>
      <div className="flex items-center gap-2">
        {loading ? (
          <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
        ) : (
          <input
            type="number"
            min={1}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            className="w-24 rounded-lg border border-border bg-[#212121] px-3 py-1.5 text-right text-sm text-white focus:border-[#404040] focus:outline-none"
          />
        )}
        <span className="w-6 text-xs text-muted-foreground">MB</span>
      </div>
    </div>
  )
}

function Row({
  icon: Icon, title, value, hint, muted,
}: {
  icon: typeof HardDrive
  title: string
  value: string
  hint: string
  muted?: boolean
}) {
  return (
    <div className="flex items-center justify-between gap-4 rounded-lg border border-border bg-card p-4">
      <div className="flex items-center gap-3">
        <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-secondary">
          <Icon className="h-4 w-4 text-neutral-400" />
        </div>
        <div>
          <div className="text-sm font-medium text-neutral-200">{title}</div>
          <div className="text-xs text-muted-foreground">{hint}</div>
        </div>
      </div>
      <div className={muted ? 'text-sm text-neutral-500' : 'text-sm font-semibold text-neutral-100'}>{value}</div>
    </div>
  )
}
