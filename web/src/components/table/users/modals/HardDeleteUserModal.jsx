/*
Copyright (C) 2025 QuantumNous

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

import React from 'react';
import { Modal, Typography } from '@douyinfe/semi-ui';
import { useTranslation } from 'react-i18next';

const { Text } = Typography;

const HardDeleteUserModal = ({
  visible,
  onCancel,
  user,
  users,
  activePage,
  refresh,
  hardDeleteUser,
}) => {
  const { t } = useTranslation();
  const handleConfirm = async () => {
    await hardDeleteUser(user.id);
    await refresh();
    setTimeout(() => {
      if (users.length === 0 && activePage > 1) {
        refresh(activePage - 1);
      }
    }, 100);
    onCancel();
  };

  return (
    <Modal
      title={t('确定要永久删除此用户？')}
      visible={visible}
      onCancel={onCancel}
      onOk={handleConfirm}
      type='danger'
    >
      <Text type='danger'>
        {t('此操作将永久删除该用户及其所有关联数据（API 密钥、日志、充值记录等），且无法恢复。')}
      </Text>
    </Modal>
  );
};

export default HardDeleteUserModal;
