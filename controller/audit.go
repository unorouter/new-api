package controller

import (
	"fmt"
	"os"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// auditContentTemplates 将稳定的操作标识 action 映射为英文兜底模板，渲染后写入
// Log.Content（供导出等非本地化消费者使用）。占位符为 ${name}，由该
// action 的 params 填充。本地化展示文案在前端 i18n 模板中维护，本表是语言中立的
// 英文基线——调用方因此无需在每个埋点处手写句子（避免与 params 重复书写同一份值）。
var auditContentTemplates = map[string]string{
	"user.create":           "Created user ${username} (role ${role})",
	"user.update":           "Updated user ${username} (ID: ${id})",
	"user.delete":           "Deleted user ${username} (ID: ${id})",
	"user.manage":           "Performed ${action} on user ${username} (ID: ${id})",
	"user.quota_add":        "Increased user quota by ${quota}",
	"user.quota_subtract":   "Decreased user quota by ${quota}",
	"user.quota_override":   "Overrode user quota from ${from} to ${to}",
	"user.binding_clear":    "Cleared ${bindingType} binding for user ${username}",
	"user.2fa_disable":      "Force-disabled two-factor authentication for the user",
	"user.passkey_register": "Registered a passkey",
	"user.passkey_delete":   "Deleted a passkey",
	"user.reset_passkey":    "Reset the user passkey",
	"option.update":         "Updated system setting ${key}",

	"token.key_view":           "Revealed API key ${name} (ID: ${id})",
	"token.key_view_batch":     "Revealed ${count} API keys in bulk",
	"token.create":             "Created API key ${name} (ID: ${id})",
	"token.update":             "Changed routing or status of API key ${name} (ID: ${id})",
	"token.guest_model_limits": "Refreshed the guest key ${name} (ID: ${id}) to ${model_count} models",
	"token.delete":             "Deleted API key ${name} (ID: ${id})",
	"token.delete_batch":       "Deleted ${count} API keys in bulk",

	"partner.redemption_create": "Minted a gift card ${name} worth ${quota} from own balance",
	"partner.redemption_void":   "Voided gift card ${id} and refunded ${refunded}",
	"partner.grant":             "Granted ${quota} to user ${recipient_id} from own balance",

	"user.password_change": "Changed the account password",
	"user.aff_transfer":    "Transferred ${quota} of affiliate commission to balance",

	"user.email_bind":   "Bound email ${to} to the account (was ${from})",
	"user.oauth_bind":   "Bound ${provider} identity ${provider_user_id} to the account",
	"user.oauth_unbind": "Removed the ${provider} identity binding",

	"read.user_list":   "Listed user accounts (page ${page}, ${count} returned)",
	"read.user_search": "Searched user accounts for ${keyword} (${count} matched)",
	"read.log_search":  "Searched all users' request logs (${count} returned)",

	"channel.create":             "Created channel ${name} (type ${type}, count ${count})",
	"channel.update":             "Updated channel ${name} (ID: ${id})",
	"channel.delete":             "Deleted channel ${name} (ID: ${id})",
	"channel.delete_batch":       "Batch deleted ${count} channels",
	"channel.delete_disabled":    "Deleted all disabled channels (${count})",
	"channel.key_view":           "Viewed channel key ${name} (ID: ${id})",
	"channel.tag_disable":        "Disabled channels with tag ${tag}",
	"channel.tag_enable":         "Enabled channels with tag ${tag}",
	"channel.tag_edit":           "Edited channels with tag ${tag}",
	"channel.tag_batch_set":      "Batch set tag for ${count} channels",
	"channel.copy":               "Copied channel (source ID: ${sourceId}) to ${name} (new ID: ${id})",
	"channel.multi_key_manage":   "Multi-key management ${action} on channel (ID: ${id})",
	"channel.upstream_apply":     "Applied upstream model changes to channel (ID: ${id})",
	"channel.upstream_apply_all": "Applied upstream model changes to ${count} channels",

	"redemption.create": "Created ${count} redemption codes named ${name} (${quota} each)",

	"subscription.plan_reset":      "Reset active subscriptions for plan ${plan_id}",
	"subscription.user_plan_reset": "Reset active plan ${plan_id} subscriptions for user ${target_user_id}",
}

// auditContentEN 按 action 模板渲染英文兜底文本；未登记的 action 退回 action 本身。
func auditContentEN(action string, params map[string]interface{}) string {
	tmpl, ok := auditContentTemplates[action]
	if !ok {
		return action
	}
	return os.Expand(tmpl, func(key string) string {
		if v, ok := params[key]; ok {
			return fmt.Sprintf("%v", v)
		}
		return ""
	})
}

// auditOperatorInfo 从上下文构建操作者身份信息（管理员 id/用户名/角色）。
func auditOperatorInfo(c *gin.Context) map[string]interface{} {
	return map[string]interface{}{
		"admin_id":        c.GetInt("id"),
		"admin_username":  c.GetString("username"),
		"admin_role":      c.GetInt("role"),
		"auth_method":     auditAuthMethod(c),
		"trusted_network": middleware.IsTrustedNetwork(c.ClientIP()),
	}
}

func auditAuthMethod(c *gin.Context) string {
	if c.GetBool("use_access_token") {
		return "access_token"
	}
	return "session"
}

// markAuditLogged 标记当前请求已在 handler 内手动记录审计日志，
// 使鉴权链路中的审计兜底（finishAdminAudit）跳过兜底记录，避免重复。
func markAuditLogged(c *gin.Context) {
	common.SetContextKey(c, constant.ContextKeyAuditLogged, true)
}

// recordManageAudit 记录一条由操作者本人归属的管理/高危审计日志（资源类操作：
// 渠道 / 系统设置 / 兑换码等）。content 由 action+params 自动渲染。
func recordManageAudit(c *gin.Context, action string, params map[string]interface{}) {
	recordManageAuditFor(c, c.GetInt("id"), action, params)
}

// recordManageAuditFor 记录一条管理审计日志，日志归属于操作者；targetUserId
// 只表示被操作用户，用于在结构化参数中保留目标上下文。
func recordManageAuditFor(c *gin.Context, targetUserId int, action string, params map[string]interface{}) {
	if params == nil {
		params = map[string]interface{}{}
	}
	operatorUserId := c.GetInt("id")
	if _, ok := params["target_user_id"]; !ok && targetUserId > 0 && targetUserId != operatorUserId {
		params["target_user_id"] = targetUserId
	}
	model.RecordOperationAuditLog(operatorUserId, auditContentEN(action, params), c.ClientIP(), action, params, auditOperatorInfo(c), nil)
	markAuditLogged(c)
}

// recordUserSecurityAudit 记录普通用户自己的安全敏感操作（如 passkey 绑定/解绑）。
// 这类日志没有管理员操作者，不写 admin_info；同时不依赖 AdminAuth/RootAuth 的兜底。
//
// auth_method is still recorded: distinguishing a stolen access token from a
// real session is what identified the 2026-08-26 intruder, and that question is
// just as relevant for a user-level credential read as for an admin write.
func recordUserSecurityAudit(c *gin.Context, userId int, action string, params map[string]interface{}) {
	// The log store is absent before init and in handler tests, where the write
	// panics on a nil handle.
	if c == nil || model.LOG_DB == nil {
		return
	}
	auditInfo := map[string]interface{}{
		"auth_method": auditAuthMethod(c),
		"route":       c.FullPath(),
	}
	// Recording the action must never be what fails it, and a context without a
	// Request is real: handler tests build one, and the audit is not what they
	// are exercising. ClientIP() reads the Request too, so it is guarded here
	// rather than passed straight through.
	clientIP := ""
	if c.Request != nil {
		auditInfo["path"] = c.Request.URL.Path
		auditInfo["method"] = c.Request.Method
		clientIP = c.ClientIP()
		auditInfo["trusted_network"] = middleware.IsTrustedNetwork(clientIP)
	}
	model.RecordOperationAuditLog(userId, auditContentEN(action, params), clientIP, action, params, nil, auditInfo)
}

// recordSensitiveRead audits a READ of bulk personal or operational data.
//
// The admin audit fallback only wraps writes (beginAdminAudit bails on anything
// that is not POST/PUT/PATCH/DELETE), so browsing every user, searching all
// logs or paging the channel list has always been invisible. After the
// 2026-08-26 takeover the unanswerable question was not what the intruder
// changed, which was fully recorded, but what they READ while sitting inside ten
// hijacked accounts.
//
// Automation is skipped on purpose. new-api-sync pages the channel list on every
// run and the BFF proxies dashboard reads, both from cluster addresses; logging
// those would add thousands of rows a day to an 11GB table and bury the handful
// that matter. An intruder arrives through Cloudflare with a public address,
// which is the same reasoning the security-denial metric uses.
func recordSensitiveRead(c *gin.Context, action string, params map[string]interface{}) {
	if isInternalClient(c.ClientIP()) {
		return
	}
	recordUserSecurityAudit(c, c.GetInt("id"), action, params)
}

// isInternalClient reports whether an address belongs to our own infrastructure
// rather than a real external caller. One definition, from TRUSTED_NETWORKS.
func isInternalClient(ip string) bool {
	return middleware.IsTrustedNetwork(ip)
}
