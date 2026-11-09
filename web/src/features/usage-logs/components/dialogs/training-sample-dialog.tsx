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
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { requireServerSuccess } from '@/lib/server-error-message'

import { getLogContent } from '../../api'

export function TrainingSampleDialog(props: {
  requestId: string
  onClose: () => void
}) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['training-sample-content', props.requestId],
    queryFn: async () =>
      requireServerSuccess(await getLogContent(props.requestId)),
    gcTime: 0,
    retry: false,
    meta: { errorToast: false },
  })
  const items = query.data?.data?.items ?? []
  const sections = [
    { direction: 'request', label: t('Input content') },
    { direction: 'response', label: t('Output content') },
  ]

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('Content details')}
      description={props.requestId}
      descriptionClassName='break-all font-mono text-xs'
      contentClassName='sm:max-w-4xl'
      contentHeight='min(70dvh, 640px)'
    >
      <Button
        variant='outline'
        size='sm'
        disabled={query.isFetching}
        onClick={() => void query.refetch()}
      >
        {t('Refresh')}
      </Button>
      {query.isPending && <LoadingState />}
      {query.isError && (
        <ErrorState
          title={t('Failed to load')}
          onRetry={() => void query.refetch()}
        />
      )}
      {query.isSuccess && (
        <div className='mt-3 min-w-0 space-y-4'>
          <p className='text-muted-foreground text-xs'>
            {t(
              'Stored content may be truncated by the capture limit. Unrecorded or expired content cannot be recovered.'
            )}
          </p>
          {sections.map((section) => {
            const value =
              items.find((item) => item.direction === section.direction)
                ?.content ?? ''
            return (
              <section
                key={section.direction}
                aria-label={section.label}
                className='min-w-0 space-y-2'
              >
                <div className='flex items-center justify-between gap-2'>
                  <h3 className='text-sm font-medium'>{section.label}</h3>
                  {value && (
                    <CopyButton
                      value={value}
                      aria-label={`${t('Copy')} ${section.label}`}
                      size='sm'
                    />
                  )}
                </div>
                {value ? (
                  <pre className='bg-muted/30 max-h-80 overflow-auto rounded-md border p-3 text-xs leading-relaxed break-all whitespace-pre-wrap'>
                    {value}
                  </pre>
                ) : (
                  <p className='text-muted-foreground text-sm'>
                    {t('Not captured')}
                  </p>
                )}
              </section>
            )
          })}
        </div>
      )}
    </Dialog>
  )
}
