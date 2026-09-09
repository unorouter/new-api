package controller

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	sqlmysql "github.com/go-sql-driver/mysql"
	"gorm.io/driver/clickhouse"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func newAuditTestDatabase(t *testing.T, kind, dsn string) (*gorm.DB, string) {
	t.Helper()
	if kind == "sqlite" {
		path := t.TempDir() + "/audit.db"
		db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
		require.NoError(t, err)
		return db, path
	}
	require.NotEmpty(t, dsn)
	name := fmt.Sprintf("newapi_audit_%d", time.Now().UnixNano())
	var original, isolated gorm.Dialector
	var newDSN string
	if kind == "mysql" {
		config, err := sqlmysql.ParseDSN(dsn)
		require.NoError(t, err)
		require.Equal(t, "tcp", config.Net)
		host, _, err := net.SplitHostPort(config.Addr)
		require.NoError(t, err)
		require.True(t, net.ParseIP(host).IsLoopback(), "database tests only permit loopback instances")
		original = mysql.Open(dsn)
		config.DBName = name
		newDSN = config.FormatDSN()
		isolated = mysql.Open(newDSN)
	} else {
		parsed, err := url.Parse(dsn)
		require.NoError(t, err)
		require.True(t, net.ParseIP(parsed.Hostname()).IsLoopback(), "database tests only permit loopback instances")
		parsed.Path = "/" + name
		newDSN = parsed.String()
		if kind == "clickhouse" {
			original = clickhouse.Open(dsn)
			isolated = clickhouse.Open(newDSN)
		} else {
			original = postgres.Open(dsn)
			isolated = postgres.Open(newDSN)
		}
	}
	admin, err := gorm.Open(original, &gorm.Config{})
	require.NoError(t, err)
	// No IF NOT EXISTS: a collision fails before any test data can be written.
	createSQL := "CREATE DATABASE " + name
	if kind == "mysql" {
		createSQL += " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"
	}
	require.NoError(t, admin.Exec(createSQL).Error)
	sqlDB, err := admin.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	db, err := gorm.Open(isolated, &gorm.Config{})
	require.NoError(t, err)
	t.Logf("isolated database: %s (%s)", name, kind)
	t.Cleanup(func() {
		connection, err := db.DB()
		if err == nil {
			_ = connection.Close()
		}
	})
	return db, newDSN
}

func auditRequest(router http.Handler, method, path, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(`{"secret":"body-must-not-be-logged"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "audit-test-client")
	request.RemoteAddr = "192.0.2.8:4567"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func setupAccessTokenAudit(t *testing.T) (*model.User, string) {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis := common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.Log{}, &model.AuditLog{}, &model.CasbinRule{}, &model.AuthzRole{}))
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	previousMaster := common.IsMasterNode
	common.IsMasterNode = true
	require.NoError(t, authz.Init(db))
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.IsMasterNode = previousMaster
		common.SetDatabaseTypes(previousMain, previousLog)
		common.RedisEnabled = previousRedis
	})
	token := "legacy-opaque-token"
	user := &model.User{Username: "audit-owner", Password: "placeholder", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "default", AccessToken: &token, AuthVersion: 1, AffCode: "audit-owner"}
	require.NoError(t, db.Create(user).Error)
	return user, token
}
