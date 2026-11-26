package setting

import (
	"encoding/json"
	"strconv"
	"strings"
)

var DigistoreEnabled = false
var DigistoreSubscriptionEnabled = false

// The IPN passphrase set on the Digistore24 IPN connection, the key of sha_sign.
var DigistoreIpnPassphrase = ""

// While test mode is on, only these user ids (comma separated) see the method,
// only api_mode=test IPNs for their orders are applied and live IPNs wait.
var DigistoreTestMode = false
var DigistoreTestUserIds = ""

// JSON objects: top-up amount in USD -> Digistore24 product id, and our
// subscription plan id -> product id, e.g. {"10":"612345"}.
var DigistoreTopUpProducts = "{}"
var DigistoreSubscriptionProducts = "{}"

func DigistoreAllowedFor(userId int) bool {
	return !DigistoreTestMode || DigistoreTestUser(userId)
}

func DigistoreTestUser(userId int) bool {
	return userIdListed(DigistoreTestUserIds, userId)
}

func parseDigistoreProducts(raw string) map[int]string {
	byKey := map[string]string{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &byKey); err != nil {
		return map[int]string{}
	}
	out := make(map[int]string, len(byKey))
	for k, v := range byKey {
		n, err := strconv.Atoi(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if err != nil || n <= 0 || v == "" {
			continue
		}
		out[n] = v
	}
	return out
}

func GetDigistoreTopUpProducts() map[int]string {
	return parseDigistoreProducts(DigistoreTopUpProducts)
}

func GetDigistoreSubscriptionProducts() map[int]string {
	return parseDigistoreProducts(DigistoreSubscriptionProducts)
}
