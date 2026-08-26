/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useStatus } from '@/hooks/use-status'

import { SettingsPage } from '../components/settings-page'
import type { OperationsSettings } from '../types'
import {
  OPERATIONS_DEFAULT_SECTION,
  getOperationsSectionContent,
  getOperationsSectionMeta,
} from './section-registry.tsx'

const defaultOperationsSettings: OperationsSettings = {
  DefaultCollapseSidebar: false,
  DemoSiteEnabled: false,
  SelfUseModeEnabled: false,
  'general_setting.force_upstream_streaming_enabled': false,
  ChannelDisableThreshold: '',
  QuotaRemindThreshold: '',
  AutomaticDisableChannelEnabled: false,
  AutomaticEnableChannelEnabled: false,
  AutomaticDisableKeywords: '',
  ChannelFaultKeywords: '',
  AutomaticDisableStatusCodes: '401',
  AutomaticRetryStatusCodes:
    '100-199,300-399,401-407,409-499,500-503,505-523,525-599',
  'monitor_setting.auto_test_channel_enabled': false,
  'monitor_setting.auto_test_channel_minutes': 10,
  'monitor_setting.auto_test_disabled_channels_only': false,
  'monitor_setting.channel_status_notify_enabled': true,
  'monitor_setting.snapshot_model_status_enabled': true,
  'monitor_setting.snapshot_model_status_retention_days': 30,
  'monitor_setting.disable_on_empty_response': true,
  'monitor_setting.empty_response_rate_threshold': 0.5,
  'monitor_setting.empty_response_min_samples': 8,
  'monitor_setting.empty_response_absolute_floor': 5,
  'monitor_setting.channel_failure_rate_threshold': 0.5,
  'monitor_setting.channel_failure_min_samples': 10,
  'monitor_setting.channel_failure_absolute_floor': 20,
  'monitor_setting.channel_failure_dead_floor': 5,
  'monitor_setting.channel_failure_streak_floor': 3,
  SMTPServer: '',
  SMTPPort: '',
  SMTPAccount: '',
  SMTPFrom: '',
  SMTPToken: '',
  SMTPSSLEnabled: false,
  SMTPStartTLSEnabled: false,
  SMTPInsecureSkipVerify: false,
  SMTPForceAuthLogin: false,
  WorkerUrl: '',
  WorkerValidKey: '',
  WorkerAllowHttpImageRequestEnabled: false,
  LogConsumeEnabled: false,
  'performance_setting.disk_cache_enabled': false,
  'performance_setting.disk_cache_threshold_mb': 10,
  'performance_setting.disk_cache_max_size_mb': 1024,
  'performance_setting.disk_cache_path': '',
  'performance_setting.monitor_enabled': false,
  'performance_setting.monitor_cpu_threshold': 90,
  'performance_setting.monitor_memory_threshold': 90,
  'performance_setting.monitor_disk_threshold': 95,
  'perf_metrics_setting.enabled': true,
  'perf_metrics_setting.flush_interval': 5,
  'perf_metrics_setting.bucket_time': 'hour',
  'perf_metrics_setting.retention_days': 0,
}

export function OperationsSettings() {
  const { status } = useStatus()

  return (
    <SettingsPage
      routePath='/_authenticated/system-settings/operations/$section'
      defaultSettings={defaultOperationsSettings}
      defaultSection={OPERATIONS_DEFAULT_SECTION}
      getSectionContent={getOperationsSectionContent}
      getSectionMeta={getOperationsSectionMeta}
      extraArgs={[
        status?.version as string | undefined,
        status?.start_time as number | null | undefined,
      ]}
      loadingMessage='Loading maintenance settings...'
    />
  )
}
