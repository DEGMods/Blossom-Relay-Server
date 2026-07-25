import { useEffect, useState } from 'react'
import { Loader2, HardDrive, ShieldCheck, CreditCard, Info } from 'lucide-react'
import { getWhitelist, type WhitelistInfo } from '../api'

/**
 * Read-only for now. The values shown are real (from the node), but editing
 * them — the size cap, the whitelist multiplier, subscriber tiers — needs a
 * node config API that doesn't exist yet, so those are surfaced as
 * coming-soon rather than faked as editable.
 */
export function SettingsTab() {
  const [info, setInfo] = useState<WhitelistInfo | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    getWhitelist()
      .then(setInfo)
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  return (
    <div className="space-y-4">
      <div className="flex items-start gap-2 rounded-lg border border-border bg-card p-3 text-xs text-muted-foreground">
        <Info className="mt-0.5 h-4 w-4 shrink-0 text-neutral-500" />
        <p>
          These reflect the node’s current configuration. Editing them from here is coming — for now
          they’re set in the node’s config file.
        </p>
      </div>

      <Row
        icon={HardDrive}
        title="Max upload size"
        value={loading ? null : info ? `${info.limit_mb} MB` : '—'}
        hint="Default cap for any uploader."
      />
      <Row
        icon={ShieldCheck}
        title="Whitelisted cap"
        value={loading ? null : info ? `${info.whitelisted_mb} MB` : '—'}
        hint="Raised cap for keys on the whitelist."
      />
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

function Row({
  icon: Icon, title, value, hint, muted,
}: {
  icon: typeof HardDrive
  title: string
  value: string | null
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
      <div className={muted ? 'text-sm text-neutral-500' : 'text-sm font-semibold text-neutral-100'}>
        {value === null ? <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" /> : value}
      </div>
    </div>
  )
}
