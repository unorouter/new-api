package model

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedBonusUser(t *testing.T, percent *float64, quota int) int {
	t.Helper()
	user := User{
		Username:          "topup-bonus-user",
		Password:          "unused-password-hash",
		Role:              common.RoleCommonUser,
		Status:            common.UserStatusEnabled,
		Group:             "default",
		AuthVersion:       1,
		Quota:             quota,
		TopUpBonusPercent: percent,
	}
	require.NoError(t, DB.Create(&user).Error)
	return user.Id
}

func floatPtr(v float64) *float64 { return &v }

func TestApplyTopUpBonus(t *testing.T) {
	for _, tc := range []struct {
		name        string
		percent     *float64
		base        int
		wantQuota   int
		wantPercent float64
	}{
		{name: "no override credits base", percent: nil, base: 1000, wantQuota: 1000, wantPercent: 0},
		{name: "zero credits base", percent: floatPtr(0), base: 1000, wantQuota: 1000, wantPercent: 0},
		{name: "negative credits base", percent: floatPtr(-25), base: 1000, wantQuota: 1000, wantPercent: 0},
		{name: "NaN credits base", percent: floatPtr(math.NaN()), base: 1000, wantQuota: 1000, wantPercent: 0},
		{name: "Inf credits base", percent: floatPtr(math.Inf(1)), base: 1000, wantQuota: 1000, wantPercent: 0},
		// The WG Cards tiers as quoted: pay $10,000 at 25%, receive $12,500.
		{name: "25 percent", percent: floatPtr(25), base: 1000, wantQuota: 1250, wantPercent: 25},
		{name: "50 percent", percent: floatPtr(50), base: 1000, wantQuota: 1500, wantPercent: 50},
		// The cap: at most double, never more.
		{name: "100 percent doubles", percent: floatPtr(100), base: 1000, wantQuota: 2000, wantPercent: 100},
		{name: "over cap clamps to double", percent: floatPtr(1000), base: 1000, wantQuota: 2000, wantPercent: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			require.NoError(t, DB.Exec("DELETE FROM users").Error)
			userId := seedBonusUser(t, tc.percent, 0)

			gotQuota, gotPercent := applyTopUpBonus(DB, userId, tc.base)
			assert.Equal(t, tc.wantQuota, gotQuota)
			assert.Equal(t, tc.wantPercent, gotPercent)
		})
	}
}

// A bonus must never manufacture quota beyond the wallet ceiling. Crediting the
// base is correct here: the customer paid for that much.
func TestApplyTopUpBonusDoesNotOverflowWalletCeiling(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Exec("DELETE FROM users").Error)
	userId := seedBonusUser(t, floatPtr(100), 0)

	base := common.MaxWalletQuota - 1
	gotQuota, gotPercent := applyTopUpBonus(DB, userId, base)
	assert.Equal(t, base, gotQuota, "an overflowing bonus falls back to the paid amount")
	assert.Zero(t, gotPercent)
}

// A missing user must not fail a settlement for money already received.
func TestApplyTopUpBonusFailsOpenOnLookupError(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Exec("DELETE FROM users").Error)

	gotQuota, gotPercent := applyTopUpBonus(DB, 999999, 1000)
	assert.Equal(t, 1000, gotQuota)
	assert.Zero(t, gotPercent)
}

// THE GIFT CARD GUARD. Enterprise partners top up with a bonus and then mint
// redemption codes out of that already-bonused balance. If redemption also
// applied the bonus, every card would be worth double and the reseller discount
// would be given away twice.
func TestRedemptionDoesNotApplyTopUpBonus(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Exec("DELETE FROM users").Error)
	// Migrate explicitly: the table is otherwise only created by whichever
	// redemption test happens to run first, so this passes or fails by ordering.
	require.NoError(t, DB.AutoMigrate(&Redemption{}))
	require.NoError(t, DB.Exec("DELETE FROM redemptions").Error)
	userId := seedBonusUser(t, floatPtr(100), 0)

	redemption := Redemption{
		UserId:      userId,
		Key:         "bonus-guard-key-000000000000000",
		Status:      common.RedemptionCodeStatusEnabled,
		Name:        "gift card",
		Quota:       1000,
		CreatedTime: common.GetTimestamp(),
	}
	require.NoError(t, DB.Create(&redemption).Error)

	credited, err := Redeem(redemption.Key, userId)
	require.NoError(t, err)
	assert.Equal(t, 1000, credited, "a gift card is worth its face value, never the bonus")

	var user User
	require.NoError(t, DB.Where("id = ?", userId).First(&user).Error)
	assert.Equal(t, 1000, user.Quota, "redeeming must not double a 100% bonus user's balance")
}

// Checkout and settlement must judge the same number, or a partner near the
// ceiling passes checkout, pays, and only then has settlement rejected.
func TestValidateTopUpQuotaCapacityAccountsForBonus(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Exec("DELETE FROM users").Error)

	// Balance leaves room for the base amount but not for the bonused amount.
	base := 1000
	userId := seedBonusUser(t, floatPtr(100), common.MaxWalletQuota-1500)

	err := ValidateTopUpQuotaCapacity(userId, base)
	assert.ErrorIs(t, err, ErrTopUpQuotaLimitExceeded,
		"checkout must reject on the bonused total, not the base")
}

// The bonus lives inside the atomic ceiling check, so it cannot tunnel past it.
func TestCreditTopUpQuotaRespectsCeilingWithBonus(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Exec("DELETE FROM users").Error)

	startingQuota := common.MaxWalletQuota - 1500
	userId := seedBonusUser(t, floatPtr(100), startingQuota)

	err := DB.Transaction(func(tx *gorm.DB) error {
		bonused, _ := applyTopUpBonus(tx, userId, 1000)
		return creditTopUpQuota(tx, userId, bonused, nil)
	})
	assert.ErrorIs(t, err, ErrTopUpQuotaLimitExceeded)

	var user User
	require.NoError(t, DB.Where("id = ?", userId).First(&user).Error)
	assert.Equal(t, startingQuota, user.Quota, "a rejected credit must leave the balance untouched")
}
