package types

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

type OpenAIError struct {
	Message  string          `json:"message"`
	Type     string          `json:"type"`
	Param    string          `json:"param"`
	Code     any             `json:"code"`
	Status   string          `json:"status,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type ClaudeError struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message,omitempty"`
}

type ErrorType string

const (
	ErrorTypeNewAPIError     ErrorType = "new_api_error"
	ErrorTypeOpenAIError     ErrorType = "openai_error"
	ErrorTypeClaudeError     ErrorType = "claude_error"
	ErrorTypeMidjourneyError ErrorType = "midjourney_error"
	ErrorTypeGeminiError     ErrorType = "gemini_error"
	ErrorTypeRerankError     ErrorType = "rerank_error"
	ErrorTypeUpstreamError   ErrorType = "upstream_error"
)

type ErrorCode string

const (
	ErrorCodeInvalidRequest         ErrorCode = "invalid_request"
	ErrorCodeSensitiveWordsDetected ErrorCode = "sensitive_words_detected"
	ErrorCodeViolationFeeGrokCSAM   ErrorCode = "violation_fee.grok.csam"

	// new api error
	ErrorCodeCountTokenFailed   ErrorCode = "count_token_failed"
	ErrorCodeModelPriceError    ErrorCode = "model_price_error"
	ErrorCodeInvalidApiType     ErrorCode = "invalid_api_type"
	ErrorCodeJsonMarshalFailed  ErrorCode = "json_marshal_failed"
	ErrorCodeDoRequestFailed    ErrorCode = "do_request_failed"
	ErrorCodeGetChannelFailed   ErrorCode = "get_channel_failed"
	ErrorCodeGenRelayInfoFailed ErrorCode = "gen_relay_info_failed"

	// channel error
	ErrorCodeChannelNoAvailableKey        ErrorCode = "channel:no_available_key"
	ErrorCodeChannelParamOverrideInvalid  ErrorCode = "channel:param_override_invalid"
	ErrorCodeChannelHeaderOverrideInvalid ErrorCode = "channel:header_override_invalid"
	ErrorCodeChannelModelMappedError      ErrorCode = "channel:model_mapped_error"
	ErrorCodeChannelAwsClientError        ErrorCode = "channel:aws_client_error"
	ErrorCodeChannelInvalidKey            ErrorCode = "channel:invalid_key"
	ErrorCodeChannelResponseTimeExceeded  ErrorCode = "channel:response_time_exceeded"
	ErrorCodeChannelEmptyResponse         ErrorCode = "channel:empty_response"

	// client request error
	ErrorCodeReadRequestBodyFailed ErrorCode = "read_request_body_failed"
	ErrorCodeConvertRequestFailed  ErrorCode = "convert_request_failed"
	ErrorCodeAccessDenied          ErrorCode = "access_denied"

	// request error
	ErrorCodeBadRequestBody ErrorCode = "bad_request_body"

	// response error
	ErrorCodeReadResponseBodyFailed ErrorCode = "read_response_body_failed"
	ErrorCodeBadResponseStatusCode  ErrorCode = "bad_response_status_code"
	ErrorCodeBadResponse            ErrorCode = "bad_response"
	ErrorCodeBadResponseBody        ErrorCode = "bad_response_body"
	ErrorCodeEmptyResponse          ErrorCode = "empty_response"
	ErrorCodeAwsInvokeError         ErrorCode = "aws_invoke_error"
	ErrorCodeModelNotFound          ErrorCode = "model_not_found"
	ErrorCodePromptBlocked          ErrorCode = "prompt_blocked"

	// sql error
	ErrorCodeQueryDataError  ErrorCode = "query_data_error"
	ErrorCodeUpdateDataError ErrorCode = "update_data_error"

	// quota error
	ErrorCodeInsufficientUserQuota      ErrorCode = "insufficient_user_quota"
	ErrorCodePreConsumeTokenQuotaFailed ErrorCode = "pre_consume_token_quota_failed"
	ErrorCodeFreeModelBlockedNoQuota    ErrorCode = "free_model_blocked_no_quota"
	// ErrorCodeRateLimitExceeded is the OpenAI-standard 429 code, used to disguise
	// the free-model shadow-ban response as ordinary rate limiting.
	ErrorCodeRateLimitExceeded ErrorCode = "rate_limit_exceeded"
)

type NewAPIError struct {
	Err            error
	RelayError     any
	skipRetry      bool
	skipDisable    bool
	recordErrorLog *bool
	errorType      ErrorType
	errorCode      ErrorCode
	StatusCode     int
	Metadata       json.RawMessage
}

// Unwrap enables errors.Is / errors.As to work with NewAPIError by exposing the underlying error.
func (e *NewAPIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *NewAPIError) GetErrorCode() ErrorCode {
	if e == nil {
		return ""
	}
	return e.errorCode
}

func (e *NewAPIError) GetErrorType() ErrorType {
	if e == nil {
		return ""
	}
	return e.errorType
}

func (e *NewAPIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		// fallback message when underlying error is missing
		return string(e.errorCode)
	}
	return e.Err.Error()
}

func (e *NewAPIError) ErrorWithStatusCode() string {
	if e == nil {
		return ""
	}
	msg := e.Error()
	if e.StatusCode == 0 {
		return msg
	}
	if msg == "" {
		return fmt.Sprintf("status_code=%d", e.StatusCode)
	}
	return fmt.Sprintf("status_code=%d, %s", e.StatusCode, msg)
}

func (e *NewAPIError) MaskSensitiveError() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return string(e.errorCode)
	}
	errStr := e.Err.Error()
	if e.errorCode == ErrorCodeCountTokenFailed {
		return errStr
	}
	return kitutil.MaskSensitiveInfo(errStr)
}

func (e *NewAPIError) MaskSensitiveErrorWithStatusCode() string {
	if e == nil {
		return ""
	}
	msg := e.MaskSensitiveError()
	if e.StatusCode == 0 {
		return msg
	}
	if msg == "" {
		return fmt.Sprintf("status_code=%d", e.StatusCode)
	}
	return fmt.Sprintf("status_code=%d, %s", e.StatusCode, msg)
}

func (e *NewAPIError) SetMessage(message string) {
	e.Err = errors.New(message)
}

func (e *NewAPIError) ToOpenAIError() OpenAIError {
	var result OpenAIError
	switch e.errorType {
	case ErrorTypeOpenAIError:
		if openAIError, ok := e.RelayError.(OpenAIError); ok {
			result = openAIError
		}
	case ErrorTypeClaudeError:
		if claudeError, ok := e.RelayError.(ClaudeError); ok {
			result = OpenAIError{
				Message: e.Error(),
				Type:    claudeError.Type,
				Param:   "",
				Code:    e.errorCode,
			}
		}
	default:
		result = OpenAIError{
			Message: e.Error(),
			Type:    string(e.errorType),
			Param:   "",
			Code:    e.errorCode,
		}
	}
	if e.errorCode != ErrorCodeCountTokenFailed {
		result.Message = kitutil.MaskSensitiveInfo(result.Message)
	}
	if result.Message == "" {
		result.Message = string(e.errorType)
	}
	return result
}

func (e *NewAPIError) ToClaudeError() ClaudeError {
	var result ClaudeError
	switch e.errorType {
	case ErrorTypeOpenAIError:
		if openAIError, ok := e.RelayError.(OpenAIError); ok {
			result = ClaudeError{
				Message: e.Error(),
				Type:    fmt.Sprintf("%v", openAIError.Code),
			}
		}
	case ErrorTypeClaudeError:
		if claudeError, ok := e.RelayError.(ClaudeError); ok {
			result = claudeError
		}
	default:
		result = ClaudeError{
			Message: e.Error(),
			Type:    string(e.errorType),
		}
	}
	if e.errorCode != ErrorCodeCountTokenFailed {
		result.Message = kitutil.MaskSensitiveInfo(result.Message)
	}
	if result.Message == "" {
		result.Message = string(e.errorType)
	}
	return result
}

type NewAPIErrorOptions func(*NewAPIError)

func NewError(err error, errorCode ErrorCode, ops ...NewAPIErrorOptions) *NewAPIError {
	var newErr *NewAPIError
	// 保留深层传递的 new err
	if errors.As(err, &newErr) {
		for _, op := range ops {
			op(newErr)
		}
		return newErr
	}
	e := &NewAPIError{
		Err:        err,
		RelayError: nil,
		errorType:  ErrorTypeNewAPIError,
		StatusCode: http.StatusInternalServerError,
		errorCode:  errorCode,
	}
	for _, op := range ops {
		op(e)
	}
	return e
}

func NewOpenAIError(err error, errorCode ErrorCode, statusCode int, ops ...NewAPIErrorOptions) *NewAPIError {
	var newErr *NewAPIError
	// 保留深层传递的 new err
	if errors.As(err, &newErr) {
		if newErr.RelayError == nil {
			openaiError := OpenAIError{
				Message: newErr.Error(),
				Type:    string(errorCode),
				Code:    errorCode,
			}
			newErr.RelayError = openaiError
		}
		for _, op := range ops {
			op(newErr)
		}
		return newErr
	}
	openaiError := OpenAIError{
		Message: err.Error(),
		Type:    string(errorCode),
		Code:    errorCode,
	}
	return WithOpenAIError(openaiError, statusCode, ops...)
}

func InitOpenAIError(errorCode ErrorCode, statusCode int, ops ...NewAPIErrorOptions) *NewAPIError {
	openaiError := OpenAIError{
		Type: string(errorCode),
		Code: errorCode,
	}
	return WithOpenAIError(openaiError, statusCode, ops...)
}

func NewErrorWithStatusCode(err error, errorCode ErrorCode, statusCode int, ops ...NewAPIErrorOptions) *NewAPIError {
	e := &NewAPIError{
		Err: err,
		RelayError: OpenAIError{
			Message: err.Error(),
			Type:    string(errorCode),
		},
		errorType:  ErrorTypeNewAPIError,
		StatusCode: statusCode,
		errorCode:  errorCode,
	}
	for _, op := range ops {
		op(e)
	}

	return e
}

func WithOpenAIError(openAIError OpenAIError, statusCode int, ops ...NewAPIErrorOptions) *NewAPIError {
	code, ok := openAIError.Code.(string)
	if !ok {
		// Google-style errors carry a numeric `code` (e.g. 400) plus a textual
		// `status` (e.g. INVALID_ARGUMENT). Prefer the status so downstream
		// skip-retry/skip-disable rules can match on the real error category.
		if openAIError.Status != "" {
			code = openAIError.Status
		} else if openAIError.Code != nil {
			code = fmt.Sprintf("%v", openAIError.Code)
		} else {
			code = "unknown_error"
		}
	}
	if openAIError.Type == "" {
		openAIError.Type = "upstream_error"
	}
	// PROD-ONLY (fork): a 400/403/429 carrying an upstream credential/account
	// signature (Google "API key not valid", or a reseller's balance/quota
	// exhaustion) is our channel's fault, not the client's request. Reclassify as
	// a channel error so it fails over to a sibling and disables the bad channel
	// instead of being treated as a deterministic client error. 429 counts because
	// a drained wallet reads as a rate limit on some resellers, and unlike a
	// capacity 429 it cannot clear until the balance is topped up.
	if (statusCode == http.StatusBadRequest || statusCode == http.StatusForbidden ||
		statusCode == http.StatusTooManyRequests) &&
		isUpstreamCredentialFault(openAIError.Message) {
		code = string(ErrorCodeChannelInvalidKey)
	}
	// PROD-ONLY (fork): a 400 whose message says this channel's upstream lacks the
	// requested model ("Model 'X' is currently unavailable / not found / does not
	// exist") is a channel-side fault: the SAME request would succeed on a sibling
	// that hosts the model. Reclassify to channel:model_mapped_error so it fails
	// over and disables this channel, instead of being skipped as a deterministic
	// 400. Scoped ONLY to 400 (404 already disables via status code) and ONLY to
	// model-availability wording, so generic bad-request 400s stay deterministic.
	if statusCode == http.StatusBadRequest && isUpstreamModelUnavailable(openAIError.Message) {
		code = string(ErrorCodeChannelModelMappedError)
	}
	// PROD-ONLY (fork): a 400 that leaks a LiteLLM/vLLM proxy's fallback model
	// groups means this channel silently substitutes the requested model. Treat as
	// a channel fault so it fails over and disables instead of counting as a
	// deterministic client 400.
	if statusCode == http.StatusBadRequest && isUpstreamModelSubstitution(openAIError.Message) {
		code = string(ErrorCodeChannelModelMappedError)
	}
	e := &NewAPIError{
		RelayError: openAIError,
		errorType:  ErrorTypeOpenAIError,
		StatusCode: statusCode,
		Err:        errors.New(openAIError.Message),
		errorCode:  ErrorCode(code),
	}
	// OpenRouter
	if len(openAIError.Metadata) > 0 {
		openAIError.Message = fmt.Sprintf("%s (%s)", openAIError.Message, openAIError.Metadata)
		e.Metadata = openAIError.Metadata
		e.RelayError = openAIError
		e.Err = errors.New(openAIError.Message)
	}
	for _, op := range ops {
		op(e)
	}
	return e
}

func WithClaudeError(claudeError ClaudeError, statusCode int, ops ...NewAPIErrorOptions) *NewAPIError {
	if claudeError.Type == "" {
		claudeError.Type = "upstream_error"
	}
	e := &NewAPIError{
		RelayError: claudeError,
		errorType:  ErrorTypeClaudeError,
		StatusCode: statusCode,
		Err:        errors.New(claudeError.Message),
		errorCode:  ErrorCode(claudeError.Type),
	}
	for _, op := range ops {
		op(e)
	}
	return e
}

func IsChannelError(err *NewAPIError) bool {
	if err == nil {
		return false
	}
	return strings.HasPrefix(string(err.errorCode), "channel:")
}

// IsUpstreamTimeoutError reports an UPSTREAM first-byte/response-header timeout
// (net.Error Timeout, context.DeadlineExceeded, or the stdlib "timeout awaiting
// response headers" text). context.Canceled is excluded: a client hangup
// (Cloudflare 524, browser abort) is not the channel's fault.
func IsUpstreamTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "timeout awaiting response headers")
}

// ChannelFaultKeywordsProvider supplies the admin-editable list of message
// fragments that mark a 400/403 as THIS channel's fault (dead key, drained
// upstream wallet, exhausted free quota) rather than the client's request. Set
// once at startup by the operation_setting package (which owns the DB-backed
// option and its seed defaults) to avoid a types<->operation_setting import
// cycle. Nil (not yet wired) means no reclassification.
var ChannelFaultKeywordsProvider func() []string

// isUpstreamCredentialFault reports whether an error message is actually an
// upstream credential/account fault on our side of the channel.
func isUpstreamCredentialFault(message string) bool {
	if ChannelFaultKeywordsProvider == nil {
		return false
	}
	lower := strings.ToLower(message)
	for _, sig := range ChannelFaultKeywordsProvider() {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

// upstreamModelUnavailableSignatures are message fragments an upstream emits with
// a 400 when THIS channel does not host the requested model (e.g. "Model 'X' is
// currently unavailable", "Model 'X' not found. Available models: ...", "... does
// not exist"). The same request succeeds on a sibling channel that hosts the
// model, so it is a channel fault, not a client fault. Anchored to model-word
// wording so it never matches transient infra ("temporarily unavailable",
// "provider is currently unavailable") or generic bad-request 400s. Match is
// lowercase.
var upstreamModelUnavailableSignatures = []string{
	"is currently unavailable",
	"not found. available model",
	"model not found",
	"model_not_found",
	"does not exist",
}

// isUpstreamModelUnavailable reports whether a 400 message means this channel's
// upstream lacks the requested model (a channel fault, not a client fault).
func isUpstreamModelUnavailable(message string) bool {
	lower := strings.ToLower(message)
	// Guard: transient infra also says "unavailable"; never treat those as a
	// model-availability fault (they are handled as transient elsewhere).
	if strings.Contains(lower, "temporarily unavailable") ||
		strings.Contains(lower, "provider is currently unavailable") {
		return false
	}
	for _, sig := range upstreamModelUnavailableSignatures {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

// litellmFallbackLeakSignatures mark a 400 from a LiteLLM/vLLM reseller proxy
// that leaks its internal model-group fallback routing (e.g. "No fallback model
// group found for original model_group=glm5.2-beta. Fallbacks=[...]"). Such a
// channel silently substitutes the requested model for whatever its fallback
// list points at (qwen/gpt-oss), so it must not keep serving the model: reclassify
// to a channel fault to fail over and disable. Match is lowercase.
var litellmFallbackLeakSignatures = []string{
	"no fallback model group found",
	"received model group=",
	"available model group fallbacks",
}

// isUpstreamModelSubstitution reports whether a 400 exposes a reseller proxy's
// model-group fallback config (model substitution risk), i.e. a channel fault.
func isUpstreamModelSubstitution(message string) bool {
	lower := strings.ToLower(message)
	for _, sig := range litellmFallbackLeakSignatures {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

func IsSkipRetryError(err *NewAPIError) bool {
	if err == nil {
		return false
	}

	return err.skipRetry
}

// deterministicUpstreamStatusCodes are HTTP statuses that always indicate a
// request-side fault (malformed payload, invalid input, or content blocked by
// policy) rather than a per-channel transient failure. The SAME request fails
// identically on every channel, so retrying the pool only wastes time and the
// status code, when configured as a disable trigger, would auto-ban healthy
// channels for a bad client request.
//
//   - 400 Bad Request          : malformed/invalid argument (e.g. Gemini INVALID_ARGUMENT)
//   - 415 Unsupported Media Type: payload format the model cannot accept
//   - 422 Unprocessable Entity  : input validation failure
//   - 451 Unavailable For Legal : content blocked by upstream policy/moderation
//
// Notably EXCLUDED: 404 (a sibling channel may actually host the model, so
// failover is desirable), 413 (the TPM rate-limit variant is transient; the
// genuinely-oversized variant is already caught by alwaysSkipRetryCodes), and
// 429/5xx (transient by definition).
var deterministicUpstreamStatusCodes = map[int]struct{}{
	http.StatusBadRequest:                 {}, // 400
	http.StatusUnsupportedMediaType:       {}, // 415
	http.StatusUnprocessableEntity:        {}, // 422
	http.StatusUnavailableForLegalReasons: {}, // 451
}

var transientUpstream400Markers = []string{
	"degraded",
	"cannot be invoked",
	"temporarily unavailable",
	"try again",
	"retry later",
	"upstream provider returned an error",
	// Console Go reseller masking its upstream's failure as a 400
	"upstream request failed",
}

var upstreamModerationMarkers = []string{
	"inappropriate content",
	"datainspectionfailed",
	"sensitive words",
	"sensitive information",
	"content policy",
	"content_filter",
	"content management policy",
	"triggering azure openai",
	"unsafe or sensitive content",
	// AWS Bedrock / reseller guardrail rejections ("Blocked by guardrail policy")
	"guardrail",
	// Measured against 6 months of production 403s: these are the wordings real
	// upstreams use to refuse ONE prompt. The channel is healthy and the next
	// request succeeds, so none of them may auto-ban it.
	"safe guard policy",
	"usage guidelines",
	"usage policy",
	"内容审计",
	"违反使用规定",
	"风险规则",
	// Not moderation but the same shape: a size/step ceiling refusing one
	// oversized image request on an otherwise working channel.
	"due to heavy demand",
}

// PROD-ONLY (fork): IsUpstreamModerationError reports whether an upstream 400/422
// is that upstream's content-moderation reject. Used to failover to a sibling
// channel AND to spare the channel from auto-disable (request-caused, not a
// channel fault).
func IsUpstreamModerationError(err *NewAPIError) bool {
	if err == nil || err.errorType == ErrorTypeNewAPIError {
		return false
	}
	// 403 included: several upstreams return their content refusal as Forbidden
	// rather than 400, and 403 is in the auto-disable ranges, so without this a
	// healthy channel is banned for one bad prompt.
	if err.StatusCode != http.StatusBadRequest &&
		err.StatusCode != http.StatusUnprocessableEntity &&
		err.StatusCode != http.StatusForbidden {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range upstreamModerationMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// selfEchoMarkers are OUR OWN gateway's error strings coming back through a relay
// chain: an upstream that itself runs new-api rejects our request and quotes its
// local message. The channel is fine, so banning it would let one banned user
// walk the whole pool down.
var selfEchoMarkers = []string{
	"user has been banned",
	"用户已被封禁",
	"this channel has been disabled",
	"this model is busy right now",
}

// IsSelfEchoedError reports whether an upstream error is our own gateway's text
// reflected back, which says nothing about the channel's health.
func IsSelfEchoedError(err *NewAPIError) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range selfEchoMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// IsDeterministicUpstreamError reports whether the error is an upstream-origin
// request-side fault (see deterministicUpstreamStatusCodes). These must never
// trigger failover or channel auto-disable. Local new_api_error responses (our
// own validation) are excluded since they never reached an upstream. This is a
// safety net for upstreams whose error code does not normalize into
// alwaysSkipRetryCodes.
func IsDeterministicUpstreamError(err *NewAPIError) bool {
	if err == nil {
		return false
	}
	if err.errorType == ErrorTypeNewAPIError {
		return false
	}
	if _, ok := deterministicUpstreamStatusCodes[err.StatusCode]; !ok {
		return false
	}
	// A capacity/degradation 400 (e.g. NVIDIA "DEGRADED function cannot be
	// invoked") is transient: a sibling channel serving the same model can still
	// answer, so it must failover instead of failing fast like a malformed request.
	if IsTransientUpstream400(err) {
		return false
	}
	// PROD-ONLY (fork): per-upstream content-moderation (400/422) is transient
	// across the pool, so failover instead of failing fast.
	if IsUpstreamModerationError(err) {
		return false
	}
	return true
}

// IsTransientUpstream400 reports whether an upstream 400 carries a
// capacity/degradation marker (see transientUpstream400Markers) rather than a
// malformed-request fault. Such a 400 can succeed on a sibling channel, so it must
// failover. 400 is excluded from the generic retry status ranges, so shouldRetry
// consults this explicitly to force the failover.
func IsTransientUpstream400(err *NewAPIError) bool {
	if err == nil || err.errorType == ErrorTypeNewAPIError {
		return false
	}
	if err.StatusCode != http.StatusBadRequest {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range transientUpstream400Markers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func ErrOptionWithSkipRetry() NewAPIErrorOptions {
	return func(e *NewAPIError) {
		e.skipRetry = true
	}
}

// ErrOptionWithSkipDisable marks an error as non-disabling: the channel is not
// auto-banned even if the code/status would normally disable it. For faults that
// fail over cleanly (nothing committed to the client) where a single occurrence is
// not proof the channel is dead - the scheduled autotest still disables dead ones.
func ErrOptionWithSkipDisable() NewAPIErrorOptions {
	return func(e *NewAPIError) {
		e.skipDisable = true
	}
}

func IsSkipDisableError(err *NewAPIError) bool {
	if err == nil {
		return false
	}
	return err.skipDisable
}

func ErrOptionWithNoRecordErrorLog() NewAPIErrorOptions {
	return func(e *NewAPIError) {
		e.recordErrorLog = kitutil.GetPointer(false)
	}
}

func ErrOptionWithStatusCode(statusCode int) NewAPIErrorOptions {
	return func(e *NewAPIError) {
		e.StatusCode = statusCode
	}
}

func ErrOptionWithHideErrMsg(replaceStr string) NewAPIErrorOptions {
	return func(e *NewAPIError) {
		if kitutil.Debug.Load() {
			fmt.Printf("ErrOptionWithHideErrMsg: %s, origin error: %s", replaceStr, e.Err)
		}
		e.Err = errors.New(replaceStr)
	}
}

func IsRecordErrorLog(e *NewAPIError) bool {
	if e == nil {
		return false
	}
	if e.recordErrorLog == nil {
		// default to true if not set
		return true
	}
	return *e.recordErrorLog
}
