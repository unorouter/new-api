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
export type VerificationMethod = '2fa' | 'passkey' | 'password' | 'oauth'
export type SecurityProofScope =
  | 'channel.key.read'
  | 'passkey.register'
  | 'passkey.delete'
  | '2fa.setup'

export type VerificationOperation =
  | { scope: 'channel.key.read'; context: { channel_id: number } }
  | {
      scope: Exclude<SecurityProofScope, 'channel.key.read'>
      context?: Record<string, never>
    }

export interface SecurityProof {
  proof_token: string
  expires_at: number
  method: VerificationMethod
  scope: SecurityProofScope
}

export interface VerificationRequirements {
  scope: SecurityProofScope
  methods: { method: VerificationMethod; available: boolean; reason?: string }[]
  oauth_providers: { slug: string; name: string }[]
  password_encryption_enabled: boolean
}

export type VerificationInput =
  | { method: '2fa'; code: string }
  | { method: 'password'; password: string }
  | { method: 'passkey' }
  | { method: 'oauth'; provider: string }

export type RequestVerificationOptions = VerificationOperation & {
  title?: string
  description?: string
}

export type SecureVerificationState =
  | { phase: 'idle' }
  | { phase: 'loading'; request: RequestVerificationOptions }
  | { phase: 'error'; request: RequestVerificationOptions; error: string }
  | {
      phase: 'ready' | 'verifying'
      request: RequestVerificationOptions
      requirements: VerificationRequirements
      input: VerificationInput | null
      error?: string
    }
