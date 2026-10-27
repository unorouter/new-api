package model

import (
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// FreeConsent is an archived per-model consent from before provider consents.
// The records are no longer granted or read; they stay until the proof period
// ends so a past consent can still be shown together with its text.
type FreeConsent struct {
	Id        int    `json:"id" gorm:"primaryKey"`
	UserId    int    `json:"user_id" gorm:"index:idx_free_consent_user;not null;default:0"`
	GuestId   string `json:"-" gorm:"type:varchar(64);index:idx_free_consent_guest;not null;default:''"`
	Model     string `json:"model" gorm:"type:varchar(255);not null"`
	Locale    string `json:"locale" gorm:"type:varchar(16);not null;default:''"`
	Text      string `json:"-" gorm:"type:text;not null"`
	CreatedAt int64  `json:"created_at" gorm:"bigint;not null"`
	RevokedAt int64  `json:"revoked_at" gorm:"bigint;not null;default:0"`
	Providers string `json:"-" gorm:"type:text"`
	Countries string `json:"-" gorm:"type:text"`
}

const consentCacheTTL = 30 * time.Second

type consentEntry struct {
	// Aliases with an active provider consent.
	aliases  map[string]bool
	business bool
	expires  time.Time
}

// Per replica: a revoke reaches the other replicas within the TTL.
var consentCache sync.Map

func consentSubject(userId int, guestId string) string {
	if guestId != "" {
		return "g:" + guestId
	}
	return "u:" + strconv.Itoa(userId)
}

func consentWhere(userId int, guestId string) (string, any) {
	if guestId != "" {
		return "guest_id = ? AND revoked_at = 0", guestId
	}
	return "user_id = ? AND guest_id = '' AND revoked_at = 0", userId
}

type ConsentAccess int

const (
	ConsentAccessAllowed ConsentAccess = iota
	ConsentAccessNeedsConsent
	ConsentAccessBusiness
)

// ConsentAccessFor is the relay-path check: every provider now serving the model
// needs an active provider consent. A lookup error denies.
func ConsentAccessFor(userId int, guestId string, providers []string) ConsentAccess {
	entry, err := consentEntryFor(userId, guestId, providers)
	if err != nil {
		common.SysError("failed to load consents: " + err.Error())
		return ConsentAccessNeedsConsent
	}
	if entry.business {
		return ConsentAccessBusiness
	}
	if !entry.covers(providers) {
		return ConsentAccessNeedsConsent
	}
	return ConsentAccessAllowed
}

// consentEntryFor trusts only a cached yes: a consent just given on another
// replica must not be refused for the rest of this replica's TTL.
func consentEntryFor(userId int, guestId string, providers []string) (consentEntry, error) {
	subject := consentSubject(userId, guestId)
	if entry, ok := loadConsentEntry(subject); ok && (entry.business || entry.covers(providers)) {
		return entry, nil
	}
	entry, err := buildConsentEntry(userId, guestId)
	if err != nil {
		return entry, err
	}
	consentCache.Store(subject, entry)
	return entry, nil
}

// covers is false for a provider without an alias: it cannot be consented to yet.
func (e consentEntry) covers(providers []string) bool {
	if len(providers) == 0 {
		return true
	}
	aliases := ProviderAliasesFor(providers)
	for _, p := range providers {
		alias, ok := aliases[p]
		if !ok || !e.aliases[alias.Alias] {
			return false
		}
	}
	return true
}

func loadConsentEntry(subject string) (consentEntry, bool) {
	cached, ok := consentCache.Load(subject)
	if !ok {
		return consentEntry{}, false
	}
	entry, ok := cached.(consentEntry)
	if !ok || time.Now().After(entry.expires) {
		return consentEntry{}, false
	}
	return entry, true
}

func buildConsentEntry(userId int, guestId string) (consentEntry, error) {
	entry := consentEntry{aliases: map[string]bool{}, expires: time.Now().Add(consentCacheTTL)}
	if guestId == "" {
		var customerType string
		if err := DB.Model(&User{}).Where("id = ?", userId).Select("customer_type").Scan(&customerType).Error; err != nil {
			return entry, err
		}
		entry.business = customerType == CustomerTypeBusiness
	}
	consents, err := ActiveProviderConsents(userId, guestId)
	if err != nil {
		return entry, err
	}
	for _, consent := range consents {
		entry.aliases[consent.Alias] = true
	}
	return entry, nil
}

// consentProofYears is how long a consent record outlives the consent, as the
// privacy policy states, so it can still be proven.
const consentProofYears = 3

// A guest id lives in a one-year cookie set before any consent, so a guest's
// consent ends at the latest a year after it was given.
const guestIdLifetime = 365 * 24 * time.Hour

// PruneFreeConsents deletes archived records whose consent ended more than
// three years ago: revoked, the account deleted, or the guest cookie expired.
func PruneFreeConsents(now time.Time) (int64, error) {
	cutoff := now.AddDate(-consentProofYears, 0, 0)
	res := DB.Where(
		"(revoked_at > 0 AND revoked_at < ?)"+
			" OR (revoked_at = 0 AND guest_id <> '' AND created_at < ?)"+
			" OR (revoked_at = 0 AND user_id > 0 AND user_id IN (SELECT id FROM users WHERE deleted_at IS NOT NULL AND deleted_at < ?))"+
			" OR (revoked_at = 0 AND user_id > 0 AND created_at < ? AND NOT EXISTS (SELECT 1 FROM users WHERE users.id = free_consents.user_id))",
		cutoff.Unix(), cutoff.Add(-guestIdLifetime).Unix(), cutoff, cutoff.Unix(),
	).Delete(&FreeConsent{})
	return res.RowsAffected, res.Error
}
