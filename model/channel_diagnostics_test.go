package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ParseStatusReason must pull the numeric upstream status code (and a short
// status token when present) out of a reason string, and return zero values for
// a clean/empty reason. Guards the parsing that feeds the status_code/error_code
// history columns used for filtering.
func TestParseStatusReason(t *testing.T) {
	cases := []struct {
		name     string
		reason   string
		wantCode int
		wantErr  string
	}{
		{"empty", "", 0, ""},
		{"plain 429", "status_code=429, 当前分组上游负载已饱和", 429, ""},
		{"gemini invalid_argument", `status_code=400, {"error": {"code": 400, "status": "INVALID_ARGUMENT"}}`, 400, "INVALID_ARGUMENT"},
		{"401 no status token", "status_code=401, Invalid token", 401, ""},
		{"no code at all", "channel timed out", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, errCode := ParseStatusReason(tc.reason)
			assert.Equal(t, tc.wantCode, code)
			assert.Equal(t, tc.wantErr, errCode)
		})
	}
}

// ChannelDiagnosticStats must aggregate per-channel transition counts and attribute
// the previous-status duration to up vs down correctly: a transition arriving AT
// enabled means the channel was DOWN for that span; arriving at a disabled
// status means it was UP. This is the contract the admin uptime view depends on.
func TestChannelDiagnosticStats(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&ChannelDiagnostic{}))
	t.Cleanup(func() { DB.Exec("DELETE FROM channel_diagnostics") })
	DB.Exec("DELETE FROM channel_diagnostics")

	// Channel 1: up 100s, then down (to enabled after 30s down), then down again.
	// Transitions (chronological):
	//   to=auto_disabled, prev=enabled for 100s  -> seconds_up += 100
	//   to=enabled,       prev=disabled for 30s  -> seconds_down += 30
	//   to=auto_disabled, prev=enabled for 200s  -> seconds_up += 200
	rows := []*ChannelDiagnostic{
		{ChannelId: 1, ChannelName: "c1", ToStatus: common.ChannelStatusAutoDisabled, SecondsInPrevStatus: 100, TriggerSource: ChannelStatusTriggerLiveRequest, CreatedAt: 1000},
		{ChannelId: 1, ChannelName: "c1", ToStatus: common.ChannelStatusEnabled, SecondsInPrevStatus: 30, TriggerSource: ChannelStatusTriggerScheduledTest, CreatedAt: 1030},
		{ChannelId: 1, ChannelName: "c1", ToStatus: common.ChannelStatusAutoDisabled, SecondsInPrevStatus: 200, TriggerSource: ChannelStatusTriggerLiveRequest, CreatedAt: 1230},
		// Channel 2: a single disable, no flapping. up 500s.
		{ChannelId: 2, ChannelName: "c2", ToStatus: common.ChannelStatusAutoDisabled, SecondsInPrevStatus: 500, TriggerSource: ChannelStatusTriggerLiveRequest, CreatedAt: 900},
	}
	for _, r := range rows {
		require.NoError(t, DB.Create(r).Error)
	}

	stats, err := ChannelDiagnosticStats(0, "transitions", 0)
	require.NoError(t, err)
	require.Len(t, stats, 2)

	byId := map[int]*ChannelDiagnosticStatRow{}
	for _, s := range stats {
		byId[s.ChannelId] = s
	}

	c1 := byId[1]
	require.NotNil(t, c1)
	assert.Equal(t, 3, c1.Transitions)
	assert.Equal(t, 2, c1.DisableCount)
	assert.Equal(t, 1, c1.EnableCount)
	assert.Equal(t, int64(300), c1.SecondsUp)  // 100 + 200
	assert.Equal(t, int64(30), c1.SecondsDown) // 30
	assert.Equal(t, common.ChannelStatusAutoDisabled, c1.LastToStatus)
	assert.InDelta(t, 300.0*100/330, c1.UptimePercent, 0.01)

	c2 := byId[2]
	require.NotNil(t, c2)
	assert.Equal(t, 1, c2.Transitions)
	assert.Equal(t, int64(500), c2.SecondsUp)
	assert.Equal(t, int64(0), c2.SecondsDown)
	assert.Equal(t, 100.0, c2.UptimePercent)

	// transitions sort: c1 (3) before c2 (1).
	assert.Equal(t, 1, stats[0].ChannelId)

	// uptime sort puts the worst (lowest %) first: c1 (~90.9%) before c2 (100%).
	statsByUptime, err := ChannelDiagnosticStats(0, "uptime", 0)
	require.NoError(t, err)
	assert.Equal(t, 1, statsByUptime[0].ChannelId)

	// limit caps the result set.
	limited, err := ChannelDiagnosticStats(0, "transitions", 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	assert.Equal(t, 1, limited[0].ChannelId)
}

// upsertChannelProbeFailure must collapse repeated identical probe failures into
// one self-transition row (occurrence_count ticks up) while a changed error
// signature inserts a new row; and ChannelDiagnosticStats must ignore these
// self-transition rows so uptime math is unaffected.
func TestUpsertChannelProbeFailure(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&ChannelDiagnostic{}))
	t.Cleanup(func() { DB.Exec("DELETE FROM channel_diagnostics") })
	DB.Exec("DELETE FROM channel_diagnostics")

	dis := common.ChannelStatusAutoDisabled
	mkRow := func(code int, reason string) *ChannelDiagnostic {
		return &ChannelDiagnostic{
			ChannelId: 42, ChannelName: "c42",
			FromStatus: dis, ToStatus: dis, ProbeOnly: true,
			StatusCode: code, StatusReason: reason,
			TriggerSource: ChannelStatusTriggerScheduledTest,
		}
	}

	// First failure inserts a row with occurrence_count = 1.
	upsertChannelProbeFailure(mkRow(402, "status_code=402, quota spent"))
	var rows []*ChannelDiagnostic
	require.NoError(t, DB.Where("channel_id = ?", 42).Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, 1, rows[0].OccurrenceCount)

	// Same error again updates the SAME row to occurrence_count = 2.
	upsertChannelProbeFailure(mkRow(402, "status_code=402, quota spent"))
	rows = nil
	require.NoError(t, DB.Where("channel_id = ?", 42).Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, 2, rows[0].OccurrenceCount)

	// A changed error signature inserts a SECOND row.
	upsertChannelProbeFailure(mkRow(403, "status_code=403, free quota exhausted"))
	rows = nil
	require.NoError(t, DB.Where("channel_id = ?", 42).Find(&rows).Error)
	require.Len(t, rows, 2)

	// ChannelDiagnosticStats ignores self-transition (probe-failure) rows entirely.
	stats, err := ChannelDiagnosticStats(0, "transitions", 0)
	require.NoError(t, err)
	assert.Empty(t, stats)
}

// GetChannelDiagnostics must filter and paginate newest-first.
func TestGetChannelDiagnosticFilter(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&ChannelDiagnostic{}))
	t.Cleanup(func() { DB.Exec("DELETE FROM channel_diagnostics") })
	DB.Exec("DELETE FROM channel_diagnostics")

	seed := []*ChannelDiagnostic{
		{ChannelId: 7, ToStatus: common.ChannelStatusAutoDisabled, StatusCode: 429, TriggerSource: ChannelStatusTriggerLiveRequest, CreatedAt: 10},
		{ChannelId: 7, ToStatus: common.ChannelStatusEnabled, StatusCode: 0, TriggerSource: ChannelStatusTriggerScheduledTest, CreatedAt: 20},
		{ChannelId: 8, ToStatus: common.ChannelStatusAutoDisabled, StatusCode: 400, TriggerSource: ChannelStatusTriggerLiveRequest, CreatedAt: 30},
	}
	for _, r := range seed {
		require.NoError(t, DB.Create(r).Error)
	}

	// filter by channel 7
	rows, total, err := GetChannelDiagnostics(ChannelDiagnosticFilter{ChannelId: 7}, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	require.Len(t, rows, 2)
	assert.Equal(t, int64(20), rows[0].CreatedAt) // newest first

	// filter by status_code
	rows, total, err = GetChannelDiagnostics(ChannelDiagnosticFilter{StatusCode: 400}, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, rows, 1)
	assert.Equal(t, 8, rows[0].ChannelId)
}

// The flap hold is capped at one probe cycle: it exists to stop a flapping channel
// leaking errors every cycle, not to strand a channel that is healthy again.
func TestFlapBackoffSeconds(t *testing.T) {
	cases := []struct {
		disables int
		want     int64
	}{
		{0, 0},
		{2, 0},
		{3, 30},
		{4, 60},
		{5, 60},
		{10, 60},
		{300, 60},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, FlapBackoffSeconds(c.disables),
			"disables=%d", c.disables)
	}
}
