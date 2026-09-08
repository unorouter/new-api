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
import { useTranslation } from 'react-i18next'

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import dayjs from '@/lib/dayjs'
import { formatTimestampRelative, formatTimestampToDate } from '@/lib/format'
import { cn } from '@/lib/utils'

import type { ApiKey } from '../types'

interface ApiKeyTimestampCellProps {
  timestamp: number
  now: number
  locale?: string
  justNowLabel: string
  className?: string
}

export function ApiKeyTimestampCell(props: ApiKeyTimestampCellProps) {
  if (!props.timestamp || props.timestamp === -1) {
    return <span className='text-muted-foreground text-xs'>-</span>
  }

  const timestampMs = props.timestamp * 1000
  const isJustNow = timestampMs <= props.now && props.now - timestampMs < 60_000
  const relativeTime = isJustNow
    ? props.justNowLabel
    : formatTimestampRelative(props.timestamp, 'seconds', props.locale)
  const [relativePrefix, relativeNumber, relativeSuffix] = relativeTime.split(
    /(\p{Number}+(?:[.,\u00a0\u202f]\p{Number}+)*)/u
  )
  const absoluteTime = formatTimestampToDate(props.timestamp)

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <time
            dateTime={new Date(timestampMs).toISOString()}
            tabIndex={0}
            className={cn('block truncate text-xs', props.className)}
          />
        }
      >
        {relativePrefix}
        {relativeNumber && (
          <span className='font-mono tabular-nums'>{relativeNumber}</span>
        )}
        {relativeSuffix}
      </TooltipTrigger>
      <TooltipContent>
        <span className='font-mono tabular-nums'>{absoluteTime}</span>
      </TooltipContent>
    </Tooltip>
  )
}

export function ApiKeyActivityCell(props: {
  apiKey: ApiKey
  now: number
  layout?: 'rows' | 'columns'
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const accessedTime = props.apiKey.accessed_time
  const isStale =
    accessedTime > 0 &&
    accessedTime * 1000 < dayjs(props.now).subtract(3, 'month').valueOf()

  return (
    <div
      className={cn(
        'grid min-w-0 gap-y-1 text-xs',
        props.layout === 'columns'
          ? 'grid-flow-col grid-cols-2 grid-rows-[auto_1fr] items-start gap-x-3'
          : 'grid-cols-[auto_minmax(0,1fr)] items-center gap-x-2'
      )}
    >
      <span className='text-muted-foreground'>{t('Created')}</span>
      <ApiKeyTimestampCell
        timestamp={props.apiKey.created_time}
        now={props.now}
        locale={locale}
        justNowLabel={t('Just now')}
        className={cn(
          'text-muted-foreground',
          props.layout === 'columns' && 'whitespace-normal'
        )}
      />
      <span className='text-muted-foreground'>{t('Last Used')}</span>
      <ApiKeyTimestampCell
        timestamp={accessedTime}
        now={props.now}
        locale={locale}
        justNowLabel={t('Just now')}
        className={cn(
          isStale ? 'text-warning' : 'text-muted-foreground',
          props.layout === 'columns' && 'whitespace-normal'
        )}
      />
    </div>
  )
}
