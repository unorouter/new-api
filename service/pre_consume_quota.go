package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

// effectiveGroupRatio resolves the group ratio that actually applies to this
// request, honoring auto-group failover and per-user-group special ratios. This
// is the same resolution billing settlement uses.
func effectiveGroupRatio(c *gin.Context, relayInfo *relaycommon.RelayInfo) float64 {
	usingGroup := relayInfo.UsingGroup
	if autoGroup, exists := common.GetContextKey(c, constant.ContextKeyAutoGroup); exists {
		if g, ok := autoGroup.(string); ok {
			usingGroup = g
		}
	}
	groupRatio := ratio_setting.GetGroupRatio(usingGroup)
	if userGroupRatio, ok := ratio_setting.GetGroupGroupRatio(relayInfo.UserGroup, usingGroup); ok {
		groupRatio = userGroupRatio
	}
	return groupRatio
}

// requestChargesQuota reports whether this request lands on a PAID group, i.e.
// the owner intends to charge for it. A group ratio > 0 signals a paid request
// even when the model's base ratio/price happens to be 0 (e.g. a paid model
// priced entirely via per-channel group markups: modelRatio 0 * groupRatio 0.24
// still bills 0 per token, but is NOT a free model). Used to gate zero-balance
// users out of paid models while still allowing genuinely free ones (groupRatio
// == 0), which is the whole point of a quota=0 free-only token.
func requestChargesQuota(c *gin.Context, relayInfo *relaycommon.RelayInfo) bool {
	return effectiveGroupRatio(c, relayInfo) > 0
}

// cameFromFreeFailover reports whether this request was admitted on a free group
// and has since been re-priced onto a paid one mid-chain, which is the only case
// where "the free providers are busy" explains a $0-balance error. It reads the
// marker the relay sets at that single re-price site; it deliberately does not
// infer failover from the group ratio, because AutoGroups is not ordered by
// ratio and a paid model's first candidate is already paid.
func cameFromFreeFailover(c *gin.Context, relayInfo *relaycommon.RelayInfo) bool {
	if relayInfo.TokenGroup != "auto" && !IsCompositeTokenGroup(relayInfo.TokenGroup) {
		return false
	}
	return common.GetContextKeyBool(c, constant.ContextKeyFreeFailoverReprice)
}
