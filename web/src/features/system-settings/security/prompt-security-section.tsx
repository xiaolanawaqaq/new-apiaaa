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
import { zodResolver } from '@hookform/resolvers/zod'
import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import { Input } from '@/components/ui/input'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'

const SECURE_CONTENT_LOG_MAX_BYTES_LIMIT = 64 * 1024

const promptSecuritySchema = z.object({
  PromptBlacklist: z.string().optional(),
  SecureContentLogEnabled: z.boolean(),
  SecureContentLogRetentionHours: z
    .number()
    .int()
    .min(1, 'Retention must be at least 1 hour'),
  SecureContentLogMaxBytes: z
    .number()
    .int()
    .min(1, 'Max bytes must be at least 1')
    .max(
      SECURE_CONTENT_LOG_MAX_BYTES_LIMIT,
      'Max bytes must not exceed 65536'
    ),
})

type PromptSecurityFormValues = z.infer<typeof promptSecuritySchema>

type PromptSecuritySectionProps = {
  defaultValues: PromptSecurityFormValues
}

export function PromptSecuritySection({
  defaultValues,
}: PromptSecuritySectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const form = useForm<PromptSecurityFormValues>({
    resolver: zodResolver(promptSecuritySchema),
    defaultValues,
  })

  useEffect(() => {
    form.reset(defaultValues)
  }, [defaultValues, form])

  const onSubmit = async (values: PromptSecurityFormValues) => {
    const updates = Object.entries(values).filter(
      ([key, value]) =>
        value !== defaultValues[key as keyof PromptSecurityFormValues]
    )

    for (const [key, value] of updates) {
      await updateOption.mutateAsync({
        key,
        value: typeof value === 'number' ? String(value) : (value ?? ''),
      })
    }
  }

  return (
    <SettingsSection title={t('Prompt Security')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
            saveLabel='Save prompt security'
          />
          <div className='space-y-4'>
            <FormField
              control={form.control}
              name='PromptBlacklist'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Prompt blacklist (JSON array)')}</FormLabel>
                  <FormControl>
                    <Textarea
                      rows={8}
                      placeholder='["keyword1", "keyword2"]'
                      className='font-mono text-xs'
                      {...field}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'JSON array of blocked keywords. Matching is Unicode-normalized and case-insensitive. At most 512 entries; hit requests are rejected with 403 prompt_blocked and never reach upstream models.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='SecureContentLogEnabled'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>{t('Log request/response content')}</FormLabel>
                    <FormDescription>
                      {t(
                        'Record a redacted, size-capped copy of relay request and response text for administrator troubleshooting. Stored separately with its own retention; only administrators can view it.'
                      )}
                    </FormDescription>
                  </SettingsSwitchContent>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                </SettingsSwitchItem>
              )}
            />

            <FormField
              control={form.control}
              name='SecureContentLogRetentionHours'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Content log retention (hours)')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min='1'
                      step='1'
                      value={field.value ?? 24}
                      onChange={(e) =>
                        field.onChange(
                          Math.max(0, Number.parseInt(e.target.value, 10) || 0)
                        )
                      }
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Entries older than this many hours are removed by the periodic cleanup task.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='SecureContentLogMaxBytes'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Content log max bytes per side')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min='1'
                      max={String(SECURE_CONTENT_LOG_MAX_BYTES_LIMIT)}
                      step='1'
                      value={field.value ?? 16384}
                      onChange={(e) =>
                        field.onChange(
                          Math.max(0, Number.parseInt(e.target.value, 10) || 0)
                        )
                      }
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Each request and response is capped at this many bytes before redaction and storage. Longer content is truncated; the response stream is never altered. Range 1-65536, default 16384.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
