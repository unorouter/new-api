package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupManageUserTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	require.NoError(t, i18n.Init())
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedisEnabled, previousSecret := common.RedisEnabled, common.SessionSecret
	previousMainDatabaseType, previousLogDatabaseType := common.MainDatabaseType(), common.LogDatabaseType()
	dialect := os.Getenv("TEST_MANAGE_USER_DIALECT")
	if dialect == "" {
		dialect = "sqlite"
	}
	databaseTypes := map[string]common.DatabaseType{
		"sqlite": common.DatabaseTypeSQLite, "mysql": common.DatabaseTypeMySQL, "postgres": common.DatabaseTypePostgreSQL,
	}
	require.Contains(t, databaseTypes, dialect)
	dsn := os.Getenv("TEST_" + strings.ToUpper(dialect) + "_DSN")
	db, _ := newAuditTestDatabase(t, dialect, dsn)
	logDB := db
	if os.Getenv("TEST_MANAGE_USER_SEPARATE_LOG_DB") == "1" {
		logDB, _ = newAuditTestDatabase(t, dialect, dsn)
	}
	model.DB, model.LOG_DB = db, logDB
	common.RedisEnabled = false
	common.SessionSecret = "manage-user-test-secret"
	common.SetDatabaseTypes(databaseTypes[dialect], databaseTypes[dialect])

	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled, common.SessionSecret = previousRedisEnabled, previousSecret
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		if logDB != db {
			sqlLogDB, err := logDB.DB()
			if err == nil {
				_ = sqlLogDB.Close()
			}
		}
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.CasbinRule{}, &model.AuthzRole{}, &model.AuthFlow{}, &model.TwoFA{}, &model.PasskeyCredential{}, &model.UserAccessToken{}))
	require.NoError(t, logDB.AutoMigrate(&model.Log{}, &model.AuditLog{}))
	versionQuery := "SELECT version()"
	if dialect == "sqlite" {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("database: %s %s, separate log database: %v", dialect, version, logDB != db)
	return db
}

func performManageUserRequest(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return performVerifiedManageUserRequest(t, body, service.AuthIdentity{UserID: 9999}, "")
}

// performVerifiedManageUserRequest drives ManageUser as the root operator from
// a dashboard login session, carrying the step-up proof status or role changes need.
func performVerifiedManageUserRequest(t *testing.T, body string, identity service.AuthIdentity, proof string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("id", identity.UserID)
		c.Set("role", common.RoleRootUser)
		c.Set("username", "root-operator")
		c.Set("session_id", identity.SessionID)
		c.Set("auth_version", identity.UserAuthVersion)
		c.Set("session_version", identity.SessionVersion)
		c.Set(common.RequestIdKey, "quota-test-request")
	})
	userRouter := dto.NewRouter(nil, engine.Group("/api/user"), "User")
	dto.PostB(userRouter, "/manage", ManageUser)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/user/manage", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if proof != "" {
		request.Header.Set("X-Security-Proof", proof)
	}
	engine.ServeHTTP(recorder, request)
	return recorder
}

// manageUserProof signs the root operator in and issues a password-method proof
// bound to one ManageUser operation.
func manageUserProof(t *testing.T, db *gorm.DB, operation service.VerificationOperation) (service.AuthIdentity, string) {
	t.Helper()
	operator := createQuotaTestOperator(t, db, common.RoleRootUser)
	require.NoError(t, model.PublishUserAuthCache(operator.Id))
	bundle, err := service.CreateLoginSession(operator.Id, "password", "127.0.0.1", "manage-user-test")
	require.NoError(t, err)
	identity, err := service.ParseAccessToken(bundle.AccessToken)
	require.NoError(t, err)
	binding, err := service.BindVerificationOperation(operation)
	require.NoError(t, err)
	proof, _, err := service.IssueSecurityProof(identity, service.VerificationMethodPassword, binding)
	require.NoError(t, err)
	return identity, proof
}

func TestManageUserDisableAdvancesAuthVersionOnceAndRevokesSession(t *testing.T) {
	db := setupManageUserTestDB(t)
	now := time.Now().Unix()
	user := model.User{
		Username: "managed-disable-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&model.UserSession{
		SID: "managed-disable-session", UserID: user.Id, Version: 1, UserAuthVersion: 1,
		Status: model.UserSessionStatusActive, RefreshHash: "refresh-hash", LoginMethod: "password",
		LastActiveAt: now, ExpiresAt: now + 3600,
	}).Error)

	identity, proof := manageUserProof(t, db, service.VerificationOperation{Scope: service.VerificationScopeAdminUserManage, Context: []byte(fmt.Sprintf(`{"user_id":%d,"action":"disable"}`, user.Id))})
	recorder := performVerifiedManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"disable"}`, user.Id), identity, proof)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)

	var updated model.User
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.Equal(t, common.UserStatusDisabled, updated.Status)
	assert.EqualValues(t, 2, updated.AuthVersion)
	var session model.UserSession
	require.NoError(t, db.First(&session, "sid = ?", "managed-disable-session").Error)
	assert.Equal(t, model.UserSessionStatusRevoked, session.Status)
}

func TestManageUserDemoteAdvancesAuthVersionAndRevokesSessionsOnce(t *testing.T) {
	db := setupManageUserTestDB(t)
	previousMaster := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	require.NoError(t, authz.Init(db))

	now := time.Now().Unix()
	user := model.User{
		Username: "managed-demote-user", Password: "password", Role: common.RoleAdminUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)
	for _, sid := range []string{"managed-demote-session-one", "managed-demote-session-two"} {
		require.NoError(t, db.Create(&model.UserSession{
			SID: sid, UserID: user.Id, Version: 1, UserAuthVersion: 1,
			Status: model.UserSessionStatusActive, RefreshHash: "refresh-" + sid, LoginMethod: "password",
			LastActiveAt: now, ExpiresAt: now + 3600,
		}).Error)
	}

	sessionUpdateCount := 0
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:count_demote_session_updates", func(tx *gorm.DB) {
		// Prod also slides the operator's own session expiry; count revocations only.
		if tx.Statement == nil || tx.Statement.Table != "user_sessions" {
			return
		}
		if updates, ok := tx.Statement.Dest.(map[string]any); ok && updates["revoked_reason"] != nil {
			sessionUpdateCount++
		}
	}))

	identity, proof := manageUserProof(t, db, service.VerificationOperation{Scope: service.VerificationScopeAdminUserManage, Context: []byte(fmt.Sprintf(`{"user_id":%d,"action":"demote"}`, user.Id))})
	recorder := performVerifiedManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"demote"}`, user.Id), identity, proof)
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"success":true`)

	var updated model.User
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.Equal(t, common.RoleModUser, updated.Role)
	assert.EqualValues(t, 2, updated.AuthVersion)
	var sessions []model.UserSession
	require.NoError(t, db.Where("user_id = ?", user.Id).Order("sid asc").Find(&sessions).Error)
	require.Len(t, sessions, 2)
	for _, session := range sessions {
		assert.Equal(t, model.UserSessionStatusRevoked, session.Status)
		assert.Equal(t, "admin_demote", session.RevokedReason)
	}
	assert.Equal(t, 1, sessionUpdateCount)
}

func TestManageUserDeleteReturnsImmediatelyAndUnknownActionFails(t *testing.T) {
	db := setupManageUserTestDB(t)
	deleted := model.User{
		Username: "managed-delete-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "delete-aff",
	}
	require.NoError(t, db.Create(&deleted).Error)

	identity, proof := manageUserProof(t, db, service.VerificationOperation{Scope: service.VerificationScopeAdminUserDelete, Context: []byte(fmt.Sprintf(`{"user_id":%d}`, deleted.Id))})
	recorder := performVerifiedManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"delete"}`, deleted.Id), identity, proof)
	assert.Contains(t, recorder.Body.String(), `"success":true`)
	var deletedCount int64
	require.NoError(t, db.Unscoped().Model(&model.User{}).Where("id = ? AND deleted_at IS NOT NULL", deleted.Id).Count(&deletedCount).Error)
	assert.EqualValues(t, 1, deletedCount)

	unchanged := model.User{
		Username: "managed-unknown-user", Password: "password", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "unknown-aff",
	}
	require.NoError(t, db.Create(&unchanged).Error)
	recorder = performManageUserRequest(t, fmt.Sprintf(`{"id":%d,"action":"unknown"}`, unchanged.Id))
	assert.Contains(t, recorder.Body.String(), `"success":false`)
	require.NoError(t, db.First(&unchanged, unchanged.Id).Error)
	assert.EqualValues(t, 1, unchanged.AuthVersion)
	assert.Equal(t, common.UserStatusEnabled, unchanged.Status)
}

func createQuotaTestOperator(t *testing.T, db *gorm.DB, role int) model.User {
	t.Helper()
	if role == 0 {
		role = common.RoleRootUser
	}
	operator := model.User{Id: 9999, Username: "root-operator", Password: "root-operator-hash", Role: role, Status: common.UserStatusEnabled, AuthVersion: 1, AffCode: "root-operator-aff"}
	require.NoError(t, db.Create(&operator).Error)
	return operator
}
