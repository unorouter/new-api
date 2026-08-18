package controller

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-fuego/fuego"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// maxSettleableTopUpAmount is the largest currency amount whose credited quota
// still fits under common.MaxQuota at the configured QuotaPerUnit. Derived
// rather than hardcoded: this fork raised MaxQuota to 2^53 because every quota
// column is bigint, and a literal from the int32 era caps a legitimate purchase.
func maxSettleableTopUpAmount() int64 {
	return decimal.NewFromInt(common.MaxQuota - 1).
		Div(decimal.NewFromFloat(common.QuotaPerUnit)).
		Floor().IntPart()
}

func configureTopUpQuotaTest(t *testing.T, displayType string) {
	t.Helper()
	oldQuotaPerUnit := common.QuotaPerUnit
	oldDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	common.QuotaPerUnit = 500000
	operation_setting.GetGeneralSetting().QuotaDisplayType = displayType
	t.Cleanup(func() {
		common.QuotaPerUnit = oldQuotaPerUnit
		operation_setting.GetGeneralSetting().QuotaDisplayType = oldDisplayType
	})
}

func newRequestAmountContext(userID int, amount int64) *fuego.MockContext[dto.AmountRequest, any] {
	ctx := fuego.NewMockContext[dto.AmountRequest, any](dto.AmountRequest{Amount: amount}, nil)
	ginCtx, _ := gin.CreateTestContext(nil)
	ginCtx.Set("id", userID)
	ctx.CommonCtx = ginCtx
	return ctx
}

func TestTopUpQuotaValidation(t *testing.T) {
	configureTopUpQuotaTest(t, operation_setting.QuotaDisplayTypeUSD)
	maxAmount := maxSettleableTopUpAmount()

	testCases := []struct {
		name        string
		displayType string
		amount      int64
		wantQuota   int
		wantErr     bool
	}{
		{
			name:        "currency amount below limit",
			displayType: operation_setting.QuotaDisplayTypeUSD,
			amount:      maxAmount,
			wantQuota:   int(decimal.NewFromInt(maxAmount).Mul(decimal.NewFromFloat(500000)).IntPart()),
		},
		{
			name:        "currency amount above limit",
			displayType: operation_setting.QuotaDisplayTypeUSD,
			amount:      maxAmount + 1,
			wantErr:     true,
		},
		{
			name:        "token amount preserves settlement truncation",
			displayType: operation_setting.QuotaDisplayTypeTokens,
			amount:      common.MaxQuota - 1,
			wantQuota:   int(decimal.NewFromInt(maxAmount).Mul(decimal.NewFromFloat(500000)).IntPart()),
		},
		{
			name:        "token amount above settlement limit",
			displayType: operation_setting.QuotaDisplayTypeTokens,
			amount:      common.MaxQuota + int64(common.QuotaPerUnit),
			wantErr:     true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			operation_setting.GetGeneralSetting().QuotaDisplayType = tc.displayType
			quota, err := getTopUpQuota(tc.amount)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantQuota, quota)
		})
	}
}

func TestValidateTopUpQuotaReturnsMaximumAmount(t *testing.T) {
	configureTopUpQuotaTest(t, operation_setting.QuotaDisplayTypeUSD)
	maxAmount := maxSettleableTopUpAmount()

	_, err := validateTopUpQuota(maxAmount)
	require.NoError(t, err)
	_, err = validateTopUpQuota(maxAmount + 1)
	require.EqualError(t, err, fmt.Sprintf("a single top-up cannot exceed %d", maxAmount))
}

func TestRequestAmountRejectsTopUpThatCannotBeSettled(t *testing.T) {
	configureTopUpQuotaTest(t, operation_setting.QuotaDisplayTypeUSD)
	maxAmount := maxSettleableTopUpAmount()

	response, err := RequestAmount(newRequestAmountContext(1, maxAmount+1))

	require.NoError(t, err)
	require.NotNil(t, response)
	assert.False(t, response.Success)
	assert.Equal(t, fmt.Sprintf("a single top-up cannot exceed %d", maxAmount), response.Message)
}

func TestRequestAmountRejectsTopUpThatWouldOverflowWallet(t *testing.T) {
	configureTopUpQuotaTest(t, operation_setting.QuotaDisplayTypeUSD)
	oldDB := model.DB

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}))
	model.DB = db
	t.Cleanup(func() {
		model.DB = oldDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			require.NoError(t, sqlDB.Close())
		}
	})

	maxAmount := maxSettleableTopUpAmount()
	// A wallet already holding almost the whole ceiling cannot absorb another
	// maximum-size top-up, which is what the capacity check exists to refuse.
	require.NoError(t, model.DB.Create(&model.User{
		Id:       42,
		Username: "topup_capacity_user",
		Quota:    common.MaxQuota - 1,
		Status:   common.UserStatusEnabled,
	}).Error)

	response, err := RequestAmount(newRequestAmountContext(42, maxAmount))

	require.NoError(t, err)
	require.NotNil(t, response)
	assert.False(t, response.Success)
	assert.Equal(t, model.ErrTopUpQuotaLimitExceeded.Error(), response.Message)
}

func TestValidateCreditedQuotaRejectsOverflow(t *testing.T) {
	_, err := validateCreditedQuota(decimal.NewFromInt(common.MaxQuota - 1))
	require.NoError(t, err)
	_, err = validateCreditedQuota(decimal.Zero)
	require.EqualError(t, err, "top-up quota must be greater than 0")
	_, err = validateCreditedQuota(decimal.NewFromInt(common.MaxQuota))
	require.EqualError(t, err, "top-up quota exceeds the representable range")
}

func TestStripeCreditedQuotaIncludesGroupRatio(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	oldTopupGroupRatio := common.TopupGroupRatio2JSONString()
	common.QuotaPerUnit = 500000
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"vip":2}`))
	t.Cleanup(func() {
		common.QuotaPerUnit = oldQuotaPerUnit
		require.NoError(t, common.UpdateTopupGroupRatioByJSONString(oldTopupGroupRatio))
	})

	// The group ratio multiplies the credited quota, so the ceiling is reached
	// at half the amount a ratio-1 group could buy.
	maxVipAmount := maxSettleableTopUpAmount() / 2
	_, err := validateCreditedQuota(getStripeCreditedQuota(maxVipAmount, "vip"))
	require.NoError(t, err)
	_, err = validateCreditedQuota(getStripeCreditedQuota(maxVipAmount+1, "vip"))
	require.Error(t, err)

	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"free":0}`))
	assert.True(t, decimal.NewFromInt(500000).Equal(getStripeCreditedQuota(1, "free")))
}
