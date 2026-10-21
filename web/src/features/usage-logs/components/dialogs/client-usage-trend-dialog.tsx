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
import { useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import { BarChart3, RefreshCw } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { formatNumber, formatTimestampToDate } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import { buildApiParams } from '../../lib/utils'
import { getClientAgentUsageTrend } from '../../api'
import type { ClientAgentUsageTrendPoint } from '../../types'

const route = getRouteApi('/_authenticated/usage-logs/$section')

const CHART_WIDTH = 640
const CHART_HEIGHT = 160
const CHART_PADDING = 12

function trendPath(values: number[], width: number, height: number) {
  if (values.length === 0) {
    return ''
  }
  const step = values.length > 1 ? width / (values.length - 1) : 0
  return values
    .map((value, index) => {
      const x = index * step
      return `${index === 0 ? 'M' : 'L'}${x.toFixed(1)},${(height - value).toFixed(1)}`
    })
    .join(' ')
}

function ClientUsageTrendChart(props: {
  points: ClientAgentUsageTrendPoint[]
}) {
  const { t } = useTranslation()
  const max = useMemo(
    () => props.points.reduce((peak, p) => Math.max(peak, p.requests), 0),
    [props.points]
  )
  const plotHeight = CHART_HEIGHT - CHART_PADDING * 2
  const values = useMemo(
    () =>
      props.points.map((p) =>
        max > 0 ? (p.requests / max) * plotHeight : 0
      ),
    [props.points, max, plotHeight]
  )
  const path = trendPath(values, CHART_WIDTH, CHART_HEIGHT - CHART_PADDING)
  const totalRequests = props.points.reduce((sum, p) => sum + p.requests, 0)

  if (props.points.length === 0) {
    return (
      <p className='text-muted-foreground py-8 text-center text-sm'>
        {t('No data')}
      </p>
    )
  }

  return (
    <div className='min-w-0 space-y-2'>
      <div className='flex items-center justify-between gap-2'>
        <span className='text-sm font-medium'>{t('Requests')}</span>
        <span className='text-muted-foreground text-xs tabular-nums'>
          {formatNumber(totalRequests)}
        </span>
      </div>
      <div className='w-full overflow-x-auto'>
        <svg
          width={CHART_WIDTH}
          height={CHART_HEIGHT}
          viewBox={`0 0 ${CHART_WIDTH} ${CHART_HEIGHT}`}
          className='h-40 w-full min-w-[320px]'
          role='img'
          aria-label={t('Client usage trend')}
        >
          <line
            x1='0'
            y1={CHART_HEIGHT - CHART_PADDING}
            x2={CHART_WIDTH}
            y2={CHART_HEIGHT - CHART_PADDING}
            stroke='currentColor'
            className='text-border'
            strokeWidth='1'
          />
          <path
            d={path}
            fill='none'
            stroke='#2563eb'
            strokeWidth='2'
            strokeLinecap='round'
            strokeLinejoin='round'
          />
          {values.map((value, index) => {
            const step =
              values.length > 1
                ? CHART_WIDTH / (values.length - 1)
                : 0
            return (
              <circle
                key={props.points[index].window_start}
                cx={index * step}
                cy={CHART_HEIGHT - CHART_PADDING - value}
                r='3'
                fill='#2563eb'
              >
                <title>
                  {`${formatTimestampToDate(props.points[index].window_start)} · ${formatNumber(props.points[index].requests)}`}
                </title>
              </circle>
            )
          })}
        </svg>
      </div>
      <div className='text-muted-foreground flex justify-between text-[10px] tabular-nums'>
        <span>
          {formatTimestampToDate(props.points[0].window_start)}
        </span>
        <span>
          {formatTimestampToDate(
            props.points[props.points.length - 1].window_start
          )}
        </span>
      </div>
    </div>
  )
}

export function ClientUsageTrendDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  clientAgent?: string
  rawUserAgent?: string
  unknown?: boolean
}) {
  const { t } = useTranslation()
  const searchParams = route.useSearch()
  const query = useQuery({
    queryKey: ['usage-logs-client-trend', searchParams, props],
    queryFn: async () => {
      const params = buildApiParams({
        page: 1,
        pageSize: 1,
        searchParams,
        columnFilters: [],
        isAdmin: true,
      })
      return requireServerSuccess(
        await getClientAgentUsageTrend({
          start_timestamp: params.start_timestamp,
          end_timestamp: params.end_timestamp,
          username: params.username,
          model_name: params.model_name,
          token_name: params.token_name,
          channel: params.channel,
          group: params.group,
          client_agent: props.clientAgent || undefined,
          user_agent: props.rawUserAgent || undefined,
          unknown: props.unknown ? 'true' : undefined,
        })
      )
    },
    enabled: props.open,
    placeholderData: (previousData) => previousData,
  })

  const points = query.data.data?.points ?? []

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={
        <span className='flex items-center gap-2'>
          <BarChart3 className='size-4' aria-hidden='true' />
          {t('Client usage trend')}
        </span>
      }
      description={
        props.rawUserAgent || props.clientAgent
          ? props.rawUserAgent || props.clientAgent
          : t('Requests per time bucket, aggregated from logs.')
      }
      contentClassName='sm:max-w-3xl'
      contentHeight='min(70dvh, 520px)'
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
          <ClientUsageTrendChart points={points} />
          <div className='flex items-center justify-end gap-2'>
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
