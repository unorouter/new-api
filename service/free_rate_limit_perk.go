package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
)

// The Discord bot owns the server-tag half of the free-model rate-limit discount
// and new-api owns the subscription half, but they share one field, so expiry
// cannot simply zero it: that would strip a tag wearer of the perk the bot
// granted. The bot is cluster-internal and answers what the tag is worth.
var botBaseURL = strings.TrimRight(common.GetEnvOrDefaultString("BOT_INTERNAL_URL", ""), "/")

var botClient = &http.Client{Timeout: 3 * time.Second}

type botTagStatus struct {
	Wearing bool `json:"wearing"`
	Pct     int  `json:"pct"`
}

// tagDiscountFor asks the bot what this user's server tag is worth right now.
// Any failure returns 0: the subscription perk is paid for, so an unreachable
// bot must not leave it granted, and the bot restores the tag value on its own
// hourly reconcile once it is back.
func tagDiscountFor(ctx context.Context, userId int) int {
	if botBaseURL == "" {
		return 0
	}
	url := fmt.Sprintf("%s/tag/%d", botBaseURL, userId)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0
	}
	resp, err := botClient.Do(req)
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("tag lookup failed for user %d, clearing perk: %v", userId, err))
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.LogWarn(ctx, fmt.Sprintf("tag lookup returned %d for user %d, clearing perk", resp.StatusCode, userId))
		return 0
	}
	var status botTagStatus
	if err := common.DecodeJson(resp.Body, &status); err != nil {
		return 0
	}
	if !status.Wearing {
		return 0
	}
	return types.ClampFreeRateLimitWindowPct(status.Pct)
}

// GrantSubscriptionRateLimitPerk raises a user's discount to what their plan
// grants. Never lowers it: the server tag may already be worth more, and the two
// share this field.
func GrantSubscriptionRateLimitPerk(userId int, planPct int) {
	pct := types.ClampFreeRateLimitWindowPct(planPct)
	if pct == 0 {
		return
	}
	ctx := context.Background()
	user, err := model.GetUserById(userId, false)
	if err != nil || user == nil {
		return
	}
	setting := user.GetSetting()
	if setting.FreeRateLimitWindowPct >= pct {
		return
	}
	setting.FreeRateLimitWindowPct = pct
	user.SetSetting(setting)
	if err := user.Update(false); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("failed to grant rate-limit perk for user %d: %v", userId, err))
		return
	}
	logger.LogInfo(ctx, fmt.Sprintf("subscription active for user %d, rate-limit discount now %d", userId, pct))
}

// BackfillSubscriptionRateLimitPerks grants the perk to everyone already holding
// an active subscription. The discount is newer than the subscriptions, so
// without this pass existing subscribers would wait for a renewal to get what
// the pricing page already promises them.
func BackfillSubscriptionRateLimitPerks() {
	subs := model.ActiveSubscribersForPerk(subscriptionBackfillBatchSize, 0)
	if len(subs) == 0 {
		return
	}
	for _, sub := range subs {
		GrantSubscriptionRateLimitPerk(sub.UserId, sub.Pct)
	}
	logger.LogInfo(context.Background(),
		fmt.Sprintf("subscription rate-limit perk backfill checked %d active subscribers", len(subs)))
}

// ClearSubscriptionRateLimitPerk drops a user back to whatever their server tag
// alone earns them. Called when a subscription expires.
func ClearSubscriptionRateLimitPerk(userId int) {
	ctx := context.Background()
	user, err := model.GetUserById(userId, false)
	if err != nil || user == nil {
		return
	}
	setting := user.GetSetting()
	if setting.FreeRateLimitWindowPct == 0 {
		return
	}
	next := tagDiscountFor(ctx, userId)
	if setting.FreeRateLimitWindowPct == next {
		return
	}
	setting.FreeRateLimitWindowPct = next
	user.SetSetting(setting)
	if err := user.Update(false); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("failed to clear rate-limit perk for user %d: %v", userId, err))
		return
	}
	logger.LogInfo(ctx, fmt.Sprintf("subscription expired for user %d, rate-limit discount now %d", userId, next))
}
