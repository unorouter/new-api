package common

import "strconv"

// ShadowBanUserKey is the Redis key that holds a request-time free-model shadow
// ban for one account. Shared between the service that sets it and the model
// layer that lifts it on an identity bind or a top-up.
func ShadowBanUserKey(userId int) string {
	return "shadowBan:user:" + strconv.Itoa(userId)
}
