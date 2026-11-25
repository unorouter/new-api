package setting

import (
	"encoding/json"
	"strconv"
	"strings"
)

var FreemiusEnabled = false
var FreemiusSubscriptionEnabled = false
var FreemiusProductId = ""
var FreemiusPublicKey = ""

// The product secret key signs both the webhook body and the sandbox checkout
// token, so it doubles as the webhook secret.
var FreemiusSecretKey = ""
var FreemiusSandbox = false

// While sandbox checkout links are live, only these user ids (comma separated)
// see and use the method; a sandbox payment is a test card, never real money.
var FreemiusSandboxUserIds = ""

func FreemiusAllowedFor(userId int) bool {
	if !FreemiusSandbox {
		return true
	}
	for _, f := range strings.Split(FreemiusSandboxUserIds, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil && n > 0 && n == userId {
			return true
		}
	}
	return false
}

// Freemius prices are fixed per plan, so every sellable amount is its own plan
// with one pricing. JSON objects: top-up amount in USD -> ids, and our
// subscription plan id -> ids, e.g. {"10":{"plan_id":"81724","pricing_id":"123911"}}.
var FreemiusTopUpPlans = "{}"
var FreemiusSubscriptionPlans = "{}"

type FreemiusPlanRef struct {
	PlanId    string `json:"plan_id"`
	PricingId string `json:"pricing_id"`
}

func parseFreemiusPlans(raw string) map[int]FreemiusPlanRef {
	byKey := map[string]FreemiusPlanRef{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &byKey); err != nil {
		return map[int]FreemiusPlanRef{}
	}
	out := make(map[int]FreemiusPlanRef, len(byKey))
	for k, v := range byKey {
		n, err := strconv.Atoi(strings.TrimSpace(k))
		if err != nil || v.PlanId == "" || v.PricingId == "" {
			continue
		}
		out[n] = v
	}
	return out
}

func GetFreemiusTopUpPlans() map[int]FreemiusPlanRef {
	return parseFreemiusPlans(FreemiusTopUpPlans)
}

func GetFreemiusSubscriptionPlans() map[int]FreemiusPlanRef {
	return parseFreemiusPlans(FreemiusSubscriptionPlans)
}
