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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { getRouteApi, Link } from '@tanstack/react-router'
import { ArrowLeft, BarChart3, Copy, FileText, RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge, type StatusBadgeProps } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { TitledCard } from '@/components/ui/titled-card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useUpdateOption } from '@/features/system-settings/hooks/use-update-option'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { formatLogQuota, formatNumber, formatTimestampToDate } from '@/lib/format'
import { ROLE } from '@/lib/roles'
import { requireServerSuccess } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

import { getClientAgentUsage, getClientAgentUsageLogs } from '../../api'
import { LOG_TYPE_ENUM } from '../../constants'
import { getLogTypeConfig } from '../../lib'
import { buildApiParams } from '../../lib/utils'
import type { ClientAgentLogItem, ClientAgentUsageRow } from '../../types'
import { ClientUsageTrendDialog } from './client-usage-trend-dialog'

const route = getRouteApi('/_authenticated/usage-logs/$section')

type ClientSelection = {
  clientAgent: string
  rawUserAgent?: string
  unknown?: boolean
}

function useClientUsageQuery(open: boolean) {
  const searchParams = route.useSearch()
  return useQuery({
    queryKey: ['usage-logs-client-usage', searchParams],
    queryFn: async () => {
      const params = buildApiParams({
        page: 1,
        pageSize: 1,
        searchParams,
        columnFilters: [],
        isAdmin: true,
      })
      return requireServerSuccess(
        await getClientAgentUsage({
          start_timestamp: params.start_timestamp,
          end_timestamp: params.end_timestamp,
          username: params.username,
          model_name: params.model_name,
          token_name: params.token_name,
          channel: params.channel,
          group: params.group,
        })
      )
    },
    enabled: open,
    placeholderData: (previousData) => previousData,
  })
}

function ClientUsageTable(props: {
  rows: ClientAgentUsageRow[]
  unknown?: boolean
  onSelect: (selection: ClientSelection) => void
}) {
  const { t } = useTranslation()

  return (
    <Table withContainer={false}>
      <TableHeader>
        <TableRow>
          <TableHead>{props.unknown ? t('User Agent') : t('Client')}</TableHead>
          <TableHead className='text-right'>{t('Requests')}</TableHead>
          <TableHead className='text-right'>{t('Errors')}</TableHead>
          <TableHead className='text-right'>{t('Tokens')}</TableHead>
          <TableHead className='text-right'>{t('Quota')}</TableHead>
          <TableHead className='text-right'>{t('Last seen')}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {props.rows.length > 0 ? (
          props.rows.map((row) => {
            const label = props.unknown
              ? row.user_agent || ''
              : row.client_agent
            let selection: ClientSelection
            if (props.unknown) {
              selection = { clientAgent: '', rawUserAgent: label, unknown: true }
            } else {
              selection = { clientAgent: row.client_agent }
            }
            return (
              <TableRow key={`${row.client_agent}:${row.user_agent ?? ''}`}>
                <TableCell className='max-w-[260px]'>
                  <button
                    type='button'
                    className='block max-w-[260px] truncate text-left text-sm underline-offset-4 hover:underline'
                    aria-label={label || t('Unknown')}
                    onClick={() => props.onSelect(selection)}
                  >
                    {label || t('Unknown')}
                  </button>
                </TableCell>
                <TableCell className='text-right font-mono text-xs tabular-nums'>
                  {formatNumber(row.requests)}
                </TableCell>
                <TableCell
                  className={cn(
                    'text-right font-mono text-xs tabular-nums',
                    row.errors > 0 && 'text-destructive'
                  )}
                >
                  {formatNumber(row.errors)}
                </TableCell>
                <TableCell className='text-right font-mono text-xs tabular-nums'>
                  {formatNumber(row.prompt_tokens + row.completion_tokens)}
                </TableCell>
                <TableCell className='text-right font-mono text-xs tabular-nums'>
                  {formatLogQuota(row.quota)}
                </TableCell>
                <TableCell className='text-right font-mono text-xs tabular-nums'>
                  {formatTimestampToDate(row.last_seen)}
                </TableCell>
              </TableRow>
            )
          })
        ) : (
          <TableRow>
            <TableCell colSpan={6} className='text-muted-foreground text-center'>
              {t('No data')}
            </TableCell>
          </TableRow>
        )}
      </TableBody>
    </Table>
  )
}

function ContentPreview(props: { value?: string; emptyLabel: string }) {
  if (!props.value) {
    return (
      <span className='text-muted-foreground text-xs'>{props.emptyLabel}</span>
    )
  }
  return (
    <span className='block max-w-[220px] truncate text-xs' title={props.value}>
      {props.value}
    </span>
  )
}

function ClientLogContentDialog(props: {
  item: ClientAgentLogItem | null
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const { copiedText, copyToClipboard } = useCopyToClipboard({ notify: false })
  const item = props.item

  const sections = [
    { key: 'request', label: t('Input content'), value: item?.request_content },
    { key: 'response', label: t('Output content'), value: item?.response_content },
  ]
  // Error logs carry the failure reason in content; consume logs only hold
  // billing text, which belongs in the log list rather than this dialog.
  if (item && item.type === LOG_TYPE_ENUM.ERROR && item.content) {
    sections.push({ key: 'log', label: t('Details'), value: item.content })
  }

  return (
    <Dialog
      open={item !== null}
      onOpenChange={props.onOpenChange}
      title={t('Content details')}
      description={
        item
          ? `${formatTimestampToDate(item.created_at)} · ${item.model_name || '-'}`
          : undefined
      }
      contentClassName='sm:max-w-3xl'
      contentHeight='min(70dvh, 560px)'
      bodyClassName='space-y-4'
    >
      {item && !item.content_captured ? (
        <p className='text-muted-foreground text-sm'>
          {t(
            'No captured content for this request. Enable content logging under Prompt Security to record future requests.'
          )}
        </p>
      ) : null}
      {sections.map((section) => (
        <div key={section.key} className='space-y-2'>
          <div className='flex items-center justify-between gap-2'>
            <span className='text-sm font-medium'>{section.label}</span>
            {section.value ? (
              <Button
                variant='ghost'
                size='sm'
                onClick={() => void copyToClipboard(section.value ?? '')}
                aria-label={t('Copy content')}
              >
                <Copy className='size-3.5' aria-hidden='true' />
                {copiedText === section.value ? t('Copied') : t('Copy')}
              </Button>
            ) : null}
          </div>
          <div className='bg-muted/50 max-h-64 overflow-auto rounded-md border p-3'>
            <pre className='text-xs leading-relaxed break-words whitespace-pre-wrap'>
              {section.value || t('No data')}
            </pre>
          </div>
        </div>
      ))}
    </Dialog>
  )
}

function CaptureDisabledNotice() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const updateOption = useUpdateOption()
  const isRoot = useAuthStore(
    (state) => state.auth.user?.role === ROLE.SUPER_ADMIN
  )

  const handleEnable = async () => {
    await updateOption.mutateAsync({
      key: 'SecureContentLogEnabled',
      value: true,
    })
    await queryClient.invalidateQueries({
      queryKey: ['usage-logs-client-detail'],
    })
  }

  return (
    <Alert className='border-amber-200 bg-amber-50 dark:border-amber-900/60 dark:bg-amber-950/40'>
      <AlertTitle>{t('Content capture is disabled')}</AlertTitle>
      <AlertDescription>
        {t(
          'Request and response text is only recorded while content logging is enabled, and entries expire after the configured retention window.'
        )}
        {isRoot ? (
          <>
            <Button
              variant='link'
              size='sm'
              className='ml-1 h-auto p-0 align-baseline'
              onClick={() => void handleEnable()}
              disabled={updateOption.isPending}
            >
              {t('Enable content logging')}
            </Button>
            <Link
              className='text-primary ml-3 underline underline-offset-4'
              to='/system-settings/security/$section'
              params={{ section: 'prompt-security' }}
            >
              {t('Open settings')}
            </Link>
          </>
        ) : null}
      </AlertDescription>
    </Alert>
  )
}

function ClientLogsTable(props: { items: ClientAgentLogItem[] }) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState<ClientAgentLogItem | null>(null)

  return (
    <>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('Time')}</TableHead>
            <TableHead>{t('Type')}</TableHead>
            <TableHead>{t('Model')}</TableHead>
            <TableHead className='text-right'>{t('Tokens')}</TableHead>
            <TableHead className='text-right'>{t('Quota')}</TableHead>
            <TableHead>{t('Request Path')}</TableHead>
            <TableHead>{t('Input')}</TableHead>
            <TableHead>{t('Output')}</TableHead>
            <TableHead className='text-right'>{t('Details')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {props.items.map((item) => {
            const config = getLogTypeConfig(item.type)
            return (
              <TableRow key={item.id}>
                <TableCell className='font-mono text-xs tabular-nums'>
                  {formatTimestampToDate(item.created_at)}
                </TableCell>
                <TableCell>
                  <StatusBadge
                    label={t(config.label)}
                    variant={config.color as StatusBadgeProps['variant']}
                    copyable={false}
                  />
                </TableCell>
                <TableCell className='max-w-[160px] truncate text-xs'>
                  {item.model_name || '-'}
                </TableCell>
                <TableCell className='text-right font-mono text-xs tabular-nums'>
                  {formatNumber(item.prompt_tokens + item.completion_tokens)}
                </TableCell>
                <TableCell className='text-right font-mono text-xs tabular-nums'>
                  {formatLogQuota(item.quota)}
                </TableCell>
                <TableCell className='max-w-[160px] truncate text-xs font-mono'>
                  {item.request_path || '-'}
                </TableCell>
                <TableCell>
                  <ContentPreview
                    value={item.request_content}
                    emptyLabel={t('Not captured')}
                  />
                </TableCell>
                <TableCell>
                  <ContentPreview
                    value={item.response_content}
                    emptyLabel={t('Not captured')}
                  />
                </TableCell>
                <TableCell className='text-right'>
                  <Button
                    variant='ghost'
                    size='sm'
                    onClick={() => setSelected(item)}
                    aria-label={t('View content')}
                  >
                    <FileText className='size-3.5' aria-hidden='true' />
                    {t('View')}
                  </Button>
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
      <ClientLogContentDialog
        item={selected}
        onOpenChange={(open) => {
          if (!open) setSelected(null)
        }}
      />
    </>
  )
}

function ClientDetail(props: {
  selection: ClientSelection
  onBack: () => void
}) {
  const { t } = useTranslation()
  const searchParams = route.useSearch()
  const [page, setPage] = useState(1)
  const [trendOpen, setTrendOpen] = useState(false)
  const pageSize = 20
  const query = useQuery({
    queryKey: [
      'usage-logs-client-detail',
      searchParams,
      props.selection,
      page,
    ],
    queryFn: async () => {
      const params = buildApiParams({
        page: 1,
        pageSize: 1,
        searchParams,
        columnFilters: [],
        isAdmin: true,
      })
      return requireServerSuccess(
        await getClientAgentUsageLogs({
          start_timestamp: params.start_timestamp,
          end_timestamp: params.end_timestamp,
          username: params.username,
          model_name: params.model_name,
          token_name: params.token_name,
          channel: params.channel,
          group: params.group,
          client_agent: props.selection.clientAgent || undefined,
          user_agent: props.selection.rawUserAgent || undefined,
          unknown: props.selection.unknown ? 'true' : undefined,
          p: page,
          page_size: pageSize,
        })
      )
    },
    placeholderData: (previousData) => previousData,
  })

  const data = query.data.data
  const total = data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  return (
    <div className='min-w-0 space-y-4'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div className='flex min-w-0 items-center gap-2'>
          <Button variant='ghost' size='sm' onClick={props.onBack}>
            <ArrowLeft className='size-3.5' aria-hidden='true' />
            {t('Back')}
          </Button>
          <span className='truncate text-sm font-medium'>
            {props.selection.rawUserAgent || props.selection.clientAgent}
          </span>
        </div>
        <div className='flex items-center gap-2'>
          <Button
            variant='outline'
            size='sm'
            onClick={() => setTrendOpen(true)}
          >
            <BarChart3 className='size-3.5' aria-hidden='true' />
            {t('Trend')}
          </Button>
          <Button
            variant='outline'
            size='sm'
            onClick={() => void query.refetch()}
            disabled={query.isFetching}
          >
            <RefreshCw
              className={cn('size-3.5', query.isFetching && 'animate-spin')}
              aria-hidden='true'
            />
            {t('Refresh')}
          </Button>
        </div>
      </div>

      {query.isPending ? (
        <LoadingState />
      ) : query.isError ? (
        <ErrorState
          title={t('Failed to load')}
          onRetry={() => void query.refetch()}
        />
      ) : (
        <>
          {data?.content_capture_enabled === false ? (
            <CaptureDisabledNotice />
          ) : null}
          <ClientLogsTable items={data?.items ?? []} />
          <div className='flex items-center justify-end gap-2'>
            <Button
              variant='outline'
              size='sm'
              disabled={page <= 1}
              onClick={() => setPage((current) => Math.max(1, current - 1))}
            >
              {t('Previous')}
            </Button>
            <span className='text-muted-foreground text-xs tabular-nums'>
              {page} / {totalPages}
            </span>
            <Button
              variant='outline'
              size='sm'
              disabled={page >= totalPages}
              onClick={() => setPage((current) => current + 1)}
            >
              {t('Next')}
            </Button>
          </div>
        </>
      )}
      <ClientUsageTrendDialog
        open={trendOpen}
        onOpenChange={setTrendOpen}
        clientAgent={props.selection.clientAgent || undefined}
        rawUserAgent={props.selection.rawUserAgent}
        unknown={props.selection.unknown}
      />
    </div>
  )
}

export function ClientUsageDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const [selection, setSelection] = useState<ClientSelection | null>(null)
  const query = useClientUsageQuery(props.open)

  const handleOpenChange = (open: boolean) => {
    if (!open) setSelection(null)
    props.onOpenChange(open)
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={handleOpenChange}
      title={
        <span className='flex items-center gap-2'>
          <BarChart3 className='size-4' aria-hidden='true' />
          {t('Client usage')}
        </span>
      }
      description={t('Aggregated from the most recent logs only.')}
      contentClassName='sm:max-w-3xl'
      contentHeight='min(70dvh, 640px)'
    >
      {selection ? (
        <ClientDetail
          selection={selection}
          onBack={() => setSelection(null)}
        />
      ) : query.isPending ? (
        <LoadingState />
      ) : query.isError ? (
        <ErrorState
          title={t('Failed to load')}
          onRetry={() => void query.refetch()}
        />
      ) : (
        <div className='min-w-0 space-y-4'>
          <TitledCard
            title={t('Client')}
            icon={<BarChart3 />}
            iconTone='info'
            disableHoverEffect
            contentClassName='overflow-x-auto'
          >
            <ClientUsageTable
              rows={query.data.data?.items ?? []}
              onSelect={setSelection}
            />
          </TitledCard>
          <TitledCard
            title={t('Unrecognized clients')}
            icon={<BarChart3 />}
            iconTone='warning'
            disableHoverEffect
            contentClassName='overflow-x-auto'
          >
            <ClientUsageTable
              rows={query.data.data?.unknown_user_agents ?? []}
              unknown
              onSelect={setSelection}
            />
          </TitledCard>
          <div className='flex flex-wrap items-center justify-between gap-2'>
            <span className='text-muted-foreground text-xs'>
              {t('Scanned {{count}} logs', {
                count: query.data.data?.scanned ?? 0,
              })}
              {query.data.data?.truncated
                ? ` ${t('Analysis truncated to the scan limit.')}`
                : ''}
            </span>
            <Button
              variant='outline'
              size='sm'
              onClick={() => void query.refetch()}
              disabled={query.isFetching}
            >
              <RefreshCw
                className={cn('size-3.5', query.isFetching && 'animate-spin')}
                aria-hidden='true'
              />
              {t('Refresh')}
            </Button>
          </div>
        </div>
      )}
    </Dialog>
  )
}
