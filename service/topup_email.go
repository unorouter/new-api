package service

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

// SendTopupConfirmationEmail notifies a paying customer that their topup
// succeeded. If SMTP_BCC is set, the message is also BCC'd to that address
// (e.g. for Trustpilot's Automatic Feedback Service).
func SendTopupConfirmationEmail(userId int, money float64, amount int64, currency string, tradeNo string) {
	user, err := model.GetUserById(userId, false)
	if err != nil || user == nil || user.Email == "" {
		return
	}

	quotaStr := logger.FormatQuota(int(money * common.QuotaPerUnit))
	subject := i18n.Translate("email.topup_success.subject", map[string]any{
		"SystemName": common.SystemName,
	})
	body := i18n.Translate("email.topup_success.body", map[string]any{
		"Username":   user.Username,
		"Amount":     amount,
		"Currency":   currency,
		"Quota":      quotaStr,
		"TradeNo":    tradeNo,
		"SystemName": common.SystemName,
	})

	if err := common.SendEmailWithBcc(subject, user.Email, common.SMTPBcc, body); err != nil {
		common.SysError(fmt.Sprintf("topup confirmation email failed for user %d: %v", userId, err))
	}
}
