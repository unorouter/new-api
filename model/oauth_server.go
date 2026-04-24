package model

import (
	"time"

	"gorm.io/gorm"
)

// OAuth 2.1 server tables backing the zitadel/oidc op.Storage interface.
//
// Shape rationale: every entity is stored as a vendor-neutral JSON blob keyed
// by a business ID. Narrow lookup indices (callback_id, auth_code,
// refresh_token, etc.) are extracted columns. Decoupling our DB from the
// library means future swaps don't require a migration.
//
// These are distinct from the existing user-login OAuth *client* code in
// model/custom_oauth_provider.go, which is the opposite direction (we act as
// a client to GitHub/Discord).

// OAuthClient persists an op.Client record by its client id.
type OAuthClient struct {
	ClientId  string         `gorm:"type:varchar(64);primaryKey;column:client_id" json:"client_id"`
	Data      string         `gorm:"type:text;not null" json:"-"` // JSON-serialized OAuthClientRecord
	CreatedAt time.Time      `gorm:"index"`
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

// OAuthAuthnSession persists the short-lived auth-request state carried
// across /authorize, consent, and /token. zitadel looks sessions up by
// session id and by auth code; the auth_code column is set by SaveAuthCode
// once the consent step is done.
type OAuthAuthnSession struct {
	Id            int       `gorm:"primaryKey" json:"id"`
	SessionId     string    `gorm:"type:varchar(64);uniqueIndex;not null;column:session_id" json:"session_id"`
	CallbackId    string    `gorm:"type:varchar(64);index;column:callback_id"`
	AuthCode      string    `gorm:"type:text;index;column:auth_code"`
	Data          string    `gorm:"type:text;not null" json:"-"`
	ExpiresAtUnix int64     `gorm:"index;not null;column:expires_at_unix"`
	CreatedAt     time.Time `gorm:"index"`
	UpdatedAt     time.Time
}

// OAuthGrant persists the long-lived record of what an agent is allowed to
// do on behalf of a user. Backs refresh tokens; cascading revokes flow from
// here.
type OAuthGrant struct {
	Id             int            `gorm:"primaryKey" json:"id"`
	GrantId        string         `gorm:"type:varchar(64);uniqueIndex;not null;column:grant_id" json:"grant_id"`
	RefreshToken   string         `gorm:"type:text;index;column:refresh_token"`
	AuthCode       string         `gorm:"type:text;index;column:auth_code"`
	ClientId       string         `gorm:"type:varchar(64);index;column:client_id"`
	UserId         string         `gorm:"type:varchar(64);index;column:user_id"`
	Data           string         `gorm:"type:text;not null" json:"-"`
	ExpiresAtUnix  int64          `gorm:"index;not null;column:expires_at_unix"`
	CreatedAt      time.Time      `gorm:"index"`
	UpdatedAt      time.Time
	DeletedAt      gorm.DeletedAt `gorm:"index"`
}

// OAuthToken persists access-token bookkeeping. With JWT access tokens the
// token itself is stateless; this row exists so /revoke can invalidate by
// token_id and so cascade-on-grant-revoke can wipe related rows. Rows here
// accumulate; prune by expires_at_unix on a cron.
type OAuthToken struct {
	Id            int            `gorm:"primaryKey" json:"id"`
	TokenId       string         `gorm:"type:varchar(64);uniqueIndex;not null;column:token_id" json:"token_id"`
	GrantId       string         `gorm:"type:varchar(64);index;column:grant_id"`
	ClientId      string         `gorm:"type:varchar(64);index;column:client_id"`
	Data          string         `gorm:"type:text;not null" json:"-"`
	ExpiresAtUnix int64          `gorm:"index;not null;column:expires_at_unix"`
	CreatedAt     time.Time      `gorm:"index"`
	DeletedAt    gorm.DeletedAt `gorm:"index"`
}

