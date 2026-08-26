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
import { useState, useCallback } from 'react'
import i18next from 'i18next'
import { toast } from 'sonner'
import { requestDeloPayPayment, isApiSuccess } from '../api'

export function useDeloPayPayment() {
  const [processing, setProcessing] = useState(false)

  const processDeloPayPayment = useCallback(async (amount: number) => {
    setProcessing(true)
    try {
      const response = await requestDeloPayPayment({
        amount: Math.floor(amount),
        payment_method: 'delopay',
      })

      if (isApiSuccess(response) && response.data?.pay_link) {
        window.open(response.data.pay_link, '_blank')
        toast.success(i18next.t('Redirecting to PayPal checkout...'))
        return true
      }

      toast.error(response.message || i18next.t('Payment request failed'))
      return false
    } catch (_error) {
      toast.error(i18next.t('Payment request failed'))
      return false
    } finally {
      setProcessing(false)
    }
  }, [])

  return { processing, processDeloPayPayment }
}
