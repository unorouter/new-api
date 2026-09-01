package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupPartnerRedemptionTest(t *testing.T) {
	t.Helper()
	truncateTables(t)
	require.NoError(t, DB.Exec("DELETE FROM users").Error)
	require.NoError(t, DB.AutoMigrate(&Redemption{}))
	require.NoError(t, DB.Exec("DELETE FROM redemptions").Error)

	oldRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = oldRedis })
}

func seedPartner(t *testing.T, username string, quota int) int {
	t.Helper()
	user := User{
		Username:    username,
		Password:    "unused-password-hash",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AuthVersion: 1,
		Quota:       quota,
		// aff_code is UNIQUE, so seeding two users needs distinct values.
		AffCode: username,
	}
	require.NoError(t, DB.Create(&user).Error)
	return user.Id
}

func quotaOf(t *testing.T, userId int) int {
	t.Helper()
	var user User
	require.NoError(t, DB.Where("id = ?", userId).First(&user).Error)
	return user.Quota
}

// A gift card is paid for out of the partner's balance, so minting must be an
// all-or-nothing move: charge and card, or neither.
func TestCreateFundedRedemptionDeductsBalance(t *testing.T) {
	setupPartnerRedemptionTest(t)
	partner := seedPartner(t, "partner-mint", 1000)

	key, err := CreateFundedRedemption(partner, "gift", 400, 0)
	require.NoError(t, err)
	assert.NotEmpty(t, key)
	assert.Equal(t, 600, quotaOf(t, partner), "the card's face value leaves the partner's balance")

	var redemption Redemption
	require.NoError(t, DB.Where("`key` = ?", key).First(&redemption).Error)
	assert.Equal(t, partner, redemption.UserId, "the creator owns the code")
	assert.Equal(t, 400, redemption.Quota)
	assert.Equal(t, common.RedemptionCodeStatusEnabled, redemption.Status)
}

func TestCreateFundedRedemptionRefusesOverspend(t *testing.T) {
	setupPartnerRedemptionTest(t)
	partner := seedPartner(t, "partner-broke", 100)

	// One unit more than the balance: the guard is >=, not >.
	_, err := CreateFundedRedemption(partner, "gift", 101, 0)
	assert.ErrorIs(t, err, ErrInsufficientQuota)
	assert.Equal(t, 100, quotaOf(t, partner), "a refused mint must not move money")

	var count int64
	require.NoError(t, DB.Model(&Redemption{}).Count(&count).Error)
	assert.Zero(t, count, "a refused mint must not leave a code behind")

	// Exactly the balance is allowed.
	_, err = CreateFundedRedemption(partner, "gift", 100, 0)
	require.NoError(t, err)
	assert.Zero(t, quotaOf(t, partner))
}

// Two mints that each fit the balance but do not fit together: exactly one wins.
// Without an atomic check-and-deduct both would read the same balance and pass.
func TestCreateFundedRedemptionIsAtomicUnderConcurrency(t *testing.T) {
	setupPartnerRedemptionTest(t)
	partner := seedPartner(t, "partner-race", 100)

	type res struct{ err error }
	out := make(chan res, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := CreateFundedRedemption(partner, "gift", 100, 0)
			out <- res{err}
		}()
	}
	succeeded := 0
	for i := 0; i < 2; i++ {
		if (<-out).err == nil {
			succeeded++
		}
	}
	assert.Equal(t, 1, succeeded, "only one mint may be funded by a balance that covers one")
	assert.Zero(t, quotaOf(t, partner), "the balance must not go negative")
}

func TestVoidFundedRedemptionRefundsFaceValue(t *testing.T) {
	setupPartnerRedemptionTest(t)
	partner := seedPartner(t, "partner-void", 1000)

	key, err := CreateFundedRedemption(partner, "gift", 400, 0)
	require.NoError(t, err)
	require.Equal(t, 600, quotaOf(t, partner))

	var redemption Redemption
	require.NoError(t, DB.Where("`key` = ?", key).First(&redemption).Error)

	refunded, err := VoidFundedRedemption(partner, redemption.Id)
	require.NoError(t, err)
	assert.Equal(t, 400, refunded)
	assert.Equal(t, 1000, quotaOf(t, partner), "voiding returns the full face value")

	require.NoError(t, DB.Where("id = ?", redemption.Id).First(&redemption).Error)
	assert.Equal(t, common.RedemptionCodeStatusDisabled, redemption.Status,
		"a voided code stays listed, marked disabled, so the refund can be reconciled")
}

// The money bug this guards: a void racing a customer's redemption must not both
// refund the partner and credit the customer. The status compare-and-swap decides
// one winner, and the refund only runs when the void claimed the row.
func TestVoidFundedRedemptionCannotRefundARedeemedCode(t *testing.T) {
	setupPartnerRedemptionTest(t)
	partner := seedPartner(t, "partner-redeemed", 1000)
	customer := seedPartner(t, "customer", 0)

	key, err := CreateFundedRedemption(partner, "gift", 400, 0)
	require.NoError(t, err)
	var redemption Redemption
	require.NoError(t, DB.Where("`key` = ?", key).First(&redemption).Error)

	credited, err := Redeem(key, customer)
	require.NoError(t, err)
	require.Equal(t, 400, credited)
	balanceBeforeVoid := quotaOf(t, partner)

	_, err = VoidFundedRedemption(partner, redemption.Id)
	assert.ErrorIs(t, err, ErrRedemptionNotVoidable)
	assert.Equal(t, balanceBeforeVoid, quotaOf(t, partner),
		"the customer already has the value; refunding would pay it out twice")
}

func TestVoidFundedRedemptionIsNotRepeatable(t *testing.T) {
	setupPartnerRedemptionTest(t)
	partner := seedPartner(t, "partner-double-void", 1000)

	key, err := CreateFundedRedemption(partner, "gift", 400, 0)
	require.NoError(t, err)
	var redemption Redemption
	require.NoError(t, DB.Where("`key` = ?", key).First(&redemption).Error)

	_, err = VoidFundedRedemption(partner, redemption.Id)
	require.NoError(t, err)
	require.Equal(t, 1000, quotaOf(t, partner))

	_, err = VoidFundedRedemption(partner, redemption.Id)
	assert.ErrorIs(t, err, ErrRedemptionNotVoidable)
	assert.Equal(t, 1000, quotaOf(t, partner), "voiding twice must refund once")
}

// user_id in the void predicate is the authorization check, not a convenience.
func TestVoidFundedRedemptionRejectsAnotherPartnersCode(t *testing.T) {
	setupPartnerRedemptionTest(t)
	owner := seedPartner(t, "partner-owner", 1000)
	attacker := seedPartner(t, "partner-attacker", 1000)

	key, err := CreateFundedRedemption(owner, "gift", 400, 0)
	require.NoError(t, err)
	var redemption Redemption
	require.NoError(t, DB.Where("`key` = ?", key).First(&redemption).Error)

	_, err = VoidFundedRedemption(attacker, redemption.Id)
	assert.ErrorIs(t, err, ErrRedemptionNotVoidable)
	assert.Equal(t, 1000, quotaOf(t, attacker), "voiding someone else's code must not pay out")
	assert.Equal(t, 600, quotaOf(t, owner), "and must not refund the real owner either")

	require.NoError(t, DB.Where("id = ?", redemption.Id).First(&redemption).Error)
	assert.Equal(t, common.RedemptionCodeStatusEnabled, redemption.Status, "the code stays usable")
}

// Multi-tenancy: the stock list and search functions do not filter by owner, so
// without this predicate every partner would see every other partner's codes.
func TestGetRedemptionsByCreatorIsScopedToOnePartner(t *testing.T) {
	setupPartnerRedemptionTest(t)
	partnerA := seedPartner(t, "partner-a", 1000)
	partnerB := seedPartner(t, "partner-b", 1000)

	_, err := CreateFundedRedemption(partnerA, "a-card", 100, 0)
	require.NoError(t, err)
	_, err = CreateFundedRedemption(partnerB, "b-card", 100, 0)
	require.NoError(t, err)

	codes, total, err := GetRedemptionsByCreator(partnerA, 0, 50)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, codes, 1)
	assert.Equal(t, "a-card", codes[0].Name)
	assert.Equal(t, partnerA, codes[0].UserId)
}
