package controller

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/go-fuego/fuego"
	"gorm.io/gorm"
)

// ---- Local types that reference model (cannot live in dto due to import cycle) ----

type SubscriptionPlanDTO struct {
	Plan model.SubscriptionPlan `json:"plan"`
}

type AdminUpsertSubscriptionPlanRequest struct {
	Plan model.SubscriptionPlan `json:"plan"`
}

type SubscriptionSelfData struct {
	BillingPreference string                      `json:"billing_preference"`
	Subscriptions     []model.SubscriptionSummary `json:"subscriptions"`
	AllSubscriptions  []model.SubscriptionSummary `json:"all_subscriptions"`
}

// ---- User APIs ----

func GetSubscriptionPlans(c fuego.ContextNoBody) (*dto.Response[[]SubscriptionPlanDTO], error) {
	var plans []model.SubscriptionPlan
	if err := model.DB.Where("enabled = ?", true).Order("sort_order desc, id desc").Find(&plans).Error; err != nil {
		return dto.Fail[[]SubscriptionPlanDTO](err.Error())
	}
	result := make([]SubscriptionPlanDTO, 0, len(plans))
	for _, p := range plans {
		result = append(result, SubscriptionPlanDTO{
			Plan: p,
		})
	}
	return dto.Ok(result)
}

func GetSubscriptionSelf(c fuego.ContextNoBody) (*dto.Response[SubscriptionSelfData], error) {
	userId := dto.UserID(c)
	settingMap, _ := model.GetUserSetting(userId, false)
	pref := common.NormalizeBillingPreference(settingMap.BillingPreference)

	// Get all subscriptions (including expired)
	allSubscriptions, err := model.GetAllUserSubscriptions(userId)
	if err != nil {
		allSubscriptions = []model.SubscriptionSummary{}
	}

	// Get active subscriptions for backward compatibility
	activeSubscriptions, err := model.GetAllActiveUserSubscriptions(userId)
	if err != nil {
		activeSubscriptions = []model.SubscriptionSummary{}
	}

	return dto.Ok(SubscriptionSelfData{
		BillingPreference: pref,
		Subscriptions:     activeSubscriptions, // all active subscriptions
		AllSubscriptions:  allSubscriptions,    // all subscriptions including expired
	})
}

func UpdateSubscriptionPreference(c fuego.ContextWithBody[dto.BillingPreferenceRequest]) (*dto.Response[dto.BillingPreferenceData], error) {
	userId := dto.UserID(c)
	req, err := c.Body()
	if err != nil {
		return dto.Fail[dto.BillingPreferenceData](common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}
	pref := common.NormalizeBillingPreference(req.BillingPreference)

	user, err := model.GetUserById(userId, true)
	if err != nil {
		return dto.Fail[dto.BillingPreferenceData](err.Error())
	}
	current := user.GetSetting()
	current.BillingPreference = pref
	user.SetSetting(current)
	if err := user.Update(false); err != nil {
		return dto.Fail[dto.BillingPreferenceData](err.Error())
	}
	return dto.Ok(dto.BillingPreferenceData{BillingPreference: pref})
}

// ---- Admin APIs ----

func AdminListSubscriptionPlans(c fuego.ContextNoBody) (*dto.Response[[]SubscriptionPlanDTO], error) {
	var plans []model.SubscriptionPlan
	if err := model.DB.Order("sort_order desc, id desc").Find(&plans).Error; err != nil {
		return dto.Fail[[]SubscriptionPlanDTO](err.Error())
	}
	result := make([]SubscriptionPlanDTO, 0, len(plans))
	for _, p := range plans {
		result = append(result, SubscriptionPlanDTO{
			Plan: p,
		})
	}
	return dto.Ok(result)
}

func AdminCreateSubscriptionPlan(c fuego.ContextWithBody[AdminUpsertSubscriptionPlanRequest]) (*dto.Response[model.SubscriptionPlan], error) {
	req, err := c.Body()
	if err != nil {
		return dto.Fail[model.SubscriptionPlan](common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}
	req.Plan.Id = 0
	if strings.TrimSpace(req.Plan.Title) == "" {
		return dto.Fail[model.SubscriptionPlan](common.TranslateMessage(dto.GinCtx(c), "subscription.title_empty"))
	}
	if req.Plan.PriceAmount < 0 {
		return dto.Fail[model.SubscriptionPlan](common.TranslateMessage(dto.GinCtx(c), "subscription.price_negative"))
	}
	if req.Plan.PriceAmount > 9999 {
		return dto.Fail[model.SubscriptionPlan](common.TranslateMessage(dto.GinCtx(c), "subscription.price_max"))
	}
	if req.Plan.Currency == "" {
		req.Plan.Currency = "USD"
	}
	req.Plan.Currency = "USD"
	if req.Plan.DurationUnit == "" {
		req.Plan.DurationUnit = model.SubscriptionDurationMonth
	}
	if req.Plan.DurationValue <= 0 && req.Plan.DurationUnit != model.SubscriptionDurationCustom {
		req.Plan.DurationValue = 1
	}
	if req.Plan.MaxPurchasePerUser < 0 {
		return dto.Fail[model.SubscriptionPlan](common.TranslateMessage(dto.GinCtx(c), "subscription.purchase_limit_negative"))
	}
	if req.Plan.TotalAmount < 0 {
		return dto.Fail[model.SubscriptionPlan](common.TranslateMessage(dto.GinCtx(c), "subscription.quota_negative"))
	}
	req.Plan.UpgradeGroup = strings.TrimSpace(req.Plan.UpgradeGroup)
	if req.Plan.UpgradeGroup != "" {
		if _, ok := ratio_setting.GetGroupRatioCopy()[req.Plan.UpgradeGroup]; !ok {
			return dto.Fail[model.SubscriptionPlan](common.TranslateMessage(dto.GinCtx(c), "subscription.group_not_exists"))
		}
	}
	req.Plan.QuotaResetPeriod = model.NormalizeResetPeriod(req.Plan.QuotaResetPeriod)
	if req.Plan.QuotaResetPeriod == model.SubscriptionResetCustom && req.Plan.QuotaResetCustomSeconds <= 0 {
		return dto.Fail[model.SubscriptionPlan](common.TranslateMessage(dto.GinCtx(c), "subscription.reset_cycle_gt_zero"))
	}
	if err := model.DB.Create(&req.Plan).Error; err != nil {
		return dto.Fail[model.SubscriptionPlan](err.Error())
	}
	model.InvalidateSubscriptionPlanCache(req.Plan.Id)
	return dto.Ok(req.Plan)
}

func AdminUpdateSubscriptionPlan(c fuego.ContextWithBody[AdminUpsertSubscriptionPlanRequest]) (dto.MessageResponse, error) {
	id, err := c.PathParamIntErr("id")
	if err != nil || id <= 0 {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "common.invalid_id"))
	}
	req, err := c.Body()
	if err != nil {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}
	if strings.TrimSpace(req.Plan.Title) == "" {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "subscription.title_empty"))
	}
	if req.Plan.PriceAmount < 0 {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "subscription.price_negative"))
	}
	if req.Plan.PriceAmount > 9999 {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "subscription.price_max"))
	}
	req.Plan.Id = id
	if req.Plan.Currency == "" {
		req.Plan.Currency = "USD"
	}
	req.Plan.Currency = "USD"
	if req.Plan.DurationUnit == "" {
		req.Plan.DurationUnit = model.SubscriptionDurationMonth
	}
	if req.Plan.DurationValue <= 0 && req.Plan.DurationUnit != model.SubscriptionDurationCustom {
		req.Plan.DurationValue = 1
	}
	if req.Plan.MaxPurchasePerUser < 0 {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "subscription.purchase_limit_negative"))
	}
	if req.Plan.TotalAmount < 0 {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "subscription.quota_negative"))
	}
	req.Plan.UpgradeGroup = strings.TrimSpace(req.Plan.UpgradeGroup)
	if req.Plan.UpgradeGroup != "" {
		if _, ok := ratio_setting.GetGroupRatioCopy()[req.Plan.UpgradeGroup]; !ok {
			return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "subscription.group_not_exists"))
		}
	}
	req.Plan.QuotaResetPeriod = model.NormalizeResetPeriod(req.Plan.QuotaResetPeriod)
	if req.Plan.QuotaResetPeriod == model.SubscriptionResetCustom && req.Plan.QuotaResetCustomSeconds <= 0 {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "subscription.reset_cycle_gt_zero"))
	}

	txErr := model.DB.Transaction(func(tx *gorm.DB) error {
		// update plan (allow zero values updates with map)
		updateMap := map[string]interface{}{
			"title":                      req.Plan.Title,
			"subtitle":                   req.Plan.Subtitle,
			"price_amount":               req.Plan.PriceAmount,
			"currency":                   req.Plan.Currency,
			"duration_unit":              req.Plan.DurationUnit,
			"duration_value":             req.Plan.DurationValue,
			"custom_seconds":             req.Plan.CustomSeconds,
			"enabled":                    req.Plan.Enabled,
			"sort_order":                 req.Plan.SortOrder,
			"stripe_price_id":            req.Plan.StripePriceId,
			"creem_product_id":           req.Plan.CreemProductId,
			"max_purchase_per_user":      req.Plan.MaxPurchasePerUser,
			"total_amount":               req.Plan.TotalAmount,
			"upgrade_group":              req.Plan.UpgradeGroup,
			"quota_reset_period":         req.Plan.QuotaResetPeriod,
			"quota_reset_custom_seconds": req.Plan.QuotaResetCustomSeconds,
			"updated_at":                 common.GetTimestamp(),
		}
		if err := tx.Model(&model.SubscriptionPlan{}).Where("id = ?", id).Updates(updateMap).Error; err != nil {
			return err
		}
		return nil
	})
	if txErr != nil {
		return dto.FailMsg(txErr.Error())
	}
	model.InvalidateSubscriptionPlanCache(id)
	return dto.Msg("")
}

func AdminUpdateSubscriptionPlanStatus(c fuego.ContextWithBody[dto.AdminUpdateSubscriptionPlanStatusRequest]) (dto.MessageResponse, error) {
	id, err := c.PathParamIntErr("id")
	if err != nil || id <= 0 {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "common.invalid_id"))
	}
	req, err := c.Body()
	if err != nil || req.Enabled == nil {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}
	if err := model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", id).Update("enabled", *req.Enabled).Error; err != nil {
		return dto.FailMsg(err.Error())
	}
	model.InvalidateSubscriptionPlanCache(id)
	return dto.Msg("")
}

func AdminBindSubscription(c fuego.ContextWithBody[dto.AdminBindSubscriptionRequest]) (dto.MessageResponse, error) {
	req, err := c.Body()
	if err != nil || req.UserId <= 0 || req.PlanId <= 0 {
		return dto.FailMsg(common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}
	msg, err := model.AdminBindSubscription(req.UserId, req.PlanId, "")
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	return dto.Msg(msg)
}

// ---- Admin: user subscription management ----

func AdminListUserSubscriptions(c fuego.ContextNoBody) (*dto.Response[[]model.SubscriptionSummary], error) {
	userId, err := c.PathParamIntErr("id")
	if err != nil || userId <= 0 {
		return dto.Fail[[]model.SubscriptionSummary](common.TranslateMessage(dto.GinCtx(c), "subscription.invalid_user_id"))
	}
	subs, err := model.GetAllUserSubscriptions(userId)
	if err != nil {
		return dto.Fail[[]model.SubscriptionSummary](err.Error())
	}
	return dto.Ok(subs)
}

// AdminCreateUserSubscription creates a new user subscription from a plan (no payment).
func AdminCreateUserSubscription(c fuego.ContextWithBody[dto.AdminCreateUserSubscriptionRequest]) (*dto.Response[dto.SubscriptionActionData], error) {
	userId, err := c.PathParamIntErr("id")
	if err != nil || userId <= 0 {
		return dto.Fail[dto.SubscriptionActionData](common.TranslateMessage(dto.GinCtx(c), "subscription.invalid_user_id"))
	}
	req, err := c.Body()
	if err != nil || req.PlanId <= 0 {
		return dto.Fail[dto.SubscriptionActionData](common.TranslateMessage(dto.GinCtx(c), "common.invalid_params"))
	}
	msg, err := model.AdminBindSubscription(userId, req.PlanId, "")
	if err != nil {
		return dto.Fail[dto.SubscriptionActionData](err.Error())
	}
	return dto.Ok(dto.SubscriptionActionData{Message: msg})
}

// AdminInvalidateUserSubscription cancels a user subscription immediately.
func AdminInvalidateUserSubscription(c fuego.ContextNoBody) (*dto.Response[dto.SubscriptionActionData], error) {
	subId, err := c.PathParamIntErr("id")
	if err != nil || subId <= 0 {
		return dto.Fail[dto.SubscriptionActionData](common.TranslateMessage(dto.GinCtx(c), "subscription.invalid_id"))
	}
	msg, err := model.AdminInvalidateUserSubscription(subId)
	if err != nil {
		return dto.Fail[dto.SubscriptionActionData](err.Error())
	}
	return dto.Ok(dto.SubscriptionActionData{Message: msg})
}

// AdminDeleteUserSubscription hard-deletes a user subscription.
func AdminDeleteUserSubscription(c fuego.ContextNoBody) (*dto.Response[dto.SubscriptionActionData], error) {
	subId, err := c.PathParamIntErr("id")
	if err != nil || subId <= 0 {
		return dto.Fail[dto.SubscriptionActionData](common.TranslateMessage(dto.GinCtx(c), "subscription.invalid_id"))
	}
	msg, err := model.AdminDeleteUserSubscription(subId)
	if err != nil {
		return dto.Fail[dto.SubscriptionActionData](err.Error())
	}
	return dto.Ok(dto.SubscriptionActionData{Message: msg})
}
