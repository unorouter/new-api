import type { ChannelDiagnosticRecord } from './types'

// Channel status enum (matches common.ChannelStatus* in the backend).
export const CHANNEL_STATUS_ENABLED = 1
export const CHANNEL_STATUS_MANUAL_DISABLED = 2
export const CHANNEL_STATUS_AUTO_DISABLED = 3

export function statusLabel(status: number): string {
  switch (status) {
    case CHANNEL_STATUS_ENABLED:
      return 'Enabled'
    case CHANNEL_STATUS_MANUAL_DISABLED:
      return 'Manually Disabled'
    case CHANNEL_STATUS_AUTO_DISABLED:
      return 'Auto Disabled'
    default:
      return 'Unknown'
  }
}

export function statusVariant(
  status: number
): 'success' | 'danger' | 'warning' | 'neutral' {
  switch (status) {
    case CHANNEL_STATUS_ENABLED:
      return 'success'
    case CHANNEL_STATUS_AUTO_DISABLED:
      return 'danger'
    case CHANNEL_STATUS_MANUAL_DISABLED:
      return 'warning'
    default:
      return 'neutral'
  }
}

export const TRIGGER_SOURCES = [
  'live_request',
  'scheduled_test',
  'manual',
  'by_tag',
  'balance',
] as const

// Human-readable seconds -> "1h 2m" style for the time-in-prev-status column.
export function formatDuration(seconds: number): string {
  if (!seconds || seconds <= 0) return '-'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = seconds % 60
  const parts: string[] = []
  if (d) parts.push(`${d}d`)
  if (h) parts.push(`${h}h`)
  if (m) parts.push(`${m}m`)
  if (!d && !h && s) parts.push(`${s}s`)
  return parts.join(' ') || `${s}s`
}

export function rowKey(r: ChannelDiagnosticRecord): string {
  return String(r.id)
}
