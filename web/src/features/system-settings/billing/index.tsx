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
import { SettingsPage } from '../components/settings-page'
import type { BillingSettings } from '../types'
import {
  BILLING_DEFAULT_SECTION,
  getBillingSectionContent,
  getBillingSectionMeta,
} from './section-registry.tsx'

const defaultBillingSettings: BillingSettings = {
  QuotaForNewUser: 0,
  PreConsumedQuota: 0,
  QuotaForInviter: 0,
  QuotaForInvitee: 0,
  TopUpLink: '',
  'general_setting.docs_link': '',
  'quota_setting.enable_free_model_pre_consume': true,
  'quota_setting.enable_free_abuse_auto_block': false,
  'quota_setting.free_abuse_max_per_minute': 5,
  'quota_setting.free_abuse_max_distinct_models': 8,
  'quota_setting.free_abuse_max_per_day': 0,
  'quota_setting.free_abuse_max_errors_per_hour': 0,
  'quota_setting.free_abuse_max_media_err_models': 3,
  'quota_setting.charge_on_error': false,
  QuotaPerUnit: 500000,
  USDExchangeRate: 7,
  'general_setting.quota_display_type': 'USD',
  'general_setting.custom_currency_symbol': '¤',
  'general_setting.custom_currency_exchange_rate': 1,
  DisplayInCurrencyEnabled: true,
  DisplayTokenStatEnabled: true,
  ModelPrice: '',
  ModelRatio: '',
  CacheRatio: '',
  CreateCacheRatio: '',
  CompletionRatio: '',
  ImageRatio: '',
  AudioRatio: '',
  AudioCompletionRatio: '',
  ExposeRatioEnabled: false,
  'billing_setting.billing_mode': '{}',
  'billing_setting.billing_expr': '{}',
  'tool_price_setting.prices': '{}',
  TopupGroupRatio: '',
  GroupRatio: '',
  UserUsableGroups: '',
  GroupGroupRatio: '',
  AutoGroups: '',
  MaxTokenAutoGroups: 5,
  DefaultUseAutoGroup: false,
  'group_ratio_setting.group_special_usable_group': '{}',
  PayAddress: '',
  EpayId: '',
  EpayKey: '',
  Price: 7.3,
  MinTopUp: 1,
  CustomCallbackAddress: '',
  PayMethods: '',
  'payment_setting.amount_options': '',
  'payment_setting.amount_discount': '',
  'payment_setting.compliance_confirmed': false,
  'payment_setting.compliance_terms_version': '',
  'payment_setting.compliance_confirmed_at': 0,
  'payment_setting.compliance_confirmed_by': 0,
  'payment_setting.compliance_confirmed_ip': '',
  StripeEnabled: true,
  StripeApiSecret: '',
  StripeWebhookSecret: '',
  StripePriceId: '',
  StripeUnitPrice: 8.0,
  StripeMinTopUp: 1,
  StripePromotionCodesEnabled: false,
  StripeManagedPayments: false,
  StripeTextModerationEnabled: false,
  CreemEnabled: true,
  CreemApiKey: '',
  CreemWebhookSecret: '',
  CreemTestMode: false,
  CreemFeeFixed: 0,
  CreemFeePercent: 0,
  CreemFeeThreshold: 2,
  CreemModerationEnabled: false,
  ModerationApiKey: '',
  ModerationBaseUrl: 'https://api.openai.com',
  ModerationModel: 'omni-moderation-latest',
  ModerationProvidersText: 'openai',
  ModerationProvidersMedia: 'openai,creem',
  ModerationCategoryThresholds:
    '{"sexual/minors":0.2,"self-harm/instructions":0.5,"illicit/violent":0.6,"hate/threatening":0.6,"sexual":0.92,"violence":0.97,"violence/graphic":0.95,"harassment":0.97}',
  ModerationDefaultThreshold: 0.8,
  ModerationFailOpen: true,
  ModerationMaxInputChars: 8000,
  CreemProducts: '[]',
  NowPaymentsEnabled: false,
  NowPaymentsApiKey: '',
  NowPaymentsIpnSecret: '',
  NowPaymentsSandbox: false,
  NowPaymentsUnitPrice: 1.0,
  NowPaymentsMinTopUp: 1,
  NowPaymentsFeePaidByUser: true,
  NowPaymentsIsFixedRate: true,
  NowPaymentsSubscriptionEnabled: false,
  NowPaymentsEmail: '',
  NowPaymentsPassword: '',
  DeloPayEnabled: false,
  DeloPayApiKey: '',
  DeloPayProfileId: '',
  DeloPayWebhookSecret: '',
  DeloPayTestMode: false,
  DeloPayMinTopUp: 1,
  DeloPayFeeFixed: 0,
  DeloPayFeePercent: 0,
  DeloPayFeeThreshold: 2,
  DeloPaySubscriptionEnabled: false,
  DeloPayCheckoutPane: 'paypal',
  WaffoEnabled: false,
  WaffoApiKey: '',
  WaffoPrivateKey: '',
  WaffoPublicCert: '',
  WaffoSandboxPublicCert: '',
  WaffoSandboxApiKey: '',
  WaffoSandboxPrivateKey: '',
  WaffoSandbox: false,
  WaffoMerchantId: '',
  WaffoCurrency: 'USD',
  WaffoUnitPrice: 1,
  WaffoMinTopUp: 1,
  WaffoNotifyUrl: '',
  WaffoReturnUrl: '',
  WaffoPayMethods: '[]',
  WaffoPancakeMerchantID: '',
  WaffoPancakePrivateKey: '',
  WaffoPancakeReturnURL: '',
  WaffoPancakeStoreID: '',
  WaffoPancakeProductID: '',
  'checkin_setting.enabled': false,
  'checkin_setting.min_quota': 1000,
  'checkin_setting.max_quota': 10000,
  ReferralCommissionEnabled: false,
  ReferralCommissionPercent: 10,
  ReferralCommissionMaxRecharges: 0,
}

export function BillingSettings() {
  return (
    <SettingsPage
      routePath='/_authenticated/system-settings/billing/$section'
      defaultSettings={defaultBillingSettings}
      defaultSection={BILLING_DEFAULT_SECTION}
      getSectionContent={getBillingSectionContent}
      getSectionMeta={getBillingSectionMeta}
    />
  )
}
