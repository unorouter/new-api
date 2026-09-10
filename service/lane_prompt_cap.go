package service

import (
	"sync"
	"time"
)

// A lane's context ceiling, learned from its own 413: a merchant's cap is often
// below the model's, and nothing in the listing says so. The lowest prompt size
// it refused is remembered for a day so the picker routes bigger prompts past
// it; a raised limit is relearned when the entry expires.
type lanePromptCap struct {
	tokens int
	until  time.Time
}

const lanePromptCapTTL = 24 * time.Hour

var lanePromptCaps sync.Map // channel id -> lanePromptCap

func RecordLanePromptCap(channelId int, promptTokens int) {
	if promptTokens <= 0 {
		return
	}
	if v, ok := lanePromptCaps.Load(channelId); ok {
		cur := v.(lanePromptCap)
		if time.Now().Before(cur.until) && cur.tokens <= promptTokens {
			return
		}
	}
	lanePromptCaps.Store(channelId, lanePromptCap{tokens: promptTokens, until: time.Now().Add(lanePromptCapTTL)})
}

// LaneRejectsPrompt reports whether the lane refused a prompt this size or
// smaller within the last day.
func LaneRejectsPrompt(channelId int, promptTokens int) bool {
	if promptTokens <= 0 {
		return false
	}
	v, ok := lanePromptCaps.Load(channelId)
	if !ok {
		return false
	}
	cap := v.(lanePromptCap)
	if time.Now().After(cap.until) {
		lanePromptCaps.Delete(channelId)
		return false
	}
	return promptTokens >= cap.tokens
}
