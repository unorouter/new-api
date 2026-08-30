package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Creem already delivers refund.created / subscription.canceled /
// subscription.expired, but nothing consumed them, so a refunded customer kept a
// live subscription and its whole quota pool until the original end_time: the
// money went back and the access stayed. These tests pin the contract that a
// termination event ends the subscription without touching wallet credit the
// user bought separately.
func seedTerminationFixture(t *testing.T, userGroup string, sub *UserSubscription) (int, int) {
	t.Helper()
	truncateTables(t)
	require.NoError(t, DB.Exec("DELETE FROM users").Error)

	user := User{
		Username:      "creem-termination-user",
		Password:      "unused-password-hash",
		Role:          common.RoleCommonUser,
		Status:        common.UserStatusEnabled,
		Group:         userGroup,
		AuthVersion:   1,
		Quota:         1500000, // wallet credit from an earlier top-up
		CreemCustomer: "cus_termination",
	}
	require.NoError(t, DB.Create(&user).Error)

	order := SubscriptionOrder{
		UserId:          user.Id,
		PlanId:          1,
		Status:          "success",
		TradeNo:         "sub_ref_termination",
		PaymentProvider: "creem",
	}
	require.NoError(t, DB.Create(&order).Error)

	sub.UserId = user.Id
	require.NoError(t, DB.Create(sub).Error)
	return user.Id, sub.Id
}

func TestTerminateUserSubscriptionByCreemEndsSubscriptionAndKeepsWallet(t *testing.T) {
	now := time.Now().Unix()
	userId, subId := seedTerminationFixture(t, "default", &UserSubscription{
		PlanId:      1,
		Status:      "active",
		Source:      "order",
		AmountTotal: 5000000,
		AmountUsed:  0,
		StartTime:   now - 3600,
		EndTime:     now + 30*86400,
	})

	gotUser, gotSub, err := TerminateUserSubscriptionByCreem(CreemTerminationInput{
		ReferenceId:     "sub_ref_termination",
		CreemCustomerId: "cus_termination",
		Reason:          "refund.created",
	})
	require.NoError(t, err)
	assert.Equal(t, userId, gotUser)
	assert.Equal(t, subId, gotSub)

	var sub UserSubscription
	require.NoError(t, DB.Where("id = ?", subId).First(&sub).Error)
	assert.Equal(t, "cancelled", sub.Status)
	assert.Equal(t, sub.AmountTotal, sub.AmountUsed, "subscription quota pool must be spent out")
	assert.LessOrEqual(t, sub.EndTime, time.Now().Unix()+5, "access must stop now, not at the original end_time")

	// The wallet is credit the user bought with a separate top-up; a subscription
	// refund must never claw it back.
	var user User
	require.NoError(t, DB.Where("id = ?", userId).First(&user).Error)
	assert.Equal(t, int(1500000), user.Quota)

	var order SubscriptionOrder
	require.NoError(t, DB.Where("trade_no = ?", "sub_ref_termination").First(&order).Error)
	assert.Equal(t, "cancelled", order.Status)
}

// A second delivery of the same event (Creem retries) must not error or damage
// state.
func TestTerminateUserSubscriptionByCreemIsIdempotent(t *testing.T) {
	now := time.Now().Unix()
	userId, _ := seedTerminationFixture(t, "default", &UserSubscription{
		PlanId:      1,
		Status:      "active",
		Source:      "order",
		AmountTotal: 5000000,
		StartTime:   now - 3600,
		EndTime:     now + 30*86400,
	})

	_, firstSub, err := TerminateUserSubscriptionByCreem(CreemTerminationInput{
		ReferenceId: "sub_ref_termination", Reason: "refund.created",
	})
	require.NoError(t, err)
	require.NotZero(t, firstSub)

	gotUser, secondSub, err := TerminateUserSubscriptionByCreem(CreemTerminationInput{
		ReferenceId: "sub_ref_termination", Reason: "refund.created",
	})
	require.NoError(t, err)
	assert.Equal(t, userId, gotUser)
	assert.Zero(t, secondSub, "no active subscription remains to end")
}

// subscription.expired is a lifecycle end, not a reversal, so it records the
// expired status rather than cancelled.
func TestTerminateUserSubscriptionByCreemRecordsExpiredStatus(t *testing.T) {
	now := time.Now().Unix()
	_, subId := seedTerminationFixture(t, "default", &UserSubscription{
		PlanId:      1,
		Status:      "active",
		Source:      "order",
		AmountTotal: 5000000,
		StartTime:   now - 3600,
		EndTime:     now + 86400,
	})

	_, _, err := TerminateUserSubscriptionByCreem(CreemTerminationInput{
		ReferenceId: "sub_ref_termination", Reason: "subscription.expired",
	})
	require.NoError(t, err)

	var sub UserSubscription
	require.NoError(t, DB.Where("id = ?", subId).First(&sub).Error)
	assert.Equal(t, "expired", sub.Status)
}

// A plan that elevated the user's group must hand that group back on refund,
// otherwise the refunded customer keeps paid-tier routing.
func TestTerminateUserSubscriptionByCreemRevertsUpgradedGroup(t *testing.T) {
	now := time.Now().Unix()
	userId, _ := seedTerminationFixture(t, "vip", &UserSubscription{
		PlanId:        1,
		Status:        "active",
		Source:        "order",
		AmountTotal:   5000000,
		StartTime:     now - 3600,
		EndTime:       now + 30*86400,
		UpgradeGroup:  "vip",
		PrevUserGroup: "default",
	})

	_, _, err := TerminateUserSubscriptionByCreem(CreemTerminationInput{
		ReferenceId: "sub_ref_termination", Reason: "subscription.canceled",
	})
	require.NoError(t, err)

	var user User
	require.NoError(t, DB.Where("id = ?", userId).First(&user).Error)
	assert.Equal(t, "default", user.Group)
}

func TestTerminateUserSubscriptionByCreemReportsUnmappableEvent(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Exec("DELETE FROM users").Error)

	_, _, err := TerminateUserSubscriptionByCreem(CreemTerminationInput{
		ReferenceId:     "sub_ref_does_not_exist",
		CreemCustomerId: "cus_does_not_exist",
		Reason:          "refund.created",
	})
	assert.ErrorIs(t, err, ErrSubscriptionOrderNotFound)
}
