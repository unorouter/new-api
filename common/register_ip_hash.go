package common

import (
	"log"
	"os"
	"sync"
)

// RegisterIpHash turns a registration IP into a keyed, non-reversible marker.
//
// The abuse caps only ever ask "is this the same address as before", never "what
// address was it", so the column can hold an HMAC instead of the address itself.
// That lets the plaintext be cleared after 30 days (Art. 5(1)(e) GDPR) while the
// cap keeps working across the whole history, which is what it needs: account
// farms play out over months, and the cap counts soft-deleted rows too.
//
// Empty in, empty out. publicClientIp returns "" whenever no trusted, public,
// routable address is available, which is a common and legitimate state meaning
// "unknown". Hashing that would put every unknown-IP account on one marker, so
// the per-IP cap would fire for all of them and the network scorer would read
// them as a farm.
func RegisterIpHash(ip string) string {
	if ip == "" {
		return ""
	}
	return GenerateHMACWithKey(registerIpHashKey(), ip)
}

var (
	registerIpHashKeyOnce  sync.Once
	registerIpHashKeyValue []byte
)

// Its own key rather than CryptoSecret, because CryptoSecret falls back to
// SessionSecret (common/init.go) and rotating that would silently stop every
// comparison from matching: old rows keep old markers, new rows get new ones,
// and the cap reports "no earlier account" for everybody with no error anywhere.
// Falls back with a warning so a dev box without the key still runs.
func registerIpHashKey() []byte {
	registerIpHashKeyOnce.Do(func() {
		if key := os.Getenv("REGISTER_IP_HASH_KEY"); key != "" {
			registerIpHashKeyValue = []byte(key)
			return
		}
		log.Println("WARNING: REGISTER_IP_HASH_KEY is unset, falling back to CryptoSecret. " +
			"Registration IP markers are then tied to SESSION_SECRET and every stored marker " +
			"becomes uncomparable if it is rotated, disabling the per-IP registration cap silently.")
		registerIpHashKeyValue = []byte(CryptoSecret)
	})
	return registerIpHashKeyValue
}
