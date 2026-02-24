import React, { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import RiskAcknowledgementModal from '../../../common/modals/RiskAcknowledgementModal';
import {
  STATUS_CODE_RISK_I18N_KEYS,
  STATUS_CODE_RISK_CHECKLIST_KEYS,
} from './statusCodeRiskGuard';

const StatusCodeRiskGuardModal = React.memo(function StatusCodeRiskGuardModal({
  visible,
  detailItems,
  onCancel,
  onConfirm,
}) {
  const { t, i18n } = useTranslation();
  const checklist = useMemo(
    () => STATUS_CODE_RISK_CHECKLIST_KEYS.map((item) => item),
    [i18n.language],
  );

  return (
    <RiskAcknowledgementModal
      visible={visible}
      title={STATUS_CODE_RISK_I18N_KEYS.title}
      markdownContent={STATUS_CODE_RISK_I18N_KEYS.markdown}
      detailTitle={STATUS_CODE_RISK_I18N_KEYS.detailTitle}
      detailItems={detailItems}
      checklist={checklist}
      inputPrompt={STATUS_CODE_RISK_I18N_KEYS.inputPrompt}
      requiredText={STATUS_CODE_RISK_I18N_KEYS.confirmText}
      inputPlaceholder={STATUS_CODE_RISK_I18N_KEYS.inputPlaceholder}
      mismatchText={STATUS_CODE_RISK_I18N_KEYS.mismatchText}
      cancelText={t('取消')}
      confirmText={STATUS_CODE_RISK_I18N_KEYS.confirmButton}
      onCancel={onCancel}
      onConfirm={onConfirm}
    />
  );
});

export default StatusCodeRiskGuardModal;
