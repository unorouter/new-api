import { api } from '@/lib/api'
import type {
  ChannelDiagnosticStatRow,
  ChannelDiagnosticsPage,
  GetChannelDiagnosticsParams,
} from './types'

interface Envelope<T> {
  success: boolean
  message: string
  data: T
}

function buildQuery(params: Record<string, unknown>): string {
  const sp = new URLSearchParams()
  for (const key of Object.keys(params)) {
    const value = params[key]
    if (value === undefined || value === null || value === '') continue
    sp.set(key, String(value))
  }
  return sp.toString()
}

export async function getChannelDiagnostics(
  params: GetChannelDiagnosticsParams
): Promise<Envelope<ChannelDiagnosticsPage>> {
  const query = buildQuery({
    p: params.p ?? 1,
    page_size: params.page_size ?? 20,
    channel_id: params.channel_id,
    to_status: params.to_status,
    trigger_source: params.trigger_source,
    status_code: params.status_code,
    model_name: params.model_name,
    keyword: params.keyword,
    row_type: params.row_type,
    start_timestamp: params.start_timestamp,
    end_timestamp: params.end_timestamp,
    sort_by: params.sort_by,
    sort_order: params.sort_order,
  })
  const res = await api.get(`/api/channel/diagnostics?${query}`)
  return res.data
}

export async function getChannelDiagnosticStats(params: {
  start_timestamp?: number
  order_by?: string
  limit?: number
}): Promise<Envelope<ChannelDiagnosticStatRow[]>> {
  const query = buildQuery({
    start_timestamp: params.start_timestamp,
    order_by: params.order_by,
    limit: params.limit,
  })
  const res = await api.get(`/api/channel/diagnostics/stats?${query}`)
  return res.data
}
