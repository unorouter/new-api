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
import { useQuery } from '@tanstack/react-query'
import { Pencil } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  SideDrawerSection,
  sideDrawerContentClassName,
  sideDrawerFooterClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
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
import { Label } from '@/components/ui/label'
import { MultiSelect } from '@/components/multi-select'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  EMPTY_PERMISSION_CATALOG,
  hasPermission,
  normalizeAdminPermissions,
} from '@/lib/admin-permissions'
import { getCurrencyDisplay, getCurrencyLabel } from '@/lib/currency'
import { formatQuota, parseQuotaFromDollars } from '@/lib/format'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import {
  createUser,
  updateUser,
  getUser,
  getGroups,
  setUserBlockFree,
  setUserUnlimitedFree,
  setUserFreeRateLimitWindowPct,
  setUserModerationExempt,
  setUserUsableGroups,
  getPermissionCatalog,
} from '../api'
import { BINDING_FIELDS, ERROR_MESSAGES, SUCCESS_MESSAGES } from '../constants'
import {
  userFormSchema,
  type UserFormValues,
  USER_FORM_DEFAULT_VALUES,
  transformFormDataToPayload,
  transformUserToFormDefaults,
} from '../lib'
import { type User } from '../types'
import { UserQuotaDialog } from './user-quota-dialog'
import { useUsers } from './users-provider'

type UsersMutateDrawerProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  currentRow?: User
}

function parseBlockFree(settingJson?: string): boolean {
  if (!settingJson) return false
  try {
    const parsed = JSON.parse(settingJson)
    return parsed.block_free_when_no_quota === true
  } catch (_e) {
    return false
  }
}

function parseUnlimitedFree(settingJson?: string): boolean {
  if (!settingJson) return false
  try {
    const parsed = JSON.parse(settingJson)
    return parsed.unlimited_free_models === true
  } catch (_e) {
    return false
  }
}

function parseModerationExempt(settingJson?: string): boolean {
  if (!settingJson) return false
  try {
    const parsed = JSON.parse(settingJson)
    return parsed.moderation_exempt === true
  } catch (_e) {
    return false
  }
}

function parseFreeRateLimitWindowPct(settingJson?: string): number {
  if (!settingJson) return 0
  try {
    const parsed = JSON.parse(settingJson)
    const pct = Number(parsed.free_rate_limit_window_pct)
    return Number.isFinite(pct) && pct > 0 ? pct : 0
  } catch (_e) {
    return 0
  }
}

function parseUsableGroups(settingJson?: string): string[] {
  if (!settingJson) return []
  try {
    const parsed = JSON.parse(settingJson)
    return Array.isArray(parsed.usable_groups) ? parsed.usable_groups : []
  } catch (_e) {
    return []
  }
}

export function UsersMutateDrawer({
  open,
  onOpenChange,
  currentRow,
}: UsersMutateDrawerProps) {
  const { t } = useTranslation()
  const isUpdate = !!currentRow
  const { triggerRefresh } = useUsers()
  const currentUser = useAuthStore((s) => s.auth.user)
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [quotaDialogOpen, setQuotaDialogOpen] = useState(false)
  const [blockFree, setBlockFree] = useState(false)
  const [blockFreeSaving, setBlockFreeSaving] = useState(false)
  const [unlimitedFree, setUnlimitedFree] = useState(false)
  const [unlimitedFreeSaving, setUnlimitedFreeSaving] = useState(false)
  const [moderationExempt, setModerationExempt] = useState(false)
  const [moderationExemptSaving, setModerationExemptSaving] = useState(false)
  const [freeRateLimitPct, setFreeRateLimitPct] = useState(0)
  const [freeRateLimitPctSaving, setFreeRateLimitPctSaving] = useState(false)
  const [usableGroups, setUsableGroups] = useState<string[]>([])
  const [usableGroupsSaving, setUsableGroupsSaving] = useState(false)

  // Fetch groups
  const { data: groupsData } = useQuery({
    queryKey: ['groups'],
    queryFn: getGroups,
    staleTime: 5 * 60 * 1000,
  })

  const groups = groupsData?.data || []

  // Permission catalog is owned by the backend; fetched once and reused.
  const { data: permissionCatalog = EMPTY_PERMISSION_CATALOG } = useQuery({
    queryKey: ['admin-permission-catalog'],
    queryFn: getPermissionCatalog,
    staleTime: 5 * 60 * 1000,
  })

  const form = useForm<UserFormValues>({
    resolver: zodResolver(userFormSchema),
    defaultValues: USER_FORM_DEFAULT_VALUES,
  })

  // Load existing data when updating
  useEffect(() => {
    if (open && isUpdate && currentRow) {
      // For update, fetch fresh data
      getUser(currentRow.id).then((result) => {
        if (result.success && result.data) {
          form.reset(transformUserToFormDefaults(result.data))
          setBlockFree(parseBlockFree(result.data.setting))
          setUnlimitedFree(parseUnlimitedFree(result.data.setting))
          setModerationExempt(parseModerationExempt(result.data.setting))
          setFreeRateLimitPct(
            parseFreeRateLimitWindowPct(result.data.setting)
          )
          setUsableGroups(parseUsableGroups(result.data.setting))
        }
      })
    } else if (open && !isUpdate) {
      // For create, reset to defaults
      form.reset(USER_FORM_DEFAULT_VALUES)
    }
  }, [open, isUpdate, currentRow, form])

  const { meta: currencyMeta } = getCurrencyDisplay()
  const currencyLabel = getCurrencyLabel()
  const tokensOnly = currencyMeta.kind === 'tokens'

  const currentQuotaRaw = form.watch('quota_dollars') || 0
  const selectedRole = form.watch('role')
  const canEditAdminPermissions = currentUser?.role === ROLE.SUPER_ADMIN
  const canManageUsers = (currentUser?.role ?? ROLE.GUEST) >= ROLE.ADMIN
  const targetIsAdmin = (selectedRole ?? currentRow?.role ?? 0) >= ROLE.ADMIN

  const onSubmit = async (data: UserFormValues) => {
    if (!isUpdate) {
      const passwordLength = data.password?.length || 0
      if (passwordLength < 8 || passwordLength > 20) {
        form.setError('password', {
          type: 'manual',
          message: t('Password must be between 8 and 20 characters'),
        })
        return
      }
    }

    setIsSubmitting(true)
    try {
      const payload = transformFormDataToPayload(
        data,
        currentRow?.id,
        permissionCatalog
      )
      const result = isUpdate
        ? await updateUser(payload as typeof payload & { id: number })
        : await createUser(payload)

      if (result.success) {
        toast.success(
          isUpdate
            ? t(SUCCESS_MESSAGES.USER_UPDATED)
            : t(SUCCESS_MESSAGES.USER_CREATED)
        )
        onOpenChange(false)
        triggerRefresh()
      } else {
        toast.error(
          result.message ||
            (isUpdate
              ? t(ERROR_MESSAGES.UPDATE_FAILED)
              : t(ERROR_MESSAGES.CREATE_FAILED))
        )
      }
    } catch (_error) {
      toast.error(t(ERROR_MESSAGES.UNEXPECTED))
    } finally {
      setIsSubmitting(false)
    }
  }

  const refreshUserData = async () => {
    if (!currentRow) return
    const result = await getUser(currentRow.id)
    if (result.success && result.data) {
      form.reset(transformUserToFormDefaults(result.data))
      setBlockFree(parseBlockFree(result.data.setting))
      setUnlimitedFree(parseUnlimitedFree(result.data.setting))
      setModerationExempt(parseModerationExempt(result.data.setting))
      setFreeRateLimitPct(parseFreeRateLimitWindowPct(result.data.setting))
      setUsableGroups(parseUsableGroups(result.data.setting))
    }
    triggerRefresh()
  }

  const handleBlockFreeChange = async (checked: boolean) => {
    if (!currentRow) return
    setBlockFree(checked)
    setBlockFreeSaving(true)
    try {
      const result = await setUserBlockFree(currentRow.id, checked)
      if (result.success) {
        toast.success(t('Setting saved'))
        triggerRefresh()
      } else {
        setBlockFree(!checked)
        toast.error(result.message || t(ERROR_MESSAGES.UPDATE_FAILED))
      }
    } catch (_error) {
      setBlockFree(!checked)
      toast.error(t(ERROR_MESSAGES.UNEXPECTED))
    } finally {
      setBlockFreeSaving(false)
    }
  }

  const handleUnlimitedFreeChange = async (checked: boolean) => {
    if (!currentRow) return
    setUnlimitedFree(checked)
    setUnlimitedFreeSaving(true)
    try {
      const result = await setUserUnlimitedFree(currentRow.id, checked)
      if (result.success) {
        toast.success(t('Setting saved'))
        triggerRefresh()
      } else {
        setUnlimitedFree(!checked)
        toast.error(result.message || t(ERROR_MESSAGES.UPDATE_FAILED))
      }
    } catch (_error) {
      setUnlimitedFree(!checked)
      toast.error(t(ERROR_MESSAGES.UNEXPECTED))
    } finally {
      setUnlimitedFreeSaving(false)
    }
  }

  const handleModerationExemptChange = async (checked: boolean) => {
    if (!currentRow) return
    setModerationExempt(checked)
    setModerationExemptSaving(true)
    try {
      const result = await setUserModerationExempt(currentRow.id, checked)
      if (result.success) {
        toast.success(t('Setting saved'))
        triggerRefresh()
      } else {
        setModerationExempt(!checked)
        toast.error(result.message || t(ERROR_MESSAGES.UPDATE_FAILED))
      }
    } catch (_error) {
      setModerationExempt(!checked)
      toast.error(t(ERROR_MESSAGES.UNEXPECTED))
    } finally {
      setModerationExemptSaving(false)
    }
  }

  // Committed on blur/Enter rather than per keystroke: typing "25" would
  // otherwise fire a save for "2" first.
  const handleFreeRateLimitPctCommit = async () => {
    if (!currentRow) return
    const previous = parseFreeRateLimitWindowPct(currentRow.setting)
    const next = Math.min(Math.max(Math.trunc(freeRateLimitPct) || 0, 0), 100)
    if (next === previous) return
    setFreeRateLimitPct(next)
    setFreeRateLimitPctSaving(true)
    try {
      const result = await setUserFreeRateLimitWindowPct(currentRow.id, next)
      if (result.success) {
        toast.success(t('Setting saved'))
        triggerRefresh()
      } else {
        setFreeRateLimitPct(previous)
        toast.error(result.message || t(ERROR_MESSAGES.UPDATE_FAILED))
      }
    } catch (_error) {
      setFreeRateLimitPct(previous)
      toast.error(t(ERROR_MESSAGES.UNEXPECTED))
    } finally {
      setFreeRateLimitPctSaving(false)
    }
  }

  const handleUsableGroupsChange = async (next: string[]) => {
    if (!currentRow) return
    const prev = usableGroups
    setUsableGroups(next)
    setUsableGroupsSaving(true)
    try {
      const result = await setUserUsableGroups(currentRow.id, next)
      if (result.success) {
        toast.success(t('Setting saved'))
        triggerRefresh()
      } else {
        setUsableGroups(prev)
        toast.error(result.message || t(ERROR_MESSAGES.UPDATE_FAILED))
      }
    } catch (_error) {
      setUsableGroups(prev)
      toast.error(t(ERROR_MESSAGES.UNEXPECTED))
    } finally {
      setUsableGroupsSaving(false)
    }
  }

  return (
    <>
      <Sheet
        open={open}
        onOpenChange={(v) => {
          onOpenChange(v)
          if (!v) {
            form.reset()
          }
        }}
      >
        <SheetContent
          className={sideDrawerContentClassName('sm:max-w-[600px]')}
        >
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle>
              {isUpdate ? t('Update') : t('Create')} {t('User')}
            </SheetTitle>
            <SheetDescription>
              {isUpdate
                ? t('Update the user by providing necessary info.')
                : t('Add a new user by providing necessary info.')}
            </SheetDescription>
          </SheetHeader>
          <Form {...form}>
            <form
              id='user-form'
              onSubmit={form.handleSubmit(onSubmit)}
              className={sideDrawerFormClassName()}
            >
              {/* Basic Information */}
              <SideDrawerSection>
                <h3 className='text-sm font-medium'>
                  {t('Basic Information')}
                </h3>

                <FormField
                  control={form.control}
                  name='username'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Username')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          placeholder={t('Enter username')}
                          disabled={isUpdate}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />

                {!isUpdate && (
                  <FormField
                    control={form.control}
                    name='role'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Role')}</FormLabel>
                        <Select
                          items={[
                            { value: '1', label: t('Common User') },
                            { value: '5', label: t('Moderator') },
                            { value: '10', label: t('Admin') },
                          ]}
                          onValueChange={(value) =>
                            value !== null && field.onChange(parseInt(value))
                          }
                          value={String(field.value)}
                        >
                          <FormControl>
                            <SelectTrigger>
                              <SelectValue placeholder={t('Select a role')} />
                            </SelectTrigger>
                          </FormControl>
                          <SelectContent alignItemWithTrigger={false}>
                            <SelectGroup>
                              <SelectItem value='1'>
                                {t('Common User')}
                              </SelectItem>
                              <SelectItem value='5'>
                                {t('Moderator')}
                              </SelectItem>
                              <SelectItem value='10'>{t('Admin')}</SelectItem>
                            </SelectGroup>
                          </SelectContent>
                        </Select>
                        <FormDescription>
                          {t("Set the user's role (cannot be Root)")}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                )}

                <FormField
                  control={form.control}
                  name='display_name'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Display Name')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          placeholder={t('Enter display name')}
                        />
                      </FormControl>
                      <FormDescription>
                        {t('Leave empty to use username')}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />

                <FormField
                  control={form.control}
                  name='password'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Password')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          type='password'
                          placeholder={
                            isUpdate
                              ? t('Leave empty to keep unchanged')
                              : t('Enter password (8-20 characters)')
                          }
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </SideDrawerSection>

              {/* Group & Quota Settings (Update only; admin+ only) */}
              {isUpdate && canManageUsers && (
                <SideDrawerSection>
                  <h3 className='text-sm font-medium'>{t('Group & Quota')}</h3>

                  <FormField
                    control={form.control}
                    name='group'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Group')}</FormLabel>
                        <Select
                          items={[
                            ...groups.map((group) => ({
                              value: group,
                              label: group,
                            })),
                          ]}
                          onValueChange={field.onChange}
                          value={field.value}
                        >
                          <FormControl>
                            <SelectTrigger>
                              <SelectValue placeholder={t('Select a group')} />
                            </SelectTrigger>
                          </FormControl>
                          <SelectContent alignItemWithTrigger={false}>
                            <SelectGroup>
                              {groups.map((group) => (
                                <SelectItem key={group} value={group}>
                                  {group}
                                </SelectItem>
                              ))}
                            </SelectGroup>
                          </SelectContent>
                        </Select>
                        <FormMessage />
                      </FormItem>
                    )}
                  />

                  <div className='space-y-2'>
                    <Label>{t('Usable groups')}</Label>
                    <MultiSelect
                      options={groups.map((group) => ({
                        label: group,
                        value: group,
                      }))}
                      selected={usableGroups}
                      onChange={handleUsableGroupsChange}
                      placeholder={t('Grant extra groups (searchable)')}
                      disabled={usableGroupsSaving}
                    />
                    <p className='text-muted-foreground text-xs'>
                      {t(
                        'Extra groups this user may target via the group override header, on top of their account group. Used for private/per-user channel access.'
                      )}
                    </p>
                  </div>

                  <FormField
                    control={form.control}
                    name='quota_dollars'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>
                          {t('Remaining Quota ({{currency}})', {
                            currency: currencyLabel,
                          })}
                        </FormLabel>
                        <div className='flex gap-2'>
                          <FormControl>
                            <Input
                              value={
                                tokensOnly
                                  ? String(field.value || 0)
                                  : (field.value || 0).toFixed(6)
                              }
                              readOnly
                              className='flex-1'
                            />
                          </FormControl>
                          <Button
                            type='button'
                            variant='outline'
                            onClick={() => setQuotaDialogOpen(true)}
                          >
                            <Pencil className='mr-1 h-4 w-4' />
                            {t('Adjust Quota')}
                          </Button>
                        </div>
                        <FormDescription>
                          {formatQuota(parseQuotaFromDollars(field.value || 0))}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />

                  <FormField
                    control={form.control}
                    name='remark'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Remark')}</FormLabel>
                        <FormControl>
                          <Textarea
                            {...field}
                            placeholder={t(
                              'Admin notes (only visible to admins)'
                            )}
                            rows={3}
                          />
                        </FormControl>
                        <FormMessage />
                      </FormItem>
                    )}
                  />

                  {isUpdate && (
                    <FormField
                      control={form.control}
                      name='referral_commission_percent'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>
                            {t('Referral commission rate override (%)')}
                          </FormLabel>
                          <FormControl>
                            <Input
                              {...field}
                              type='number'
                              min={0}
                              max={100}
                              step='0.01'
                              placeholder={t('Leave empty to use global rate')}
                            />
                          </FormControl>
                          <FormDescription>
                            {t(
                              'Commission this user earns when people they invited top up. Empty uses the global rate; 0 disables commission for this user.'
                            )}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                  )}

                  {isUpdate && (
                    <FormField
                      control={form.control}
                      name='topup_bonus_percent'
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{t('Top-up bonus (%)')}</FormLabel>
                          <FormControl>
                            <Input
                              {...field}
                              type='number'
                              min={0}
                              max={100}
                              step='0.01'
                              placeholder={t('Leave empty for no bonus')}
                            />
                          </FormControl>
                          <FormDescription>
                            {t(
                              'Extra quota granted on top-up, for enterprise partners. 50 means pay $100 and receive $150. Empty or 0 gives no bonus; the maximum is 100. Does not apply to redemption codes.'
                            )}
                          </FormDescription>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                  )}

                  <div className='flex items-start justify-between gap-3 rounded-lg border p-3 sm:items-center sm:p-4'>
                    <div className='space-y-0.5'>
                      <Label>
                        {t('Block free models when balance is zero')}
                      </Label>
                      <p className='text-muted-foreground text-xs sm:text-sm'>
                        {t(
                          'When enabled, this user cannot call zero-cost models once their balance reaches zero. Cleared automatically when they top up.'
                        )}
                      </p>
                    </div>
                    <Switch
                      className='shrink-0'
                      checked={blockFree}
                      onCheckedChange={handleBlockFreeChange}
                      disabled={blockFreeSaving}
                    />
                  </div>

                  <div className='flex items-start justify-between gap-3 rounded-lg border p-3 sm:items-center sm:p-4'>
                    <div className='space-y-0.5'>
                      <Label>{t('Unlimited free models')}</Label>
                      <p className='text-muted-foreground text-xs sm:text-sm'>
                        {t(
                          'Exempts this user from the per-model free-model rate limits. Grant sparingly; free upstream quotas are shared.'
                        )}
                      </p>
                    </div>
                    <Switch
                      className='shrink-0'
                      checked={unlimitedFree}
                      onCheckedChange={handleUnlimitedFreeChange}
                      disabled={unlimitedFreeSaving}
                    />
                  </div>

                  <div className='flex items-start justify-between gap-3 rounded-lg border p-3 sm:items-center sm:p-4'>
                    <div className='space-y-0.5'>
                      <Label>{t('Exempt from prompt moderation')}</Label>
                      <p className='text-muted-foreground text-xs sm:text-sm'>
                        {t(
                          'Skips content moderation on this user\'s image and video generation prompts. Grant only to trusted accounts; the platform stays liable for what they generate.'
                        )}
                      </p>
                    </div>
                    <Switch
                      className='shrink-0'
                      checked={moderationExempt}
                      onCheckedChange={handleModerationExemptChange}
                      disabled={moderationExemptSaving}
                    />
                  </div>

                  <div className='flex items-start justify-between gap-3 rounded-lg border p-3 sm:items-center sm:p-4'>
                    <div className='space-y-0.5'>
                      <Label>{t('Free rate-limit window discount')}</Label>
                      <p className='text-muted-foreground text-xs sm:text-sm'>
                        {t(
                          'Percent off the wait between free-model requests. 0 disables it; 99 makes it nearly instant. The Discord bot sets this while the server tag is worn and clears it when the tag comes off, but it never overwrites a value above 0 - so an amount set here survives until the tag is removed.'
                        )}
                      </p>
                    </div>
                    <Input
                      type='number'
                      min={0}
                      max={100}
                      className='w-20 shrink-0'
                      value={freeRateLimitPct}
                      onChange={(e) =>
                        setFreeRateLimitPct(Number(e.target.value))
                      }
                      onBlur={handleFreeRateLimitPctCommit}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') {
                          e.preventDefault()
                          void handleFreeRateLimitPctCommit()
                        }
                      }}
                      disabled={freeRateLimitPctSaving}
                    />
                  </div>
                </SideDrawerSection>
              )}

              {canEditAdminPermissions &&
                targetIsAdmin &&
                permissionCatalog.resources.length > 0 && (
                  <SideDrawerSection>
                    <h3 className='text-sm font-medium'>
                      {t('Admin Permissions')}
                    </h3>
                    <p className='text-muted-foreground text-xs'>
                      {t(
                        'Default administrator permissions can be overridden for this user.'
                      )}
                    </p>
                    <FormField
                      control={form.control}
                      name='admin_permissions'
                      render={({ field }) => {
                        const selected = normalizeAdminPermissions(
                          field.value,
                          permissionCatalog
                        )
                        return (
                          <FormItem>
                            <div className='space-y-3'>
                              {permissionCatalog.resources.map((resource) => (
                                <div
                                  key={resource.resource}
                                  className='space-y-2 rounded-md border p-3'
                                >
                                  <div className='text-sm font-medium'>
                                    {t(resource.label_key)}
                                  </div>
                                  <div className='space-y-2'>
                                    {resource.actions.map((option) => (
                                      <label
                                        key={option.action}
                                        className='flex items-start gap-3'
                                      >
                                        <Checkbox
                                          checked={
                                            selected[resource.resource]?.[
                                              option.action
                                            ] === true
                                          }
                                          onCheckedChange={(checked) => {
                                            field.onChange({
                                              ...selected,
                                              [resource.resource]: {
                                                ...selected[resource.resource],
                                                [option.action]:
                                                  checked === true,
                                              },
                                            })
                                          }}
                                        />
                                        <span className='flex flex-col gap-1'>
                                          <span className='text-sm font-medium'>
                                            {t(option.label_key)}
                                          </span>
                                          <span className='text-muted-foreground text-xs'>
                                            {t(option.description_key)}
                                          </span>
                                        </span>
                                      </label>
                                    ))}
                                  </div>
                                </div>
                              ))}
                            </div>
                            <FormMessage />
                          </FormItem>
                        )
                      }}
                    />
                    {currentUser && (
                      <p className='text-muted-foreground text-xs'>
                        {hasPermission(
                          currentUser,
                          ADMIN_PERMISSION_RESOURCES.CHANNEL,
                          ADMIN_PERMISSION_ACTIONS.SENSITIVE_WRITE
                        )
                          ? t(
                              'Your account can edit sensitive channel settings.'
                            )
                          : t(
                              'Your account cannot edit sensitive channel settings.'
                            )}
                      </p>
                    )}
                  </SideDrawerSection>
                )}

              {/* Binding Information (Read-only) */}
              {isUpdate && (
                <SideDrawerSection>
                  <h3 className='text-sm font-medium'>
                    {t('Binding Information')}
                  </h3>
                  <p className='text-muted-foreground text-xs'>
                    {t(
                      'Third-party account bindings (read-only, managed by user in profile settings)'
                    )}
                  </p>

                  <div className='flex flex-col gap-3'>
                    {BINDING_FIELDS.map(({ key, label }) => (
                      <div key={key}>
                        <Label className='text-muted-foreground text-xs'>
                          {t(label)}
                        </Label>
                        <Input
                          value={
                            (currentRow?.[key as keyof User] as string) || '-'
                          }
                          disabled
                          className='mt-1'
                        />
                      </div>
                    ))}
                  </div>
                </SideDrawerSection>
              )}
            </form>
          </Form>
          <SheetFooter className={sideDrawerFooterClassName()}>
            <SheetClose render={<Button variant='outline' />}>
              {t('Close')}
            </SheetClose>
            <Button form='user-form' type='submit' disabled={isSubmitting}>
              {isSubmitting ? t('Saving...') : t('Save changes')}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>

      {/* Adjust Quota Dialog */}
      {currentRow && (
        <UserQuotaDialog
          open={quotaDialogOpen}
          onOpenChange={setQuotaDialogOpen}
          userId={currentRow.id}
          currentQuota={parseQuotaFromDollars(currentQuotaRaw || 0)}
          onSuccess={refreshUserData}
        />
      )}
    </>
  )
}
