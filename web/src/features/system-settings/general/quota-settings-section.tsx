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
import i18next from 'i18next'
import type { ChangeEvent } from 'react'
import type { Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import { Alert, AlertDescription } from '@/components/ui/alert'
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
import { formatQuota } from '@/lib/format'

import { FormDirtyIndicator } from '../components/form-dirty-indicator'
import { FormNavigationGuard } from '../components/form-navigation-guard'
import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
  SettingsFormGrid,
  SettingsFormGridItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useSettingsForm } from '../hooks/use-settings-form'
import { useUpdateOption } from '../hooks/use-update-option'

const quotaSchema = z.object({
  QuotaForNewUser: z.coerce.number().min(0),
  QuotaForInviter: z.coerce.number().min(0),
  QuotaForInvitee: z.coerce.number().min(0),
  TopUpLink: z.string(),
  quota_setting: z.object({
    enable_free_model_pre_consume: z.boolean(),
    enable_free_abuse_auto_block: z.boolean(),
    free_abuse_max_per_minute: z.coerce.number().min(0),
    free_abuse_max_distinct_models: z.coerce.number().min(0),
    free_abuse_max_distinct_models_per_day: z.coerce.number().min(0),
    free_abuse_max_per_day: z.coerce.number().min(0),
    free_abuse_max_errors_per_hour: z.coerce.number().min(0),
    free_abuse_max_media_err_models: z.coerce.number().min(0),
    free_abuse_network_min_accounts: z.coerce.number().min(0),
    free_abuse_network_max_identity_pct: z.coerce.number().min(0).max(100),
    free_abuse_network_window_days: z.coerce.number().min(1),
    free_abuse_burst_min_accounts: z.coerce.number().min(0),
    free_abuse_burst_window_days: z.coerce.number().min(1),
    free_abuse_unverified_pct: z.coerce.number().min(1).max(100),
    free_abuse_cooccur_mode: z.coerce.number().min(0).max(2),
    free_abuse_cooccur_ip_min_accounts: z.coerce.number().min(0),
    free_abuse_cooccur_fp_min_accounts: z.coerce.number().min(0),
    free_abuse_cooccur_net_min_accounts: z.coerce.number().min(0),
    free_abuse_cooccur_window_seconds: z.coerce.number().min(60),
    free_abuse_cooccur_ban_days: z.coerce.number().min(1),
    free_abuse_username_domain_min_accounts: z.coerce.number().min(0),
    free_abuse_username_domain_allowlist: z.string(),
    charge_on_error: z.boolean(),
    trust_quota_usd: z.preprocess(
      (value) => (value === '' ? undefined : value),
      z.coerce
        .number({ error: () => i18next.t('Please enter a valid number') })
        .min(0, {
          error: () => i18next.t('Must be greater than or equal to 0'),
        })
    ),
    pre_consume_multiplier: z.coerce
      .number({ error: () => i18next.t('Please enter a valid number') })
      .positive({ error: () => i18next.t('Must be greater than 0') }),
  }),
})

type QuotaFormValues = z.infer<typeof quotaSchema>
type QuotaInputValue = number | ''

function formatQuotaInputValue(value: QuotaInputValue): string {
  return formatQuota(value === '' ? 0 : value)
}

type QuotaSettingsSectionProps = {
  defaultValues: QuotaFormValues
  complianceConfirmed?: boolean
}

export function QuotaSettingsSection({
  defaultValues,
  complianceConfirmed = true,
}: QuotaSettingsSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const handleNumberChange =
    (onChange: (value: QuotaInputValue) => void) =>
    (event: ChangeEvent<HTMLInputElement>) => {
      const value = event.currentTarget.valueAsNumber
      onChange(Number.isNaN(value) ? '' : value)
    }

  const { form, handleSubmit, isDirty, isSubmitting } =
    useSettingsForm<QuotaFormValues>({
      resolver: zodResolver(quotaSchema) as Resolver<
        QuotaFormValues,
        unknown,
        QuotaFormValues
      >,
      defaultValues,
      onSubmit: async (_data, changedFields) => {
        for (const [key, value] of Object.entries(changedFields)) {
          await updateOption.mutateAsync({
            key,
            value: value as string | number | boolean,
          })
        }
      },
    })

  return (
    <SettingsSection title={t('Quota Settings')}>
      <FormNavigationGuard when={isDirty} />

      {!complianceConfirmed ? (
        <Alert variant='destructive'>
          <AlertDescription>
            {t(
              'Non-zero invitation rewards require compliance confirmation in Payment Gateway settings.'
            )}
          </AlertDescription>
        </Alert>
      ) : null}

      <Form {...form}>
        <SettingsForm onSubmit={handleSubmit}>
          <SettingsPageFormActions
            onSave={handleSubmit}
            isSaving={updateOption.isPending || isSubmitting}
          />
          <FormDirtyIndicator isDirty={isDirty} />
          <SettingsFormGrid>
            <FormField
              control={form.control}
              name='QuotaForNewUser'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('New User Quota')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Initial quota given to new users ({{formattedQuota}})',
                      {
                        formattedQuota: formatQuotaInputValue(field.value),
                      }
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.trust_quota_usd'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>
                    {t('Wallet pre-consume bypass threshold (USD)')}
                  </FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={0}
                      step='any'
                      value={field.value ?? ''}
                      onChange={field.onChange}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Skip pre-consumption when the wallet balance and limited API key balance both exceed this amount. Set to 0 to always pre-consume. Subscriptions and asynchronous tasks always reserve quota.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.pre_consume_multiplier'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Input pre-consume multiplier')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={0}
                      step='any'
                      value={field.value ?? ''}
                      onChange={field.onChange}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Reserve the estimated input cost multiplied by this value, without estimating output tokens. Defaults to 1; positive decimals such as 0.5 and 1.5 are supported. Final charges use actual usage. Per-request and task prices are unaffected.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='QuotaForInviter'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Inviter Reward')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Quota given to users who invite others ({{formattedQuota}})',
                      {
                        formattedQuota: formatQuotaInputValue(field.value),
                      }
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='QuotaForInvitee'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Invitee Reward')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t('Quota given to invited users ({{formattedQuota}})', {
                      formattedQuota: formatQuotaInputValue(field.value),
                    })}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <SettingsFormGridItem span='full'>
              <FormField
                control={form.control}
                name='quota_setting.enable_free_model_pre_consume'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Pre-Consume for Free Models')}</FormLabel>
                      <FormDescription>
                        {t(
                          'When enabled, zero-cost models also pre-consume quota before final settlement.'
                        )}
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                        disabled={updateOption.isPending}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />
            </SettingsFormGridItem>

            <SettingsFormGridItem span='full'>
              <FormField
                control={form.control}
                name='quota_setting.enable_free_abuse_auto_block'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Auto-block free model abuse')}</FormLabel>
                      <FormDescription>
                        {t(
                          'When a zero-balance user exceeds the free-request limit below, automatically block their free models until they top up.'
                        )}
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                        disabled={updateOption.isPending}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />
            </SettingsFormGridItem>

            <SettingsFormGridItem span='full'>
              <FormField
                control={form.control}
                name='quota_setting.charge_on_error'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Charge on failed requests')}</FormLabel>
                      <FormDescription>
                        {t(
                          'When enabled, pre-consumed quota is kept (not refunded) for requests that an upstream processed but returned an error. Local failures (no available channel, invalid request) are always refunded.'
                        )}
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                        disabled={updateOption.isPending}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />
            </SettingsFormGridItem>

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_max_per_minute'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>
                    {t('Free model requests per minute before auto-block')}
                  </FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Threshold for auto-block detection. Only applies when auto-block is enabled.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_max_distinct_models'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>
                    {t('Distinct free models per minute before auto-block')}
                  </FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Auto-block when a user hits more than this many different free models in a minute (fast model-switching = scraping). Only applies when auto-block is enabled.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_max_distinct_models_per_day'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>
                    {t('Distinct free models per day before auto-block')}
                  </FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Catches the same catalog scraping paced slowly enough to stay under the per-minute limit above: dozens of different free models across a day, never many in one minute. Requires Redis; without it this signal does nothing. 0 disables. Only applies when auto-block is enabled.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_max_per_day'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>
                    {t('Free model requests per day before auto-block')}
                  </FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Catches slow-but-relentless scrapers that stay under the per-minute limits. 0 disables. Only applies when auto-block is enabled.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_max_errors_per_hour'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>
                    {t('Free model errors per hour before auto-block')}
                  </FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Auto-block when a zero-balance user racks up this many failed free-model requests in an hour (relentless retrying of rate-limited models = bot). 0 disables. Only applies when auto-block is enabled.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_max_media_err_models'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>
                    {t('Distinct failing free media models before auto-block')}
                  </FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Auto-block when a zero-balance user fails this many DISTINCT free image/audio/video models within a minute (catalog probing). Media only; text models are exempt. Kept low because no legitimate user fires several failing media generations back-to-back. 0 disables. Only applies when auto-block is enabled.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_network_min_accounts'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>
                    {t('Accounts per network before it counts as a farm')}
                  </FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Registrations are grouped by network (IPv4 /24, IPv6 /48). Once a network passes this count, further sign-ups from it are refused and its existing identity-less, never-paid accounts are shadow-banned from free models. Catches farms that rent a range and rotate addresses to stay under the per-IP cap. 0 disables.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_network_max_identity_pct'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Network identity exemption (%)')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'A network is left alone when at least this percent of its accounts bound a third-party login (GitHub, Discord, OIDC, Telegram, LinuxDO, WeChat). Protects households, campuses and carrier NAT, which share an address but do bind real identities. A typed email does not count while email verification is off.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_network_window_days'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Network reputation window (days)')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'How far back registrations are counted when scoring a network. Longer catches farms that register slowly; shorter lets a reformed network recover sooner.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_burst_min_accounts'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t("Registration burst threshold (per minute)")}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      "Password-only signups per wall-clock minute, across all networks, above which every account from that minute that never bound a third-party login, typed no email and never paid is shadow banned on free models. Farms renting one residential exit per account escape the network rule but still register in bursts. 0 disables."
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_burst_window_days'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t("Registration burst window (days)")}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      "How far back registration minutes are scanned for bursts. Longer keeps older farms banned; nothing beyond it is checked."
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_unverified_pct'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t("Unverified account threshold (%)")}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      "Every free abuse threshold above is scaled to this percent for an account with no third-party login, no verified email and zero balance. Account farms have none of the three, most real users have at least one. 100 means no difference."
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_cooccur_mode'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t("Co-occurrence detection mode")}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      "0 disables, 1 only logs which client IPs and client fingerprints would be flagged, 2 shadow bans. Run in log mode for a day and read the system log before enforcing."
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_cooccur_ip_min_accounts'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t("Co-occurrence threshold per client IP")}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      "Zero-balance accounts without a third-party login, verified email or spend seen from one client IP inside one window, above which every account in that window is shadow banned on free models and any later account from that IP joins them. Browsers stay under five, account farms rotating one host run sixty and more. 0 disables."
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_cooccur_fp_min_accounts'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t("Co-occurrence threshold per client fingerprint")}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      "Same rule keyed on the client software (exact User-Agent plus which headers it sends) instead of the IP, for farms that rotate proxies. Browser requests are never fingerprinted. 0 disables."
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_cooccur_net_min_accounts'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t("Co-occurrence threshold per client network")}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      "Same rule keyed on the /24 (IPv6 /48) the requests come from, for farms that spread over the addresses of one rented block. Counted for non-browser clients only and, like the fingerprint rule, only when the accounts share few client fingerprints. 0 disables."
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_cooccur_window_seconds'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t("Co-occurrence window (seconds)")}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      "Length of the window in which distinct accounts on one IP or fingerprint are counted."
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_cooccur_ban_days'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t("Co-occurrence ban duration (days)")}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      "How long a co-occurrence shadow ban lasts after the last hit. A farm that keeps going keeps its bans, one that stops is released."
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_username_domain_min_accounts'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t("Username domain cohort threshold")}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      value={field.value ?? ''}
                      onChange={handleNumberChange(field.onChange)}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      "Accounts whose username is an address at the same domain, with no email set, above which the domain counts as a farm and its identity-free, never paid accounts are shadow banned on free models. Public mail providers in the allowlist are ignored. 0 disables."
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='quota_setting.free_abuse_username_domain_allowlist'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t("Username domain allowlist")}</FormLabel>
                  <FormControl>
                    <Input
                      type='text'
                      value={field.value ?? ''}
                      onChange={field.onChange}
                      name={field.name}
                      onBlur={field.onBlur}
                      ref={field.ref}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      "Comma separated domains never counted as a username domain cohort, because real people type these addresses as usernames."
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='TopUpLink'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Top-Up Link')}</FormLabel>
                  <FormControl>
                    <Input
                      placeholder={t('https://example.com/topup')}
                      {...field}
                    />
                  </FormControl>
                  <FormDescription>
                    {t('External link for users to purchase quota')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </SettingsFormGrid>
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
