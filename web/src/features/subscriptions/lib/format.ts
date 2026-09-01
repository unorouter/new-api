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
import type { TFunction } from 'i18next'

import dayjs from '@/lib/dayjs'

import type { SubscriptionPlan } from '../types'

export function formatDuration(
  plan: Partial<SubscriptionPlan>,
  t: TFunction
): string {
  const unit = plan?.duration_unit || 'month'
  const value = plan?.duration_value || 1
  const unitLabels: Record<string, string> = {
    year: t('years'),
    month: t('months'),
    day: t('days'),
    hour: t('hours'),
    custom: t('Custom (seconds)'),
  }
  if (unit === 'custom') {
    const seconds = plan?.custom_seconds || 0
    if (seconds >= 86400) return `${Math.floor(seconds / 86400)} ${t('days')}`
    if (seconds >= 3600) return `${Math.floor(seconds / 3600)} ${t('hours')}`
    return `${seconds} ${t('seconds')}`
  }
  return `${value} ${unitLabels[unit] || unit}`
}

export function formatResetPeriod(
  plan: Partial<SubscriptionPlan>,
  t: TFunction
): string {
  const period = plan?.quota_reset_period || 'never'
  if (period === 'daily') return t('Daily')
  if (period === 'weekly') return t('Weekly')
  if (period === 'monthly') return t('Monthly')
  if (period === 'custom') {
    const seconds = Number(plan?.quota_reset_custom_seconds || 0)
    if (seconds >= 86400) return `${Math.floor(seconds / 86400)} ${t('days')}`
    if (seconds >= 3600) return `${Math.floor(seconds / 3600)} ${t('hours')}`
    if (seconds >= 60) return `${Math.floor(seconds / 60)} ${t('minutes')}`
    return `${seconds} ${t('seconds')}`
  }
  return t('No Reset')
}

export function formatTimestamp(ts: number): string {
  if (!ts) return '-'
  return dayjs(ts * 1000).format('YYYY-MM-DD HH:mm:ss')
}

/**
 * Number of times the plan's quota resets across its full validity window.
 * Returns 0 if the plan does not reset, or the math does not make sense.
 *
 * Uses integer-day approximations (365 days/year, 30 days/month) to avoid
 * fractional-period rounding artifacts in the displayed estimate.
 */
export function getResetPeriodsCount(plan: Partial<SubscriptionPlan>): number {
  const period = plan?.quota_reset_period || 'never'
  if (period === 'never') return 0

  const durationUnit = plan?.duration_unit || 'month'
  const durationValue = Number(plan?.duration_value ?? 1)
  if (!Number.isFinite(durationValue) || durationValue <= 0) return 0

  let durationSeconds = 0
  switch (durationUnit) {
    case 'year':
      durationSeconds = durationValue * 365 * 86400
      break
    case 'month':
      durationSeconds = durationValue * 30 * 86400
      break
    case 'day':
      durationSeconds = durationValue * 86400
      break
    case 'hour':
      durationSeconds = durationValue * 3600
      break
    case 'custom':
      durationSeconds = Number(plan?.custom_seconds || 0)
      break
    default:
      return 0
  }

  let resetSeconds = 0
  switch (period) {
    case 'daily':
      resetSeconds = 86400
      break
    case 'weekly':
      resetSeconds = 7 * 86400
      break
    case 'monthly':
      resetSeconds = 30 * 86400
      break
    case 'custom':
      resetSeconds = Number(plan?.quota_reset_custom_seconds || 0)
      break
    default:
      return 0
  }

  if (resetSeconds <= 0 || durationSeconds <= 0) return 0
  return Math.floor(durationSeconds / resetSeconds)
}
