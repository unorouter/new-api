package dto

import (
	relaydto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/types"
)

// Type aliases for backward compatibility.
// These types were moved from dto to types to break the dto↔model import cycle;
// the relay-facing ones then moved again into the standalone relaykit module.

type UserSetting = types.UserSetting
type ChannelSettings = relaydto.ChannelSettings
type ChannelOtherSettings = relaydto.ChannelOtherSettings
type AdvancedCustomConfig = relaydto.AdvancedCustomConfig
type AdvancedCustomRoute = relaydto.AdvancedCustomRoute
type AdvancedCustomRouteAuth = relaydto.AdvancedCustomRouteAuth
type OpenAIVideoError = relaydto.OpenAIVideoError

// Re-export constants.
var (
	NotifyTypeEmail   = types.NotifyTypeEmail
	NotifyTypeWebhook = types.NotifyTypeWebhook
	NotifyTypeBark    = types.NotifyTypeBark
	NotifyTypeGotify  = types.NotifyTypeGotify
)

const (
	VideoStatusUnknown    = relaydto.VideoStatusUnknown
	VideoStatusQueued     = relaydto.VideoStatusQueued
	VideoStatusInProgress = relaydto.VideoStatusInProgress
	VideoStatusCompleted  = relaydto.VideoStatusCompleted
	VideoStatusFailed     = relaydto.VideoStatusFailed
)

var (
	VertexKeyTypeAPIKey = relaydto.VertexKeyTypeAPIKey
	AwsKeyTypeApiKey    = relaydto.AwsKeyTypeApiKey
)

const (
	AdvancedCustomConverterNone                                         = relaydto.AdvancedCustomConverterNone
	AdvancedCustomConverterAnthropicMessagesToOpenAIChatCompletions     = relaydto.AdvancedCustomConverterAnthropicMessagesToOpenAIChatCompletions
	AdvancedCustomConverterOpenAIChatCompletionsToAnthropicMessages     = relaydto.AdvancedCustomConverterOpenAIChatCompletionsToAnthropicMessages
	AdvancedCustomConverterOpenAIChatCompletionsToOpenAIResponses       = relaydto.AdvancedCustomConverterOpenAIChatCompletionsToOpenAIResponses
	AdvancedCustomConverterOpenAIResponsesToOpenAIChatCompletions       = relaydto.AdvancedCustomConverterOpenAIResponsesToOpenAIChatCompletions
	AdvancedCustomConverterGeminiGenerateContentToOpenAIChatCompletions = relaydto.AdvancedCustomConverterGeminiGenerateContentToOpenAIChatCompletions
	AdvancedCustomConverterOpenAIChatCompletionsToGeminiGenerateContent = relaydto.AdvancedCustomConverterOpenAIChatCompletionsToGeminiGenerateContent
)

const (
	AdvancedCustomAuthTypeNone   = relaydto.AdvancedCustomAuthTypeNone
	AdvancedCustomAuthTypeHeader = relaydto.AdvancedCustomAuthTypeHeader
	AdvancedCustomAuthTypeQuery  = relaydto.AdvancedCustomAuthTypeQuery
)

const AdvancedCustomModelListPath = relaydto.AdvancedCustomModelListPath

var NewOpenAIVideo = relaydto.NewOpenAIVideo
