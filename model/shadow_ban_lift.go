package model

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/common"
)

// LiftShadowBan clears every per-account free-model ban an account can carry:
// the auto-block flag in its settings and the request-time Redis key. Called
// when the account proves it is a person by binding a login or paying. The
// network verdict needs no clearing, it exempts identities and spend itself.
func LiftShadowBan(userId int, reason string) {
	if userId <= 0 {
		return
	}
	lifted := false
	if common.RedisEnabled {
		if n, err := common.RDB.Del(context.Background(), common.ShadowBanUserKey(userId)).Result(); err == nil && n > 0 {
			lifted = true
		}
	}
	if s, err := GetUserSetting(userId, false); err == nil && s.BlockFreeWhenNoQuota {
		if user, err := GetUserById(userId, true); err == nil {
			ns := user.GetSetting()
			if ns.BlockFreeWhenNoQuota {
				ns.BlockFreeWhenNoQuota = false
				user.SetSetting(ns)
				if err := user.Update(false); err == nil {
					lifted = true
				} else {
					common.SysLog(fmt.Sprintf("failed to lift free-model block for user %d: %s", userId, err.Error()))
				}
			}
		}
	}
	if lifted {
		RecordLog(userId, LogTypeManage, "free-model shadow ban lifted: "+reason)
	}
}
