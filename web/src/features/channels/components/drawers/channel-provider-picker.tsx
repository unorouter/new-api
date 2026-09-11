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
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { Badge } from '@/components/ui/badge'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { AutoGroupFlowBorder } from '@/features/keys/components/auto-group-visuals'
import { PluginIcon } from '@/features/task-plugins/components/plugin-icon'
import { useMediaQuery } from '@/hooks/use-media-query'
import { resolveLocalizedText } from '@/lib/localized-text'

import type { TaskPluginOption } from '../../api'
import {
  CHANNEL_PROVIDER_PRESENTATION,
  CHANNEL_TYPE_OPTIONS,
  CHANNEL_TYPE_TASK_PLUGIN,
  type ChannelProviderPresentation,
} from '../../constants'
import { CHANNEL_TYPE_ADVANCED_CUSTOM } from '../../lib/advanced-custom'
import type { ChannelProviderTarget } from '../../lib/channel-configuration'
import { ChannelTypeLogo } from '../channel-type-badge'

type ChannelProviderPickerProps = {
  plugins: TaskPluginOption[]
  currentProvider?: ChannelProviderTarget | null
  canBindPlugin: boolean
  loading: boolean
  failed: boolean
  disabled: boolean
  onRetry: () => void
  onSelect: (target: ChannelProviderTarget) => void
}

export function ChannelProviderPicker(props: ChannelProviderPickerProps) {
  const { t, i18n } = useTranslation()
  const shouldReduceMotion = useMediaQuery('(prefers-reduced-motion: reduce)')
  const [search, setSearch] = useState('')
  const [selectedFilter, setSelectedFilter] = useState('all')
  const filter =
    !props.canBindPlugin && selectedFilter === 'plugin' ? 'all' : selectedFilter
  const keyword = search.trim().toLocaleLowerCase()
  const options = useMemo(() => {
    const entries: Array<{
      id: string
      label: string
      target: ChannelProviderTarget
      plugin?: TaskPluginOption
      description?: string
      detail?: string
      badge?: ChannelProviderPresentation['badge']
      searchText: string
    }> = []
    for (const option of CHANNEL_TYPE_OPTIONS) {
      if (option.value === CHANNEL_TYPE_TASK_PLUGIN) {
        if (!props.canBindPlugin || (filter !== 'all' && filter !== 'plugin')) {
          continue
        }
        for (const plugin of props.plugins) {
          entries.push({
            id: `plugin:${plugin.key}`,
            label: plugin.name,
            target: { kind: 'plugin', key: plugin.key },
            plugin,
            description: resolveLocalizedText(
              plugin.description,
              i18n.language
            ),
            searchText: `${plugin.name} ${plugin.key}`,
          })
        }
      } else if (filter !== 'plugin') {
        const isCustom =
          option.value === 8 || option.value === CHANNEL_TYPE_ADVANCED_CUSTOM
        if (filter === 'custom' && !isCustom) continue
        if (filter === 'builtin' && isCustom) continue
        const presentation = CHANNEL_PROVIDER_PRESENTATION[option.value]
        entries.push({
          id: `builtin:${option.value}`,
          label: t(option.label),
          target: { kind: 'builtin', type: option.value },
          description: presentation
            ? t(presentation.descriptionKey)
            : undefined,
          detail: presentation?.detailKey
            ? t(presentation.detailKey)
            : undefined,
          badge: presentation?.badge,
          searchText: `${option.value} ${option.label} ${t(option.label)}`,
        })
      }
    }
    return entries.filter((entry) =>
      entry.searchText.toLocaleLowerCase().includes(keyword)
    )
  }, [filter, i18n.language, keyword, props.canBindPlugin, props.plugins, t])

  const customType = Number(search.trim())
  const canUseCustomType =
    (filter === 'all' || filter === 'custom') &&
    /^\d+$/.test(search.trim()) &&
    Number.isSafeInteger(customType) &&
    customType > 0 &&
    !CHANNEL_TYPE_OPTIONS.some((option) => option.value === customType)
  const showPluginStatus =
    props.canBindPlugin && (filter === 'all' || filter === 'plugin')
  let currentProviderId: string | undefined
  if (props.currentProvider?.kind === 'builtin') {
    currentProviderId = `builtin:${props.currentProvider.type}`
  } else if (props.currentProvider?.kind === 'plugin') {
    currentProviderId = `plugin:${props.currentProvider.key}`
  }

  return (
    <Tabs
      value={filter}
      onValueChange={(value) => setSelectedFilter(String(value))}
      className='min-h-0 flex-1 gap-4 p-4 sm:p-6'
    >
      <TabsList
        aria-label={t('Provider source')}
        className='max-w-full shrink-0 flex-wrap justify-start group-data-horizontal/tabs:h-auto'
      >
        <TabsTrigger value='all' className='h-auto'>
          {t('All')}
        </TabsTrigger>
        <TabsTrigger value='builtin' className='h-auto'>
          {t('Built-in')}
        </TabsTrigger>
        {props.canBindPlugin && (
          <TabsTrigger value='plugin' className='h-auto'>
            {t('Plugins')}
          </TabsTrigger>
        )}
        <TabsTrigger value='custom' className='h-auto'>
          {t('Custom')}
        </TabsTrigger>
      </TabsList>
      {/* Keep the search and command state when changing categories. */}
      <TabsContent
        value={filter}
        keepMounted
        className='flex min-h-0 flex-1 flex-col gap-4'
      >
        <Command
          label={t('Search providers, plugins, or type numbers')}
          defaultValue={currentProviderId}
          shouldFilter={false}
          className='min-h-0 flex-1 border'
        >
          <CommandInput
            autoFocus
            value={search}
            onValueChange={setSearch}
            placeholder={t('Search providers, plugins, or type numbers')}
            aria-label={t('Search providers, plugins, or type numbers')}
          />
          <CommandList className='max-h-none min-h-0 flex-1'>
            <CommandEmpty>{t('No matching provider')}</CommandEmpty>
            <CommandGroup className='md:p-3 md:[&_[cmdk-group-items]]:grid md:[&_[cmdk-group-items]]:grid-cols-2 md:[&_[cmdk-group-items]]:gap-3 xl:[&_[cmdk-group-items]]:grid-cols-3'>
              {options.map((option) => (
                <CommandItem
                  key={option.id}
                  value={option.id}
                  aria-current={
                    option.id === currentProviderId ? true : undefined
                  }
                  data-checked={option.id === currentProviderId}
                  aria-label={
                    option.target.kind === 'builtin'
                      ? `${option.label} ${t('Built-in')} #${option.target.type}`
                      : `${option.label} ${t('Plugin')} ${option.target.key}`
                  }
                  aria-description={
                    [
                      option.badge && t(option.badge.labelKey),
                      option.detail || option.description,
                    ]
                      .filter(Boolean)
                      .join(' · ') || undefined
                  }
                  disabled={props.disabled}
                  onSelect={() => props.onSelect(option.target)}
                  className='md:data-selected:border-primary/50 md:data-selected:bg-primary/5 gap-3 px-3 py-3 md:min-h-32 md:flex-col md:items-stretch md:justify-between md:rounded-xl md:border md:p-4 md:[&>svg]:hidden'
                >
                  {option.target.kind === 'builtin' &&
                    option.target.type === CHANNEL_TYPE_ADVANCED_CUSTOM && (
                      <AutoGroupFlowBorder
                        shouldReduceMotion={
                          shouldReduceMotion || props.disabled
                        }
                      />
                    )}
                  <span className='contents md:flex md:min-w-0 md:items-start md:gap-3'>
                    <span className='shrink-0'>
                      {option.plugin ? (
                        <PluginIcon plugin={option.plugin} size={24} />
                      ) : (
                        <ChannelTypeLogo
                          type={
                            option.target.kind === 'builtin'
                              ? option.target.type
                              : CHANNEL_TYPE_TASK_PLUGIN
                          }
                          size={24}
                        />
                      )}
                    </span>
                    <span className='min-w-0 flex-1'>
                      <span className='flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1'>
                        <span className='max-w-full truncate font-medium md:line-clamp-2 md:break-words md:whitespace-normal'>
                          {option.label}
                        </span>
                        {option.badge && (
                          <StatusBadge
                            label={t(option.badge.labelKey)}
                            variant={
                              option.badge.tone === 'warning'
                                ? 'warning'
                                : 'info'
                            }
                            copyable={false}
                            size='sm'
                            className={
                              option.badge.tone === 'warning'
                                ? 'bg-warning/10 text-xs text-[color-mix(in_oklab,var(--warning)_50%,var(--foreground))]'
                                : 'bg-primary/10 text-xs text-[color-mix(in_oklab,var(--primary)_50%,var(--foreground))]'
                            }
                          />
                        )}
                      </span>
                      {option.plugin && (
                        <span className='text-muted-foreground block truncate text-xs'>
                          {option.plugin.key}
                          <span className='md:hidden'>
                            {' · '}
                            {t('{{count}} models', {
                              count: option.plugin.models.length,
                            })}
                          </span>
                        </span>
                      )}
                      {option.description && (
                        <span
                          className='text-muted-foreground mt-1 line-clamp-2 text-xs leading-relaxed break-words'
                          title={option.detail || option.description}
                        >
                          {option.description}
                        </span>
                      )}
                    </span>
                  </span>
                  <span className='contents md:flex md:items-center md:justify-between md:gap-3'>
                    <Badge
                      variant={
                        option.id === currentProviderId ? 'default' : 'outline'
                      }
                    >
                      {option.id === currentProviderId && t('Current')}
                      {option.id !== currentProviderId &&
                        (option.plugin ? t('Plugin') : t('Built-in'))}
                    </Badge>
                    {option.target.kind === 'builtin' && (
                      <span className='text-muted-foreground text-xs'>
                        #{option.target.type}
                      </span>
                    )}
                    {option.plugin && (
                      <span className='text-muted-foreground hidden text-xs md:inline'>
                        {t('{{count}} models', {
                          count: option.plugin.models.length,
                        })}
                      </span>
                    )}
                  </span>
                </CommandItem>
              ))}
              {canUseCustomType && (
                <CommandItem
                  value={`custom:${customType}`}
                  aria-current={
                    currentProviderId === `builtin:${customType}`
                      ? true
                      : undefined
                  }
                  data-checked={currentProviderId === `builtin:${customType}`}
                  aria-label={t('Use channel type {{type}}', {
                    type: customType,
                  })}
                  disabled={props.disabled}
                  onSelect={() =>
                    props.onSelect({ kind: 'builtin', type: customType })
                  }
                  className='md:data-selected:border-primary/50 md:data-selected:bg-primary/5 gap-3 px-3 py-3 md:min-h-32 md:rounded-xl md:border md:p-4 md:[&>svg]:hidden'
                >
                  <span className='shrink-0'>
                    <ChannelTypeLogo type={customType} size={24} />
                  </span>
                  <span className='min-w-0 break-words'>
                    {t('Use channel type {{type}}', { type: customType })}
                  </span>
                  {currentProviderId === `builtin:${customType}` && (
                    <Badge>{t('Current')}</Badge>
                  )}
                </CommandItem>
              )}
            </CommandGroup>
          </CommandList>
        </Command>
        {showPluginStatus && props.loading && (
          <LoadingState inline message={t('Loading plugins...')} />
        )}
        {showPluginStatus && props.failed && (
          <ErrorState
            className='min-h-0 p-3'
            title={t('Failed to load plugins')}
            onRetry={props.onRetry}
          />
        )}
        {showPluginStatus &&
          !props.loading &&
          !props.failed &&
          props.plugins.length === 0 && (
            <EmptyState
              className='min-h-0 p-3'
              title={t('No plugins available for binding')}
            />
          )}
      </TabsContent>
    </Tabs>
  )
}
