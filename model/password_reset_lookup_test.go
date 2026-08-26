package model

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A username is never verified, so the reset fallback must not let one divert
// mail away from the account that actually owns that address, and must refuse to
// resolve when two accounts claim the same one.
func TestGetUniqueUserForPasswordReset(t *testing.T) {
	truncateTables(t)

	require.NoError(t, DB.Create(&User{Username: "owner", Email: "real@example.com", Password: "x", AffCode: "a1"}).Error)
	require.NoError(t, DB.Create(&User{Username: "real@example.com", Password: "x", AffCode: "a2"}).Error)
	require.NoError(t, DB.Create(&User{Username: "solo@example.com", Password: "x", AffCode: "a3"}).Error)

	t.Run("a real email outranks a colliding username", func(t *testing.T) {
		user, err := GetUniqueUserForPasswordReset("real@example.com")
		require.NoError(t, err)
		assert.Equal(t, "owner", user.Username)
	})

	t.Run("username resolves when no account holds the address", func(t *testing.T) {
		user, err := GetUniqueUserForPasswordReset("solo@example.com")
		require.NoError(t, err)
		assert.Equal(t, "solo@example.com", user.Username)
	})

	t.Run("unknown address is not found", func(t *testing.T) {
		_, err := GetUniqueUserForPasswordReset("nobody@example.com")
		assert.True(t, errors.Is(err, ErrEmailNotFound))
	})
}
