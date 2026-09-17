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

// A lane that cannot serve a long prompt rarely says so: it accepts the request
// and never answers. On 2026-09-17, 16 of 36 gpt lanes had zero successes above
// 100k tokens while near perfect below 50k, and a customer sending 200k prompts
// failed 99% because the picker drew blind across all of them. Whole-lane health
// cannot see this (small prompts keep the rate green), so capacity is learned per
// lane from outcomes: the largest prompt it completed, and the sizes it stalled on.
//
// Redis only. Without it every lane reads Unknown, which is the old behaviour.
type LaneFit int

const (
	LaneFitUnknown LaneFit = iota
	LaneFitProven
	LaneFitRejected
)

// Below this a stall is ordinary capacity and belongs to the failure-rate guard.
const LongPromptFloor = 50_000

const (
	lanePromptProvenTTL = 24 * time.Hour
	// Shorter than the refusal cap: a stall is weaker evidence than a 413.
	lanePromptStallTTL = 6 * time.Hour
	// Stalls above the proven size needed before the lane stops getting that size.
	lanePromptStallsToCap = 3
)

func lanePromptProvenKey(channelId int) string {
	return fmt.Sprintf("lane_prompt_proven:%d", channelId)
}

func lanePromptStallsKey(channelId int) string {
	return fmt.Sprintf("lane_prompt_stalls:%d", channelId)
}

// KEYS: refusal cap, proven, stalls. ARGV: prompt tokens, floor, stalls to cap.
// Proven stretches 25% past the largest completed prompt: sizes are estimates,
// and a lane that served 190k serves 200k.
const lanePromptFitScript = `
local p = tonumber(ARGV[1])
local cap = tonumber(redis.call('GET', KEYS[1]) or '0')
if cap > 0 and p >= cap then return 2 end
if p < tonumber(ARGV[2]) then return 0 end
local proven = tonumber(redis.call('GET', KEYS[2]) or '0')
if proven * 5 >= p * 4 then return 1 end
local nth = tonumber(ARGV[3])
local s = redis.call('ZRANGEBYSCORE', KEYS[3], '(' .. proven, '+inf', 'WITHSCORES', 'LIMIT', nth - 1, 1)
if s[2] and p >= tonumber(s[2]) then return 2 end
return 0`

// LanePromptFit reports whether the lane is known to serve, or known to fail, a
// prompt this size. A refusal cap applies at any size; the learned long-prompt
// verdicts only from LongPromptFloor up.
func LanePromptFit(channelId int, promptTokens int) LaneFit {
	if promptTokens <= 0 {
		return LaneFitUnknown
	}
	if !common.RedisEnabled {
		if LaneRejectsPrompt(channelId, promptTokens) {
			return LaneFitRejected
		}
		return LaneFitUnknown
	}
	fit, err := common.RDB.Eval(context.Background(), lanePromptFitScript,
		[]string{lanePromptCapKey(channelId), lanePromptProvenKey(channelId), lanePromptStallsKey(channelId)},
		promptTokens, LongPromptFloor, lanePromptStallsToCap,
	).Int()
	if err != nil {
		common.SysError("lane prompt fit Redis eval failed: " + err.Error())
		return LaneFitUnknown
	}
	return LaneFit(fit)
}

// Returns how many stalls sit above the proven size after this one.
const lanePromptStallScript = `
redis.call('ZADD', KEYS[2], ARGV[1], ARGV[2])
redis.call('EXPIRE', KEYS[2], ARGV[3])
local proven = tonumber(redis.call('GET', KEYS[1]) or '0')
return redis.call('ZCOUNT', KEYS[2], '(' .. proven, '+inf')`

// RecordLaneStall notes that the lane never answered a prompt this size, and
// reports the stalls now standing above its proven size. lanePromptStallsToCap
// of them is the moment the lane stops receiving prompts that large.
func RecordLaneStall(channelId int, promptTokens int, requestId string) int {
	if promptTokens < LongPromptFloor || requestId == "" || !common.RedisEnabled {
		return 0
	}
	stalls, err := common.RDB.Eval(context.Background(), lanePromptStallScript,
		[]string{lanePromptProvenKey(channelId), lanePromptStallsKey(channelId)},
		promptTokens, requestId, int(lanePromptStallTTL.Seconds()),
	).Int()
	if err != nil {
		common.SysError("lane prompt stall Redis eval failed: " + err.Error())
		return 0
	}
	return stalls
}

// A success outranks every stall at or below its size. Returns the stalls it cleared.
const lanePromptServedScript = `
local cur = tonumber(redis.call('GET', KEYS[1]) or '0')
local p = tonumber(ARGV[1])
if p > cur then
  redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
else
  redis.call('EXPIRE', KEYS[1], ARGV[2])
end
return redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', ARGV[1])`

func RecordLanePromptServed(channelId int, promptTokens int) int {
	if promptTokens < LongPromptFloor || !common.RedisEnabled {
		return 0
	}
	cleared, err := common.RDB.Eval(context.Background(), lanePromptServedScript,
		[]string{lanePromptProvenKey(channelId), lanePromptStallsKey(channelId)},
		promptTokens, int(lanePromptProvenTTL.Seconds()),
	).Int()
	if err != nil {
		common.SysError("lane prompt served Redis eval failed: " + err.Error())
		return 0
	}
	return cleared
}

// LanePromptCapacity is the learned state for the admin view: the largest prompt
// the lane completed and the size from which it is passed over (0 = none).
func LanePromptCapacity(channelId int) (provenTokens int, rejectsFrom int) {
	if !common.RedisEnabled {
		return 0, 0
	}
	ctx := context.Background()
	provenTokens, _ = common.RDB.Get(ctx, lanePromptProvenKey(channelId)).Int()
	rejectsFrom, _ = common.RDB.Get(ctx, lanePromptCapKey(channelId)).Int()
	stalled, err := common.RDB.ZRangeByScoreWithScores(ctx, lanePromptStallsKey(channelId), &redis.ZRangeBy{
		Min: "(" + strconv.Itoa(provenTokens), Max: "+inf", Offset: lanePromptStallsToCap - 1, Count: 1,
	}).Result()
	if err == nil && len(stalled) == 1 {
		if stallCap := int(stalled[0].Score); rejectsFrom == 0 || stallCap < rejectsFrom {
			rejectsFrom = stallCap
		}
	}
	return provenTokens, rejectsFrom
}
