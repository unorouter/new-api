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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'

import { UpstreamModelSelection } from '../upstream-model-selection'

type ConfigureModelsDialogProps = {
  open: boolean
  models: string[]
  onOpenChange: (open: boolean) => void
  onApply: (models: string[]) => void
}

export function ConfigureModelsDialog(props: ConfigureModelsDialogProps) {
  const { t } = useTranslation()
  // Mounted for each opening. Keep unchecked candidates available until close.
  const [models] = useState(props.models)
  const [selected, setSelected] = useState(models)

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Configure Models')}
      description={t('Select the models to keep in this channel.')}
      contentClassName='sm:max-w-3xl'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='button'
            onClick={() => {
              props.onApply(selected)
              props.onOpenChange(false)
            }}
          >
            {t('Apply')}
          </Button>
        </>
      }
    >
      <UpstreamModelSelection
        models={models}
        selected={selected}
        existingModels={models}
        onChange={setSelected}
        showChanges={false}
        summaryText={t('Current models: {{count}}', { count: models.length })}
      />
    </Dialog>
  )
}
