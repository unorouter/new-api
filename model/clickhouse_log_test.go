package model

import (
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsClickHouseDSN(t *testing.T) {
	cases := []struct {
		dsn  string
		want bool
	}{
		{"clickhouse://default:pass@localhost:9000/logs", true},
		{"tcp://localhost:9000/logs", true},
		{"http://localhost:8123/logs", true},
		{"https://localhost:8443/logs", true},
		{"postgres://root:pass@localhost:5432/db", false},
		{"postgresql://root:pass@localhost:5432/db", false},
		{"root:pass@tcp(localhost:3306)/db", false},
		{"local", false},
		{"", false},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, isClickHouseDSN(c.dsn), "dsn=%q", c.dsn)
	}
}

func TestNormalizeClickHouseDSN(t *testing.T) {
	// https without secure gets secure=true appended
	normalized := normalizeClickHouseDSN("https://default:pass@localhost:8443/logs")
	assert.Contains(t, normalized, "secure=true")
	assert.True(t, strings.HasPrefix(normalized, "https://"))

	// explicit settings in the DSN are kept
	assert.Equal(t,
		"https://localhost:8443/logs?async_insert=0&secure=false&wait_for_async_insert=0",
		normalizeClickHouseDSN("https://localhost:8443/logs?secure=false&async_insert=0&wait_for_async_insert=0"),
	)

	// async inserts are on by default for every scheme
	assert.Equal(t, "clickhouse://localhost:9000/logs?async_insert=1&wait_for_async_insert=1", normalizeClickHouseDSN("clickhouse://localhost:9000/logs"))
	assert.Equal(t, "tcp://localhost:9000/logs?async_insert=1&wait_for_async_insert=1", normalizeClickHouseDSN("tcp://localhost:9000/logs"))
}

func TestChooseDBRejectsClickHouseForMainDatabase(t *testing.T) {
	original, had := os.LookupEnv("SQL_DSN")
	t.Cleanup(func() {
		if had {
			require.NoError(t, os.Setenv("SQL_DSN", original))
		} else {
			require.NoError(t, os.Unsetenv("SQL_DSN"))
		}
	})
	require.NoError(t, os.Setenv("SQL_DSN", "clickhouse://default:pass@localhost:9000/logs"))

	db, dbType, err := chooseDB("SQL_DSN", false)
	require.Error(t, err)
	assert.Nil(t, db)
	assert.Equal(t, common.DatabaseType(""), dbType)
	assert.Contains(t, err.Error(), "does not support ClickHouse")
}

func TestClickHouseLogTTLExpression(t *testing.T) {
	assert.Equal(t, "", clickHouseLogTTLExpression(clickHouseLogStorage{}))
	assert.Equal(t, "toDateTime(created_at) + toIntervalDay(30)", clickHouseLogTTLExpression(clickHouseLogStorage{ttlDays: 30}))
	assert.Equal(t, "toDateTime(created_at) + toIntervalDay(7) TO VOLUME 'cold'", clickHouseLogTTLExpression(clickHouseLogStorage{moveAfterDays: 7}))
	assert.Equal(t,
		"toDateTime(created_at) + toIntervalDay(7) TO VOLUME 'cold', toDateTime(created_at) + toIntervalDay(365)",
		clickHouseLogTTLExpression(clickHouseLogStorage{ttlDays: 365, moveAfterDays: 7}),
	)
}

func TestClickHouseLogCreateTableSQL(t *testing.T) {
	plain := clickHouseLogCreateTableSQL(clickHouseLogStorage{})
	assert.Contains(t, plain, "CREATE TABLE IF NOT EXISTS logs")
	assert.Contains(t, plain, "ENGINE = MergeTree()")
	assert.Contains(t, plain, "PARTITION BY toYYYYMM(toDateTime(created_at))")
	assert.Contains(t, plain, "ORDER BY (created_at, request_id)")
	assert.Contains(t, plain, "ip String DEFAULT '' TTL toDateTime(created_at) + toIntervalDay(30),")
	assert.NotContains(t, plain, "\nTTL ")
	assert.NotContains(t, plain, "SETTINGS")

	tiered := clickHouseLogCreateTableSQL(clickHouseLogStorage{ttlDays: 30, moveAfterDays: 7, storagePolicy: "tiered"})
	assert.True(t, strings.HasSuffix(tiered, "ORDER BY (created_at, request_id)\nTTL toDateTime(created_at) + toIntervalDay(7) TO VOLUME 'cold', toDateTime(created_at) + toIntervalDay(30)\nSETTINGS storage_policy = 'tiered'"))
}

func TestClickHouseTableTTL(t *testing.T) {
	assert.Equal(t, "", clickHouseTableTTL("MergeTree PARTITION BY toYYYYMM(toDateTime(created_at)) ORDER BY (created_at, request_id) SETTINGS index_granularity = 8192"))
	assert.Equal(t,
		"toDateTime(created_at) + toIntervalDay(5) TO VOLUME 'cold', toDateTime(created_at) + toIntervalDay(90)",
		clickHouseTableTTL("MergeTree ORDER BY created_at TTL toDateTime(created_at) + toIntervalDay(5) TO VOLUME 'cold', toDateTime(created_at) + toIntervalDay(90) SETTINGS index_granularity = 8192"),
	)
}

func TestClickHouseLikeFromBangEscaped(t *testing.T) {
	cases := []struct{ in, want string }{
		{"%gpt!_4%", `%gpt\_4%`},
		{"%a!!b%", "%a!b%"},
		{"%c!%d%", `%c\%d%`},
		{`%back\slash%`, `%back\\slash%`},
		{"glm%:free", "glm%:free"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, clickHouseLikeFromBangEscaped(c.in), "pattern=%q", c.in)
	}
}

func TestClickHouseLogOrder(t *testing.T) {
	assert.Equal(t, "created_at desc, request_id desc", clickHouseLogOrder(""))
	assert.Equal(t, "logs.created_at desc, logs.request_id desc", clickHouseLogOrder("logs."))
}

func TestBuildLogLikeConditionUsesStandardEscape(t *testing.T) {
	originalLogDatabaseType := common.LogDatabaseType()
	t.Cleanup(func() {
		common.SetLogDatabaseType(originalLogDatabaseType)
	})
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)

	condition, pattern, err := buildLogLikeCondition("logs.model_name", "gpt_4%")

	require.NoError(t, err)
	assert.Equal(t, "logs.model_name LIKE ? ESCAPE '!'", condition)
	assert.Equal(t, "gpt!_4%", pattern)
}

func TestBuildLogLikeConditionUsesClickHouseEscaping(t *testing.T) {
	originalLogDatabaseType := common.LogDatabaseType()
	t.Cleanup(func() {
		common.SetLogDatabaseType(originalLogDatabaseType)
	})
	common.SetLogDatabaseType(common.DatabaseTypeClickHouse)

	condition, pattern, err := buildLogLikeCondition("logs.model_name", `gpt_4\mini%`)

	require.NoError(t, err)
	assert.Equal(t, "logs.model_name LIKE ?", condition)
	assert.Equal(t, `gpt\_4\\mini%`, pattern)
}

func TestEnsureLogRequestId(t *testing.T) {
	empty := &Log{}
	ensureLogRequestId(empty)
	assert.NotEmpty(t, empty.RequestId, "empty request id should be backfilled")

	existing := &Log{RequestId: "fixed-request-id"}
	ensureLogRequestId(existing)
	assert.Equal(t, "fixed-request-id", existing.RequestId, "existing request id must be preserved")

	assert.NotPanics(t, func() { ensureLogRequestId(nil) })
}

func TestAssignDisplayLogIds(t *testing.T) {
	logs := []*Log{{}, {}, {}}

	assignDisplayLogIds(logs, 0)
	assert.Equal(t, []int{1, 2, 3}, []int{logs[0].Id, logs[1].Id, logs[2].Id})

	assignDisplayLogIds(logs, 20)
	assert.Equal(t, []int{21, 22, 23}, []int{logs[0].Id, logs[1].Id, logs[2].Id})

	assert.NotPanics(t, func() { assignDisplayLogIds(nil, 0) })
}
