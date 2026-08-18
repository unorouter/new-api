package common

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
)

type verificationValue struct {
	code string
	time time.Time
}

const (
	EmailVerificationPurpose = "v"
	PasswordResetPurpose     = "r"
)

var verificationMutex sync.Mutex
var verificationMap map[string]verificationValue
var verificationMapMaxSize = 10
var VerificationValidMinutes = 10

// verificationRedisKey namespaces codes so a single Redis instance shared across
// swarm replicas holds one authoritative copy (the in-memory map is per-process
// and breaks verification when send and verify land on different replicas).
func verificationRedisKey(key string, purpose string) string {
	return "verification:" + purpose + key
}

func GenerateVerificationCode(length int) string {
	code := uuid.New().String()
	code = strings.Replace(code, "-", "", -1)
	if length == 0 {
		return code
	}
	return code[:length]
}

func RegisterVerificationCodeWithKey(key string, code string, purpose string) {
	if RedisEnabled {
		if err := RedisSet(verificationRedisKey(key, purpose), code, time.Duration(VerificationValidMinutes)*time.Minute); err == nil {
			return
		}
		// fall through to in-memory on Redis failure
	}
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	verificationMap[purpose+key] = verificationValue{
		code: code,
		time: time.Now(),
	}
	if len(verificationMap) > verificationMapMaxSize {
		removeExpiredPairs()
	}
}

func VerifyCodeWithKey(key string, code string, purpose string) bool {
	if RedisEnabled {
		stored, err := RedisGet(verificationRedisKey(key, purpose))
		if err == nil {
			return code == stored
		}
		if !errors.Is(err, redis.Nil) {
			// Redis reachable errors fall through to the in-memory map; a plain
			// cache miss (redis.Nil) means the code truly is absent/expired.
			return verifyCodeInMemory(key, code, purpose)
		}
		return false
	}
	return verifyCodeInMemory(key, code, purpose)
}

func verifyCodeInMemory(key string, code string, purpose string) bool {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	value, okay := verificationMap[purpose+key]
	now := time.Now()
	if !okay || int(now.Sub(value.time).Seconds()) >= VerificationValidMinutes*60 {
		return false
	}
	return code == value.code
}

func DeleteKey(key string, purpose string) {
	if RedisEnabled {
		_ = RedisDel(verificationRedisKey(key, purpose))
	}
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	delete(verificationMap, purpose+key)
}

// no lock inside, so the caller must lock the verificationMap before calling!
func removeExpiredPairs() {
	now := time.Now()
	for key := range verificationMap {
		if int(now.Sub(verificationMap[key].time).Seconds()) >= VerificationValidMinutes*60 {
			delete(verificationMap, key)
		}
	}
}

func init() {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	verificationMap = make(map[string]verificationValue)
}
