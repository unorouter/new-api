import { createFileRoute, redirect } from '@tanstack/react-router'
import z from 'zod'
import { useAuthStore } from '@/stores/auth-store'
import { ROLE } from '@/lib/roles'
import { ChannelDiagnostics } from '@/features/channel-diagnostics'

const channelDiagnosticsSearchSchema = z.object({
  page: z.number().optional().catch(1),
  pageSize: z.number().optional().catch(undefined),
  filter: z.string().optional().catch(''),
  status: z.array(z.string()).optional().catch([]),
  trigger_source: z.array(z.string()).optional().catch([]),
  row_type: z.array(z.string()).optional().catch([]),
  status_code: z.string().optional().catch(''),
})

export const Route = createFileRoute('/_authenticated/channel-diagnostics/')({
  validateSearch: channelDiagnosticsSearchSchema,
  beforeLoad: () => {
    const { auth } = useAuthStore.getState()
    if (!auth.user || auth.user.role < ROLE.MOD) {
      throw redirect({
        to: '/403',
      })
    }
  },
  component: ChannelDiagnostics,
})
