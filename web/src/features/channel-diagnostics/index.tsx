import { useState } from 'react'
import type {
  ColumnDef,
  OnChangeFn,
  PaginationState,
  SortingState,
} from '@tanstack/react-table'
import { useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { SectionPageLayout } from '@/components/layout'
import { StatusBadge } from '@/components/status-badge'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { formatTimestampToDate } from '@/lib/format'
import { useTableUrlState } from '@/hooks/use-table-url-state'
import {
  DataTablePage,
  DataTableRow,
  useDataTable,
} from '@/components/data-table'
import { getChannelDiagnosticStats, getChannelDiagnostics } from './api'
import {
  CHANNEL_STATUS_AUTO_DISABLED,
  CHANNEL_STATUS_ENABLED,
  CHANNEL_STATUS_MANUAL_DISABLED,
  TRIGGER_SOURCES,
  formatDuration,
  statusLabel,
  statusVariant,
} from './constants'
import type {
  ChannelDiagnosticStatRow,
  ChannelDiagnosticRecord,
} from './types'

const route = getRouteApi('/_authenticated/channel-diagnostics/')

const DIAGNOSTIC_SORTABLE_COLUMNS = new Set([
  'created_at',
  'first_seen_at',
  'status_code',
  'occurrence_count',
  'channel_id',
  'seconds_in_prev_status',
])

const ROW_TYPE_FILTER_OPTIONS = [
  { label: 'Transitions', value: 'transitions' },
  { label: 'Probe Failures', value: 'probe' },
]

function StatusTransition(props: { from: number; to: number }) {
  const { t } = useTranslation()
  return (
    <div className='flex items-center gap-1'>
      <StatusBadge
        label={t(statusLabel(props.from))}
        variant={statusVariant(props.from)}
        size='sm'
      />
      <span className='text-muted-foreground'>{'->'}</span>
      <StatusBadge
        label={t(statusLabel(props.to))}
        variant={statusVariant(props.to)}
        size='sm'
      />
    </div>
  )
}

function useHistoryColumns(): ColumnDef<ChannelDiagnosticRecord>[] {
  const { t } = useTranslation()
  return [
    {
      accessorKey: 'created_at',
      header: t('Last Seen'),
      enableSorting: true,
      cell: ({ row }) => (
        <span className='font-mono text-xs tabular-nums'>
          {formatTimestampToDate(row.original.created_at)}
        </span>
      ),
      size: 160,
    },
    {
      accessorKey: 'first_seen_at',
      header: t('First Seen'),
      enableSorting: true,
      cell: ({ row }) =>
        row.original.first_seen_at ? (
          <span className='font-mono text-xs tabular-nums'>
            {formatTimestampToDate(row.original.first_seen_at)}
          </span>
        ) : (
          <span className='text-muted-foreground'>-</span>
        ),
      size: 160,
    },
    {
      accessorKey: 'channel_id',
      header: t('Channel'),
      enableSorting: true,
      cell: ({ row }) => (
        <div className='flex min-w-0 flex-col'>
          <span className='font-mono text-xs'>#{row.original.channel_id}</span>
          <span className='text-muted-foreground truncate text-xs'>
            {row.original.channel_name}
          </span>
        </div>
      ),
      size: 200,
    },
    {
      id: 'status',
      header: t('Status'),
      cell: ({ row }) => (
        <div className='flex items-center gap-1'>
          <StatusTransition from={row.original.from_status} to={row.original.to_status} />
          {row.original.probe_only ? (
            <StatusBadge label={t('probe')} variant='warning' size='sm' />
          ) : null}
        </div>
      ),
      size: 260,
    },
    {
      accessorKey: 'trigger_source',
      header: t('Source'),
      cell: ({ row }) => (
        <StatusBadge label={t(row.original.trigger_source)} variant='neutral' size='sm' />
      ),
      size: 140,
    },
    {
      accessorKey: 'status_code',
      header: t('Code'),
      enableSorting: true,
      cell: ({ row }) =>
        row.original.status_code ? (
          <span className='font-mono text-xs'>{row.original.status_code}</span>
        ) : (
          <span className='text-muted-foreground'>-</span>
        ),
      size: 70,
    },
    {
      accessorKey: 'occurrence_count',
      header: t('Occurrences'),
      enableSorting: true,
      cell: ({ row }) =>
        row.original.occurrence_count > 1 ? (
          <StatusBadge
            label={`x${row.original.occurrence_count}`}
            variant='warning'
            size='sm'
          />
        ) : (
          <span className='text-muted-foreground'>-</span>
        ),
      size: 100,
    },
    {
      accessorKey: 'model_name',
      header: t('Model'),
      cell: ({ row }) => (
        <span className='truncate text-xs'>{row.original.model_name || '-'}</span>
      ),
      size: 180,
    },
    {
      accessorKey: 'seconds_in_prev_status',
      header: t('Time in Previous Status'),
      enableSorting: true,
      cell: ({ row }) => (
        <span className='font-mono text-xs'>
          {formatDuration(row.original.seconds_in_prev_status)}
        </span>
      ),
      size: 120,
    },
    {
      accessorKey: 'status_reason',
      header: t('Reason'),
      cell: ({ row }) => (
        <span className='text-muted-foreground block max-w-105 whitespace-pre-wrap wrap-break-word text-xs'>
          {row.original.status_reason || '-'}
        </span>
      ),
      size: 420,
    },
  ]
}

const STATUS_FILTER_OPTIONS = [
  { label: 'Enabled', value: String(CHANNEL_STATUS_ENABLED) },
  { label: 'Manually Disabled', value: String(CHANNEL_STATUS_MANUAL_DISABLED) },
  { label: 'Auto Disabled', value: String(CHANNEL_STATUS_AUTO_DISABLED) },
]

const TRIGGER_FILTER_OPTIONS = TRIGGER_SOURCES.map((s) => ({
  label: s,
  value: s,
}))

function HistoryTab() {
  const { t } = useTranslation()
  const columns = useHistoryColumns()
  const [sorting, setSorting] = useState<SortingState>([])

  const {
    globalFilter,
    onGlobalFilterChange,
    columnFilters,
    onColumnFiltersChange,
    pagination,
    onPaginationChange,
  } = useTableUrlState({
    search: route.useSearch(),
    navigate: route.useNavigate(),
    pagination: { defaultPage: 1, defaultPageSize: 50 },
    globalFilter: { enabled: true, key: 'filter' },
    columnFilters: [
      { columnId: 'status', searchKey: 'status', type: 'array' },
      {
        columnId: 'trigger_source',
        searchKey: 'trigger_source',
        type: 'array',
      },
      { columnId: 'row_type', searchKey: 'row_type', type: 'array' },
      { columnId: 'status_code', searchKey: 'status_code', type: 'string' },
    ],
  })

  const toStatusFilter =
    (columnFilters.find((f) => f.id === 'status')?.value as string[]) || []
  const triggerFilter =
    (columnFilters.find((f) => f.id === 'trigger_source')?.value as string[]) ||
    []
  const rowTypeFilter =
    (columnFilters.find((f) => f.id === 'row_type')?.value as string[]) || []
  const statusCodeFilter =
    (columnFilters.find((f) => f.id === 'status_code')?.value as string) || ''

  const setSingleFilter = (id: string, value: string) => {
    const isArray = id !== 'status_code'
    onColumnFiltersChange((prev) => {
      const next = prev.filter((f) => f.id !== id)
      if (value) next.push({ id, value: isArray ? [value] : value })
      return next
    })
  }

  const activeSort = sorting[0]
  const sortParams =
    activeSort && DIAGNOSTIC_SORTABLE_COLUMNS.has(activeSort.id)
      ? { sort_by: activeSort.id, sort_order: activeSort.desc ? 'desc' : 'asc' }
      : {}

  const handleSortingChange: OnChangeFn<SortingState> = (updater) => {
    const next = typeof updater === 'function' ? updater(sorting) : updater
    setSorting(next)
  }

  const query = useQuery({
    queryKey: [
      'channel-diagnostics',
      'list',
      {
        keyword: globalFilter,
        to_status: toStatusFilter[0],
        trigger_source: triggerFilter[0],
        row_type: rowTypeFilter[0],
        status_code: statusCodeFilter,
        ...sortParams,
        page: pagination.pageIndex,
        pageSize: pagination.pageSize,
      },
    ],
    queryFn: () =>
      getChannelDiagnostics({
        p: pagination.pageIndex + 1,
        page_size: pagination.pageSize,
        keyword: globalFilter || undefined,
        to_status:
          toStatusFilter.length > 0 ? Number(toStatusFilter[0]) : undefined,
        trigger_source: triggerFilter[0] || undefined,
        row_type: rowTypeFilter[0] || undefined,
        status_code: statusCodeFilter ? Number(statusCodeFilter) : undefined,
        ...sortParams,
      }),
  })

  const data = query.data?.data
  const items = data?.items ?? []

  const { table } = useDataTable({
    data: items as unknown as Record<string, unknown>[],
    columns: columns as ColumnDef<Record<string, unknown>>[],
    pagination,
    sorting,
    onSortingChange: handleSortingChange,
    columnFilters,
    onColumnFiltersChange,
    globalFilter,
    onGlobalFilterChange,
    enableRowSelection: false,
    onPaginationChange,
    manualPagination: true,
    manualFiltering: true,
    manualSorting: true,
    totalCount: data?.total ?? 0,
  })

  return (
    <DataTablePage
      table={table}
      columns={columns as ColumnDef<Record<string, unknown>>[]}
      isLoading={query.isLoading}
      isFetching={query.isFetching}
      emptyTitle={t('No Status History')}
      emptyDescription={t(
        'No channel status changes recorded yet. Transitions appear here as channels are enabled or disabled.'
      )}
      toolbarProps={{
        searchPlaceholder: t('Filter by channel, reason...'),
        searchDebounceMs: 500,
        additionalSearch: (
          <div className='flex gap-2'>
            <NativeSelect
              value={rowTypeFilter[0] ?? ''}
              onChange={(e) => setSingleFilter('row_type', e.target.value)}
              className='w-full sm:w-40'
            >
              <option value=''>{t('Row Type')}</option>
              {ROW_TYPE_FILTER_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>
                  {t(o.label)}
                </option>
              ))}
            </NativeSelect>
            <NativeSelect
              value={toStatusFilter[0] ?? ''}
              onChange={(e) => setSingleFilter('status', e.target.value)}
              className='w-full sm:w-40'
            >
              <option value=''>{t('Status')}</option>
              {STATUS_FILTER_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>
                  {t(o.label)}
                </option>
              ))}
            </NativeSelect>
            <NativeSelect
              value={triggerFilter[0] ?? ''}
              onChange={(e) => setSingleFilter('trigger_source', e.target.value)}
              className='w-full sm:w-40'
            >
              <option value=''>{t('Source')}</option>
              {TRIGGER_FILTER_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>
                  {t(o.label)}
                </option>
              ))}
            </NativeSelect>
            <Input
              placeholder={t('Status code...')}
              value={statusCodeFilter}
              onChange={(e) => setSingleFilter('status_code', e.target.value)}
              className='w-full sm:w-35'
            />
          </div>
        ),
      }}
      renderRow={(row) => <DataTableRow key={row.id} row={row} />}
    />
  )
}

function useStatsColumns(): ColumnDef<ChannelDiagnosticStatRow>[] {
  const { t } = useTranslation()
  return [
    {
      accessorKey: 'channel_id',
      header: t('Channel'),
      cell: ({ row }) => (
        <div className='flex min-w-0 flex-col'>
          <span className='font-mono text-xs'>#{row.original.channel_id}</span>
          <span className='text-muted-foreground truncate text-xs'>
            {row.original.channel_name}
          </span>
        </div>
      ),
      size: 220,
    },
    {
      accessorKey: 'transitions',
      header: t('Transitions'),
      cell: ({ row }) => (
        <span className='font-mono text-xs'>{row.original.transitions}</span>
      ),
      size: 110,
    },
    {
      accessorKey: 'disable_count',
      header: t('Disables'),
      cell: ({ row }) => (
        <span className='font-mono text-xs'>{row.original.disable_count}</span>
      ),
      size: 100,
    },
    {
      accessorKey: 'uptime_percent',
      header: t('Uptime'),
      cell: ({ row }) => (
        <span className='font-mono text-xs'>
          {row.original.uptime_percent.toFixed(2)}%
        </span>
      ),
      size: 100,
    },
    {
      accessorKey: 'seconds_down',
      header: t('Total Downtime'),
      cell: ({ row }) => (
        <span className='font-mono text-xs'>
          {formatDuration(row.original.seconds_down)}
        </span>
      ),
      size: 140,
    },
    {
      accessorKey: 'last_to_status',
      header: t('Current'),
      cell: ({ row }) => (
        <StatusBadge
          label={t(statusLabel(row.original.last_to_status))}
          variant={statusVariant(row.original.last_to_status)}
          size='sm'
        />
      ),
      size: 150,
    },
  ]
}

function StatsTab() {
  const { t } = useTranslation()
  const columns = useStatsColumns()
  const [orderBy, setOrderBy] = useState('transitions')
  const [pagination, setPagination] = useState<PaginationState>({
    pageIndex: 0,
    pageSize: 100,
  })

  const query = useQuery({
    queryKey: ['channel-diagnostics', 'stats', orderBy],
    queryFn: () => getChannelDiagnosticStats({ order_by: orderBy, limit: 200 }),
  })

  const rows = query.data?.data ?? []

  const { table } = useDataTable({
    data: rows as unknown as Record<string, unknown>[],
    columns: columns as ColumnDef<Record<string, unknown>>[],
    pagination,
    enableRowSelection: false,
    onPaginationChange: setPagination,
    manualPagination: false,
    manualFiltering: true,
    totalCount: rows.length,
  })

  return (
    <div className='flex h-full flex-col gap-3'>
      <Tabs value={orderBy} onValueChange={setOrderBy}>
        <TabsList>
          <TabsTrigger value='transitions'>{t('Most Flapping')}</TabsTrigger>
          <TabsTrigger value='uptime'>{t('Worst Uptime')}</TabsTrigger>
          <TabsTrigger value='downtime'>{t('Most Downtime')}</TabsTrigger>
        </TabsList>
      </Tabs>
      <div className='min-h-0 flex-1'>
        <DataTablePage
          table={table}
          columns={columns as ColumnDef<Record<string, unknown>>[]}
          isLoading={query.isLoading}
          isFetching={query.isFetching}
          emptyTitle={t('No Status History')}
          emptyDescription={t('No channel status changes recorded yet.')}
          renderRow={(row) => <DataTableRow key={row.id} row={row} />}
        />
      </div>
    </div>
  )
}

export function ChannelDiagnostics() {
  const { t } = useTranslation()
  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>
        {t('Channel Diagnostics')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <Tabs defaultValue='history' className='flex h-full flex-col gap-3'>
          <TabsList>
            <TabsTrigger value='history'>{t('History')}</TabsTrigger>
            <TabsTrigger value='stats'>{t('Channel Analytics')}</TabsTrigger>
          </TabsList>
          <TabsContent value='history' className='min-h-0 flex-1'>
            <HistoryTab />
          </TabsContent>
          <TabsContent value='stats' className='min-h-0 flex-1'>
            <StatsTab />
          </TabsContent>
        </Tabs>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
