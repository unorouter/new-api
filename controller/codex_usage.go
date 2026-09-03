package controller

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/codex"
	"github.com/QuantumNous/new-api/service"
	"github.com/go-fuego/fuego"
)

type codexWhamFetchFunc func(ctx context.Context, client *http.Client, baseURL, accessToken, accountID string) (int, []byte, error)

func GetCodexChannelUsage(c fuego.ContextNoBody) (dto.CodexUsageData, error) {
	return fetchCodexChannelWhamData(c, service.FetchCodexWhamUsage, "failed to fetch codex usage")
}

func GetCodexChannelRateLimitResetCredits(c fuego.ContextNoBody) (dto.CodexUsageData, error) {
	return fetchCodexChannelWhamData(c, service.FetchCodexWhamRateLimitResetCredits, "failed to fetch codex reset credits")
}

func ResetCodexChannelUsage(c fuego.ContextNoBody) (dto.CodexUsageData, error) {
	return fetchCodexChannelWhamData(c, service.ConsumeCodexWhamRateLimitResetCredit, "failed to reset codex usage")
}

// fetchCodexChannelWhamData resolves the channel's Codex OAuth credential, calls
// one wham endpoint, and retries once after a token refresh on 401/403.
func fetchCodexChannelWhamData(c fuego.ContextNoBody, fetch codexWhamFetchFunc, logPrefix string) (dto.CodexUsageData, error) {
	channelId, err := c.PathParamIntErr("id")
	if err != nil {
		return dto.CodexUsageData{Success: false, Message: fmt.Sprintf("invalid channel id: %v", err)}, nil
	}

	ch, err := model.GetChannelById(channelId, true)
	if err != nil {
		return dto.CodexUsageData{Success: false, Message: err.Error()}, nil
	}
	if ch == nil {
		return dto.CodexUsageData{Success: false, Message: "channel not found"}, nil
	}
	if ch.Type != constant.ChannelTypeCodex {
		return dto.CodexUsageData{Success: false, Message: "channel type is not Codex"}, nil
	}
	if ch.ChannelInfo.IsMultiKey {
		return dto.CodexUsageData{Success: false, Message: "multi-key channel is not supported"}, nil
	}

	oauthKey, err := codex.ParseOAuthKey(strings.TrimSpace(ch.Key))
	if err != nil {
		common.SysError("failed to parse oauth key: " + err.Error())
		return dto.CodexUsageData{Success: false, Message: "Failed to parse authorization information, please check input format"}, nil
	}
	accessToken := strings.TrimSpace(oauthKey.AccessToken)
	accountID := strings.TrimSpace(oauthKey.AccountID)
	if accessToken == "" {
		return dto.CodexUsageData{Success: false, Message: "codex channel: access_token is required"}, nil
	}
	if accountID == "" {
		return dto.CodexUsageData{Success: false, Message: "codex channel: account_id is required"}, nil
	}

	client, err := service.GetHttpClientWithProxy(ch.GetSetting().Proxy)
	if err != nil {
		return dto.CodexUsageData{Success: false, Message: err.Error()}, nil
	}

	reqCtx := c.Request().Context()
	ctx, cancel := context.WithTimeout(reqCtx, 15*time.Second)
	defer cancel()

	statusCode, body, err := fetch(ctx, client, ch.GetBaseURL(), accessToken, accountID)
	if err != nil {
		common.SysError(logPrefix + ": " + err.Error())
		return dto.CodexUsageData{Success: false, Message: "Please try again later"}, nil
	}

	if (statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden) && strings.TrimSpace(oauthKey.RefreshToken) != "" {
		refreshCtx, refreshCancel := context.WithTimeout(reqCtx, 10*time.Second)
		defer refreshCancel()

		res, refreshErr := service.RefreshCodexOAuthTokenWithProxy(refreshCtx, oauthKey.RefreshToken, ch.GetSetting().Proxy)
		if refreshErr == nil {
			oauthKey.AccessToken = res.AccessToken
			oauthKey.RefreshToken = res.RefreshToken
			oauthKey.LastRefresh = time.Now().Format(time.RFC3339)
			oauthKey.Expired = res.ExpiresAt.Format(time.RFC3339)
			if strings.TrimSpace(oauthKey.Type) == "" {
				oauthKey.Type = "codex"
			}

			encoded, encErr := common.Marshal(oauthKey)
			if encErr == nil {
				_ = model.DB.Model(&model.Channel{}).Where("id = ?", ch.Id).Update("key", string(encoded)).Error
				model.InitChannelCache()
			}

			ctx2, cancel2 := context.WithTimeout(reqCtx, 15*time.Second)
			defer cancel2()
			statusCode, body, err = fetch(ctx2, client, ch.GetBaseURL(), oauthKey.AccessToken, accountID)
			if err != nil {
				common.SysError(logPrefix + " after refresh: " + err.Error())
				return dto.CodexUsageData{Success: false, Message: "Please try again later"}, nil
			}
		}
	}

	var payload any
	if common.Unmarshal(body, &payload) != nil {
		payload = string(body)
	}

	ok := statusCode >= 200 && statusCode < 300
	msg := ""
	if !ok {
		msg = fmt.Sprintf("upstream status: %d", statusCode)
	}
	return dto.CodexUsageData{
		Success:        ok,
		Message:        msg,
		UpstreamStatus: statusCode,
		Data:           payload,
	}, nil
}
