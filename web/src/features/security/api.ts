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
import type { ApiResponse, TwoFAStatus } from '@/features/profile/types'
import { api } from '@/lib/api'
import { authRequestOptions, authResult } from '@/lib/secure-verification'

export interface AccessTokenStatus {
  exists: boolean
  token_ref: string
  created_at: number | null
  last_used_at: number | null
  last_used_ip: string
}

export async function getAccessTokenStatus(): Promise<AccessTokenStatus> {
  const response = await api.get<ApiResponse<AccessTokenStatus>>(
    '/api/user/token/status'
  )
  if (!response.data.success || !response.data.data) {
    throw new Error(response.data.message || 'Failed to load token status')
  }
  return response.data.data
}

export async function createAccessToken(): Promise<string> {
  const response = await api.post<ApiResponse<string>>('/api/user/token')
  if (!response.data.success || !response.data.data) {
    throw new Error(response.data.message || 'Failed to generate token')
  }
  return response.data.data
}

export async function revokeAccessToken(): Promise<void> {
  const response = await api.delete<ApiResponse>('/api/user/token')
  if (!response.data.success) {
    throw new Error(response.data.message || 'Failed to revoke token')
  }
}

export interface TwoFASetupData {
  secret: string
  qr_code_data: string
  backup_codes: string[]
  flow_token: string
  expires_at: number
}

export function get2FAStatus(): Promise<TwoFAStatus> {
  return authResult(api.get('/api/user/2fa/status', authRequestOptions))
}

export function setup2FA(
  proofToken: string,
  signal: AbortSignal
): Promise<TwoFASetupData> {
  return authResult(
    api.post('/api/user/2fa/setup', undefined, {
      ...authRequestOptions,
      signal,
      headers: { 'X-Security-Proof': proofToken },
    })
  )
}

export function enable2FA(
  code: string,
  flowToken: string,
  signal: AbortSignal
): Promise<unknown> {
  return authResult(
    api.post(
      '/api/user/2fa/enable',
      { code, flow_token: flowToken },
      {
        ...authRequestOptions,
        signal,
        acceptAuthRotation: true,
        singleUseAuthorization: true,
      }
    )
  )
}
