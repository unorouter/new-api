package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TransferQuotaBetweenUsers must move quota atomically, refuse to overspend, and
// reject self-transfers. These are accounting invariants: a failed transfer must
// leave both balances untouched.
func TestTransferQuotaBetweenUsers(t *testing.T) {
	truncateTables(t)

	sender := &User{Username: "sender", AffCode: "affsender", Quota: 1000}
	receiver := &User{Username: "receiver", AffCode: "affreceiver", Quota: 200}
	require.NoError(t, DB.Create(sender).Error)
	require.NoError(t, DB.Create(receiver).Error)

	t.Run("sufficient balance moves quota", func(t *testing.T) {
		after, err := TransferQuotaBetweenUsers(sender.Id, receiver.Id, 300)
		require.NoError(t, err)
		assert.Equal(t, 700, after)

		var s, r User
		require.NoError(t, DB.First(&s, sender.Id).Error)
		require.NoError(t, DB.First(&r, receiver.Id).Error)
		assert.Equal(t, 700, s.Quota)
		assert.Equal(t, 500, r.Quota)
	})

	t.Run("insufficient balance is rejected with no mutation", func(t *testing.T) {
		_, err := TransferQuotaBetweenUsers(sender.Id, receiver.Id, 10000)
		require.ErrorIs(t, err, ErrInsufficientQuota)

		var s, r User
		require.NoError(t, DB.First(&s, sender.Id).Error)
		require.NoError(t, DB.First(&r, receiver.Id).Error)
		assert.Equal(t, 700, s.Quota)
		assert.Equal(t, 500, r.Quota)
	})

	t.Run("self-transfer is rejected", func(t *testing.T) {
		_, err := TransferQuotaBetweenUsers(sender.Id, sender.Id, 100)
		require.Error(t, err)
	})

	t.Run("non-positive amount is rejected", func(t *testing.T) {
		_, err := TransferQuotaBetweenUsers(sender.Id, receiver.Id, 0)
		require.Error(t, err)
	})
}
