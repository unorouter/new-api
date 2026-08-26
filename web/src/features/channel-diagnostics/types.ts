export interface ChannelDiagnosticRecord {
  id: number
  channel_id: number
  channel_name: string
  base_url: string
  from_status: number
  to_status: number
  status_reason: string
  status_code: number
  error_code: string
  model_name: string
  trigger_source: string
  response_time_ms: number
  seconds_in_prev_status: number
  probe_only: boolean
  occurrence_count: number
  first_seen_at: number
  created_at: number
}

export interface ChannelDiagnosticStatRow {
  channel_id: number
  channel_name: string
  base_url: string
  transitions: number
  disable_count: number
  enable_count: number
  seconds_up: number
  seconds_down: number
  uptime_percent: number
  last_to_status: number
}

export interface GetChannelDiagnosticsParams {
  p?: number
  page_size?: number
  channel_id?: number
  to_status?: number
  trigger_source?: string
  status_code?: number
  model_name?: string
  keyword?: string
  row_type?: string
  start_timestamp?: number
  end_timestamp?: number
  sort_by?: string
  sort_order?: string
}

export interface ChannelDiagnosticsPage {
  items: ChannelDiagnosticRecord[]
  total: number
  page: number
  page_size: number
}
