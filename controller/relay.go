package controller

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/samber/lo"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func relayHandler(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	var err *types.NewAPIError
	switch info.RelayMode {
	case relayconstant.RelayModeImagesGenerations, relayconstant.RelayModeImagesEdits:
		err = relay.ImageHelper(c, info)
	case relayconstant.RelayModeAudioSpeech:
		fallthrough
	case relayconstant.RelayModeAudioTranslation:
		fallthrough
	case relayconstant.RelayModeAudioTranscription:
		err = relay.AudioHelper(c, info)
	case relayconstant.RelayModeRerank:
		err = relay.RerankHelper(c, info)
	case relayconstant.RelayModeEmbeddings:
		err = relay.EmbeddingHelper(c, info)
	case relayconstant.RelayModeResponses, relayconstant.RelayModeResponsesCompact:
		err = relay.ResponsesHelper(c, info)
	case relayconstant.RelayModeAlphaSearch:
		err = relay.AlphaSearchHelper(c, info)
	default:
		err = relay.TextHelper(c, info)
	}
	return err
}

// isModeratableRelayMode reports whether a relay mode generates content that
// Stripe strict-mode moderation should screen: text (chat/completions/responses)
// AND image generation/edits. Stripe prohibits adult/AI content across text and
// imagery, so both are gated. Audio/embeddings carry no such content. (Video task
// submission flows through RelayTask, gated separately there.)
func isModeratableRelayMode(mode int) bool {
	switch mode {
	case relayconstant.RelayModeChatCompletions,
		relayconstant.RelayModeCompletions,
		relayconstant.RelayModeResponses,
		relayconstant.RelayModeResponsesCompact,
		relayconstant.RelayModeImagesGenerations,
		relayconstant.RelayModeImagesEdits:
		return true
	default:
		return false
	}
}

// isMediaRelayMode reports whether a relay mode produces media (image/audio/video)
// rather than text. Catalog-probe scrapers hammer many distinct free MEDIA models
// that mostly fail; the distinct-failing-media abuse signal targets exactly these
// modes and leaves text (chat/completions/responses/embeddings) untouched.
func isMediaRelayMode(mode int) bool {
	switch mode {
	case relayconstant.RelayModeImagesGenerations,
		relayconstant.RelayModeImagesEdits,
		relayconstant.RelayModeAudioSpeech,
		relayconstant.RelayModeAudioTranscription,
		relayconstant.RelayModeAudioTranslation,
		relayconstant.RelayModeVideoSubmit,
		relayconstant.RelayModeSunoSubmit:
		return true
	default:
		return false
	}
}

// moderationGateError maps a moderation result to a client error and, on a
// denial, records a user-visible error log (LogTypeError) so the prompt's
// rejection - including the triggering category and score - shows up in the
// user's usage logs. Returns nil when modErr is nil (allowed).
func moderationGateError(c *gin.Context, relayInfo *relaycommon.RelayInfo, surface string, modErr error) *types.NewAPIError {
	if modErr == nil {
		return nil
	}
	if errors.Is(modErr, service.ErrPromptDenied) {
		reason := service.ModerationDenyReason(modErr)
		if reason == "" {
			reason = "Inappropriate prompt: blocked by content moderation. Reword the prompt and retry."
		}
		other := map[string]interface{}{
			"error_type":   "moderation_rejected",
			"surface":      surface,
			"request_path": c.Request.URL.Path,
		}
		if denyErr := new(service.ModerationDenyError); errors.As(modErr, &denyErr) {
			other["moderation_category"] = denyErr.Category
			other["moderation_score"] = denyErr.Score
			other["moderation_threshold"] = denyErr.Threshold
		}
		model.RecordErrorLog(c, relayInfo.UserId, c.GetInt("channel_id"),
			c.GetString("original_model"), c.GetString("token_name"), reason,
			c.GetInt("token_id"), 0, false, c.GetString("group"), other)
		return types.NewErrorWithStatusCode(errors.New(reason), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	return types.NewErrorWithStatusCode(modErr, types.ErrorCodeBadResponse, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
}

func geminiRelayHandler(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	var err *types.NewAPIError
	if strings.Contains(c.Request.URL.Path, "embed") {
		err = relay.GeminiEmbeddingHandler(c, info)
	} else {
		err = relay.GeminiHelper(c, info)
	}
	return err
}

func Relay(c *gin.Context, relayFormat types.RelayFormat) {

	requestId := c.GetString(common.RequestIdKey)
	//group := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	//originalModel := common.GetContextKeyString(c, constant.ContextKeyOriginalModel)

	var (
		newAPIError *types.NewAPIError
		ws          *websocket.Conn
	)

	if relayFormat == types.RelayFormatOpenAIRealtime {
		var err error
		ws, err = upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			helper.WssError(c, ws, types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry()).ToOpenAIError())
			return
		}
		defer ws.Close()
	}

	defer func() {
		if newAPIError != nil {
			logger.LogError(c, fmt.Sprintf("relay error: %s", common.LocalLogPreview(newAPIError.Error())))
			// Response already streamed to the client (e.g. an empty-stream fault
			// detected after the SSE + [DONE] were sent): the channel-disable side
			// effect already ran via processChannelError, but writing a JSON error
			// now would corrupt the committed stream. Skip the client write.
			if c.Writer.Written() {
				return
			}
			newAPIError.SetMessage(common.MessageWithRequestId(newAPIError.Error(), requestId))
			switch relayFormat {
			case types.RelayFormatOpenAIRealtime:
				helper.WssError(c, ws, newAPIError.ToOpenAIError())
			case types.RelayFormatClaude:
				c.JSON(newAPIError.StatusCode, gin.H{
					"type":  "error",
					"error": newAPIError.ToClaudeError(),
				})
			default:
				c.JSON(newAPIError.StatusCode, gin.H{
					"error": newAPIError.ToOpenAIError(),
				})
			}
		}
	}()

	request, err := helper.GetAndValidateRequest(c, relayFormat)
	if err != nil {
		// Map "request body too large" to 413 so clients can handle it correctly
		if common.IsRequestBodyTooLargeError(err) || errors.Is(err, common.ErrRequestBodyTooLarge) {
			newAPIError = types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry())
		} else {
			newAPIError = types.NewError(err, types.ErrorCodeInvalidRequest)
		}
		return
	}

	relayInfo, err := relaycommon.GenRelayInfo(c, relayFormat, request, ws)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeGenRelayInfoFailed)
		return
	}

	needSensitiveCheck := setting.ShouldCheckPromptSensitive()
	needCountToken := constant.CountToken
	// Stripe strict-mode moderation gates ALL generation (text + image + video) -
	// Stripe prohibits adult/AI content across "literature, pictures and other media",
	// so every generative surface is screened, not just text. Needs CombineText built.
	needStripeModeration := setting.StripeTextModerationEnabled && !service.ModerationExempt(c) && isModeratableRelayMode(relayInfo.RelayMode)
	// Avoid building huge CombineText (strings.Join) when token counting and sensitive check are both disabled.
	var meta *types.TokenCountMeta
	if needSensitiveCheck || needCountToken || needStripeModeration {
		meta = request.GetTokenCountMeta()
	} else {
		meta = fastTokenCountMetaForPricing(request)
	}

	if needSensitiveCheck && meta != nil {
		contains, words := service.CheckSensitiveText(meta.CombineText)
		if contains {
			logger.LogWarn(c, fmt.Sprintf("user sensitive words detected: %s", strings.Join(words, ", ")))
			newAPIError = types.NewError(err, types.ErrorCodeSensitiveWordsDetected)
			return
		}
	}

	// Image-generation moderation (merchant-of-record content-safety requirement).
	// Screens the image prompt through OpenAI omni-moderation before dispatch; fails
	// closed when enabled. Image prompt is meta.CombineText.
	if setting.CreemModerationEnabled && !service.ModerationExempt(c) &&
		(relayInfo.RelayMode == relayconstant.RelayModeImagesGenerations ||
			relayInfo.RelayMode == relayconstant.RelayModeImagesEdits) {
		moderationMeta := meta
		if moderationMeta == nil {
			moderationMeta = request.GetTokenCountMeta()
		}
		prompt := ""
		if moderationMeta != nil {
			prompt = moderationMeta.CombineText
		}
		if modErr := service.AssertPromptAllowed(c.Request.Context(), service.ModerationSurfaceImage, prompt); modErr != nil {
			newAPIError = moderationGateError(c, relayInfo, service.ModerationSurfaceImage, modErr)
			return
		}
	}

	// Stripe strict-mode moderation - gates text AND image generation (Stripe MoR
	// holds the merchant liable for AI outputs across text + imagery). Dynamic: only
	// when StripeTextModerationEnabled is on; applies to all traffic, free + paid.
	// meta.CombineText is the prompt. The surface picks the provider chain: image
	// modes use the media chain (OpenAI->Creem), text modes the text chain (OpenAI).
	// (Image is also covered by the image gate above; they never run simultaneously.)
	if needStripeModeration && meta != nil {
		surface := service.ModerationSurfaceText
		if relayInfo.RelayMode == relayconstant.RelayModeImagesGenerations ||
			relayInfo.RelayMode == relayconstant.RelayModeImagesEdits {
			surface = service.ModerationSurfaceImage
		}
		if modErr := service.AssertPromptAllowed(c.Request.Context(), surface, meta.CombineText); modErr != nil {
			newAPIError = moderationGateError(c, relayInfo, surface, modErr)
			return
		}
	}

	tokens, err := service.EstimateRequestToken(c, meta, relayInfo)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeCountTokenFailed)
		return
	}

	relayInfo.SetEstimatePromptTokens(tokens)

	priceData, err := helper.ModelPriceHelper(c, relayInfo, tokens, meta)
	if err != nil {
		newAPIError = types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusBadRequest))
		return
	}

	// common.SetContextKey(c, constant.ContextKeyTokenCountMeta, meta)

	if priceData.FreeModel {
		if relayInfo.UserSetting.BlockFreeWhenNoQuota && relayInfo.UserQuota <= 0 {
			// Shadow ban: return the same 429 rate-limit response a throttled free
			// user gets, so an abuser cannot tell they are specifically blocked.
			paidName := strings.TrimSuffix(relayInfo.OriginModelName, ":free")
			newAPIError = types.NewErrorWithStatusCode(
				fmt.Errorf("Too many requests. The free tier allows %d request(s) every %d min per account on %s - nothing is used up, retry in %ds. The paid %s has no per-minute limit.",
					1, 1, relayInfo.OriginModelName, 60, paidName),
				types.ErrorCodeRateLimitExceeded, http.StatusTooManyRequests,
				types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
			return
		}
		if relayInfo.UserId > 0 && !relayInfo.UserSetting.UnlimitedFreeModels {
			service.TrackFreeModelUsage(relayInfo.UserId, relayInfo.UserQuota, relayInfo.OriginModelName)
		}
		logger.LogInfo(c, fmt.Sprintf("model %s is free, skipping pre-consume billing", relayInfo.OriginModelName))
	} else {
		newAPIError = service.PreConsumeBilling(c, priceData.QuotaToPreConsume, relayInfo)
		if newAPIError != nil {
			return
		}
	}

	defer func() {
		// Only return quota if downstream failed and quota was actually pre-consumed
		if newAPIError != nil {
			newAPIError = service.NormalizeViolationFeeError(newAPIError)
			if relayInfo.Billing != nil && !shouldChargeOnError(newAPIError) {
				relayInfo.Billing.Refund(c)
			}
			service.ChargeViolationFeeIfNeeded(c, relayInfo, newAPIError)
			// Retry-spam signal: a free-model request that failed because the user
			// hammered a disabled/nonexistent model counts toward the hourly
			// free-error budget. A bot retries relentlessly; a human gives up.
			// Transient upstream infra faults (5xx/429/timeout) are OUR capacity
			// failing, not abuse, so they must NOT count: a heavy legit user hitting
			// an overloaded free model would otherwise be auto-blocked for our outage.
			if priceData.FreeModel && relayInfo.UserId > 0 &&
				!relayInfo.UserSetting.UnlimitedFreeModels &&
				!isTransientInfraError(newAPIError) {
				service.TrackFreeModelError(relayInfo.UserId, relayInfo.UserQuota,
					relayInfo.OriginModelName, isMediaRelayMode(relayInfo.RelayMode))
			}
		}
	}()

	retryParam := &service.RetryParam{
		Ctx:         c,
		TokenGroup:  relayInfo.TokenGroup,
		ModelName:   relayInfo.OriginModelName,
		RequestPath: c.Request.URL.Path,
		Retry:       common.GetPointer(0),
	}
	relayInfo.RetryIndex = 0
	relayInfo.LastError = nil

	// Channels already tried by this request. A retry that re-picks the channel that
	// just failed wastes the only failover attempt, since the disable it triggered is
	// dispatched asynchronously and has usually not landed in abilities yet.
	triedChannels := make(map[int]bool)
	// Counted here rather than read from retryParam: cross-group auto-retry resets
	// that counter on every group switch, so only a tally the switch cannot reach
	// bounds the whole chain.
	attempts := 0
	for ; retryParam.GetRetry() <= common.RetryTimes; retryParam.IncreaseRetry() {
		if attempts >= common.MaxTotalRelayAttempts {
			logger.LogError(c, fmt.Sprintf("relay attempt ceiling reached (%d attempts across groups), giving up", attempts))
			break
		}
		attempts++
		relayInfo.RetryIndex = retryParam.GetRetry()
		channel, channelErr := getChannel(c, relayInfo, retryParam, triedChannels)
		if channelErr != nil {
			logger.LogError(c, channelErr.Error())
			if relayInfo.LastError != nil {
				newAPIError = relayInfo.LastError
			} else {
				newAPIError = channelErr
			}
			break
		}

		// getChannel may switch auto-group to a paid group on retry (free channel
		// failed first). If the request was admitted free (no billing session) but
		// now resolves to a paid group, run pre-consume so the wallet/subscription
		// gate applies before hitting upstream. Without this a negative-balance user
		// rides free on the initial ratio-0 group and gets charged post-hoc.
		if relayInfo.Billing == nil {
			repriced, priceErr := helper.ModelPriceHelper(c, relayInfo, tokens, meta)
			if priceErr != nil {
				newAPIError = types.NewError(priceErr, types.ErrorCodeModelPriceError, types.ErrOptionWithStatusCode(http.StatusBadRequest))
				break
			}
			if !repriced.FreeModel {
				newAPIError = service.PreConsumeBilling(c, repriced.QuotaToPreConsume, relayInfo)
				if newAPIError != nil {
					break
				}
			}
		}

		addUsedChannel(c, channel.Id)
		if billingErr := service.PrepareTieredBillingForSelectedGroup(c, relayInfo); billingErr != nil {
			newAPIError = billingErr
			break
		}
		triedChannels[channel.Id] = true
		bodyStorage, bodyErr := common.GetBodyStorage(c)
		if bodyErr != nil {
			// Ensure consistent 413 for oversized bodies even when error occurs later (e.g., retry path)
			if common.IsRequestBodyTooLargeError(bodyErr) || errors.Is(bodyErr, common.ErrRequestBodyTooLarge) {
				newAPIError = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusRequestEntityTooLarge, types.ErrOptionWithSkipRetry())
			} else {
				newAPIError = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
			}
			break
		}
		c.Request.Body = io.NopCloser(bodyStorage)

		switch relayFormat {
		case types.RelayFormatOpenAIRealtime:
			newAPIError = relay.WssHelper(c, relayInfo)
		case types.RelayFormatClaude:
			newAPIError = relay.ClaudeHelper(c, relayInfo)
		case types.RelayFormatGemini:
			newAPIError = geminiRelayHandler(c, relayInfo)
		default:
			newAPIError = relayHandler(c, relayInfo)
		}

		if newAPIError == nil {
			relayInfo.LastError = nil
			// Denominator for every rate-based disable gate, so it must be recorded
			// regardless of which of those gates happens to be enabled.
			service.RecordChannelSuccess(relayInfo.ChannelId)
			return
		}

		newAPIError = service.NormalizeViolationFeeError(newAPIError)
		relayInfo.LastError = newAPIError

		processChannelError(c, *types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey, common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan()), newAPIError)

		if !shouldRetry(c, newAPIError, common.RetryTimes-retryParam.GetRetry()) {
			break
		}
	}

	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		retryLogStr := fmt.Sprintf("retry: %s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		logger.LogInfo(c, retryLogStr)
	}
	if newAPIError != nil {
		gopool.Go(func() {
			perfmetrics.RecordRelaySample(relayInfo, false, 0)
		})
	}
}

var upgrader = websocket.Upgrader{
	Subprotocols: []string{"realtime"}, // WS 握手支持的协议，如果有使用 Sec-WebSocket-Protocol，则必须在此声明对应的 Protocol TODO add other protocol
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许跨域
	},
}

func addUsedChannel(c *gin.Context, channelId int) {
	useChannel := c.GetStringSlice("use_channel")
	useChannel = append(useChannel, fmt.Sprintf("%d", channelId))
	c.Set("use_channel", useChannel)
}

func fastTokenCountMetaForPricing(request dto.Request) *types.TokenCountMeta {
	if request == nil {
		return &types.TokenCountMeta{}
	}
	meta := &types.TokenCountMeta{
		TokenType: types.TokenTypeTokenizer,
	}
	switch r := request.(type) {
	case *dto.GeneralOpenAIRequest:
		maxCompletionTokens := lo.FromPtrOr(r.MaxCompletionTokens, uint(0))
		maxTokens := lo.FromPtrOr(r.MaxTokens, uint(0))
		if maxCompletionTokens > maxTokens {
			meta.MaxTokens = int(maxCompletionTokens)
		} else {
			meta.MaxTokens = int(maxTokens)
		}
	case *dto.OpenAIResponsesRequest:
		meta.MaxTokens = int(lo.FromPtrOr(r.MaxOutputTokens, uint(0)))
	case *dto.ClaudeRequest:
		meta.MaxTokens = int(lo.FromPtr(r.MaxTokens))
	case *dto.ImageRequest:
		// Pricing for image requests depends on ImagePriceRatio; safe to compute even when CountToken is disabled.
		return r.GetTokenCountMeta()
	default:
		// Best-effort: leave CombineText empty to avoid large allocations.
	}
	return meta
}

func getChannel(c *gin.Context, info *relaycommon.RelayInfo, retryParam *service.RetryParam, skipChannels ...map[int]bool) (*model.Channel, *types.NewAPIError) {
	if info.ChannelMeta == nil {
		autoBan := c.GetBool("auto_ban")
		autoBanInt := 1
		if !autoBan {
			autoBanInt = 0
		}
		return &model.Channel{
			Id:      c.GetInt("channel_id"),
			Type:    c.GetInt("channel_type"),
			Name:    c.GetString("channel_name"),
			AutoBan: &autoBanInt,
		}, nil
	}
	channel, selectGroup, err := service.CacheGetRandomSatisfiedChannel(retryParam, skipChannels...)
	// Excluding the tried channels can empty the candidate set when the model has no
	// untried sibling left; retrying the same channel still beats failing outright.
	if (err != nil || channel == nil) && len(skipChannels) > 0 && len(skipChannels[0]) > 0 {
		channel, selectGroup, err = service.CacheGetRandomSatisfiedChannel(retryParam)
	}

	info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)

	if err != nil {
		return nil, types.NewError(fmt.Errorf("failed to get an available channel for model %s in group %s (retry): %s", info.OriginModelName, selectGroup, err.Error()), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}
	if channel == nil {
		// An auto token that exhausted EVERY group for this model: the free-first failover
		// pool is fully down/rate-limited for it (e.g. a model whose only free provider hit
		// its daily limit). Surface the friendly "model busy, try another" message instead of
		// a bare no-channel error.
		// A pinned token whose groups are all down gets a targeted error when the
		// model is still served elsewhere - the OVERRIDE is the lockout, not the
		// platform. Pins come in two shapes: a per-model GroupMapping (flag below)
		// and a plain token.group set to a specific group (never flagged, which
		// left real pin lockouts wearing the generic "all providers busy" message).
		// When nothing serves the model, the generic busy message is the truthful one.
		pinUserGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
		tokenPinned := common.GetContextKeyBool(c, constant.ContextKeyTokenGroupMappingApplied) ||
			(info.TokenGroup != "" && info.TokenGroup != "auto" && info.TokenGroup != "default" && info.TokenGroup != pinUserGroup)
		if tokenPinned &&
			model.HasEnabledChannelForModelOutsideGroups(info.OriginModelName, service.ParseTokenGroups(info.TokenGroup)) {
			return nil, types.NewErrorWithStatusCode(fmt.Errorf("This API key is pinned to a billing group that currently has no available provider for \"%s\" - the model itself is online and served by other groups. This is a problem with the key, not the model: open your token settings and set its group to \"auto\" (or delete the pin), then retry.", info.OriginModelName), types.ErrorCodeGetChannelFailed, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
		}
		if info.TokenGroup == "auto" || service.IsCompositeTokenGroup(info.TokenGroup) {
			return nil, types.NewErrorWithStatusCode(fmt.Errorf("This model is busy right now (free providers hit their rate limit). Please try again in a little while, or switch to another model."), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		// The model exists but every channel serving it is currently disabled
		// (auto-disabled on rate limit / maintenance). Tell the user it is busy,
		// not that they mistyped the model name.
		if model.ModelHasAnyChannel(info.OriginModelName) {
			return nil, types.NewErrorWithStatusCode(fmt.Errorf("All providers for model \"%s\" are busy right now (they hit their rate limit). This is not a spelling error. Please try again in a little while, or switch to another model.", info.OriginModelName), types.ErrorCodeGetChannelFailed, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
		}
		return nil, types.NewError(fmt.Errorf("no available channel for model %s in group %s (retry)", info.OriginModelName, selectGroup), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}

	info.PriceData.GroupRatioInfo = helper.HandleGroupRatio(c, info)

	newAPIError := middleware.SetupContextForSelectedChannel(c, channel, info.OriginModelName)
	if newAPIError != nil {
		return nil, newAPIError
	}
	return channel, nil
}

func shouldRetry(c *gin.Context, openaiErr *types.NewAPIError, retryTimes int) bool {
	if openaiErr == nil {
		return false
	}
	if c.Request != nil && c.Request.Context().Err() != nil {
		return false
	}
	if service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		return false
	}
	if types.IsChannelError(openaiErr) {
		return true
	}
	if types.IsSkipRetryError(openaiErr) {
		return false
	}
	// Upstream 400 = malformed request; retrying other channels yields the same
	// rejection. Fail fast, never failover.
	if types.IsDeterministicUpstreamError(openaiErr) {
		return false
	}
	if retryTimes <= 0 {
		return false
	}
	if _, ok := c.Get("specific_channel_id"); ok {
		return false
	}
	// PROD-ONLY (fork): force failover for per-channel 400s that a sibling can
	// still serve - moderation is per-upstream (one channel's content-policy reject
	// passes on a laxer sibling), and a capacity/degradation 400 (NVIDIA DEGRADED,
	// a reseller's masked "retry later") is transient. 400 is excluded from the
	// generic retry ranges, so ShouldRetryByStatusCode would otherwise drop these.
	if types.IsUpstreamModerationError(openaiErr) || types.IsTransientUpstream400(openaiErr) {
		return true
	}
	code := openaiErr.StatusCode
	if code >= 200 && code < 300 {
		return false
	}
	if code < 100 || code > 599 {
		return true
	}
	if operation_setting.IsAlwaysSkipRetryCode(openaiErr.GetErrorCode()) {
		return false
	}
	return operation_setting.ShouldRetryByStatusCode(code)
}

// shouldChargeOnError reports whether a failed request should keep its
// pre-consumed quota instead of refunding. Only true when the ChargeOnError
// setting is on AND the error came back from an upstream (the upstream actually
// processed the request). Local new_api_error failures (no available channel,
// invalid request, model-mapping, param-override, etc.) never reached an
// upstream, so they are always refunded regardless of the toggle.
//
// Deterministic upstream rejections (400/415/422/451: malformed request,
// unsupported param, validation, policy block) are ALSO always refunded even
// though they carry an upstream error type: the upstream rejected the request
// up front and did no billable work (zero tokens), so charging the user the
// full pre-consumed estimate for a client-side mistake would be a phantom
// charge. ChargeOnError is meant for requests the upstream actually processed
// before failing, not for instant validation rejections.
func shouldChargeOnError(err *types.NewAPIError) bool {
	if err == nil || !operation_setting.GetQuotaSetting().ChargeOnError {
		return false
	}
	if types.IsDeterministicUpstreamError(err) {
		return false
	}
	if isTransientInfraError(err) {
		return false
	}
	return err.GetErrorType() != types.ErrorTypeNewAPIError
}

// isTransientInfraError reports whether a failed upstream call is an
// infrastructure fault (timeout, saturation, gateway/server error) that
// delivered nothing to the user, so the pre-consumed quota must be refunded even
// with ChargeOnError on. Deterministic user-side rejections (400/415/422/451)
// are handled separately and are not included here.
func isTransientInfraError(err *types.NewAPIError) bool {
	if err == nil || err.GetErrorType() == types.ErrorTypeNewAPIError {
		return false
	}
	switch err.StatusCode {
	case http.StatusRequestTimeout, // 408
		http.StatusTooManyRequests: // 429
		return true
	}
	return err.StatusCode >= 500
}

func processChannelError(c *gin.Context, channelError types.ChannelError, err *types.NewAPIError, statusOpts ...model.ChannelStatusChangeOpt) {
	if c.Request != nil && c.Request.Context().Err() != nil {
		return
	}
	logger.LogError(c, fmt.Sprintf("channel error (channel #%d, status code: %d): %s", channelError.ChannelId, err.StatusCode, common.LocalLogPreview(err.Error())))
	// 不要使用context获取渠道信息，异步处理时可能会出现渠道信息不一致的情况
	// do not use context to get channel info, there may be inconsistent channel info when processing asynchronously
	shouldDisable := service.ShouldDisableChannel(err)
	// PROD-ONLY (fork): spare non-recoverable media/native-image channels from disable on
	// user-caused request errors (the channel-test cron has no probe path for them, so a
	// false-disable is permanent). Genuine channel faults are channel:* coded and NOT skipped.
	if shouldDisable && shouldSkipDisableForModality(c.GetString("original_model"), err) {
		logger.LogError(c, fmt.Sprintf("PROD-ONLY(fork): skip auto-disable channel #%d non-recoverable modality model=%s code=%s status=%d",
			channelError.ChannelId, c.GetString("original_model"), err.GetErrorCode(), err.StatusCode))
		shouldDisable = false
	}
	// A single upstream fault is not evidence a channel is dead. Require the failure
	// to be a sustained share of the channel's recent traffic before pulling it, so a
	// capacity blip on a busy lane fails over instead of removing it for everyone.
	// Credential faults are exempt: those cannot recover on their own.
	if shouldDisable && !service.IsCredentialFault(err) && !service.RecordChannelFailure(channelError.ChannelId) {
		fails, oks := service.ChannelFailureWindow(channelError.ChannelId)
		logger.LogInfo(c, fmt.Sprintf("channel-guard: kept channel #%d (%s) enabled, fault below threshold: fail=%d ok=%d status=%d code=%s",
			channelError.ChannelId, channelError.ChannelName, fails, oks, err.StatusCode, err.GetErrorCode()))
		shouldDisable = false
	}
	if shouldDisable && channelError.AutoBan {
		// Default the status-history trigger to a live relay request; the
		// scheduled-test caller overrides it via statusOpts. Model name comes
		// from the request context when present.
		opts := append([]model.ChannelStatusChangeOpt{
			model.WithChannelStatusTrigger(model.ChannelStatusTriggerLiveRequest),
			model.WithChannelStatusModel(c.GetString("original_model")),
		}, statusOpts...)
		gopool.Go(func() {
			service.DisableChannel(channelError, err.ErrorWithStatusCode(), opts...)
		})
	}

	if constant.ErrorLogEnabled && types.IsRecordErrorLog(err) {
		// 保存错误日志到mysql中
		userId := c.GetInt("id")
		tokenName := c.GetString("token_name")
		modelName := c.GetString("original_model")
		tokenId := c.GetInt("token_id")
		userGroup := c.GetString("group")
		channelId := c.GetInt("channel_id")
		other := make(map[string]interface{})
		if c.Request != nil && c.Request.URL != nil {
			other["request_path"] = c.Request.URL.Path
		}
		other["error_type"] = err.GetErrorType()
		other["error_code"] = err.GetErrorCode()
		other["status_code"] = err.StatusCode
		other["channel_id"] = channelId
		other["channel_name"] = c.GetString("channel_name")
		other["channel_type"] = c.GetInt("channel_type")
		adminInfo := make(map[string]interface{})
		adminInfo["use_channel"] = c.GetStringSlice("use_channel")
		isMultiKey := common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey)
		if isMultiKey {
			adminInfo["is_multi_key"] = true
			adminInfo["multi_key_index"] = common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)
		}
		service.AppendChannelAffinityAdminInfo(c, adminInfo)
		other["admin_info"] = adminInfo
		startTime := common.GetContextKeyTime(c, constant.ContextKeyRequestStartTime)
		if startTime.IsZero() {
			startTime = time.Now()
		}
		useTimeSeconds := int(time.Since(startTime).Seconds())
		model.RecordErrorLog(c, userId, channelId, modelName, tokenName, err.MaskSensitiveErrorWithStatusCode(), tokenId, useTimeSeconds, common.GetContextKeyBool(c, constant.ContextKeyIsStream), userGroup, other)
	}

}

func RelayMidjourney(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatMjProxy, nil, nil)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"description": fmt.Sprintf("failed to generate relay info: %s", err.Error()),
			"type":        "upstream_error",
			"code":        4,
		})
		return
	}

	var mjErr *taskdto.MidjourneyResponse
	switch relayInfo.RelayMode {
	case relayconstant.RelayModeMidjourneyNotify:
		mjErr = relay.RelayMidjourneyNotify(c)
	case relayconstant.RelayModeMidjourneyTaskFetch, relayconstant.RelayModeMidjourneyTaskFetchByCondition:
		mjErr = relay.RelayMidjourneyTask(c, relayInfo.RelayMode)
	case relayconstant.RelayModeMidjourneyTaskImageSeed:
		mjErr = relay.RelayMidjourneyTaskImageSeed(c)
	case relayconstant.RelayModeSwapFace:
		mjErr = relay.RelaySwapFace(c, relayInfo)
	default:
		mjErr = relay.RelayMidjourneySubmit(c, relayInfo)
	}
	//err = relayMidjourneySubmit(c, relayMode)
	log.Println(mjErr)
	if mjErr != nil {
		statusCode := http.StatusBadRequest
		if mjErr.Code == 30 {
			mjErr.Result = "The current group is at full load, please try again later, or upgrade your account to improve service quality."
			statusCode = http.StatusTooManyRequests
		}
		c.JSON(statusCode, gin.H{
			"description": fmt.Sprintf("%s %s", mjErr.Description, mjErr.Result),
			"type":        "upstream_error",
			"code":        mjErr.Code,
		})
		channelId := c.GetInt("channel_id")
		logger.LogError(c, fmt.Sprintf("relay error (channel #%d, status code %d): %s", channelId, statusCode, fmt.Sprintf("%s %s", mjErr.Description, mjErr.Result)))
	}
}

func RelayNotImplemented(c *gin.Context) {
	err := types.OpenAIError{
		Message: "API not implemented",
		Type:    "new_api_error",
		Param:   "",
		Code:    "api_not_implemented",
	}
	c.JSON(http.StatusNotImplemented, gin.H{
		"error": err,
	})
}

func RelayNotFound(c *gin.Context) {
	err := types.OpenAIError{
		Message: fmt.Sprintf("Invalid URL (%s %s)", c.Request.Method, c.Request.URL.Path),
		Type:    "invalid_request_error",
		Param:   "",
		Code:    "",
	}
	c.JSON(http.StatusNotFound, gin.H{
		"error": err,
	})
}

func RelayTaskFetch(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, &taskdto.TaskError{
			Code:       "gen_relay_info_failed",
			Message:    err.Error(),
			StatusCode: http.StatusInternalServerError,
		})
		return
	}
	if taskErr := relay.RelayTaskFetch(c, relayInfo.RelayMode); taskErr != nil {
		respondTaskError(c, taskErr)
	}
}

func RelayTask(c *gin.Context) {
	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, &taskdto.TaskError{
			Code:       "gen_relay_info_failed",
			Message:    err.Error(),
			StatusCode: http.StatusInternalServerError,
		})
		return
	}

	if taskErr := relay.ResolveOriginTask(c, relayInfo); taskErr != nil {
		respondTaskError(c, taskErr)
		return
	}

	var result *relay.TaskSubmitResult
	var taskErr *taskdto.TaskError
	defer func() {
		if taskErr != nil && relayInfo.Billing != nil {
			relayInfo.Billing.Refund(c)
		}
	}()

	retryParam := &service.RetryParam{
		Ctx:         c,
		TokenGroup:  relayInfo.TokenGroup,
		ModelName:   relayInfo.OriginModelName,
		RequestPath: c.Request.URL.Path,
		Retry:       common.GetPointer(0),
	}

	for ; retryParam.GetRetry() <= common.RetryTimes; retryParam.IncreaseRetry() {
		var channel *model.Channel

		if lockedCh, ok := relayInfo.LockedChannel.(*model.Channel); ok && lockedCh != nil {
			channel = lockedCh
			if retryParam.GetRetry() > 0 {
				if setupErr := middleware.SetupContextForSelectedChannel(c, channel, relayInfo.OriginModelName); setupErr != nil {
					taskErr = service.TaskErrorWrapperLocal(setupErr.Err, "setup_locked_channel_failed", http.StatusInternalServerError)
					break
				}
			}
		} else {
			var channelErr *types.NewAPIError
			channel, channelErr = getChannel(c, relayInfo, retryParam)
			if channelErr != nil {
				logger.LogError(c, channelErr.Error())
				taskErr = service.TaskErrorWrapperLocal(channelErr.Err, "get_channel_failed", http.StatusInternalServerError)
				break
			}
		}

		addUsedChannel(c, channel.Id)
		bodyStorage, bodyErr := common.GetBodyStorage(c)
		if bodyErr != nil {
			if common.IsRequestBodyTooLargeError(bodyErr) || errors.Is(bodyErr, common.ErrRequestBodyTooLarge) {
				taskErr = service.TaskErrorWrapperLocal(bodyErr, "read_request_body_failed", http.StatusRequestEntityTooLarge)
			} else {
				taskErr = service.TaskErrorWrapperLocal(bodyErr, "read_request_body_failed", http.StatusBadRequest)
			}
			break
		}
		c.Request.Body = io.NopCloser(bodyStorage)

		result, taskErr = relay.RelayTaskSubmit(c, relayInfo)
		if taskErr == nil {
			break
		}

		if !taskErr.LocalError {
			processChannelError(c,
				*types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey,
					common.GetContextKeyString(c, constant.ContextKeyChannelKey), channel.GetAutoBan()),
				types.NewOpenAIError(taskErr.Error, types.ErrorCodeBadResponseStatusCode, taskErr.StatusCode))
		}

		if !shouldRetryTaskRelay(c, channel.Id, taskErr, common.RetryTimes-retryParam.GetRetry()) {
			break
		}
	}

	useChannel := c.GetStringSlice("use_channel")
	if len(useChannel) > 1 {
		retryLogStr := fmt.Sprintf("retry: %s", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(useChannel)), "->"), "[]"))
		logger.LogInfo(c, retryLogStr)
	}

	// ── 成功：结算 + 日志 + 插入任务 ──
	if taskErr == nil {
		if settleErr := service.SettleBilling(c, relayInfo, result.Quota); settleErr != nil {
			common.SysError("settle task billing error: " + settleErr.Error())
		}
		service.LogTaskConsumption(c, relayInfo)

		task := model.InitTask(result.Platform, relayInfo)
		task.PrivateData.UpstreamTaskID = result.UpstreamTaskID
		task.PrivateData.BillingSource = relayInfo.BillingSource
		task.PrivateData.SubscriptionId = relayInfo.SubscriptionId
		task.PrivateData.TokenId = relayInfo.TokenId
		task.PrivateData.NodeName = common.NodeName
		task.PrivateData.BillingContext = &model.TaskBillingContext{
			ModelPrice:      relayInfo.PriceData.ModelPrice,
			GroupRatio:      relayInfo.PriceData.GroupRatioInfo.GroupRatio,
			ModelRatio:      relayInfo.PriceData.ModelRatio,
			OtherRatios:     relayInfo.PriceData.OtherRatios(),
			OriginModelName: relayInfo.OriginModelName,
			PerCallBilling:  common.StringsContains(constant.TaskPricePatches, relayInfo.OriginModelName) || relayInfo.PriceData.UsePrice,
		}
		task.Quota = result.Quota
		task.Data = result.TaskData
		task.Action = relayInfo.Action
		if insertErr := task.Insert(); insertErr != nil {
			common.SysError("insert task error: " + insertErr.Error())
		}
	}

	if taskErr != nil {
		respondTaskError(c, taskErr)
	}
}

// respondTaskError 统一输出 Task 错误响应（含 429 限流提示改写）
func respondTaskError(c *gin.Context, taskErr *taskdto.TaskError) {
	if taskErr.StatusCode == http.StatusTooManyRequests {
		taskErr.Message = "The current group's upstream is at full load, please try again later"
	}
	c.JSON(taskErr.StatusCode, taskErr)
}

func shouldRetryTaskRelay(c *gin.Context, channelId int, taskErr *taskdto.TaskError, retryTimes int) bool {
	if taskErr == nil {
		return false
	}
	if service.ShouldSkipRetryAfterChannelAffinityFailure(c) {
		return false
	}
	if retryTimes <= 0 {
		return false
	}
	if _, ok := c.Get("specific_channel_id"); ok {
		return false
	}
	if taskErr.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if taskErr.StatusCode == 307 {
		return true
	}
	if taskErr.StatusCode/100 == 5 {
		// 超时不重试
		if operation_setting.IsAlwaysSkipRetryStatusCode(taskErr.StatusCode) {
			return false
		}
		return true
	}
	if taskErr.StatusCode == http.StatusBadRequest {
		return false
	}
	if taskErr.StatusCode == 408 {
		// azure处理超时不重试
		return false
	}
	if taskErr.LocalError {
		return false
	}
	if taskErr.StatusCode/100 == 2 {
		return false
	}
	return true
}
