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
import { Link } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { formatNumber, formatTimestampToDate } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { ROLE } from '@/lib/roles'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { exportTrainingData, getTrainingSamples } from '../../api'
import type { TrainingSampleFilters } from '../../types'
import { CompactDateTimeRangePicker } from '../compact-date-time-range-picker'
import { TrainingSampleDialog } from './training-sample-dialog'

export function TrainingDataView() {
  const role = useAuthStore((state) => state.auth.user?.role ?? ROLE.GUEST)
  if (role < ROLE.ADMIN) return null
  return <TrainingDataBrowser isRoot={role === ROLE.SUPER_ADMIN} />
}

function TrainingDataBrowser(props: { isRoot: boolean }) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [model, setModel] = useState('')
  const [userId, setUserId] = useState('')
  const [range, setRange] = useState<{ start?: Date; end?: Date }>({})
  const [filters, setFilters] = useState<TrainingSampleFilters>({})
  const [selected, setSelected] = useState<string | null>(null)
  const [exporting, setExporting] = useState(false)
  const abortRef = useRef<AbortController | null>(null)
  const pageSize = 20
  useEffect(() => () => abortRef.current?.abort(), [])

  const query = useQuery({
    queryKey: ['training-samples', filters, page],
    queryFn: async () =>
      requireServerSuccess(
        await getTrainingSamples({
          ...filters,
          p: page,
          page_size: pageSize,
        })
      ),
    gcTime: 0,
    meta: { errorToast: false },
  })
  const data = query.data?.data
  const total = data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / pageSize))
  const invalidRange = !!(range.start && range.end && range.end < range.start)
  const invalidUser =
    userId !== '' &&
    (!Number.isSafeInteger(Number(userId)) || Number(userId) <= 0)

  const handleExport = async (format: 'raw' | 'openai') => {
    if (abortRef.current) return
    const controller = new AbortController()
    abortRef.current = controller
    setExporting(true)
    try {
      const result = await exportTrainingData(
        format,
        filters,
        controller.signal
      )
      if (controller.signal.aborted) return
      if (!result.count) {
        toast.info(t('No exportable samples match these filters.'))
        return
      }
      const url = URL.createObjectURL(result.blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = `training-${format}-${Date.now()}.jsonl`
      anchor.click()
      setTimeout(() => URL.revokeObjectURL(url), 1000)
      toast.success(t('Exported {{count}} samples', { count: result.count }))
    } catch (error) {
      if (!controller.signal.aborted) {
        handleServerError(error, t('Failed to export content'))
      }
    } finally {
      if (abortRef.current === controller) abortRef.current = null
      setExporting(false)
    }
  }

  return (
    <div className='min-w-0 space-y-4'>
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <h3 className='font-medium'>{t('Training Data')}</h3>
        <div className='flex flex-wrap items-center gap-2'>
          <Button
            variant='outline'
            size='sm'
            disabled={query.isFetching}
            onClick={() => void query.refetch()}
          >
            {t('Refresh')}
          </Button>
          <Button
            variant='outline'
            size='sm'
            disabled={exporting || !total}
            onClick={() => void handleExport('raw')}
          >
            {t('Export raw JSONL')}
          </Button>
          <Button
            size='sm'
            disabled={exporting || !total}
            onClick={() => void handleExport('openai')}
          >
            {t('Export OpenAI JSONL')}
          </Button>
          {exporting && (
            <Button
              variant='ghost'
              size='sm'
              onClick={() => abortRef.current?.abort()}
            >
              {t('Cancel')}
            </Button>
          )}
        </div>
      </div>
      <p className='text-muted-foreground text-xs'>
        {t(
          'Browse all retained raw samples. Exports use the applied filters, not only the current page. Use only data you are authorized to train on.'
        )}
      </p>
      <form
        className='flex flex-wrap items-center gap-2'
        onSubmit={(event) => {
          event.preventDefault()
          if (invalidRange || invalidUser || exporting) return
          setPage(1)
          setFilters({
            model_name: model.trim() || undefined,
            user_id: userId ? Number(userId) : undefined,
            start_timestamp: range.start
              ? Math.floor(range.start.getTime() / 1000)
              : undefined,
            end_timestamp: range.end
              ? Math.floor(range.end.getTime() / 1000)
              : undefined,
          })
        }}
      >
        <Input
          className='w-48'
          aria-label={t('Model')}
          placeholder={t('Model')}
          value={model}
          onChange={(event) => setModel(event.target.value)}
        />
        <Input
          className='w-32'
          aria-label={t('User ID')}
          placeholder={t('User ID')}
          type='number'
          min='1'
          step='1'
          value={userId}
          onChange={(event) => setUserId(event.target.value)}
          aria-invalid={invalidUser}
        />
        <CompactDateTimeRangePicker
          start={range.start}
          end={range.end}
          onChange={setRange}
        />
        <Button
          type='submit'
          variant='outline'
          size='sm'
          disabled={invalidRange || invalidUser || exporting}
        >
          {t('Search')}
        </Button>
        <Button
          type='button'
          variant='ghost'
          size='sm'
          disabled={exporting}
          onClick={() => {
            setModel('')
            setUserId('')
            setRange({})
            setFilters({})
            setPage(1)
          }}
        >
          {t('Reset')}
        </Button>
      </form>
      {invalidRange && (
        <p role='alert' className='text-destructive text-sm'>
          {t('End time must be after start time')}
        </p>
      )}
      {exporting && (
        <p role='status' className='text-muted-foreground text-sm'>
          {t('Exporting training data…')}
        </p>
      )}
      {query.isPending && <LoadingState />}
      {query.isError && (
        <ErrorState
          title={t('Failed to load')}
          onRetry={() => void query.refetch()}
        />
      )}
      {query.isSuccess && !data?.items?.length && (
        <EmptyState
          title={t('No data')}
          description={t(
            'No raw samples match these filters. Check capture settings, send a new request, then refresh. Historical unrecorded or expired data cannot be restored.'
          )}
          action={
            props.isRoot && (
              <Link
                className='text-primary underline'
                to='/system-settings/security/$section'
                params={{ section: 'prompt-security' }}
              >
                {t('Open settings')}
              </Link>
            )
          }
        />
      )}
      {query.isSuccess && !!data?.items?.length && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('Time')}</TableHead>
              <TableHead>{t('User')}</TableHead>
              <TableHead>{t('Model')}</TableHead>
              <TableHead>{t('Input')}</TableHead>
              <TableHead>{t('Output')}</TableHead>
              <TableHead>{t('Details')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.items.map((sample) => (
              <TableRow key={`${sample.request_id}:${sample.created_at}`}>
                <TableCell className='font-mono text-xs'>
                  {formatTimestampToDate(sample.created_at)}
                </TableCell>
                <TableCell className='max-w-32 truncate text-xs'>
                  {sample.username || '-'}
                </TableCell>
                <TableCell className='max-w-40 truncate text-xs'>
                  {sample.model_name || '-'}
                </TableCell>
                <TableCell>
                  <span className='block max-w-48 truncate text-xs'>
                    {sample.input_preview || '-'}
                  </span>
                </TableCell>
                <TableCell>
                  <span className='block max-w-48 truncate text-xs'>
                    {sample.output_preview || t('Not captured')}
                  </span>
                </TableCell>
                <TableCell>
                  <Button
                    variant='ghost'
                    size='sm'
                    onClick={() => setSelected(sample.request_id)}
                    aria-label={`${t('View content')} ${sample.request_id}`}
                  >
                    {t('View')}
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <span className='text-muted-foreground text-xs'>
          {t('Total')}: {formatNumber(total)}
        </span>
        <div className='flex items-center gap-2'>
          <Button
            variant='outline'
            size='sm'
            disabled={page <= 1 || query.isFetching}
            onClick={() => setPage((current) => current - 1)}
          >
            {t('Previous')}
          </Button>
          <span className='text-muted-foreground text-xs tabular-nums'>
            {page} / {totalPages}
          </span>
          <Button
            variant='outline'
            size='sm'
            disabled={page >= totalPages || query.isFetching}
            onClick={() => setPage((current) => current + 1)}
          >
            {t('Next')}
          </Button>
        </div>
      </div>
      {selected && (
        <TrainingSampleDialog
          requestId={selected}
          onClose={() => setSelected(null)}
        />
      )}
    </div>
  )
}
