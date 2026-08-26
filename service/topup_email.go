package service

import (
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// SendTopupConfirmationEmail notifies a paying customer that their topup
// succeeded. If SMTP_BCC is set, the message is also BCC'd to that address
// (e.g. for Trustpilot's Automatic Feedback Service).
// amountPaid is the amount the customer actually paid, in currency units
// (fiat or crypto), never quota.
func SendTopupConfirmationEmail(userId int, money float64, amountPaid float64, currency string, tradeNo string) {
	user, err := model.GetUserById(userId, false)
	if err != nil || user == nil || user.Email == "" {
		return
	}

	// Trimmed float keeps crypto precision (0.0042 BTC) without fiat
	// trailing-zero noise (1.09 EUR).
	amountStr := strconv.FormatFloat(amountPaid, 'f', -1, 64)
	subject := fmt.Sprintf("%s payment confirmation", common.SystemName)
	body := fmt.Sprintf("<p>Hi %s,</p><p>Thanks for your payment of %s %s. We have added %s of credit to your account.</p><p>Order reference: %s</p><p>The %s team</p>",
		user.Username,
		amountStr,
		currency,
		fmt.Sprintf("$%.2f", money),
		tradeNo,
		common.SystemName,
	)

	if err := common.SendEmailWithBcc(subject, user.Email, common.SMTPBcc, body); err != nil {
		common.SysError(fmt.Sprintf("topup confirmation email failed for user %d: %v", userId, err))
	}
}
