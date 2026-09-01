package controller

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"github.com/shopspring/decimal"
)

// validateTokenGroupMapping checks a token's per-model group mapping: valid
// bounded JSON with every pinned group usable by this user.
func validateTokenGroupMapping(userId int, mappingJSON string) error {
	mappingJSON = strings.TrimSpace(mappingJSON)
	if mappingJSON == "" || mappingJSON == "{}" {
		return nil
	}
	if len(mappingJSON) > 65536 {
		return errors.New("group_mapping too large")
	}
	mapping := service.ParseTokenGroupMapping(mappingJSON)
	if mapping == nil {
		return errors.New("invalid group_mapping")
	}
	userGroup, err := model.GetUserGroup(userId, false)
	if err != nil {
		return err
	}
	usable := service.GetUserUsableGroups(userGroup)
	for m, groups := range mapping {
		if strings.TrimSpace(m) == "" {
			return errors.New("group_mapping contains an empty model name")
		}
		for _, g := range groups {
			g = strings.TrimSpace(g)
			if g == "" || g == "auto" {
				continue
			}
			if _, ok := usable[g]; !ok {
				return fmt.Errorf("group %q is not available for this account", g)
			}
			if !ratio_setting.ContainsGroupRatio(g) {
				return fmt.Errorf("group %q has been deprecated", g)
			}
		}
	}
	return nil
}

// TokenResponse is a token with its resolved auto-group order.
type TokenResponse struct {
	*model.Token
	AutoGroups []string `json:"auto_groups"`
}

func maxTokenQuota() int {
	quota, err := common.WalletQuotaFromDecimalStrict(
		decimal.NewFromInt(1_000_000_000).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
	)
	if err != nil {
		return common.MaxWalletQuota
	}
	return quota
}

func buildMaskedTokenResponse(token *model.Token) *TokenResponse {
	if token == nil {
		return nil
	}
	maskedToken := *token
	maskedToken.Key = token.GetMaskedKey()
	autoGroups, err := token.GetAutoGroups()
	if err != nil {
		common.SysError(fmt.Sprintf("failed to parse auto groups for token %d: %v", token.Id, err))
		autoGroups = nil
	}
	if len(autoGroups) == 0 {
		autoGroups = nil
	}
	return &TokenResponse{Token: &maskedToken, AutoGroups: autoGroups}
}

func buildMaskedTokenResponses(tokens []*model.Token) []*TokenResponse {
	maskedTokens := make([]*TokenResponse, 0, len(tokens))
	for _, token := range tokens {
		maskedTokens = append(maskedTokens, buildMaskedTokenResponse(token))
	}
	return maskedTokens
}

func getTokenRequestUserGroup(c *gin.Context) (string, error) {
	if userGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup); userGroup != "" {
		return userGroup, nil
	}
	if userGroup := c.GetString("group"); userGroup != "" {
		return userGroup, nil
	}
	return model.GetUserGroup(c.GetInt("id"), false)
}

func setTokenAutoGroups(c *gin.Context, token *model.Token, groups []string) error {
	if len(groups) == 0 {
		return token.SetAutoGroups(nil)
	}

	maxCount := setting.GetMaxTokenAutoGroups()
	if len(groups) > maxCount {
		return fmt.Errorf("at most %d auto groups are allowed", maxCount)
	}

	userGroup, err := getTokenRequestUserGroup(c)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		if _, ok := seen[group]; ok {
			return fmt.Errorf("group %q is duplicated", group)
		}
		seen[group] = struct{}{}
		if !service.IsUserSelectableGroup(userGroup, group) {
			return fmt.Errorf("group %q is not available for this account", group)
		}
	}

	return token.SetAutoGroups(groups)
}

func GetAllTokens(c fuego.ContextNoBody) (*dto.Response[dto.PageData[*TokenResponse]], error) {
	page := dto.PageInfo(c)
	tokens, err := model.GetAllUserTokens(dto.UserID(c), page.GetStartIdx(), page.GetPageSize())
	if err != nil {
		return dto.FailPage[*TokenResponse](err.Error())
	}
	total, _ := model.CountUserTokens(dto.UserID(c))
	return dto.OkPage(page, buildMaskedTokenResponses(tokens), int(total))
}

func SearchTokens(c fuego.ContextWithParams[dto.SearchTokensParams]) (*dto.Response[dto.PageData[*TokenResponse]], error) {
	p, _ := dto.ParseParams[dto.SearchTokensParams](c)
	page := dto.PageInfo(c)

	tokens, total, err := model.SearchUserTokens(dto.UserID(c), p.Keyword, p.Token, page.GetStartIdx(), page.GetPageSize())
	if err != nil {
		return dto.FailPage[*TokenResponse](err.Error())
	}
	return dto.OkPage(page, buildMaskedTokenResponses(tokens), int(total))
}

func GetToken(c fuego.ContextNoBody) (*dto.Response[TokenResponse], error) {
	id, err := c.PathParamIntErr("id")
	if err != nil {
		return dto.Fail[TokenResponse](err.Error())
	}
	token, err := model.GetTokenByIds(id, dto.UserID(c))
	if err != nil {
		return dto.Fail[TokenResponse](err.Error())
	}
	return dto.Ok(*buildMaskedTokenResponse(token))
}

func GetTokenAutoGroups(c fuego.ContextNoBody) (*dto.Response[dto.TokenAutoGroupsData], error) {
	userGroup, err := getTokenRequestUserGroup(dto.GinCtx(c))
	if err != nil {
		return dto.Fail[dto.TokenAutoGroupsData](err.Error())
	}
	return dto.Ok(dto.TokenAutoGroupsData{
		Groups:   service.GetUserAutoGroup(userGroup),
		MaxCount: setting.GetMaxTokenAutoGroups(),
	})
}

func GetTokenKey(c fuego.ContextNoBody) (*dto.Response[map[string]string], error) {
	id, err := c.PathParamIntErr("id")
	if err != nil {
		return dto.Fail[map[string]string](err.Error())
	}
	token, err := model.GetTokenByIds(id, dto.UserID(c))
	if err != nil {
		return dto.Fail[map[string]string](err.Error())
	}
	// Handing out a plaintext API key is the highest-value read in the product,
	// and it sits under UserAuth, so the admin audit fallback never covers it.
	// Without this an attacker inside a hijacked account can drain every key the
	// account owns and leave no trace at all.
	recordUserSecurityAudit(dto.GinCtx(c), dto.UserID(c), "token.key_view", map[string]interface{}{
		"id":   token.Id,
		"name": token.Name,
	})
	return dto.Ok(map[string]string{
		"key": token.GetFullKey(),
	})
}

func GetTokenStatus(c *gin.Context) {
	tokenId := c.GetInt("token_id")
	userId := c.GetInt("id")
	token, err := model.GetTokenByIds(tokenId, userId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	expiredAt := token.ExpiredTime
	if expiredAt == -1 {
		expiredAt = 0
	}
	c.JSON(200, dto.CreditSummary{
		Object:         "credit_summary",
		TotalGranted:   token.RemainQuota,
		TotalUsed:      0,
		TotalAvailable: token.RemainQuota,
		ExpiresAt:      expiredAt * 1000,
	})
}

func GetTokenUsage(c fuego.ContextNoBody) (*dto.Response[dto.TokenUsageData], error) {
	authHeader := c.Header("Authorization")
	if authHeader == "" {
		return dto.Fail[dto.TokenUsageData]("No Authorization header")
	}

	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		return dto.Fail[dto.TokenUsageData]("Invalid Bearer token")
	}
	tokenKey := parts[1]

	token, err := model.GetTokenByKey(strings.TrimPrefix(tokenKey, "sk-"), false)
	if err != nil {
		common.SysError("failed to get token by key: " + err.Error())
		return dto.Fail[dto.TokenUsageData]("Failed to get token info, please try again later")
	}

	expiredAt := token.ExpiredTime
	if expiredAt == -1 {
		expiredAt = 0
	}

	return dto.Ok(dto.TokenUsageData{
		Object:             "token_usage",
		Name:               token.Name,
		TotalGranted:       token.RemainQuota + token.UsedQuota,
		TotalUsed:          token.UsedQuota,
		TotalAvailable:     token.RemainQuota,
		UnlimitedQuota:     token.UnlimitedQuota,
		ModelLimits:        token.GetModelLimitsMap(),
		ModelLimitsEnabled: token.ModelLimitsEnabled,
		ExpiresAt:          expiredAt,
	})
}

func AddToken(c fuego.ContextWithBody[dto.CreateTokenRequest]) (dto.MessageResponse, error) {
	token, err := c.Body()
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	if len(token.Name) > 50 {
		return dto.FailMsg("Token name is too long")
	}
	if !token.UnlimitedQuota {
		if token.RemainQuota <= 0 {
			return dto.FailMsg("Token quota must be greater than 0. Enable unlimited quota for a free-tier key.")
		}
		maxQuotaValue := maxTokenQuota()
		if token.RemainQuota > maxQuotaValue {
			return dto.FailMsg(fmt.Sprintf("Quota value exceeds valid range, maximum is %d", maxQuotaValue))
		}
	}
	maxTokens := operation_setting.GetMaxUserTokens()
	count, err := model.CountUserTokens(dto.UserID(c))
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	if int(count) >= maxTokens {
		return dto.FailMsg(fmt.Sprintf("maximum token limit reached (%d)", maxTokens))
	}
	if token.Group != "auto" {
		token.CrossGroupRetry = false
	}
	key, err := common.GenerateKey()
	if err != nil {
		common.SysLog("failed to generate token key: " + err.Error())
		return dto.FailMsg("Failed to generate token")
	}
	if err := validateTokenGroupMapping(dto.UserID(c), token.GroupMapping); err != nil {
		return dto.FailMsg(err.Error())
	}
	cleanToken := model.Token{
		UserId:             dto.UserID(c),
		Name:               token.Name,
		Key:                key,
		CreatedTime:        common.GetTimestamp(),
		AccessedTime:       common.GetTimestamp(),
		ExpiredTime:        token.ExpiredTime,
		RemainQuota:        token.RemainQuota,
		UnlimitedQuota:     token.UnlimitedQuota,
		ModelLimitsEnabled: token.ModelLimitsEnabled,
		ModelLimits:        token.ModelLimits,
		AllowIps:           token.AllowIps,
		Group:              token.Group,
		CrossGroupRetry:    token.CrossGroupRetry,
		GroupMapping:       token.GroupMapping,
	}
	if token.Group == "auto" && token.AutoGroups != nil {
		if err := setTokenAutoGroups(dto.GinCtx(c), &cleanToken, *token.AutoGroups); err != nil {
			return dto.FailMsg(err.Error())
		}
	}
	err = cleanToken.Insert()
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	// Creating a key inside a hijacked account is how an intruder converts a
	// session into credentials that outlive it. On 2026-08-27 one was minted and
	// read back 23 seconds later, and only the read was recorded: reconstructing
	// the create needed a database restore.
	recordUserSecurityAudit(dto.GinCtx(c), dto.UserID(c), "token.create", map[string]interface{}{
		"id":              cleanToken.Id,
		"name":            cleanToken.Name,
		"unlimited_quota": cleanToken.UnlimitedQuota,
		"expired_time":    cleanToken.ExpiredTime,
		"group":           cleanToken.Group,
	})
	return dto.Msg("")
}

func DeleteToken(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	id := c.PathParamInt("id")
	// Read before the delete: afterwards the row is gone and the audit could
	// only name an opaque id.
	name := ""
	if existing, lookupErr := model.GetTokenByIds(id, dto.UserID(c)); lookupErr == nil {
		name = existing.Name
	}
	err := model.DeleteTokenById(id, dto.UserID(c))
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	recordUserSecurityAudit(dto.GinCtx(c), dto.UserID(c), "token.delete", map[string]interface{}{
		"id":   id,
		"name": name,
	})
	return dto.Msg("")
}

func UpdateToken(c fuego.Context[dto.UpdateTokenRequest, dto.StatusOnlyParams]) (*dto.Response[TokenResponse], error) {
	p, _ := dto.ParseParams[dto.StatusOnlyParams](c)
	token, err := c.Body()
	if err != nil {
		return dto.Fail[TokenResponse](err.Error())
	}
	if len(token.Name) > 50 {
		return dto.Fail[TokenResponse]("Token name is too long")
	}
	if !token.UnlimitedQuota {
		if token.RemainQuota <= 0 {
			return dto.Fail[TokenResponse]("Token quota must be greater than 0. Enable unlimited quota for a free-tier key.")
		}
		maxQuotaValue := maxTokenQuota()
		if token.RemainQuota > maxQuotaValue {
			return dto.Fail[TokenResponse](fmt.Sprintf("Quota value exceeds valid range, maximum is %d", maxQuotaValue))
		}
	}
	cleanToken, err := model.GetTokenByIds(token.Id, dto.UserID(c))
	if err != nil {
		return dto.Fail[TokenResponse](err.Error())
	}
	if token.Status == common.TokenStatusEnabled {
		if cleanToken.Status == common.TokenStatusExpired && cleanToken.ExpiredTime <= common.GetTimestamp() && cleanToken.ExpiredTime != -1 {
			return dto.Fail[TokenResponse]("Token has expired and cannot be enabled. Please modify the expiration time or set it to never expire")
		}
		if cleanToken.Status == common.TokenStatusExhausted && cleanToken.RemainQuota <= 0 && !cleanToken.UnlimitedQuota {
			return dto.Fail[TokenResponse]("Token quota is exhausted and cannot be enabled. Please modify the remaining quota or set it to unlimited")
		}
	}
	if p.StatusOnly != "" {
		cleanToken.Status = token.Status
	} else {
		if err := validateTokenGroupMapping(dto.UserID(c), token.GroupMapping); err != nil {
			return dto.Fail[TokenResponse](err.Error())
		}
		cleanToken.Name = token.Name
		cleanToken.ExpiredTime = token.ExpiredTime
		cleanToken.RemainQuota = token.RemainQuota
		cleanToken.UnlimitedQuota = token.UnlimitedQuota
		cleanToken.ModelLimitsEnabled = token.ModelLimitsEnabled
		cleanToken.ModelLimits = token.ModelLimits
		cleanToken.AllowIps = token.AllowIps
		cleanToken.Group = token.Group
		cleanToken.CrossGroupRetry = token.CrossGroupRetry
		cleanToken.GroupMapping = token.GroupMapping
		if token.Group != "auto" {
			cleanToken.CrossGroupRetry = false
			_ = cleanToken.SetAutoGroups(nil)
		} else if token.AutoGroups != nil {
			if err := setTokenAutoGroups(dto.GinCtx(c), cleanToken, *token.AutoGroups); err != nil {
				return dto.Fail[TokenResponse](err.Error())
			}
		}
	}
	err = cleanToken.Update()
	if err != nil {
		return dto.Fail[TokenResponse](err.Error())
	}
	return dto.Ok(*buildMaskedTokenResponse(cleanToken))
}

func DeleteTokenBatch(c fuego.ContextWithBody[dto.TokenBatch]) (*dto.Response[int], error) {
	tokenBatch, err := c.Body()
	if err != nil || len(tokenBatch.Ids) == 0 {
		return dto.Fail[int]("Invalid parameters")
	}
	count, err := model.BatchDeleteTokens(tokenBatch.Ids, dto.UserID(c))
	if err != nil {
		return dto.Fail[int](err.Error())
	}
	recordUserSecurityAudit(dto.GinCtx(c), dto.UserID(c), "token.delete_batch", map[string]interface{}{
		"count": count,
		"ids":   tokenBatch.Ids,
	})
	return dto.Ok(count)
}

func GetTokenKeysBatch(c *gin.Context) {
	tokenBatch := dto.TokenBatch{}
	if err := c.ShouldBindJSON(&tokenBatch); err != nil || len(tokenBatch.Ids) == 0 {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	if len(tokenBatch.Ids) > 100 {
		common.ApiErrorI18n(c, i18n.MsgBatchTooMany, map[string]any{"Max": 100})
		return
	}
	userId := c.GetInt("id")
	tokens, err := model.GetTokenKeysByIds(tokenBatch.Ids, userId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	keysMap := make(map[int]string)
	revealed := make([]int, 0, len(tokens))
	for _, t := range tokens {
		keysMap[t.Id] = t.GetFullKey()
		revealed = append(revealed, t.Id)
	}
	// Bulk reveal is the cheaper path to every key an account owns, so it needs
	// the same trail as the single-token route.
	recordUserSecurityAudit(c, userId, "token.key_view_batch", map[string]interface{}{
		"count": len(revealed),
		"ids":   revealed,
	})
	common.ApiSuccess(c, gin.H{"keys": keysMap})
}
