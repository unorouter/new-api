package service

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

const (
	cooccurModeOff     = 0
	cooccurModeObserve = 1
	cooccurModeEnforce = 2

	cooccurFlagTTL = 24 * time.Hour
)

// fingerprintHeaders are hashed by presence only. Which of them a client sends is
// fixed by its HTTP library and rarely by the operator, so the set separates
// scripted clients from each other without touching any value.
var fingerprintHeaders = []string{
	"Accept", "Accept-Language", "Accept-Encoding", "Content-Type", "Authorization",
	"Origin", "Referer", "Sec-Fetch-Mode", "X-Title", "HTTP-Referer",
}

// ClientFingerprint names the client software behind a request: the exact
// User-Agent plus which attribution headers it sends. Empty for browsers, whose
// fingerprints are shared by thousands of people and would cluster humans.
func ClientFingerprint(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	h := c.Request.Header
	if h.Get("Origin") != "" || h.Get("Referer") != "" || h.Get("Sec-Fetch-Mode") != "" {
		return ""
	}
	sum := sha1.New()
	sum.Write([]byte(c.Request.UserAgent()))
	for _, name := range fingerprintHeaders {
		if _, ok := h[name]; ok {
			sum.Write([]byte{0})
			sum.Write([]byte(name))
		}
	}
	return hex.EncodeToString(sum.Sum(nil))[:16]
}

// ClientNetwork is the /24 or /48 the request came from, coarse enough to log
// against every row without recording the address itself.
func ClientNetwork(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return registrationNetworkKey(c.ClientIP())
}

func cooccurBanKey(userId int) string {
	return fmt.Sprintf("shadowBan:user:%d", userId)
}

// TrackFreeCooccurrence records that this zero-balance, identity-free account
// just used a free model from this client IP and this client fingerprint. A farm
// keeps each account under the per-account limit by rotating hundreds of them
// through one host, so the number of distinct accounts on one constraint inside
// one window is the thing it cannot hide: browsers top out at five, the farms run
// sixty to a hundred. Crossing the threshold shadow bans every account in the
// window, and any account that later shows up on the flagged constraint.
func TrackFreeCooccurrence(c *gin.Context, userId int) {
	if userId <= 0 || c == nil || c.Request == nil || !common.RedisEnabled {
		return
	}
	setting := operation_setting.GetQuotaSetting()
	mode := setting.FreeAbuseCooccurMode
	if mode == cooccurModeOff {
		return
	}
	window := setting.FreeAbuseCooccurWindowSeconds
	if window <= 0 {
		window = 600
	}
	banDays := setting.FreeAbuseCooccurBanDays
	if banDays <= 0 {
		banDays = 7
	}
	banTTL := time.Duration(banDays) * 24 * time.Hour
	bucket := time.Now().Unix() / int64(window)
	if ip := c.ClientIP(); ip != "" && setting.FreeAbuseCooccurIpMinAccounts > 0 {
		trackCooccurrence("ip", ip, ip, userId, bucket, window, setting.FreeAbuseCooccurIpMinAccounts, mode, banTTL)
	}
	if fp := ClientFingerprint(c); fp != "" && setting.FreeAbuseCooccurFpMinAccounts > 0 {
		label := fp + " " + truncateAttribution(c.Request.UserAgent())
		trackCooccurrence("fp", fp, label, userId, bucket, window, setting.FreeAbuseCooccurFpMinAccounts, mode, banTTL)
	}
}

func trackCooccurrence(kind, key, label string, userId int, bucket int64, window, min, mode int, banTTL time.Duration) {
	ctx := context.Background()
	setKey := fmt.Sprintf("freeAbuseCo:%s:%s:%d", kind, key, bucket)
	flagKey := fmt.Sprintf("freeAbuseCoFlag:%s:%s", kind, key)
	enforce := mode == cooccurModeEnforce

	pipe := common.RDB.TxPipeline()
	pipe.SAdd(ctx, setKey, userId)
	pipe.Expire(ctx, setKey, time.Duration(2*window)*time.Second)
	card := pipe.SCard(ctx, setKey)
	flagged := pipe.Exists(ctx, flagKey)
	if _, err := pipe.Exec(ctx); err != nil {
		return
	}
	if flagged.Val() > 0 {
		if enforce {
			common.RDB.Set(ctx, cooccurBanKey(userId), "1", banTTL)
		}
		return
	}
	n := int(card.Val())
	if n < min {
		return
	}

	pipe = common.RDB.TxPipeline()
	pipe.Set(ctx, flagKey, "1", cooccurFlagTTL)
	members := pipe.SMembers(ctx, setKey)
	if _, err := pipe.Exec(ctx); err != nil {
		return
	}
	banned := 0
	if enforce {
		pipe = common.RDB.TxPipeline()
		for _, member := range members.Val() {
			pipe.Set(ctx, "shadowBan:user:"+member, "1", banTTL)
		}
		if _, err := pipe.Exec(ctx); err == nil {
			banned = len(members.Val())
		}
	}
	prefix := ""
	if !enforce {
		prefix = "observe: "
	}
	common.SysLog(fmt.Sprintf("%sfree abuse: %s %s flagged, %d anonymous accounts in %ds, %d shadow banned",
		prefix, kind, label, n, window, banned))
}

// cooccurrenceShadowBanned is the request-time half of the verdict, kept in Redis
// so every instance sees the same bans and a rollout does not reset them.
func cooccurrenceShadowBanned(userId int) bool {
	if !common.RedisEnabled || operation_setting.GetQuotaSetting().FreeAbuseCooccurMode != cooccurModeEnforce {
		return false
	}
	n, err := common.RDB.Exists(context.Background(), cooccurBanKey(userId)).Result()
	return err == nil && n > 0
}
