package model

import (
	"errors"
	"regexp"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// TermsAcceptance documents that a user accepted one version of the terms of
// use. Text is the dialog or signup notice exactly as shown. Rows are never
// updated; the latest version wins.
type TermsAcceptance struct {
	Id     int `json:"id" gorm:"primaryKey"`
	UserId int `json:"user_id" gorm:"index:idx_terms_acceptance_user;not null"`
	// The date the terms last changed, YYYY-MM-DD, so versions sort as text.
	Version string `json:"version" gorm:"type:varchar(16);not null"`
	// register, oauth or dialog.
	Source    string `json:"source" gorm:"type:varchar(16);not null;default:''"`
	Locale    string `json:"locale" gorm:"type:varchar(16);not null;default:''"`
	Text      string `json:"-" gorm:"type:text;not null"`
	CreatedAt int64  `json:"created_at" gorm:"bigint;not null"`
}

const (
	TermsSourceRegister = "register"
	TermsSourceOAuth    = "oauth"
	TermsSourceDialog   = "dialog"
)

var termsVersionPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// TermsShown is what a signup form displayed about the terms; empty when the
// client sent nothing, which records no acceptance.
type TermsShown struct {
	Version string `json:"version,omitempty"`
	Locale  string `json:"locale,omitempty"`
	Text    string `json:"text,omitempty"`
}

// Valid reports whether the client named a version and the text it showed.
func (t TermsShown) Valid() bool {
	return termsVersionPattern.MatchString(t.Version) && t.Text != "" && len(t.Text) <= 20000 && len(t.Locale) <= 16
}

// RecordTermsAcceptance stores one acceptance.
func RecordTermsAcceptance(userId int, source string, shown TermsShown) error {
	if userId <= 0 {
		return errors.New("terms acceptance needs a user")
	}
	if !shown.Valid() {
		return errors.New("terms acceptance needs a version and the text shown")
	}
	return DB.Create(&TermsAcceptance{UserId: userId, Version: shown.Version, Source: source, Locale: shown.Locale, Text: shown.Text, CreatedAt: common.GetTimestamp()}).Error
}

// LatestTermsAcceptance is the user's acceptance of the newest version, nil
// when the user never accepted any.
func LatestTermsAcceptance(userId int) (*TermsAcceptance, error) {
	var rows []TermsAcceptance
	if err := DB.Where("user_id = ?", userId).Order("version desc").Order("created_at desc").Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// PruneTermsAcceptances drops the records of accounts deleted longer ago than
// the proof period of the consents.
func PruneTermsAcceptances(now time.Time) (int64, error) {
	cutoff := now.AddDate(-consentProofYears, 0, 0)
	res := DB.Where(
		"user_id IN (SELECT id FROM users WHERE deleted_at IS NOT NULL AND deleted_at < ?)"+
			" OR (created_at < ? AND NOT EXISTS (SELECT 1 FROM users WHERE users.id = terms_acceptances.user_id))",
		cutoff, cutoff.Unix(),
	).Delete(&TermsAcceptance{})
	return res.RowsAffected, res.Error
}
