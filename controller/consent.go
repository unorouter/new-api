package controller

import (
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"

	"github.com/go-fuego/fuego"
)

var guestIdPattern = regexp.MustCompile(`^[A-Za-z0-9-]{16,64}$`)

// consentSubject resolves who the consent belongs to. The guest routes take
// the guest cookie only from the chat BFF, so a browser cannot write consents
// for another guest id.
func consentSubject(c dto.FuegoCtx, guest bool) (int, string, bool) {
	if !guest {
		return dto.UserID(c), "", true
	}
	ginCtx := dto.GinCtx(c)
	guestId := strings.TrimSpace(ginCtx.GetHeader("X-Guest-Id"))
	if !middleware.ResolvedByBFF(ginCtx) || !guestIdPattern.MatchString(guestId) {
		return 0, "", false
	}
	return 0, guestId, true
}

func getOwnPurposeObjection(c dto.FuegoCtx, guest bool) (*dto.Response[dto.OwnPurposeObjectionData], error) {
	userId, guestId, ok := consentSubject(c, guest)
	if !ok {
		return dto.Fail[dto.OwnPurposeObjectionData]("guest consent routes are limited to the chat service")
	}
	if guest {
		objects, err := model.GuestObjectsToOwnPurposes(guestId)
		if err != nil {
			return dto.Fail[dto.OwnPurposeObjectionData](err.Error())
		}
		return dto.Ok(dto.OwnPurposeObjectionData{Objection: objects})
	}
	user, err := model.GetUserById(userId, true)
	if err != nil {
		return dto.Fail[dto.OwnPurposeObjectionData](err.Error())
	}
	return dto.Ok(dto.OwnPurposeObjectionData{Objection: user.GetSetting().OwnPurposeObjection})
}

func setOwnPurposeObjection(c fuego.ContextWithBody[dto.OwnPurposeObjectionRequest], guest bool) (*dto.Response[dto.OwnPurposeObjectionData], error) {
	userId, guestId, ok := consentSubject(c, guest)
	if !ok {
		return dto.Fail[dto.OwnPurposeObjectionData]("guest consent routes are limited to the chat service")
	}
	req, err := c.Body()
	if err != nil || req.Objection == nil {
		return dto.Fail[dto.OwnPurposeObjectionData]("Invalid parameters")
	}
	objects := *req.Objection
	if guest {
		if err := model.SetGuestOwnPurposeObjection(guestId, objects); err != nil {
			return dto.Fail[dto.OwnPurposeObjectionData](err.Error())
		}
		return dto.Ok(dto.OwnPurposeObjectionData{Objection: objects})
	}
	user, err := model.GetUserById(userId, true)
	if err != nil {
		return dto.Fail[dto.OwnPurposeObjectionData](err.Error())
	}
	setting := user.GetSetting()
	setting.OwnPurposeObjection = objects
	if err := model.UpdateUserSetting(user.Id, setting); err != nil {
		return dto.Fail[dto.OwnPurposeObjectionData]("Update failed")
	}
	return dto.Ok(dto.OwnPurposeObjectionData{Objection: objects})
}

func GetOwnPurposeObjection(c fuego.ContextNoBody) (*dto.Response[dto.OwnPurposeObjectionData], error) {
	return getOwnPurposeObjection(c, false)
}

func SetOwnPurposeObjection(c fuego.ContextWithBody[dto.OwnPurposeObjectionRequest]) (*dto.Response[dto.OwnPurposeObjectionData], error) {
	return setOwnPurposeObjection(c, false)
}

func GetGuestOwnPurposeObjection(c fuego.ContextNoBody) (*dto.Response[dto.OwnPurposeObjectionData], error) {
	return getOwnPurposeObjection(c, true)
}

func SetGuestOwnPurposeObjection(c fuego.ContextWithBody[dto.OwnPurposeObjectionRequest]) (*dto.Response[dto.OwnPurposeObjectionData], error) {
	return setOwnPurposeObjection(c, true)
}
