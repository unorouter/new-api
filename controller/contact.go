package controller

import (
	"fmt"
	"html"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/go-fuego/fuego"
)

// SendContactMessage forwards the public contact form to the operator mailbox.
func SendContactMessage(c fuego.ContextWithBody[dto.ContactRequest]) (dto.MessageResponse, error) {
	if common.ContactEmail == "" {
		return dto.FailMsg("Contact form is not available")
	}
	req, err := c.Body()
	if err != nil {
		return dto.FailMsg("Invalid parameters")
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(req.Email)
	req.Subject = strings.TrimSpace(req.Subject)
	req.Message = strings.TrimSpace(req.Message)
	if err := common.Validate.Struct(req); err != nil {
		return dto.FailMsg("Invalid parameters")
	}
	subject := req.Subject
	if subject == "" {
		subject = "Contact form"
	}
	subject = fmt.Sprintf("[%s] %s", common.SystemName, strings.NewReplacer("\r", " ", "\n", " ").Replace(subject))
	content := fmt.Sprintf("<p><b>Name:</b> %s<br><b>Email:</b> %s</p><p>%s</p>",
		html.EscapeString(req.Name),
		html.EscapeString(req.Email),
		strings.ReplaceAll(html.EscapeString(req.Message), "\n", "<br>"))
	if err := common.SendEmail(subject, common.ContactEmail, content); err != nil {
		common.SysError("failed to send contact message: " + err.Error())
		return dto.FailMsg("Failed to send message, please try again later")
	}
	return dto.Msg("")
}
