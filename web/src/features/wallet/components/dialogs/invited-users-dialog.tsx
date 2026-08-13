import { useEffect, useState } from 'react'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { formatQuota } from '@/lib/format'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from '@/components/ui/tabs'
import { formatTimestamp } from '@/features/subscriptions/lib'
import { getInvitedUsers, getReferralCommissions } from '../../api'
import type { InvitedUser, ReferralCommission } from '../../types'

interface InvitedUsersDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

const PAGE_SIZE = 10

export function InvitedUsersDialog({
  open,
  onOpenChange,
}: InvitedUsersDialogProps) {
  const { t } = useTranslation()
  const [tab, setTab] = useState<'invitees' | 'commissions'>('invitees')

  const [invitees, setInvitees] = useState<InvitedUser[]>([])
  const [inviteesTotal, setInviteesTotal] = useState(0)
  const [inviteesPage, setInviteesPage] = useState(1)
  const [inviteesLoading, setInviteesLoading] = useState(false)

  const [commissions, setCommissions] = useState<ReferralCommission[]>([])
  const [commissionsTotal, setCommissionsTotal] = useState(0)
  const [commissionsPage, setCommissionsPage] = useState(1)
  const [commissionsLoading, setCommissionsLoading] = useState(false)

  useEffect(() => {
    if (open) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setTab('invitees')
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setInviteesPage(1)
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setCommissionsPage(1)
    }
  }, [open])

  useEffect(() => {
    if (!open || tab !== 'invitees') return
    let cancelled = false
    setInviteesLoading(true)
    getInvitedUsers(inviteesPage, PAGE_SIZE)
      .then((res) => {
        if (cancelled) return
        if (res.success && res.data) {
          setInvitees(res.data.items ?? [])
          setInviteesTotal(res.data.total ?? 0)
        } else {
          toast.error(res.message || t('Failed to load invited users'))
        }
      })
      .catch((err) => {
        if (!cancelled) {
          toast.error(
            err instanceof Error
              ? err.message
              : t('Failed to load invited users')
          )
        }
      })
      .finally(() => {
        if (!cancelled) setInviteesLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [open, tab, inviteesPage, t])

  useEffect(() => {
    if (!open || tab !== 'commissions') return
    let cancelled = false
    setCommissionsLoading(true)
    getReferralCommissions(commissionsPage, PAGE_SIZE)
      .then((res) => {
        if (cancelled) return
        if (res.success && res.data) {
          setCommissions(res.data.items ?? [])
          setCommissionsTotal(res.data.total ?? 0)
        } else {
          toast.error(res.message || t('Failed to load commissions'))
        }
      })
      .catch((err) => {
        if (!cancelled) {
          toast.error(
            err instanceof Error
              ? err.message
              : t('Failed to load commissions')
          )
        }
      })
      .finally(() => {
        if (!cancelled) setCommissionsLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [open, tab, commissionsPage, t])

  const inviteesTotalPages = Math.max(1, Math.ceil(inviteesTotal / PAGE_SIZE))
  const commissionsTotalPages = Math.max(
    1,
    Math.ceil(commissionsTotal / PAGE_SIZE)
  )

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className='max-w-3xl'>
        <DialogHeader>
          <DialogTitle>{t('Referral details')}</DialogTitle>
          <DialogDescription>
            {t('Users invited via your link and the commissions you have earned')}
          </DialogDescription>
        </DialogHeader>

        <Tabs
          value={tab}
          onValueChange={(value) => setTab(value as 'invitees' | 'commissions')}
        >
          <TabsList>
            <TabsTrigger value='invitees'>{t('Invited users')}</TabsTrigger>
            <TabsTrigger value='commissions'>{t('Commissions')}</TabsTrigger>
          </TabsList>

          <TabsContent value='invitees'>
            <ScrollArea className='max-h-[420px]'>
              <table className='w-full text-sm'>
                <thead className='text-muted-foreground border-b'>
                  <tr>
                    <th className='py-2 text-left font-medium'>
                      {t('Username')}
                    </th>
                    <th className='py-2 text-left font-medium'>
                      {t('Display Name')}
                    </th>
                    <th className='py-2 text-left font-medium'>
                      {t('Status')}
                    </th>
                    <th className='py-2 text-right font-medium'>
                      {t('Commissions')}
                    </th>
                    <th className='py-2 text-right font-medium'>
                      {t('Total Earned')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {inviteesLoading &&
                    Array.from({ length: 5 }).map((_, i) => (
                      <tr
                        key={`invitee-skeleton-${i}`}
                        className='border-b last:border-0'
                      >
                        <td className='py-2'>
                          <Skeleton className='h-4 w-24' />
                        </td>
                        <td className='py-2'>
                          <Skeleton className='h-4 w-24' />
                        </td>
                        <td className='py-2'>
                          <Skeleton className='h-4 w-16' />
                        </td>
                        <td className='py-2 text-right'>
                          <Skeleton className='ml-auto h-4 w-10' />
                        </td>
                        <td className='py-2 text-right'>
                          <Skeleton className='ml-auto h-4 w-16' />
                        </td>
                      </tr>
                    ))}
                  {!inviteesLoading && invitees.length === 0 && (
                    <tr>
                      <td
                        colSpan={5}
                        className='text-muted-foreground py-6 text-center'
                      >
                        {t('No invited users yet')}
                      </td>
                    </tr>
                  )}
                  {!inviteesLoading &&
                    invitees.map((u) => (
                      <tr key={u.id} className='border-b last:border-0'>
                        <td className='py-2 font-mono text-xs'>{u.username}</td>
                        <td className='py-2'>{u.display_name || '-'}</td>
                        <td className='py-2'>
                          <Badge
                            variant={u.status === 1 ? 'default' : 'destructive'}
                          >
                            {u.status === 1 ? t('Enabled') : t('Disabled')}
                          </Badge>
                        </td>
                        <td className='py-2 text-right tabular-nums'>
                          {u.commission_count}
                        </td>
                        <td className='py-2 text-right tabular-nums'>
                          {formatQuota(u.total_earned)}
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </ScrollArea>

            <div className='text-muted-foreground flex items-center justify-between text-xs'>
              <span>
                {t('Page')} {inviteesPage} / {inviteesTotalPages} (
                {inviteesTotal} {t('users')})
              </span>
              <div className='flex gap-1'>
                <Button
                  variant='outline'
                  size='sm'
                  disabled={inviteesPage <= 1 || inviteesLoading}
                  onClick={() => setInviteesPage((p) => Math.max(1, p - 1))}
                >
                  <ChevronLeft className='size-4' />
                </Button>
                <Button
                  variant='outline'
                  size='sm'
                  disabled={
                    inviteesPage >= inviteesTotalPages || inviteesLoading
                  }
                  onClick={() =>
                    setInviteesPage((p) => Math.min(inviteesTotalPages, p + 1))
                  }
                >
                  <ChevronRight className='size-4' />
                </Button>
              </div>
            </div>
          </TabsContent>

          <TabsContent value='commissions'>
            <ScrollArea className='max-h-[420px]'>
              <table className='w-full text-sm'>
                <thead className='text-muted-foreground border-b'>
                  <tr>
                    <th className='py-2 text-left font-medium'>{t('Time')}</th>
                    <th className='py-2 text-left font-medium'>
                      {t('Invitee')}
                    </th>
                    <th className='py-2 text-left font-medium'>
                      {t('Method')}
                    </th>
                    <th className='py-2 text-right font-medium'>
                      {t('Recharge')}
                    </th>
                    <th className='py-2 text-right font-medium'>
                      {t('Rate')}
                    </th>
                    <th className='py-2 text-right font-medium'>
                      {t('Commission')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {commissionsLoading &&
                    Array.from({ length: 5 }).map((_, i) => (
                      <tr
                        key={`commission-skeleton-${i}`}
                        className='border-b last:border-0'
                      >
                        {Array.from({ length: 6 }).map((_, j) => (
                          <td key={j} className='py-2'>
                            <Skeleton className='h-4 w-16' />
                          </td>
                        ))}
                      </tr>
                    ))}
                  {!commissionsLoading && commissions.length === 0 && (
                    <tr>
                      <td
                        colSpan={6}
                        className='text-muted-foreground py-6 text-center'
                      >
                        {t('No commissions yet')}
                      </td>
                    </tr>
                  )}
                  {!commissionsLoading &&
                    commissions.map((c) => (
                      <tr key={c.id} className='border-b last:border-0'>
                        <td className='py-2 text-xs'>
                          {formatTimestamp(c.created_at)}
                        </td>
                        <td className='py-2 font-mono text-xs'>
                          {c.invitee_username || `#${c.invitee_id}`}
                        </td>
                        <td className='py-2 text-xs'>
                          {c.payment_method || '-'}
                        </td>
                        <td className='py-2 text-right tabular-nums'>
                          {c.recharge_amount.toFixed(2)}
                        </td>
                        <td className='py-2 text-right tabular-nums'>
                          {(c.commission_rate * 100).toFixed(1)}%
                        </td>
                        <td className='py-2 text-right tabular-nums'>
                          {formatQuota(c.commission_quota)}
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </ScrollArea>

            <div className='text-muted-foreground flex items-center justify-between text-xs'>
              <span>
                {t('Page')} {commissionsPage} / {commissionsTotalPages} (
                {commissionsTotal} {t('records')})
              </span>
              <div className='flex gap-1'>
                <Button
                  variant='outline'
                  size='sm'
                  disabled={commissionsPage <= 1 || commissionsLoading}
                  onClick={() =>
                    setCommissionsPage((p) => Math.max(1, p - 1))
                  }
                >
                  <ChevronLeft className='size-4' />
                </Button>
                <Button
                  variant='outline'
                  size='sm'
                  disabled={
                    commissionsPage >= commissionsTotalPages ||
                    commissionsLoading
                  }
                  onClick={() =>
                    setCommissionsPage((p) =>
                      Math.min(commissionsTotalPages, p + 1)
                    )
                  }
                >
                  <ChevronRight className='size-4' />
                </Button>
              </div>
            </div>
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  )
}
