import { BarChart3, RefreshCw } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { formatLogQuota, formatNumber, formatTimestampToDate } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import { buildApiParams } from '../../lib/utils'
import { getClientAgentUsageTrend } from '../api'
import type { ClientAgentUsageTrendPoint } from '../types'

const route = getRouteApi('/_authenticated/usage-logs/$section')

function trendPath(points: number[], width: number, height: number) {
  if (points.length < 2) {
    return ''
  }
  const step = width / (points.length - 1)
  return points
    .map((value, index) => {
      const y = height - value
      const x = index * step
      return `${index === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
}

function ClientUsageTrendCard({
  points,
  max,
}: {
  points: ClientAgentUsageTrendPoint[]
  max: number
}) {
  const { t } = useTranslation()
  const width = 320
  const height = 120
  const padding = 28

  const chartY = useMemo(
    () =>
      points.map((p) => (max > 0 ? (p.Requests / max) * (height - padding) : 0)),
    [points, max, height]
  )

  const path = trendPath(chartY, width, height)

  return (
    <div className='flex min-w-0 flex-col gap-2'>
      <div className='flex items-center justify-between gap-2'>
        <span className='text-sm font-medium'>{t('Requests')}</span>
        <span className='text-muted-foreground text-xs tabular-nums'>
          {formatNumber(points.reduce((sum, p) => sum + p.Requests, 0))}
        </span>
      </div>
      <div className='relative w-full h-32 min-h-[120px]'>
        <svg
          width={width}
          height={height}
          viewBox={`0 0 ${width} ${height}`}
          className='absolute inset-0'
        >
          <path
            d={path}
            fill='none'
            stroke='#2563eb'
            strokeWidth='2'
            strokeLinecap='round'
            strokeLinejoin='round'
          />
          {chartY.map((y, index) => {
            const x = index * (width / Math.max(points.length - 1, 1))
            return (
              <circle
                key={index}
                cx={x}
                cy={y}
                r={3}
                fill='#2563eb'
                className='transition-all hover:r-4 hover:fill-blue-600 cursor-pointer'
              />
            )
          })}
        </svg>
        {points.length > 0 && (
          <div className='absolute top-0 left-0 w-full h-[120px] flex justify-between px-2 text-[10px] text-muted-foreground'>
            <span>{formatTimestampToDate(points[0].WindowStart)}</span>
            <span>
              {formatTimestampToDate(
                points[points.length - 1].WindowStart
              )}
            </span>
          </div>
        )}
      </div>
    </div>
  )
}

export function ClientUsageTrendDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const searchParams = route.useSearch()
  const query = useQuery({
    queryKey: ['usage-logs-client-trend', searchParams],
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
          client_agent: params.client_agent || undefined,
          user_agent: params.user_agent || undefined,
          unknown: params.unknown === 'true' ? 'true' : undefined,
          bucket_seconds: params.bucket_seconds || undefined,
        })
      )
    },
    enabled: open,
    placeholderData: (previousData) => previousData,
  })

  const trendPoints = useMemo(
    () => query.data.data?.points ?? [],
    [query.data]
  )
  const maxRequests = useMemo(
    () =>
      trendPoints.reduce((max, p) => Math.max(max, p.Requests), 0),
    [trendPoints]
  )

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={
        <span className='flex items-center gap-2'>
          <BarChart3 className='size-4' aria-hidden='true' />
          {t('Client usage trend')}
        </span>
      }
      description={t('Requests per time bucket, aggregated from logs.')}
      contentClassName='sm:max-w-4xl'
      contentHeight='min(70dvh, 560px)'
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
          <ClientUsageTrendCard points={trendPoints} max={maxRequests} />
          <div className='flex items-center justify-end gap-2 mt-2'>
            <span className='text-muted-foreground text-xs'>
              {formatTimestampToDate(
                trendPoints[trendPoints.length - 1]?.WindowStart ?? 0
              )}
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
