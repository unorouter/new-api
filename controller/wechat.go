package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
)

type wechatLoginResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

func getWeChatIdByCode(code string) (string, error) {
	if code == "" {
		return "", errors.New("invalid parameter")
	}
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/api/wechat/user?code=%s", common.WeChatServerAddress, url.QueryEscape(code)), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", common.WeChatServerToken)
	client := http.Client{
		Timeout: 5 * time.Second,
	}
	httpResponse, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer httpResponse.Body.Close()
	var res wechatLoginResponse
	err = common.DecodeJson(httpResponse.Body, &res)
	if err != nil {
		return "", err
	}
	if !res.Success {
		return "", errors.New(res.Message)
	}
	if res.Data == "" {
		return "", errors.New("verification code is incorrect or has expired")
	}
	return res.Data, nil
}

func WeChatAuth(c *gin.Context) {
	if !common.WeChatAuthEnabled {
		c.JSON(http.StatusOK, gin.H{
			"message": "the administrator has not enabled login and registration via WeChat",
			"success": false,
		})
		return
	}
	code := c.Query("code")
	wechatId, err := getWeChatIdByCode(code)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"message": err.Error(),
			"success": false,
		})
		return
	}
	user := model.User{
		WeChatId: wechatId,
	}
	if model.IsWeChatIdAlreadyTaken(wechatId) {
		err := user.FillUserByWeChatId()
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": err.Error(),
			})
			return
		}
		if user.Id == 0 {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "user has been deactivated",
			})
			return
		}
	} else {
		if common.RegisterEnabled {
			user.Username = "wechat_" + strconv.Itoa(model.GetMaxUserId()+1)
			user.DisplayName = "WeChat User"
			user.Role = common.RoleCommonUser
			user.Status = common.UserStatusEnabled

			if err := user.Insert(0); err != nil {
				c.JSON(http.StatusOK, gin.H{
					"success": false,
					"message": err.Error(),
				})
				return
			}
		} else {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "the administrator has disabled new user registration",
			})
			return
		}
	}

	if user.Status != common.UserStatusEnabled {
		c.JSON(http.StatusOK, gin.H{
			"message": "user has been banned",
			"success": false,
		})
		return
	}
	setupLogin(&user, c)
}

func WeChatBind(c fuego.ContextWithParams[dto.WeChatBindParams]) (dto.MessageResponse, error) {
	if !common.WeChatAuthEnabled {
		return dto.FailMsg("the administrator has not enabled login and registration via WeChat")
	}
	p, err := dto.ParseParams[dto.WeChatBindParams](c)
	if err != nil {
		return dto.FailMsg("invalid request")
	}
	wechatId, err := getWeChatIdByCode(p.Code)
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	if model.IsWeChatIdAlreadyTaken(wechatId) {
		return dto.FailMsg("this WeChat account is already bound")
	}
	ginCtx := dto.GinCtx(c)
	userId := ginCtx.GetInt("id")
	if userId <= 0 {
		return dto.FailMsg("not logged in")
	}
	if err = model.UpdateUserBindColumn(userId, "wechat_id", wechatId); err != nil {
		return dto.FailMsg(err.Error())
	}
	return dto.Msg("")
}
