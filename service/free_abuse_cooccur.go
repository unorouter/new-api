package service

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
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
	return common.ShadowBanUserKey(userId)
}

// TrackFreeCooccurrence records that this zero-balance, identity-free account
// just used a free model from this client IP, this client fingerprint and this
// network. A farm keeps each account under the per-account limit by rotating
// hundreds of them through one host, so the number of distinct accounts on one
// constraint inside one window is the thing it cannot hide: browsers top out at
// five, the farms run sixty to a hundred.
//
// Every constraint is paired with a second axis (IP with the client fingerprint,
// fingerprint with the network, network with the fingerprint). A window only
// counts when its accounts are concentrated on that axis, and only the accounts
// in a concentrated group are banned: the farm's accounts all share the host's
// fingerprint or network, while a real person who happens to use the same
// library or the same VPN exit sits alone in his own group and is left alone.
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
	ip := publicClientIP(c)
	fp := ClientFingerprint(c)
	network := registrationNetworkKey(ip)
	if ip != "" && setting.FreeAbuseCooccurIpMinAccounts > 0 {
		spread := fp
		if spread == "" {
			spread = "web"
		}
		trackCooccurrence("ip", ip, ip, userId, spread, bucket, window, setting.FreeAbuseCooccurIpMinAccounts, mode, banTTL)
	}
	if fp == "" {
		return
	}
	if setting.FreeAbuseCooccurFpMinAccounts > 0 {
		label := fp + " " + truncateAttribution(c.Request.UserAgent())
		trackCooccurrence("fp", fp, label, userId, network, bucket, window, setting.FreeAbuseCooccurFpMinAccounts, mode, banTTL)
	}
	if network != "" && setting.FreeAbuseCooccurNetMinAccounts > 0 {
		trackCooccurrence("net", network, network, userId, fp, bucket, window, setting.FreeAbuseCooccurNetMinAccounts, mode, banTTL)
	}
}

// cooccurSpreadFactor is how many accounts a group on the paired axis needs
// before the window counts as concentrated and the group's members are banned.
const cooccurSpreadFactor = 5

func trackCooccurrence(kind, key, label string, userId int, spread string, bucket int64, window, min, mode int, banTTL time.Duration) {
	ctx := context.Background()
	hashKey := fmt.Sprintf("freeAbuseCo:%s:%s:%d", kind, key, bucket)
	flagKey := fmt.Sprintf("freeAbuseCoFlag:%s:%s", kind, key)
	enforce := mode == cooccurModeEnforce
	if spread == "" {
		spread = "-"
	}

	pipe := common.RDB.TxPipeline()
	pipe.HSet(ctx, hashKey, strconv.Itoa(userId), spread)
	pipe.Expire(ctx, hashKey, time.Duration(2*window)*time.Second)
	size := pipe.HLen(ctx, hashKey)
	if _, err := pipe.Exec(ctx); err != nil {
		return
	}
	n := int(size.Val())
	if n < min {
		return
	}
	members, err := common.RDB.HGetAll(ctx, hashKey).Result()
	if err != nil {
		return
	}
	groups := make(map[string]int)
	for _, g := range members {
		groups[g]++
	}
	if len(groups)*cooccurSpreadFactor > n {
		return
	}
	if enforce && groups[spread] >= cooccurSpreadFactor {
		common.RDB.Set(ctx, cooccurBanKey(userId), "1", banTTL)
	}
	set, err := common.RDB.SetNX(ctx, flagKey, "1", cooccurFlagTTL).Result()
	if err != nil || !set {
		return
	}
	banned := 0
	if enforce {
		pipe = common.RDB.TxPipeline()
		for member, g := range members {
			if groups[g] >= cooccurSpreadFactor {
				pipe.Set(ctx, "shadowBan:user:"+member, "1", banTTL)
				banned++
			}
		}
		if _, err := pipe.Exec(ctx); err != nil {
			banned = 0
		}
	}
	prefix := ""
	if !enforce {
		prefix = "observe: "
	}
	common.SysLog(fmt.Sprintf("%sfree abuse: %s %s flagged, %d anonymous accounts on %d %s in %ds, %d shadow banned",
		prefix, kind, label, n, len(groups), spreadNoun(kind), window, banned))
}

func spreadNoun(kind string) string {
	if kind == "fp" {
		return "networks"
	}
	return "clients"
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

// FreeFarmBanned tracks a zero-balance account's free request and reports whether
// the farm verdict shadow bans it. Accounts with no proof at all go through the
// classic tracker. An account whose only proof is a Google login or a mainstream
// email, that never paid and calls from a script, goes through a second tracker on
// its own keys: farms moved to bulk Google accounts once those exempted them. The
// client fingerprint stays out of that tracker, because one app's library is shared
// by hundreds of real people who signed in with Google.
func FreeFarmBanned(c *gin.Context, userId int, verified, strongIdentity bool, usedQuota int) bool {
	if !verified {
		TrackFreeCooccurrence(c, userId)
		return FreeModelsShadowBanned(userId)
	}
	if usedQuota > 0 || strongIdentity || !scriptedClient(c) {
		return false
	}
	mode := operation_setting.GetQuotaSetting().FreeAbuseCooccurWeakMode
	trackWeakCooccurrence(c, userId, mode)
	if mode != cooccurModeEnforce {
		return false
	}
	n, err := common.RDB.Exists(context.Background(), cooccurBanKey(userId)).Result()
	return err == nil && n > 0
}

// scriptedClient is a request without browser headers. The JanitorAI app is a
// native client used by real people, so it counts as a browser here.
func scriptedClient(c *gin.Context) bool {
	return ClientFingerprint(c) != "" && !strings.HasPrefix(c.Request.UserAgent(), "JanitorAIApp/")
}

func trackWeakCooccurrence(c *gin.Context, userId, mode int) {
	if userId <= 0 || mode == cooccurModeOff || !common.RedisEnabled {
		return
	}
	setting := operation_setting.GetQuotaSetting()
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
	ip := publicClientIP(c)
	fp := ClientFingerprint(c)
	if ip != "" && setting.FreeAbuseCooccurIpMinAccounts > 0 {
		trackCooccurrence("wip", ip, ip, userId, fp, bucket, window, setting.FreeAbuseCooccurIpMinAccounts, mode, banTTL)
	}
	if network := registrationNetworkKey(ip); network != "" && setting.FreeAbuseCooccurNetMinAccounts > 0 {
		trackCooccurrence("wnet", network, network, userId, fp, bucket, window, setting.FreeAbuseCooccurNetMinAccounts, mode, banTTL)
	}
}

// sharedEgressNets are Cloudflare's published ranges plus its WARP egress. Workers
// proxies (RisuAI routes every user through one) and WARP put thousands of people
// behind a handful of addresses, which read as one host with many accounts.
var sharedEgressNets = func() []*net.IPNet {
	cidrs := []string{
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
		"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
		"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
		"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22", "104.28.0.0/16",
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
		"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32", "2a09:bac0::/29",
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		if _, n, err := net.ParseCIDR(cidr); err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

// publicClientIP is empty for cluster and loopback addresses: those are our own
// services calling on behalf of many users, never one host running many accounts.
// Shared egress is empty too, so only the client fingerprint axis judges it.
func publicClientIP(c *gin.Context) string {
	ip := c.ClientIP()
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.IsPrivate() || parsed.IsLoopback() || parsed.IsLinkLocalUnicast() {
		return ""
	}
	for _, n := range sharedEgressNets {
		if n.Contains(parsed) {
			return ""
		}
	}
	return ip
}
