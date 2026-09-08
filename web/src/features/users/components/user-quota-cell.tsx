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

import { StatusBadge } from '@/components/status-badge'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { cn } from '@/lib/utils'
import { useSystemConfigStore } from '@/stores/system-config-store'

type UserQuotaCellProps = {
  remaining: number
  used: number
}

export function UserQuotaCell(props: UserQuotaCellProps) {
  const { t } = useTranslation()
  useSystemConfigStore((state) => state.config.currency)

  if (props.remaining === 0 && props.used === 0) {
    return (
      <StatusBadge
        label={t('No Quota')}
        variant='neutral'
        copyable={false}
        className='-ml-1.5'
      />
    )
  }

  return (
    <div className='min-w-0 space-y-1 text-left tabular-nums'>
      <div
        className={cn(
          'font-mono text-sm font-semibold whitespace-nowrap',
          props.remaining < 0 && 'text-destructive',
          props.remaining === 0 && 'text-muted-foreground'
        )}
      >
        {formatQuotaWithCurrency(props.remaining, { showSymbol: false })}
      </div>
      <div className='text-muted-foreground flex items-baseline gap-1 text-xs whitespace-nowrap'>
        <span>{t('Used amount')}</span>
        <span className='font-mono'>
          {formatQuotaWithCurrency(props.used, { showSymbol: false })}
        </span>
      </div>
    </div>
  )
}
