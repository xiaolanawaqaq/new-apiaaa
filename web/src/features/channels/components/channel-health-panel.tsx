import { useQuery } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { Activity, AlertTriangle, CheckCircle2, Clock3, RefreshCw, XCircle } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { TitledCard } from '@/components/ui/titled-card'
import { Button } from '@/components/ui/button'
import { IconBadge } from '@/components/ui/icon-badge'
import { StatusBadge } from '@/components/status-badge'
import { formatNumber } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'

import { getChannelHealth } from '../api'
import { CHANNEL_STATUS } from '../constants'

function formatLatency(value: number, unavailable: string) {
  return value > 0 ? `${formatNumber(value)} ms` : unavailable
}

export function ChannelHealthPanel() {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['channel-health'],
    queryFn: async () => requireServerSuccess(await getChannelHealth()),
    staleTime: 15_000,
    refetchInterval: 30_000,
  })

  if (query.isPending) return <LoadingState className='min-h-24' />
  if (query.isError) {
    return <ErrorState title={t('Failed to load channel health')} onRetry={() => void query.refetch()} />
  }

  const data = query.data.data
  if (!data) return null
  const counts = data.counts
  const totalAttempts = data.items.reduce((sum, item) => sum + item.dispatch.attempts, 0)
  const totalSuccesses = data.items.reduce((sum, item) => sum + item.dispatch.successes, 0)
  const successRate = totalAttempts > 0 ? Math.round((totalSuccesses / totalAttempts) * 100) : 0
  const attention = data.items.filter((item) => item.status !== CHANNEL_STATUS.ENABLED)

  return (
    <TitledCard
      title={t('Channel health')}
      description={t('Routing health and recent dispatch performance')}
      icon={<Activity />}
      iconTone='info'
      disableHoverEffect
      action={
        <Button variant='ghost' size='icon' onClick={() => void query.refetch()} aria-label={t('Refresh')}>
          <RefreshCw className={query.isFetching ? 'animate-spin' : undefined} />
        </Button>
      }
      className='mb-4'
    >
      <div className='grid grid-cols-2 gap-3 sm:grid-cols-4'>
        <Metric icon={<CheckCircle2 />} label={t('Enabled')} value={counts.enabled ?? 0} tone='success' />
        <Metric icon={<AlertTriangle />} label={t('Auto Disabled')} value={counts.auto_disabled ?? 0} tone='warning' />
        <Metric icon={<XCircle />} label={t('Manual Disabled')} value={counts.manual_disabled ?? 0} tone='destructive' />
        <Metric icon={<Clock3 />} label={t('Success Rate')} value={`${successRate}%`} tone='info' />
      </div>
      {attention.length > 0 && (
        <div className='mt-4 border-t pt-4'>
          <div className='mb-2 text-sm font-medium'>{t('Needs attention')}</div>
          <div className='grid gap-2 sm:grid-cols-2 lg:grid-cols-3'>
            {attention.slice(0, 6).map((item) => (
              <div key={item.id} className='bg-muted/30 flex min-w-0 items-center justify-between gap-3 rounded-lg px-3 py-2 text-sm'>
                <div className='min-w-0'>
                  <div className='truncate font-medium'>{item.name}</div>
                  <div className='text-muted-foreground truncate text-xs'>
                    {item.dispatch.attempts > 0
                      ? `${formatLatency(item.dispatch.ewma_latency_ms, t('No latency data'))} · ${item.dispatch.failures} ${t('failures')}`
                      : t('No recent dispatch data')}
                  </div>
                </div>
                <StatusBadge
                  label={item.status === CHANNEL_STATUS.AUTO_DISABLED ? t('Auto Disabled') : t('Disabled')}
                  variant={item.status === CHANNEL_STATUS.AUTO_DISABLED ? 'warning' : 'danger'}
                  copyable={false}
                />
              </div>
            ))}
          </div>
        </div>
      )}
    </TitledCard>
  )
}

function Metric(props: { icon: ReactNode; label: string; value: ReactNode; tone: 'success' | 'warning' | 'destructive' | 'info' }) {
  return (
    <div className='bg-muted/30 flex items-center gap-2 rounded-lg px-3 py-2'>
      <IconBadge tone={props.tone} size='sm'>{props.icon}</IconBadge>
      <div className='min-w-0'>
        <div className='text-lg font-semibold leading-tight'>{props.value}</div>
        <div className='text-muted-foreground truncate text-xs'>{props.label}</div>
      </div>
    </div>
  )
}
