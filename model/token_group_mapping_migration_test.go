package model

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// testTokenGroupMappingMigration asserts the contract the relay depends on:
// every legacy array payload becomes the entry shape, already-migrated and
// unparseable rows survive untouched, and a second run changes nothing.
func testTokenGroupMappingMigration(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&Token{}))
	require.NoError(t, db.Unscoped().Where("1 = 1").Delete(&Token{}).Error)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "legacy array becomes entry shape",
			in:   `{"glm-5.3":["a6-x","open1-y"]}`,
			want: `{"glm-5.3":{"groups":["a6-x","open1-y"]}}`,
		},
		{
			name: "legacy empty array keeps the model key",
			in:   `{"glm-5.3":[]}`,
			want: `{"glm-5.3":{"groups":[]}}`,
		},
		{
			name: "already migrated row is untouched",
			in:   `{"glm-5.3":{"groups":["a6-x"],"min":0.02,"max":0.05}}`,
			want: `{"glm-5.3":{"groups":["a6-x"],"min":0.02,"max":0.05}}`,
		},
		{
			name: "unparseable payload is preserved rather than destroyed",
			in:   `not json at all`,
			want: `not json at all`,
		},
		{
			name: "empty mapping is left alone",
			in:   ``,
			want: ``,
		},
	}

	ids := make([]int, len(cases))
	for i, c := range cases {
		token := Token{UserId: 1, Key: fmt.Sprintf("migration-key-%d", i), GroupMapping: c.in}
		require.NoError(t, db.Create(&token).Error)
		ids[i] = token.Id
	}

	// Twice: the migration must be idempotent and safe to re-run on restart.
	for range 2 {
		require.NoError(t, migrateTokenGroupMappingShape(db))
		for i, c := range cases {
			var got Token
			require.NoError(t, db.First(&got, ids[i]).Error)
			// Non-JSON payloads are compared literally: the contract for them
			// is that the migration leaves the bytes alone.
			if strings.HasPrefix(strings.TrimSpace(c.want), "{") {
				assert.JSONEq(t, c.want, got.GroupMapping, c.name)
				continue
			}
			assert.Equal(t, c.want, got.GroupMapping, c.name)
		}
	}
}

func TestTokenGroupMappingMigrationSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	testTokenGroupMappingMigration(t, db)
}

func TestTokenGroupMappingMigrationMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN is not configured")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	testTokenGroupMappingMigration(t, db)
}

func TestTokenGroupMappingMigrationPostgreSQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn,
		PreferSimpleProtocol: true,
	}), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	testTokenGroupMappingMigration(t, db)
}
