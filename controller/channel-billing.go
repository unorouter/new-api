package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/advancedcustom"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	relaydto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/go-fuego/fuego"
	"github.com/shopspring/decimal"
)

// https://github.com/songquanpeng/one-api/issues/79

const maxAdvancedCustomBalanceResponseBytes = 256 << 10

type channelBalanceResult struct {
	Balance     float64
	RawResponse string
}

// GetAuthHeader get auth header
func GetAuthHeader(token string) http.Header {
	h := http.Header{}
	h.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	return h
}

// GetClaudeAuthHeader get claude auth header
func GetClaudeAuthHeader(token string) http.Header {
	h := http.Header{}
	h.Add("x-api-key", token)
	h.Add("anthropic-version", "2023-06-01")
	return h
}

func GetResponseBody(method, url string, channel *model.Channel, headers http.Header) ([]byte, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	for k := range headers {
		req.Header.Add(k, headers.Get(k))
	}
	setting := channel.GetSetting()
	client, err := service.GetHttpClientWithProxySettings(setting.Proxy, setting)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status code: %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	err = res.Body.Close()
	if err != nil {
		return nil, err
	}
	return body, nil
}

func updateChannelCloseAIBalance(channel *model.Channel) (float64, error) {
	url := fmt.Sprintf("%s/dashboard/billing/credit_grants", channel.GetBaseURL())
	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))

	if err != nil {
		return 0, err
	}
	response := dto.OpenAICreditGrants{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	channel.UpdateBalance(response.TotalAvailable)
	return response.TotalAvailable, nil
}

func updateChannelOpenAISBBalance(channel *model.Channel) (float64, error) {
	url := fmt.Sprintf("https://api.openai-sb.com/sb-api/user/status?api_key=%s", channel.Key)
	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := dto.OpenAISBUsageResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	if response.Data == nil {
		return 0, errors.New(response.Msg)
	}
	balance, err := strconv.ParseFloat(response.Data.Credit, 64)
	if err != nil {
		return 0, err
	}
	channel.UpdateBalance(balance)
	return balance, nil
}

func updateChannelAIProxyBalance(channel *model.Channel) (float64, error) {
	url := "https://aiproxy.io/api/report/getUserOverview"
	headers := http.Header{}
	headers.Add("Api-Key", channel.Key)
	body, err := GetResponseBody("GET", url, channel, headers)
	if err != nil {
		return 0, err
	}
	response := dto.AIProxyUserOverviewResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	if !response.Success {
		return 0, fmt.Errorf("code: %d, message: %s", response.ErrorCode, response.Message)
	}
	channel.UpdateBalance(response.Data.TotalPoints)
	return response.Data.TotalPoints, nil
}

func updateChannelAPI2GPTBalance(channel *model.Channel) (float64, error) {
	url := "https://api.api2gpt.com/dashboard/billing/credit_grants"
	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))

	if err != nil {
		return 0, err
	}
	response := dto.API2GPTUsageResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	channel.UpdateBalance(response.TotalRemaining)
	return response.TotalRemaining, nil
}

func updateChannelSiliconFlowBalance(channel *model.Channel) (float64, error) {
	url := "https://api.siliconflow.cn/v1/user/info"
	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := dto.SiliconFlowUsageResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	if response.Code != 20000 {
		return 0, fmt.Errorf("code: %d, message: %s", response.Code, response.Message)
	}
	balance, err := strconv.ParseFloat(response.Data.TotalBalance, 64)
	if err != nil {
		return 0, err
	}
	channel.UpdateBalance(balance)
	return balance, nil
}

func getDeepSeekBalanceUSD(response dto.DeepSeekUsageResponse, usdExchangeRate float64) (float64, error) {
	var usdBalance *string
	var cnyBalance *string

	for i := range response.BalanceInfos {
		balanceInfo := &response.BalanceInfos[i]
		switch balanceInfo.Currency {
		case "USD":
			if usdBalance == nil {
				usdBalance = &balanceInfo.TotalBalance
			}
		case "CNY":
			if cnyBalance == nil {
				cnyBalance = &balanceInfo.TotalBalance
			}
		}
	}

	if usdBalance != nil {
		balance, err := strconv.ParseFloat(*usdBalance, 64)
		if err != nil {
			return 0, err
		}
		if math.IsNaN(balance) || math.IsInf(balance, 0) {
			return 0, errors.New("USD balance must be finite")
		}
		if balance < 0 {
			return 0, errors.New("USD balance must be non-negative")
		}
		if balance > 0 {
			return balance, nil
		}
	}
	if cnyBalance == nil {
		// An empty USD wallet is a valid balance. Only report a missing
		// currency when the response carried neither USD nor CNY.
		if usdBalance != nil {
			return 0, nil
		}
		return 0, errors.New("currency USD or CNY not found")
	}
	if math.IsNaN(usdExchangeRate) || math.IsInf(usdExchangeRate, 0) {
		return 0, errors.New("USD exchange rate must be finite")
	}
	if usdExchangeRate <= 0 {
		return 0, errors.New("USD exchange rate must be greater than zero")
	}

	balanceCNY, err := strconv.ParseFloat(*cnyBalance, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(balanceCNY) || math.IsInf(balanceCNY, 0) {
		return 0, errors.New("CNY balance must be finite")
	}
	if balanceCNY < 0 {
		return 0, errors.New("CNY balance must be non-negative")
	}
	balanceUSD := decimal.NewFromFloat(balanceCNY).Div(decimal.NewFromFloat(usdExchangeRate)).InexactFloat64()
	if math.IsNaN(balanceUSD) || math.IsInf(balanceUSD, 0) {
		return 0, errors.New("converted USD balance must be finite")
	}
	if balanceUSD < 0 {
		return 0, errors.New("converted USD balance must be non-negative")
	}
	return balanceUSD, nil
}

func updateChannelDeepSeekBalance(channel *model.Channel) (float64, error) {
	url := "https://api.deepseek.com/user/balance"
	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := dto.DeepSeekUsageResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	balance, err := getDeepSeekBalanceUSD(response, operation_setting.USDExchangeRate)
	if err != nil {
		return 0, err
	}
	channel.UpdateBalance(balance)
	return balance, nil
}

func updateChannelAIGC2DBalance(channel *model.Channel) (float64, error) {
	url := "https://api.aigc2d.com/dashboard/billing/credit_grants"
	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := dto.APGC2DGPTUsageResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	channel.UpdateBalance(response.TotalAvailable)
	return response.TotalAvailable, nil
}

func updateChannelOpenRouterBalance(channel *model.Channel) (float64, error) {
	url := "https://openrouter.ai/api/v1/credits"
	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := dto.OpenRouterCreditResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	balance := response.Data.TotalCredits - response.Data.TotalUsage
	channel.UpdateBalance(balance)
	return balance, nil
}

func updateChannelMoonshotBalance(channel *model.Channel) (float64, error) {
	url := "https://api.moonshot.cn/v1/users/me/balance"
	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}

	type MoonshotBalanceData struct {
		AvailableBalance float64 `json:"available_balance"`
		VoucherBalance   float64 `json:"voucher_balance"`
		CashBalance      float64 `json:"cash_balance"`
	}

	type MoonshotBalanceResponse struct {
		Code   int                 `json:"code"`
		Data   MoonshotBalanceData `json:"data"`
		Scode  string              `json:"scode"`
		Status bool                `json:"status"`
	}

	response := MoonshotBalanceResponse{}
	err = common.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	if !response.Status || response.Code != 0 {
		return 0, fmt.Errorf("failed to update moonshot balance, status: %v, code: %d, scode: %s", response.Status, response.Code, response.Scode)
	}
	availableBalanceCny := response.Data.AvailableBalance
	availableBalanceUsd := decimal.NewFromFloat(availableBalanceCny).Div(decimal.NewFromFloat(operation_setting.Price)).InexactFloat64()
	channel.UpdateBalance(availableBalanceUsd)
	return availableBalanceUsd, nil
}

func updateChannelSub2APIBalance(channel *model.Channel) (float64, error) {
	baseURL := channel.GetBaseURL()
	if baseURL == "" {
		return 0, errors.New("Sub2API channel is missing base_url")
	}
	url := fmt.Sprintf("%s/v1/usage", strings.TrimRight(baseURL, "/"))
	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}

	type sub2apiUsageResponse struct {
		Balance      *float64 `json:"balance"`
		Remaining    *float64 `json:"remaining"`
		IsValid      *bool    `json:"isValid"`
		Subscription *struct {
			DailyLimitUSD   *float64 `json:"daily_limit_usd"`
			WeeklyLimitUSD  *float64 `json:"weekly_limit_usd"`
			MonthlyLimitUSD *float64 `json:"monthly_limit_usd"`
		} `json:"subscription"`
	}
	usage := sub2apiUsageResponse{}
	if err := common.Unmarshal(body, &usage); err != nil {
		return 0, err
	}
	if usage.IsValid != nil && !*usage.IsValid {
		return 0, errors.New("Sub2API key is invalid (isValid=false)")
	}

	balance := usage.Remaining
	if balance == nil {
		balance = usage.Balance
	}
	if balance == nil {
		return 0, errors.New("Sub2API /v1/usage response is missing remaining and balance fields (the key may belong to a subscription group without an available balance)")
	}
	// Sub2API reports remaining=-1 for a subscription group with no daily, weekly,
	// or monthly limit. A wallet balance can legitimately be negative, so detect
	// the unlimited case from the subscription limits rather than the value.
	subscription := usage.Subscription
	if subscription != nil && subscription.DailyLimitUSD == nil && subscription.WeeklyLimitUSD == nil && subscription.MonthlyLimitUSD == nil {
		return 0, errors.New("Sub2API /v1/usage reports an unlimited subscription without a finite balance")
	}
	channel.UpdateBalance(*balance)
	return *balance, nil
}

// updateChannelNewAPIBalance reads the upstream key's remaining quota. When the
// upstream also returns its owner's quota, the smaller of the two is what the
// key can still spend. Quota converts to USD with the upstream's quota_per_unit.
func updateChannelNewAPIBalance(channel *model.Channel) (float64, error) {
	baseURL := strings.TrimRight(channel.GetBaseURL(), "/")
	if baseURL == "" {
		return 0, errors.New("New API channel is missing base_url")
	}
	body, err := GetResponseBody("GET", baseURL+"/api/usage/token/", channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	var usage struct {
		Code    bool   `json:"code"`
		Message string `json:"message"`
		Data    struct {
			TotalAvailable float64 `json:"total_available"`
			UnlimitedQuota bool    `json:"unlimited_quota"`
			UserQuota      *struct {
				TotalAvailable float64 `json:"total_available"`
			} `json:"user_quota"`
		} `json:"data"`
	}
	if err := common.Unmarshal(body, &usage); err != nil {
		return 0, err
	}
	if !usage.Code {
		return 0, fmt.Errorf("New API usage query failed: %s", usage.Message)
	}
	remaining := usage.Data.TotalAvailable
	if usage.Data.UnlimitedQuota {
		remaining = math.Inf(1)
	}
	if userQuota := usage.Data.UserQuota; userQuota != nil {
		remaining = min(remaining, userQuota.TotalAvailable)
	}
	if math.IsInf(remaining, 1) {
		return 0, common.NewMessage(`The upstream key has unlimited quota. Turn on "Allow querying user quota" for this key on the upstream to see the real balance.`)
	}

	body, err = GetResponseBody("GET", baseURL+"/api/status", channel, nil)
	if err != nil {
		return 0, err
	}
	var status struct {
		Data struct {
			QuotaPerUnit float64 `json:"quota_per_unit"`
		} `json:"data"`
	}
	if err := common.Unmarshal(body, &status); err != nil {
		return 0, err
	}
	if status.Data.QuotaPerUnit <= 0 {
		return 0, errors.New("New API /api/status response is missing quota_per_unit")
	}
	balance := remaining / status.Data.QuotaPerUnit
	channel.UpdateBalance(balance)
	return balance, nil
}

func fetchAdvancedCustomBalance(channel *model.Channel) (channelBalanceResult, error) {
	key := strings.TrimSpace(channel.Key)
	info := &relaycommon.RelayInfo{
		RelayFormat:    types.RelayFormatOpenAI,
		RelayMode:      relayconstant.RelayModeUnknown,
		RequestURLPath: relaydto.AdvancedCustomBalancePath,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:          constant.ChannelTypeAdvancedCustom,
			ChannelBaseUrl:       channel.GetBaseURL(),
			ApiKey:               key,
			ChannelOtherSettings: channel.GetOtherSettings(),
		},
	}
	requestURL, headers, err := (&advancedcustom.Adaptor{}).BuildBalanceRequest(info)
	if err != nil {
		return channelBalanceResult{}, sanitizeFetchModelsError(err, key)
	}
	if err := applyFetchModelsHeaderOverrides(channel, key, headers); err != nil {
		return channelBalanceResult{}, sanitizeFetchModelsError(err, key)
	}

	request, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return channelBalanceResult{}, sanitizeFetchModelsError(err, key)
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
		if strings.EqualFold(name, "Host") {
			request.Host = headers.Get(name)
		}
	}
	setting := channel.GetSetting()
	client, err := service.GetHttpClientWithProxySettings(setting.Proxy, setting)
	if err != nil {
		return channelBalanceResult{}, sanitizeFetchModelsError(err, key)
	}
	response, err := client.Do(request)
	if err != nil {
		return channelBalanceResult{}, sanitizeAdvancedCustomRequestError(err, key, requestURL)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return channelBalanceResult{}, fmt.Errorf("status code: %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAdvancedCustomBalanceResponseBytes+1))
	if err != nil {
		return channelBalanceResult{}, sanitizeAdvancedCustomRequestError(err, key, requestURL)
	}
	if len(body) > maxAdvancedCustomBalanceResponseBytes {
		return channelBalanceResult{}, fmt.Errorf("balance response exceeds %d bytes", maxAdvancedCustomBalanceResponseBytes)
	}

	var validated json.RawMessage
	if err := common.Unmarshal(body, &validated); err != nil {
		return channelBalanceResult{}, fmt.Errorf("invalid balance JSON response: %w", err)
	}
	if common.GetJsonType(validated) == "object" {
		var creditSummary struct {
			Object         string          `json:"object"`
			TotalAvailable json.RawMessage `json:"total_available"`
		}
		if err := common.Unmarshal(body, &creditSummary); err != nil {
			return channelBalanceResult{}, fmt.Errorf("invalid balance JSON response: %w", err)
		}
		if creditSummary.Object == "credit_summary" &&
			common.GetJsonType(creditSummary.TotalAvailable) == "number" {
			var balance float64
			if err := common.Unmarshal(creditSummary.TotalAvailable, &balance); err == nil &&
				balance >= 0 &&
				!math.IsNaN(balance) &&
				!math.IsInf(balance, 0) {
				channel.UpdateBalance(balance)
				return channelBalanceResult{Balance: balance}, nil
			}
		}
	}

	formatted, err := common.IndentJson(body)
	if err != nil {
		return channelBalanceResult{}, fmt.Errorf("invalid balance JSON response: %w", err)
	}
	return channelBalanceResult{RawResponse: string(formatted)}, nil
}

func updateChannelBalance(channel *model.Channel) (channelBalanceResult, error) {
	if channel.Type == constant.ChannelTypeAdvancedCustom {
		return fetchAdvancedCustomBalance(channel)
	}
	balance, err := updateStandardChannelBalance(channel)
	return channelBalanceResult{Balance: balance}, err
}

func updateStandardChannelBalance(channel *model.Channel) (float64, error) {
	baseURL := constant.GetChannelBaseURL(channel.Type)
	if channel.GetBaseURL() == "" {
		channel.BaseURL = &baseURL
	}
	switch channel.Type {
	case constant.ChannelTypeOpenAI:
		if channel.GetBaseURL() != "" {
			baseURL = channel.GetBaseURL()
		}
	case constant.ChannelTypeAzure:
		return 0, errors.New("not yet implemented")
	case constant.ChannelTypeCustom:
		baseURL = channel.GetBaseURL()
	//case common.ChannelTypeOpenAISB:
	//	return updateChannelOpenAISBBalance(channel)
	case constant.ChannelTypeAIProxy:
		return updateChannelAIProxyBalance(channel)
	case constant.ChannelTypeAPI2GPT:
		return updateChannelAPI2GPTBalance(channel)
	case constant.ChannelTypeAIGC2D:
		return updateChannelAIGC2DBalance(channel)
	case constant.ChannelTypeSiliconFlow:
		return updateChannelSiliconFlowBalance(channel)
	case constant.ChannelTypeDeepSeek:
		return updateChannelDeepSeekBalance(channel)
	case constant.ChannelTypeOpenRouter:
		return updateChannelOpenRouterBalance(channel)
	case constant.ChannelTypeMoonshot:
		return updateChannelMoonshotBalance(channel)
	case constant.ChannelTypeSub2API:
		return updateChannelSub2APIBalance(channel)
	case constant.ChannelTypeNewAPI:
		return updateChannelNewAPIBalance(channel)
	default:
		return 0, errors.New("not yet implemented")
	}
	url := fmt.Sprintf("%s/v1/dashboard/billing/subscription", baseURL)

	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	subscription := dto.OpenAISubscriptionResponse{}
	err = common.Unmarshal(body, &subscription)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	startDate := fmt.Sprintf("%s-01", now.Format("2006-01"))
	endDate := now.Format("2006-01-02")
	if !subscription.HasPaymentMethod {
		startDate = now.AddDate(0, 0, -100).Format("2006-01-02")
	}
	url = fmt.Sprintf("%s/v1/dashboard/billing/usage?start_date=%s&end_date=%s", baseURL, startDate, endDate)
	body, err = GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	usage := dto.OpenAIUsageResponse{}
	err = common.Unmarshal(body, &usage)
	if err != nil {
		return 0, err
	}
	balance := subscription.HardLimitUSD - usage.TotalUsage/100
	channel.UpdateBalance(balance)
	return balance, nil
}

func UpdateChannelBalance(c fuego.ContextNoBody) (dto.ChannelBalanceResponse, error) {
	id, err := c.PathParamIntErr("id")
	if err != nil {
		return dto.ChannelBalanceResponse{Success: false, Message: err.Error()}, nil
	}
	channel, err := model.CacheGetChannel(id)
	if err != nil {
		return dto.ChannelBalanceResponse{Success: false, Message: err.Error()}, nil
	}
	if channel.Type == constant.ChannelTypeTaskPlugin {
		return dto.ChannelBalanceResponse{Success: false, Message: "Task Plugin channels do not support balance queries"}, nil
	}
	if channel.ChannelInfo.IsMultiKey {
		return dto.ChannelBalanceResponse{Success: false, Message: "Operation not supported"}, nil
	}
	result, err := updateChannelBalance(channel)
	if err != nil {
		return dto.ChannelBalanceResponse{Success: false, Message: err.Error()}, nil
	}
	if result.RawResponse != "" {
		return dto.ChannelBalanceResponse{Success: true, Message: "", RawResponse: result.RawResponse}, nil
	}
	return dto.ChannelBalanceResponse{Success: true, Message: "", Balance: result.Balance}, nil
}

func updateAllChannelsBalance() error {
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		return err
	}
	for _, channel := range channels {
		if channel.Status != common.ChannelStatusEnabled {
			continue
		}
		if channel.ChannelInfo.IsMultiKey {
			continue // skip multi-key channels
		}
		// TODO: support Azure
		//if channel.Type != common.ChannelTypeOpenAI && channel.Type != common.ChannelTypeCustom {
		//	continue
		//}
		result, err := updateChannelBalance(channel)
		if err != nil {
			continue
		} else if result.RawResponse == "" {
			// err is nil & balance <= 0 means quota is used up
			if result.Balance <= 0 {
				service.DisableChannel(*types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey, "", channel.GetAutoBan()), "Insufficient balance", model.WithChannelStatusTrigger(model.ChannelStatusTriggerBalance))
			}
		}
		time.Sleep(common.RequestInterval)
	}
	return nil
}

func UpdateAllChannelsBalance(c fuego.ContextNoBody) (dto.MessageResponse, error) {
	// TODO: make it async
	err := updateAllChannelsBalance()
	if err != nil {
		return dto.FailMsg(err.Error())
	}
	return dto.Msg("")
}

func AutomaticallyUpdateChannels(frequency int) {
	for {
		time.Sleep(time.Duration(frequency) * time.Minute)
		common.SysLog(common.LogText("updating all channels"))
		_ = updateAllChannelsBalance()
		common.SysLog(common.LogText("channels update done"))
	}
}
