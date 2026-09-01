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
import { parseCurrencyDisplayType } from '@/lib/currency'

import { CheckinSettingsSection } from '../general/checkin-settings-section'
import { PricingSection } from '../general/pricing-section'
import { QuotaSettingsSection } from '../general/quota-settings-section'
import { ReferralSettingsSection } from '../general/referral-settings-section'
import { PaymentSettingsSection } from '../integrations/payment-settings-section'
import { RatioSettingsCard } from '../models/ratio-settings-card'
import type { BillingSettings } from '../types'
import { createSectionRegistry } from '../utils/section-registry'

const getModelDefaults = (settings: BillingSettings) => ({
  ModelPrice: settings.ModelPrice,
  ModelRatio: settings.ModelRatio,
  CacheRatio: settings.CacheRatio,
  CreateCacheRatio: settings.CreateCacheRatio,
  CompletionRatio: settings.CompletionRatio,
  ImageRatio: settings.ImageRatio,
  AudioRatio: settings.AudioRatio,
  AudioCompletionRatio: settings.AudioCompletionRatio,
  ExposeRatioEnabled: settings.ExposeRatioEnabled,
  BillingMode: settings['billing_setting.billing_mode'],
  BillingExpr: settings['billing_setting.billing_expr'],
})

const getGroupDefaults = (settings: BillingSettings) => ({
  TopupGroupRatio: settings.TopupGroupRatio,
  GroupRatio: settings.GroupRatio,
  UserUsableGroups: settings.UserUsableGroups,
  GroupGroupRatio: settings.GroupGroupRatio,
  AutoGroups: settings.AutoGroups,
  MaxTokenAutoGroups: settings.MaxTokenAutoGroups,
  DefaultUseAutoGroup: settings.DefaultUseAutoGroup,
  GroupSpecialUsableGroup:
    settings['group_ratio_setting.group_special_usable_group'],
})

const BILLING_SECTIONS = [
  {
    id: 'quota',
    titleKey: 'Quota Settings',
    build: (settings: BillingSettings) => (
      <QuotaSettingsSection
        defaultValues={{
          QuotaForNewUser: settings.QuotaForNewUser,
          PreConsumedQuota: settings.PreConsumedQuota,
          QuotaForInviter: settings.QuotaForInviter,
          QuotaForInvitee: settings.QuotaForInvitee,
          TopUpLink: settings.TopUpLink,
          general_setting: {
            docs_link: settings['general_setting.docs_link'],
          },
          quota_setting: {
            enable_free_model_pre_consume:
              settings['quota_setting.enable_free_model_pre_consume'],
            enable_free_abuse_auto_block:
              settings['quota_setting.enable_free_abuse_auto_block'],
            free_abuse_max_per_minute:
              settings['quota_setting.free_abuse_max_per_minute'],
            free_abuse_max_distinct_models:
              settings['quota_setting.free_abuse_max_distinct_models'],
            free_abuse_max_per_day:
              settings['quota_setting.free_abuse_max_per_day'],
            free_abuse_max_errors_per_hour:
              settings['quota_setting.free_abuse_max_errors_per_hour'],
            free_abuse_max_media_err_models:
              settings['quota_setting.free_abuse_max_media_err_models'],
            charge_on_error: settings['quota_setting.charge_on_error'],
          },
        }}
        complianceConfirmed={
          (settings['payment_setting.compliance_confirmed'] ?? false) &&
          settings['payment_setting.compliance_terms_version'] === 'v1'
        }
      />
    ),
  },
  {
    id: 'currency',
    titleKey: 'Currency & Display',
    build: (settings: BillingSettings) => (
      <PricingSection
        defaultValues={{
          QuotaPerUnit: settings.QuotaPerUnit,
          USDExchangeRate: settings.USDExchangeRate,
          DisplayInCurrencyEnabled: settings.DisplayInCurrencyEnabled,
          DisplayTokenStatEnabled: settings.DisplayTokenStatEnabled,
          general_setting: {
            quota_display_type: parseCurrencyDisplayType(
              settings['general_setting.quota_display_type']
            ),
            custom_currency_symbol:
              settings['general_setting.custom_currency_symbol'] ?? '¤',
            custom_currency_exchange_rate:
              settings['general_setting.custom_currency_exchange_rate'] ?? 1,
          },
        }}
      />
    ),
  },
  {
    id: 'model-pricing',
    titleKey: 'Model Pricing',
    build: (settings: BillingSettings) => (
      <RatioSettingsCard
        titleKey='Model Pricing'
        modelDefaults={getModelDefaults(settings)}
        groupDefaults={getGroupDefaults(settings)}
        toolPricesDefault={settings['tool_price_setting.prices']}
        visibleTabs={['models', 'unset-models', 'tool-prices', 'upstream-sync']}
      />
    ),
  },
  {
    id: 'group-pricing',
    titleKey: 'Group Pricing',
    build: (settings: BillingSettings) => (
      <RatioSettingsCard
        titleKey='Group Pricing'
        modelDefaults={getModelDefaults(settings)}
        groupDefaults={getGroupDefaults(settings)}
        toolPricesDefault={settings['tool_price_setting.prices']}
        visibleTabs={['groups']}
      />
    ),
  },
  {
    id: 'payment',
    titleKey: 'Payment Gateway',
    build: (settings: BillingSettings) => (
      <PaymentSettingsSection
        defaultValues={{
          PayAddress: settings.PayAddress,
          EpayId: settings.EpayId,
          EpayKey: settings.EpayKey,
          Price: settings.Price,
          MinTopUp: settings.MinTopUp,
          CustomCallbackAddress: settings.CustomCallbackAddress,
          PayMethods: settings.PayMethods,
          AmountOptions: settings['payment_setting.amount_options'],
          AmountDiscount: settings['payment_setting.amount_discount'],
          StripeEnabled: settings.StripeEnabled ?? true,
          StripeApiSecret: settings.StripeApiSecret,
          StripeWebhookSecret: settings.StripeWebhookSecret,
          StripePriceId: settings.StripePriceId,
          StripeUnitPrice: settings.StripeUnitPrice,
          StripeMinTopUp: settings.StripeMinTopUp,
          StripePromotionCodesEnabled: settings.StripePromotionCodesEnabled,
          StripeManagedPayments: settings.StripeManagedPayments ?? false,
          StripeTextModerationEnabled:
            settings.StripeTextModerationEnabled ?? false,
          CreemEnabled: settings.CreemEnabled ?? true,
          CreemApiKey: settings.CreemApiKey,
          CreemWebhookSecret: settings.CreemWebhookSecret,
          CreemTestMode: settings.CreemTestMode,
          CreemFeeFixed: settings.CreemFeeFixed ?? 0,
          CreemFeePercent: settings.CreemFeePercent ?? 0,
          CreemFeeThreshold: settings.CreemFeeThreshold ?? 2,
          CreemModerationEnabled: settings.CreemModerationEnabled ?? false,
          ModerationApiKey: settings.ModerationApiKey ?? '',
          ModerationBaseUrl:
            settings.ModerationBaseUrl ?? 'https://api.openai.com',
          ModerationModel: settings.ModerationModel ?? 'omni-moderation-latest',
          ModerationProvidersText: settings.ModerationProvidersText ?? 'openai',
          ModerationProvidersMedia:
            settings.ModerationProvidersMedia ?? 'openai,creem',
          ModerationCategoryThresholds:
            settings.ModerationCategoryThresholds ?? '{}',
          ModerationDefaultThreshold: settings.ModerationDefaultThreshold ?? 0.8,
          ModerationFailOpen: settings.ModerationFailOpen ?? true,
          ModerationMaxInputChars: settings.ModerationMaxInputChars ?? 8000,
          CreemProducts: settings.CreemProducts,
          NowPaymentsEnabled: settings.NowPaymentsEnabled ?? false,
          NowPaymentsApiKey: settings.NowPaymentsApiKey ?? '',
          NowPaymentsIpnSecret: settings.NowPaymentsIpnSecret ?? '',
          NowPaymentsSandbox: settings.NowPaymentsSandbox ?? false,
          NowPaymentsUnitPrice: settings.NowPaymentsUnitPrice ?? 1.0,
          NowPaymentsMinTopUp: settings.NowPaymentsMinTopUp ?? 1,
          NowPaymentsFeePaidByUser: settings.NowPaymentsFeePaidByUser ?? true,
          NowPaymentsIsFixedRate: settings.NowPaymentsIsFixedRate ?? true,
          NowPaymentsSubscriptionEnabled:
            settings.NowPaymentsSubscriptionEnabled ?? false,
          NowPaymentsEmail: settings.NowPaymentsEmail ?? '',
          NowPaymentsPassword: settings.NowPaymentsPassword ?? '',
          DeloPayEnabled: settings.DeloPayEnabled ?? false,
          DeloPayApiKey: settings.DeloPayApiKey ?? '',
          DeloPayProfileId: settings.DeloPayProfileId ?? '',
          DeloPayWebhookSecret: settings.DeloPayWebhookSecret ?? '',
          DeloPayTestMode: settings.DeloPayTestMode ?? false,
          DeloPayMinTopUp: settings.DeloPayMinTopUp ?? 1,
          DeloPayFeeFixed: settings.DeloPayFeeFixed ?? 0,
          DeloPayFeePercent: settings.DeloPayFeePercent ?? 0,
          DeloPayFeeThreshold: settings.DeloPayFeeThreshold ?? 2,
          DeloPaySubscriptionEnabled:
            settings.DeloPaySubscriptionEnabled ?? false,
          DeloPayCheckoutPane: settings.DeloPayCheckoutPane ?? 'paypal',
        }}
        waffoDefaultValues={{
          WaffoEnabled: settings.WaffoEnabled ?? false,
          WaffoApiKey: settings.WaffoApiKey ?? '',
          WaffoPrivateKey: settings.WaffoPrivateKey ?? '',
          WaffoPublicCert: settings.WaffoPublicCert ?? '',
          WaffoSandboxPublicCert: settings.WaffoSandboxPublicCert ?? '',
          WaffoSandboxApiKey: settings.WaffoSandboxApiKey ?? '',
          WaffoSandboxPrivateKey: settings.WaffoSandboxPrivateKey ?? '',
          WaffoSandbox: settings.WaffoSandbox ?? false,
          WaffoMerchantId: settings.WaffoMerchantId ?? '',
          WaffoCurrency: settings.WaffoCurrency ?? 'USD',
          WaffoUnitPrice: settings.WaffoUnitPrice ?? 1,
          WaffoMinTopUp: settings.WaffoMinTopUp ?? 1,
          WaffoNotifyUrl: settings.WaffoNotifyUrl ?? '',
          WaffoReturnUrl: settings.WaffoReturnUrl ?? '',
          WaffoPayMethods: settings.WaffoPayMethods ?? '[]',
        }}
        waffoPancakeDefaultValues={{
          WaffoPancakeMerchantID: settings.WaffoPancakeMerchantID ?? '',
          WaffoPancakePrivateKey: settings.WaffoPancakePrivateKey ?? '',
          WaffoPancakeReturnURL: settings.WaffoPancakeReturnURL ?? '',
        }}
        waffoPancakeProvisionedStoreID={settings.WaffoPancakeStoreID ?? ''}
        waffoPancakeProvisionedProductID={settings.WaffoPancakeProductID ?? ''}
        complianceDefaults={{
          confirmed: settings['payment_setting.compliance_confirmed'] ?? false,
          termsVersion:
            settings['payment_setting.compliance_terms_version'] ?? '',
          confirmedAt: settings['payment_setting.compliance_confirmed_at'] ?? 0,
          confirmedBy: settings['payment_setting.compliance_confirmed_by'] ?? 0,
        }}
      />
    ),
  },
  {
    id: 'referral',
    titleKey: 'Referral Commission',
    build: (settings: BillingSettings) => (
      <ReferralSettingsSection
        defaultValues={{
          enabled: settings.ReferralCommissionEnabled,
          percent: settings.ReferralCommissionPercent,
          maxRecharges: settings.ReferralCommissionMaxRecharges,
        }}
      />
    ),
  },
  {
    id: 'checkin',
    titleKey: 'Check-in Rewards',
    build: (settings: BillingSettings) => (
      <CheckinSettingsSection
        defaultValues={{
          enabled: settings['checkin_setting.enabled'],
          minQuota: settings['checkin_setting.min_quota'],
          maxQuota: settings['checkin_setting.max_quota'],
        }}
      />
    ),
  },
] as const

export type BillingSectionId = (typeof BILLING_SECTIONS)[number]['id']

const billingRegistry = createSectionRegistry<
  BillingSectionId,
  BillingSettings
>({
  sections: BILLING_SECTIONS,
  defaultSection: 'quota',
  basePath: '/system-settings/billing',
  urlStyle: 'path',
})

export const BILLING_SECTION_IDS = billingRegistry.sectionIds
export const BILLING_DEFAULT_SECTION = billingRegistry.defaultSection
export const getBillingSectionNavItems = billingRegistry.getSectionNavItems
export const getBillingSectionContent = billingRegistry.getSectionContent
export const getBillingSectionMeta = billingRegistry.getSectionMeta
