package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
)

const paidProbeWindow = 10 * time.Minute

// TrackPaidProbe counts the distinct paid models a zero-balance account that never
// spent was refused within a window, and disables the account once that reaches
// PaidProbeMaxDistinctModels. Measured over the 7 days to 2026-10-07: the farm
// registered on 2026-09-06 swept 35 to 39 paid models per 10 minutes, ordinary
// empty wallets peaked at 2 (p90 8), and every account above 25 had never spent.
// Redis only; without it the signal is inert.
func TrackPaidProbe(userId int, modelName string) {
	setting := operation_setting.GetQuotaSetting()
	limit := setting.PaidProbeMaxDistinctModels
	if limit <= 0 || userId <= 0 || modelName == "" || !common.RedisEnabled ||
		paidProbeExempt(setting.PaidProbeExemptUserIds, userId) {
		return
	}
	ctx := context.Background()
	key := fmt.Sprintf("paidProbeModels:user:%d", userId)
	if err := common.RDB.SAdd(ctx, key, modelName).Err(); err != nil {
		common.SysLog("paid probe model-set add failed: " + err.Error())
		return
	}
	// The first refusal fixes the window; re-arming it on every new model would let
	// a slow sweep hold the set open forever.
	if ttl, err := common.RDB.TTL(ctx, key).Result(); err == nil && ttl < 0 {
		common.RDB.Expire(ctx, key, paidProbeWindow)
	}
	distinct, err := common.RDB.SCard(ctx, key).Result()
	if err != nil || distinct < int64(limit) {
		return
	}
	// One disable per account, however many requests race past the threshold.
	if ok, err := common.RDB.SetNX(ctx, key+":done", 1, paidProbeWindow).Result(); err != nil || !ok {
		return
	}
	gopool.Go(func() {
		if err := model.DisableUserForFraud(userId); err != nil {
			common.SysLog(fmt.Sprintf("failed to disable paid-model prober %d: %s", userId, err.Error()))
			return
		}
		model.RecordLog(userId, model.LogTypeManage, fmt.Sprintf(
			"auto-disabled: refused %d distinct paid models within %d minutes on a balance that never held money",
			distinct, int(paidProbeWindow.Minutes())))
	})
}

func paidProbeExempt(list string, userId int) bool {
	for _, field := range strings.Split(list, ",") {
		if id, err := strconv.Atoi(strings.TrimSpace(field)); err == nil && id == userId {
			return true
		}
	}
	return false
}
