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
import { useForm, type Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'

const schema = z.object({
  enabled: z.boolean(),
  percent: z.coerce.number().min(0).max(100),
  maxRecharges: z.coerce.number().int().min(0),
})

type Values = z.infer<typeof schema>

export function ReferralSettingsSection({
  defaultValues,
}: {
  defaultValues: {
    enabled: boolean
    percent: number
    maxRecharges: number
  }
}) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()

  const form = useForm<Values>({
    resolver: zodResolver(schema) as unknown as Resolver<Values>,
    defaultValues: {
      enabled: defaultValues.enabled,
      percent: defaultValues.percent,
      maxRecharges: defaultValues.maxRecharges,
    },
  })

  const { isDirty, isSubmitting } = form.formState
  const enabled = form.watch('enabled')

  async function onSubmit(values: Values) {
    const updates: Array<{ key: string; value: string }> = []

    if (values.enabled !== defaultValues.enabled) {
      updates.push({
        key: 'ReferralCommissionEnabled',
        value: String(values.enabled),
      })
    }

    if (values.percent !== defaultValues.percent) {
      updates.push({
        key: 'ReferralCommissionPercent',
        value: String(values.percent),
      })
    }

    if (values.maxRecharges !== defaultValues.maxRecharges) {
      updates.push({
        key: 'ReferralCommissionMaxRecharges',
        value: String(values.maxRecharges),
      })
    }

    if (updates.length === 0) {
      toast.info(t('No changes to save'))
      return
    }

    for (const update of updates) {
      await updateOption.mutateAsync(update)
    }

    form.reset(values)
  }

  return (
    <SettingsSection title={t('Referral Commission')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)} autoComplete='off'>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending || isSubmitting}
            isSaveDisabled={!isDirty}
            saveLabel='Save referral settings'
          />
          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable referral commission')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Pay inviters a share of every top-up made by users they invited'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                    disabled={updateOption.isPending || isSubmitting}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />

          {enabled && (
            <div className='grid gap-6 sm:grid-cols-2'>
              <FormField
                control={form.control}
                name='percent'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Commission rate (%)')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={0}
                        max={100}
                        step='0.01'
                        placeholder={t('10')}
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Percentage of each top-up credited to the inviter. Can be overridden per user.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='maxRecharges'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>
                      {t('Max commissioned top-ups per invitee')}
                    </FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={0}
                        placeholder={t('0')}
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Stop paying commission after this many top-ups by the same invited user. 0 means unlimited.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>
          )}
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
