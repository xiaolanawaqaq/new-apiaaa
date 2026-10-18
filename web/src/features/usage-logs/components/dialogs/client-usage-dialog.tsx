import { useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import { BarChart3, RefreshCw } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
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
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'
import { formatLogQuota, formatNumber, formatTimestampToDate } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'

import { getClientAgentUsage } from '../api'
import type { ClientAgentUsageRow } from '../types'
import { buildApiParams } from '../lib/utils'

const route = getRouteApi('/_authenticated/usage-logs/$section')

function formatRate(requests: number, errors: number) {
  if (requests <= 0) return '-'
  return `${Math.round(((requests - errors) / requests) * 100)}%`
}

function ClientUsageTable(props: {
  rows: ClientAgentUsageRow[]
  unknown?: boolean
}) {
  const { t } = useTranslation()
  const hasData = props.rows.length > 0

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
        {hasData ? (
          props.rows.map((row) => (
            <TableRow key={`${row.client_agent}:${row.user_agent ?? ''}`}>
              <TableCell className='max-w-[260px]'>
                {props.unknown && row.user_agent ? (
                  <Tooltip>
                    <TooltipTrigger
                      render={
                        <span className='block max-w-[260px] truncate font-mono text-xs' />
                      }
                    >
                      {row.user_agent}
                    </TooltipTrigger>
                    <TooltipContent side='top' className='max-w-xs break-all'>
                      {row.user_agent}
                    </TooltipContent>
                  </Tooltip>
                ) : (
                  <span className='text-sm'>
                    {row.client_agent || t('Unknown')}
                  </span>
                )}
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
          ))
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

export function ClientUsageDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const searchParams = route.useSearch()
  const query = useQuery({
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
    enabled: props.open,
    placeholderData: (previousData) => previousData,
  })

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
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
      {query.isPending ? (
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
            <ClientUsageTable rows={query.data.data?.items ?? []} />
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
