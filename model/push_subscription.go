package model

import (
	"time"
)

// PushSubscription is a device-scoped web push subscription. Anonymous by
// design: keyed by the push endpoint, no user account required.
type PushSubscription struct {
	Id           int    `json:"id" gorm:"primaryKey"`
	Endpoint     string `json:"endpoint" gorm:"type:text;not null"`
	EndpointHash string `json:"endpoint_hash" gorm:"type:varchar(64);uniqueIndex;not null"`
	P256dh       string `json:"p256dh" gorm:"type:text"`
	Auth         string `json:"auth" gorm:"type:text"`
	Topics       string `json:"topics" gorm:"type:text"`
	Locale       string `json:"locale" gorm:"type:varchar(16);default:'en'"`
	UserAgent    string `json:"user_agent" gorm:"type:varchar(255)"`
	CreatedAt    int64  `json:"created_at" gorm:"bigint"`
	LastSeenAt   int64  `json:"last_seen_at" gorm:"bigint"`
	FailureCount int    `json:"failure_count" gorm:"default:0"`
}

func UpsertPushSubscription(sub *PushSubscription) error {
	now := time.Now().Unix()
	var existing PushSubscription
	err := DB.Where("endpoint_hash = ?", sub.EndpointHash).First(&existing).Error
	if err == nil {
		return DB.Model(&existing).Select("endpoint", "p256dh", "auth", "topics", "locale", "user_agent", "last_seen_at", "failure_count").Updates(map[string]interface{}{
			"endpoint":      sub.Endpoint,
			"p256dh":        sub.P256dh,
			"auth":          sub.Auth,
			"topics":        sub.Topics,
			"locale":        sub.Locale,
			"user_agent":    sub.UserAgent,
			"last_seen_at":  now,
			"failure_count": 0,
		}).Error
	}
	sub.CreatedAt = now
	sub.LastSeenAt = now
	return DB.Create(sub).Error
}

func DeletePushSubscriptionByEndpointHash(endpointHash string) error {
	return DB.Where("endpoint_hash = ?", endpointHash).Delete(&PushSubscription{}).Error
}

func GetAllPushSubscriptions() ([]PushSubscription, error) {
	var subs []PushSubscription
	err := DB.Find(&subs).Error
	return subs, err
}

func BumpPushSubscriptionFailure(id int, maxFailures int) {
	var sub PushSubscription
	if err := DB.First(&sub, id).Error; err != nil {
		return
	}
	sub.FailureCount++
	if sub.FailureCount >= maxFailures {
		_ = DB.Delete(&PushSubscription{}, id).Error
		return
	}
	_ = DB.Model(&PushSubscription{}).Where("id = ?", id).Update("failure_count", sub.FailureCount).Error
}

func ResetPushSubscriptionFailure(id int) {
	_ = DB.Model(&PushSubscription{}).Where("id = ?", id).Updates(map[string]interface{}{
		"failure_count": 0,
		"last_seen_at":  time.Now().Unix(),
	}).Error
}

// PrunePushSubscriptions removes subscriptions not seen for maxAge.
func PrunePushSubscriptions(maxAge time.Duration) (int64, error) {
	cutoff := time.Now().Add(-maxAge).Unix()
	res := DB.Where("last_seen_at < ?", cutoff).Delete(&PushSubscription{})
	return res.RowsAffected, res.Error
}
