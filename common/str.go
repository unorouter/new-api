package common

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unsafe"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"

	"github.com/samber/lo"
)

const LocalLogContentLimit = 2048

// LocalLogPreview limits log-only content unless debug logging is enabled.
func LocalLogPreview(content string) string {
	if DebugEnabled || len(content) <= LocalLogContentLimit {
		return content
	}
	return fmt.Sprintf("%s... [truncated, original_length=%d, limit=%d]", content[:LocalLogContentLimit], len(content), LocalLogContentLimit)
}

// dataUriBase64Pattern matches `data:<mime>;base64,<payload>` inline media.
// Keeps the mime so the log still says WHAT it was, drops the payload bytes.
var dataUriBase64Pattern = regexp.MustCompile(`data:([\w.+-]+/[\w.+-]+)?;base64,[A-Za-z0-9+/=]+`)

// bareBase64Pattern matches long standalone base64 runs (>=512 chars) that are
// not part of a data: URI, e.g. a raw `"b64_json":"<...>"` image payload.
var bareBase64Pattern = regexp.MustCompile(`[A-Za-z0-9+/]{512,}={0,2}`)

const debugBodyLimit = 4096

// ElideBase64 replaces inline base64 media payloads with a short
// `[base64 <mime> elided, N bytes]` marker, preserving the surrounding JSON and
// the mime/size metadata. A 500KB base64 image becomes a one-line note while the
// request/response shape stays readable and greppable.
func ElideBase64(content string) string {
	content = dataUriBase64Pattern.ReplaceAllStringFunc(content, func(m string) string {
		idx := strings.Index(m, "base64,")
		mime := strings.TrimSuffix(strings.TrimPrefix(m[:idx], "data:"), ";")
		if mime == "" {
			mime = "unknown"
		}
		return fmt.Sprintf("data:%s;base64,[elided %d bytes]", mime, len(m)-idx-7)
	})
	content = bareBase64Pattern.ReplaceAllStringFunc(content, func(m string) string {
		return fmt.Sprintf("[base64 elided, %d bytes]", len(m))
	})
	return content
}

// DebugBodyPreview sanitizes a request/response body for DEBUG logging: it
// elides base64 media (keeping mime + size) and then caps total length, so the
// logs stay diagnostic (model, params, message shape) without dumping image
// bytes or megabyte RP contexts that bloat disk and break grep.
func DebugBodyPreview(content string) string {
	content = ElideBase64(content)
	if len(content) <= debugBodyLimit {
		return content
	}
	return fmt.Sprintf("%s... [truncated, original_length=%d, limit=%d]", content[:debugBodyLimit], len(content), debugBodyLimit)
}

func GetStringIfEmpty(str string, defaultValue string) string {
	if str == "" {
		return defaultValue
	}
	return str
}

func GetRandomString(length int) string {
	if length <= 0 {
		return ""
	}
	return lo.RandomString(length, lo.AlphanumericCharset)
}

func MapToJsonStr(m map[string]interface{}) string {
	bytes, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(bytes)
}

func StrToMap(str string) (map[string]interface{}, error) {
	m := make(map[string]interface{})
	err := Unmarshal([]byte(str), &m)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func StrToJsonArray(str string) ([]interface{}, error) {
	var js []interface{}
	err := json.Unmarshal([]byte(str), &js)
	if err != nil {
		return nil, err
	}
	return js, nil
}

func IsJsonArray(str string) bool {
	var js []interface{}
	return json.Unmarshal([]byte(str), &js) == nil
}

func IsJsonObject(str string) bool {
	var js map[string]interface{}
	return json.Unmarshal([]byte(str), &js) == nil
}

func String2Int(str string) int {
	num, err := strconv.Atoi(str)
	if err != nil {
		return 0
	}
	return num
}

// IsAllowedRedirectURI checks if a redirect URI's origin is in the allowed list.
func IsAllowedRedirectURI(redirectURI string) bool {
	if len(OAuthAllowedRedirectOrigins) == 0 {
		return false
	}
	parsed, err := url.Parse(redirectURI)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	// Only allow https, with a narrow http exception for loopback development.
	host := parsed.Hostname()
	isLoopback := host == "localhost" || host == "127.0.0.1"
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopback) {
		return false
	}
	origin := parsed.Scheme + "://" + parsed.Host
	for _, allowed := range OAuthAllowedRedirectOrigins {
		if strings.EqualFold(origin, allowed) {
			return true
		}
	}
	return false
}

// redisStoreOnce stores v as JSON in Redis under prefix:randomKey with the given TTL.
func redisStoreOnce(prefix string, v any, ttl time.Duration) (string, error) {
	if !RedisEnabled {
		return "", errors.New("redis is required for cross-origin OAuth")
	}
	key := GetRandomString(32)
	data, err := Marshal(v)
	if err != nil {
		return "", err
	}
	if err := RedisSet(prefix+key, string(data), ttl); err != nil {
		return "", err
	}
	return key, nil
}

// redisRedeemOnce retrieves, deletes, and unmarshals a one-time Redis value.
func redisRedeemOnce[T any](prefix, key string) *T {
	if !RedisEnabled {
		return nil
	}
	raw, err := RedisGet(prefix + key)
	if err != nil {
		return nil
	}
	_ = RedisDel(prefix + key)
	var v T
	if Unmarshal([]byte(raw), &v) != nil {
		return nil
	}
	return &v
}

// --- Cross-origin OAuth state (redirect_uri + aff, 10min TTL) ---

type OAuthStateData struct {
	RedirectURI string `json:"r"`
	Aff         string `json:"a,omitempty"`
	UserID      int    `json:"u,omitempty"`
	Action      string `json:"act,omitempty"`
}

func CreateOAuthState(data *OAuthStateData) (string, error) {
	return redisStoreOnce("oauth_state:", data, 10*time.Minute)
}

func RedeemOAuthState(state string) *OAuthStateData {
	data := redisRedeemOnce[OAuthStateData]("oauth_state:", state)
	if data == nil || !IsAllowedRedirectURI(data.RedirectURI) {
		return nil
	}
	return data
}

// --- One-time OAuth exchange code (user credentials, 30s TTL) ---

type OAuthExchangeData struct {
	AccessToken     string `json:"access_token"`
	AccessExpiresAt int64  `json:"access_expires_at,omitempty"`
	UserID          int    `json:"user_id"`
	Username        string `json:"username"`
	DisplayName     string `json:"display_name"`
	Role            int    `json:"role"`
	Action          string `json:"action,omitempty"`
}

func StoreOAuthExchangeCode(data *OAuthExchangeData) (string, error) {
	return redisStoreOnce("oauth_exchange:", data, 30*time.Second)
}

func RedeemOAuthExchangeCode(code string) *OAuthExchangeData {
	return redisRedeemOnce[OAuthExchangeData]("oauth_exchange:", code)
}

func StringsContains(strs []string, str string) bool {
	for _, s := range strs {
		if s == str {
			return true
		}
	}
	return false
}

// StringToByteSlice []byte only read, panic on append
func StringToByteSlice(s string) []byte {
	tmp1 := (*[2]uintptr)(unsafe.Pointer(&s))
	tmp2 := [3]uintptr{tmp1[0], tmp1[1], tmp1[1]}
	return *(*[]byte)(unsafe.Pointer(&tmp2))
}

func EncodeBase64(str string) string {
	return base64.StdEncoding.EncodeToString([]byte(str))
}

func GetJsonString(data any) string {
	if data == nil {
		return ""
	}
	b, _ := json.Marshal(data)
	return string(b)
}

// NormalizeBillingPreference clamps the billing preference to valid values.
func NormalizeBillingPreference(pref string) string {
	switch strings.TrimSpace(pref) {
	case "subscription_first", "wallet_first", "subscription_only", "wallet_only":
		return strings.TrimSpace(pref)
	default:
		return "subscription_first"
	}
}

// MaskEmail masks a user email to prevent PII leakage in logs
// Returns "***masked***" if email is empty, otherwise shows only the domain part
func MaskEmail(email string) string {
	if email == "" {
		return "***masked***"
	}

	// Find the @ symbol
	atIndex := strings.Index(email, "@")
	if atIndex == -1 {
		// No @ symbol found, return masked
		return "***masked***"
	}

	// Return only the domain part with @ symbol
	return "***@" + email[atIndex+1:]
}

// MaskSensitiveInfo moved to the conversion kit (kitutil) because the types
// package error formatting depends on it; host callers keep this name.
func MaskSensitiveInfo(str string) string {
	return kitutil.MaskSensitiveInfo(str)
}
