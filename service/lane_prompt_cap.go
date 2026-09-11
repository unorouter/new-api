package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
)

// A lane's context ceiling, learned from its own 413: a merchant's cap is often
// below the model's, and nothing in the listing says so. The lowest prompt size
// it refused is remembered for a day so the picker routes bigger prompts past
// it; a raised limit is relearned when the entry expires.
//
// Shared through Redis because the gateway runs several replicas: a per-process
// map made every replica pay its own 413 to learn the same ceiling, and a roll
// forgot all of them. One lane cost 1,150 refusals in a day that way. The
// in-process map stays as the fallback when Redis is down.
type lanePromptCap struct {
	tokens int
	until  time.Time
}

const lanePromptCapTTL = 24 * time.Hour

var lanePromptCaps sync.Map // channel id -> lanePromptCap, fallback when Redis is unavailable

func lanePromptCapKey(channelId int) string {
	return fmt.Sprintf("lane_prompt_cap:%d", channelId)
}

// Only a LOWER ceiling replaces a stored one, so the compare and the write have
// to be one step: two replicas learning different sizes at once would otherwise
// race and the larger could win.
const lanePromptCapScript = `
local cur = redis.call('GET', KEYS[1])
if cur and tonumber(cur) <= tonumber(ARGV[1]) then
  return tonumber(cur)
end
redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
return tonumber(ARGV[1])`

func RecordLanePromptCap(channelId int, promptTokens int) {
	if promptTokens <= 0 {
		return
	}
	if common.RedisEnabled {
		err := common.RDB.Eval(context.Background(), lanePromptCapScript,
			[]string{lanePromptCapKey(channelId)},
			promptTokens, int(lanePromptCapTTL.Seconds()),
		).Err()
		if err == nil {
			return
		}
		common.SysError("lane prompt cap Redis eval failed: " + err.Error())
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
	if common.RedisEnabled {
		v, err := common.RDB.Get(context.Background(), lanePromptCapKey(channelId)).Result()
		if err == nil {
			capped, convErr := strconv.Atoi(v)
			return convErr == nil && capped > 0 && promptTokens >= capped
		}
		// redis.Nil means nothing learned yet; any other error falls through to the
		// in-process map rather than routing blind.
		if errors.Is(err, redis.Nil) {
			return false
		}
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
