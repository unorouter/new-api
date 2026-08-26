package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// OAuth signups lost their referral for months because InsertWithTx accepted an
// inviterId but only ever spent it on bonuses, never persisting it to the column.
// The password path sets InviterId on the struct before Insert, so it was unaffected
// and the gap stayed invisible. Both paths must agree.
func TestInsertPersistsInviterId(t *testing.T) {
	setupUserUpdateTestState(t)

	inviter := User{
		Username:    "inviter-user",
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AuthVersion: 1,
	}
	require.NoError(t, DB.Create(&inviter).Error)

	t.Run("oauth path", func(t *testing.T) {
		invitee := User{
			Username:    "oauth-invitee",
			Password:    "unused-password-hash",
			Role:        common.RoleCommonUser,
			Status:      common.UserStatusEnabled,
			Group:       "default",
			AuthVersion: 1,
		}
		require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
			return invitee.InsertWithTx(tx, inviter.Id)
		}))

		var stored User
		require.NoError(t, DB.Where("id = ?", invitee.Id).First(&stored).Error)
		assert.Equal(t, inviter.Id, stored.InviterId)
	})

	t.Run("no inviter stays zero", func(t *testing.T) {
		invitee := User{
			Username:    "organic-signup",
			Password:    "unused-password-hash",
			Role:        common.RoleCommonUser,
			Status:      common.UserStatusEnabled,
			Group:       "default",
			AuthVersion: 1,
		}
		require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
			return invitee.InsertWithTx(tx, 0)
		}))

		var stored User
		require.NoError(t, DB.Where("id = ?", invitee.Id).First(&stored).Error)
		assert.Zero(t, stored.InviterId)
	})
}
