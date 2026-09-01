package model

import (
	"regexp"
	"sort"
	"strconv"

	"github.com/QuantumNous/new-api/common"

	"github.com/bytedance/gopkg/util/gopool"
	"gorm.io/gorm"
)

// Trigger sources for a channel status transition.
const (
	ChannelStatusTriggerLiveRequest   = "live_request"
	ChannelStatusTriggerScheduledTest = "scheduled_test"
	ChannelStatusTriggerManual        = "manual"
	ChannelStatusTriggerByTag         = "by_tag"
	ChannelStatusTriggerBalance       = "balance"
)

// ChannelDiagnostic holds two kinds of row for one channel, distinguished by
// ProbeOnly:
//   - a status TRANSITION (ProbeOnly=false): one append-only row per enable<->disable
//     flip, so we can answer "which channels flap most / worst uptime / what error
//     keeps disabling channel X" with plain SQL. Written from the single chokepoint
//     UpdateChannelStatus.
//   - a PROBE FAILURE (ProbeOnly=true): a scheduled-test probe of an ALREADY-disabled
//     channel that keeps failing (no status flip, so it would otherwise be invisible).
//     Upserted on the stable (status_code, error_code) signature so a recurring error
//     ticks OccurrenceCount instead of flooding the table.
//
// Written async; a failed insert never blocks the status flip.
type ChannelDiagnostic struct {
	Id                  int64  `json:"id" gorm:"primaryKey"`
	ChannelId           int    `json:"channel_id" gorm:"index;index:idx_cd_created_channel,priority:2"`
	ChannelName         string `json:"channel_name" gorm:"type:varchar(255)"`
	BaseURL             string `json:"base_url" gorm:"type:varchar(512)"`
	FromStatus          int    `json:"from_status"`
	ToStatus            int    `json:"to_status" gorm:"index"`
	StatusReason        string `json:"status_reason" gorm:"type:text"`
	StatusCode          int    `json:"status_code" gorm:"index"`
	ErrorCode           string `json:"error_code" gorm:"type:varchar(64)"`
	ModelName           string `json:"model_name" gorm:"type:varchar(255);index"`
	TriggerSource       string `json:"trigger_source" gorm:"type:varchar(32);index"`
	ResponseTimeMs      int    `json:"response_time_ms"`
	SecondsInPrevStatus int64  `json:"seconds_in_prev_status"`
	// ProbeOnly marks a scheduled-test failure row for an ALREADY-disabled channel
	// (no status flip). These are upserted (see OccurrenceCount) and MUST be excluded
	// from transition/uptime analytics. Real transitions have ProbeOnly=false.
	ProbeOnly bool `json:"probe_only" gorm:"index"`
	// OccurrenceCount collapses repeated probe failures with the same
	// (status_code, error_code) signature into ONE probe-only row that ticks up.
	// 1 for a normal transition; incremented each time the same error recurs.
	OccurrenceCount int `json:"occurrence_count"`
	// FirstSeenAt is set once when a probe-only row is first inserted and never
	// overwritten, so first-seen is preserved while CreatedAt tracks last-seen.
	// 0 for normal transition rows (they are point-in-time, use CreatedAt).
	FirstSeenAt int64 `json:"first_seen_at"`
	CreatedAt   int64 `json:"created_at" gorm:"bigint;index:idx_cd_created_channel,priority:1"`
}

func (ChannelDiagnostic) TableName() string {
	return "channel_diagnostics"
}

var statusCodeRe = regexp.MustCompile(`status_code=(\d+)`)

// statusTokenRe matches both the bare `status: VALUE` and the JSON
// `"status": "VALUE"` forms Google/Gemini-style errors use, capturing the token.
var statusTokenRe = regexp.MustCompile(`(?i)"?status"?\s*:\s*"?([A-Za-z_][A-Za-z0-9_]*)`)

// ParseStatusReason pulls the numeric status_code and (best-effort) a short
// error-code token out of a channel status reason string such as
// "status_code=400, {error: {... status: INVALID_ARGUMENT}}". Returns (0, "")
// when nothing parseable is present (e.g. a clean enable with empty reason).
func ParseStatusReason(reason string) (int, string) {
	if reason == "" {
		return 0, ""
	}
	code := 0
	if m := statusCodeRe.FindStringSubmatch(reason); len(m) == 2 {
		code, _ = strconv.Atoi(m[1])
	}
	errCode := ""
	// Google-style status token: `status: INVALID_ARGUMENT` / `"status":"..."`.
	// The regex's leading [A-Za-z_] excludes a bare numeric "status: 400" (the
	// numeric code is already captured separately), keeping textual tokens only.
	if m := statusTokenRe.FindStringSubmatch(reason); len(m) == 2 && len(m[1]) <= 64 {
		errCode = m[1]
	}
	return code, errCode
}

// lastDiagnosticCreatedAt returns the created_at of the most recent row for a
// channel, used to compute how long it sat in the previous status. 0 = no prior
// row.
func lastDiagnosticCreatedAt(channelId int) int64 {
	var ts int64
	DB.Model(&ChannelDiagnostic{}).
		Where("channel_id = ?", channelId).
		Order("created_at DESC").
		Limit(1).
		Pluck("created_at", &ts)
	return ts
}

// InsertChannelDiagnostic records one row. Best-effort: callers run it async and a
// DB error must never propagate to the status flip. SecondsInPrev / CreatedAt are
// filled here when unset so callers stay terse.
func InsertChannelDiagnostic(row *ChannelDiagnostic) {
	if row == nil {
		return
	}
	if row.CreatedAt == 0 {
		row.CreatedAt = common.GetTimestamp()
	}
	if row.SecondsInPrevStatus == 0 {
		if prev := lastDiagnosticCreatedAt(row.ChannelId); prev > 0 && row.CreatedAt >= prev {
			row.SecondsInPrevStatus = row.CreatedAt - prev
		}
	}
	if row.StatusCode == 0 && row.ErrorCode == "" {
		row.StatusCode, row.ErrorCode = ParseStatusReason(row.StatusReason)
	}
	if row.OccurrenceCount == 0 {
		row.OccurrenceCount = 1
	}
	if err := DB.Create(row).Error; err != nil {
		common.SysLog("failed to insert channel diagnostic: " + err.Error())
	}
}

// RecordChannelProbeFailure records a scheduled-test probe that failed against an
// ALREADY-disabled channel (no status flip, so UpdateChannelStatus writes nothing
// and the failure would otherwise be invisible). It writes a probe-only row
// (probe_only=true, from_status == to_status == the channel's current disabled
// status) and UPSERTS on the STABLE signature (status_code, error_code) ONLY: a
// repeat bumps occurrence_count, refreshes created_at (last-seen) + the displayed
// reason, and preserves first_seen_at. Dropping the volatile reason text from the
// key stops error bodies with embedded timestamps/quota numbers from spawning a row
// per variant. A changed code/error inserts a new row. Fire-and-forget; never
// blocks or fails the probe loop.
func RecordChannelProbeFailure(channel *Channel, statusCode int, errorCode, reason, triggerSource, modelName string, responseTimeMs int) {
	if channel == nil {
		return
	}
	if statusCode == 0 && errorCode == "" {
		statusCode, errorCode = ParseStatusReason(reason)
	}
	status := channel.Status
	baseURL := ""
	if channel.BaseURL != nil {
		baseURL = *channel.BaseURL
	}
	source := triggerSource
	if source == "" {
		source = ChannelStatusTriggerScheduledTest
	}
	row := &ChannelDiagnostic{
		ChannelId:      channel.Id,
		ChannelName:    channel.Name,
		BaseURL:        baseURL,
		FromStatus:     status,
		ToStatus:       status,
		StatusReason:   reason,
		StatusCode:     statusCode,
		ErrorCode:      errorCode,
		ModelName:      modelName,
		TriggerSource:  source,
		ResponseTimeMs: responseTimeMs,
		ProbeOnly:      true,
	}
	gopool.Go(func() { upsertChannelProbeFailure(row) })
}

// upsertChannelProbeFailure is the synchronous core of RecordChannelProbeFailure:
// bump the matching probe-only row's occurrence_count if one exists for the same
// stable (status_code, error_code) signature, else insert a fresh one. Split out so
// it can be tested deterministically (the caller runs it via gopool.Go).
func upsertChannelProbeFailure(row *ChannelDiagnostic) {
	now := common.GetTimestamp()
	var existing ChannelDiagnostic
	err := DB.Where(
		"channel_id = ? AND probe_only = ? AND status_code = ? AND error_code = ?",
		row.ChannelId, true, row.StatusCode, row.ErrorCode,
	).Order("created_at DESC, id DESC").Limit(1).Find(&existing).Error
	if err == nil && existing.Id != 0 {
		secondsInPrev := existing.SecondsInPrevStatus
		if prev := lastDiagnosticCreatedAt(row.ChannelId); prev > 0 && now >= prev {
			secondsInPrev = now - prev
		}
		if uerr := DB.Model(&ChannelDiagnostic{}).Where("id = ?", existing.Id).Updates(map[string]any{
			"occurrence_count":       existing.OccurrenceCount + 1,
			"created_at":             now,
			"status_reason":          row.StatusReason,
			"response_time_ms":       row.ResponseTimeMs,
			"seconds_in_prev_status": secondsInPrev,
		}).Error; uerr != nil {
			common.SysLog("failed to update channel probe failure: " + uerr.Error())
		}
		return
	}
	if row.FirstSeenAt == 0 {
		row.FirstSeenAt = now
	}
	InsertChannelDiagnostic(row)
}

const (
	flapBackoffLookbackSeconds = 6 * 60 * 60
	flapBackoffMinDisables     = 3
	flapBackoffBaseSeconds     = 30
	// Cap the flap hold to ~one probe cycle (tests run every 1min): a flaky-but-
	// recovering free lane (z.ai reverse, captcha-pool latency) that passes its
	// probe should come back on the very next cycle, not stay quarantined. The hold
	// only skips a single immediate re-enable to damp thrash; it never locks a
	// healthy-again channel out.
	flapBackoffCapSeconds = 60
)

// FlapBackoffSeconds returns how long a channel must stay disabled after its
// `disables`-th auto-disable inside the lookback window: 0 for the first
// flapBackoffMinDisables (normal recovery), then 5min doubling per extra disable,
// capped at 6h. Complements ProbeBackoffSeconds, which only engages when probes
// FAIL - a flapping channel passes its recovery probe (the upstream answers tiny
// probes) but dies again under real traffic, so without this it re-enables every
// probe cycle and each flap leaks user-visible errors.
func FlapBackoffSeconds(disables int) int64 {
	shift := disables - flapBackoffMinDisables
	if shift < 0 {
		return 0
	}
	if shift >= 20 { // 300 * 2^20 already far exceeds the cap; avoid overflow
		return flapBackoffCapSeconds
	}
	wait := int64(flapBackoffBaseSeconds) << uint(shift)
	if wait > flapBackoffCapSeconds {
		return flapBackoffCapSeconds
	}
	return wait
}

// FlapCooldownRemainingSeconds returns how many seconds a channel whose recovery
// probe just passed must still stay disabled, based on how often it auto-disabled
// inside the lookback window. Best-effort: any query error yields 0 so recovery is
// never blocked by a diagnostics failure.
func FlapCooldownRemainingSeconds(channelId int) int64 {
	now := common.GetTimestamp()
	var row struct {
		DisableCount  int   `gorm:"column:disable_count"`
		LastDisableAt int64 `gorm:"column:last_disable_at"`
	}
	err := DB.Table("channel_diagnostics").
		Select("COUNT(*) AS disable_count, COALESCE(MAX(created_at), 0) AS last_disable_at").
		Where("probe_only = ? AND to_status = ? AND channel_id = ? AND created_at > ?",
			false, common.ChannelStatusAutoDisabled, channelId, now-flapBackoffLookbackSeconds).
		Scan(&row).Error
	if err != nil {
		common.SysLog("failed to load channel flap state: " + err.Error())
		return 0
	}
	wait := FlapBackoffSeconds(row.DisableCount)
	if wait == 0 {
		return 0
	}
	remaining := row.LastDisableAt + wait - now
	if remaining < 0 {
		return 0
	}
	return remaining
}

// ChannelDiagnosticFilter narrows a diagnostics list query. Zero-value fields are
// ignored.
type ChannelDiagnosticFilter struct {
	ChannelId      int
	ToStatus       int
	TriggerSource  string
	StatusCode     int
	ModelName      string
	Keyword        string
	RowType        string // "" = all, "transitions" = real flips, "probe" = probe failures
	StartTimestamp int64
	EndTimestamp   int64
	SortBy         string
	SortOrder      string
}

func (f ChannelDiagnosticFilter) apply(db *gorm.DB) *gorm.DB {
	if f.ChannelId != 0 {
		db = db.Where("channel_id = ?", f.ChannelId)
	}
	switch f.RowType {
	case "transitions":
		db = db.Where("probe_only = ?", false)
	case "probe":
		db = db.Where("probe_only = ?", true)
	}
	if f.ToStatus != 0 {
		db = db.Where("to_status = ?", f.ToStatus)
	}
	if f.TriggerSource != "" {
		db = db.Where("trigger_source = ?", f.TriggerSource)
	}
	if f.StatusCode != 0 {
		db = db.Where("status_code = ?", f.StatusCode)
	}
	if f.ModelName != "" {
		db = db.Where("model_name = ?", f.ModelName)
	}
	if f.Keyword != "" {
		like := "%" + f.Keyword + "%"
		db = db.Where("channel_name LIKE ? OR base_url LIKE ? OR status_reason LIKE ?", like, like, like)
	}
	if f.StartTimestamp != 0 {
		db = db.Where("created_at >= ?", f.StartTimestamp)
	}
	if f.EndTimestamp != 0 {
		db = db.Where("created_at <= ?", f.EndTimestamp)
	}
	return db
}

var channelDiagnosticSortColumns = map[string]string{
	"created_at":             "created_at",
	"first_seen_at":          "first_seen_at",
	"status_code":            "status_code",
	"occurrence_count":       "occurrence_count",
	"channel_id":             "channel_id",
	"seconds_in_prev_status": "seconds_in_prev_status",
}

// orderClause builds a safe ORDER BY from the whitelisted sort column + direction,
// defaulting to the newest-first ordering when unset/unknown.
func (f ChannelDiagnosticFilter) orderClause() string {
	col, ok := channelDiagnosticSortColumns[f.SortBy]
	if !ok {
		return "created_at DESC, id DESC"
	}
	dir := "DESC"
	if f.SortOrder == "asc" {
		dir = "ASC"
	}
	return col + " " + dir + ", id DESC"
}

// GetChannelDiagnostics returns a paginated, filtered slice of rows (newest first
// by default) plus the total count for the filter.
func GetChannelDiagnostics(filter ChannelDiagnosticFilter, startIdx int, num int) ([]*ChannelDiagnostic, int64, error) {
	var total int64
	if err := filter.apply(DB.Model(&ChannelDiagnostic{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return []*ChannelDiagnostic{}, 0, nil
	}
	var rows []*ChannelDiagnostic
	err := filter.apply(DB.Model(&ChannelDiagnostic{})).
		Order(filter.orderClause()).
		Limit(num).Offset(startIdx).
		Find(&rows).Error
	return rows, total, err
}

// ChannelDiagnosticStatRow is one channel's transition/uptime summary over a
// window. UptimePercent is computed in Go from the up/down second sums (AVG/SUM
// return portable types but the ratio is cleaner in Go).
type ChannelDiagnosticStatRow struct {
	ChannelId     int     `json:"channel_id" gorm:"column:channel_id"`
	ChannelName   string  `json:"channel_name" gorm:"column:channel_name"`
	BaseURL       string  `json:"base_url" gorm:"column:base_url"`
	Transitions   int     `json:"transitions" gorm:"column:transitions"`
	DisableCount  int     `json:"disable_count" gorm:"column:disable_count"`
	EnableCount   int     `json:"enable_count" gorm:"column:enable_count"`
	SecondsUp     int64   `json:"seconds_up" gorm:"column:seconds_up"`
	SecondsDown   int64   `json:"seconds_down" gorm:"column:seconds_down"`
	UptimePercent float64 `json:"uptime_percent" gorm:"-"`
	LastToStatus  int     `json:"last_to_status" gorm:"column:last_to_status"`
}

// ChannelDiagnosticStats aggregates per-channel transition counts and up/down time
// over [since, now]. Cross-DB: uses SUM(CASE WHEN ...) (no Postgres FILTER).
// seconds_up/down attribute the time spent in the PREVIOUS status to whichever
// status the channel was leaving: a transition arriving AT enabled (to_status=1)
// means it was DOWN for seconds_in_prev_status; arriving at a disabled status
// means it was UP. Probe-only rows are excluded. orderBy: "transitions" | "uptime"
// | "downtime" (default transitions). limit 0 = no limit.
func ChannelDiagnosticStats(since int64, orderBy string, limit int) ([]*ChannelDiagnosticStatRow, error) {
	enabled := common.ChannelStatusEnabled
	autoDis := common.ChannelStatusAutoDisabled
	manDis := common.ChannelStatusManuallyDisabled

	db := DB.Table("channel_diagnostics").
		Select(`
			channel_id,
			MAX(channel_name) AS channel_name,
			MAX(base_url) AS base_url,
			COUNT(*) AS transitions,
			SUM(CASE WHEN to_status IN (?, ?) THEN 1 ELSE 0 END) AS disable_count,
			SUM(CASE WHEN to_status = ? THEN 1 ELSE 0 END) AS enable_count,
			SUM(CASE WHEN to_status = ? THEN seconds_in_prev_status ELSE 0 END) AS seconds_down,
			SUM(CASE WHEN to_status IN (?, ?) THEN seconds_in_prev_status ELSE 0 END) AS seconds_up,
			MAX(id) AS last_id
		`, autoDis, manDis, enabled, enabled, autoDis, manDis).
		Where("created_at >= ?", since).
		Where("probe_only = ?", false).
		Group("channel_id")

	var rows []*ChannelDiagnosticStatRow
	if err := db.Scan(&rows).Error; err != nil {
		return nil, err
	}

	// Resolve last_to_status per channel (the status of the most recent
	// transition in the window) and compute uptime%.
	for _, r := range rows {
		total := r.SecondsUp + r.SecondsDown
		if total > 0 {
			r.UptimePercent = float64(r.SecondsUp) * 100 / float64(total)
		} else {
			r.UptimePercent = 100
		}
		var lastStatus int
		DB.Model(&ChannelDiagnostic{}).
			Where("channel_id = ? AND created_at >= ? AND probe_only = ?", r.ChannelId, since, false).
			Order("created_at DESC, id DESC").Limit(1).
			Pluck("to_status", &lastStatus)
		r.LastToStatus = lastStatus
	}

	sortDiagnosticStatRows(rows, orderBy)
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

type GroupUptimeRow struct {
	Group       string `gorm:"column:group"`
	SecondsUp   int64  `gorm:"column:seconds_up"`
	SecondsDown int64  `gorm:"column:seconds_down"`
}

// GroupUptimeForModel returns uptime% keyed by ability group for one model, over
// [since, now]. Same seconds_up/down attribution as ChannelDiagnosticStats (a
// transition arriving AT enabled means the channel was DOWN for the preceding
// span), joined through abilities so a channel's history counts toward every
// group that publishes it.
//
// A group with no transitions in the window is absent from the result rather
// than 0: silence means "never flipped", which is 100%, and the caller defaults
// it. Deliberately omits the per-row last_to_status lookup that
// ChannelDiagnosticStats does, since that is an N+1 and nothing here needs it.
func GroupUptimeForModel(modelName string, since int64) (map[string]float64, error) {
	enabled := common.ChannelStatusEnabled
	autoDis := common.ChannelStatusAutoDisabled
	manDis := common.ChannelStatusManuallyDisabled

	var rows []*GroupUptimeRow
	err := DB.Table("channel_diagnostics AS d").
		Select(`
			a.`+commonGroupCol+` AS `+commonGroupCol+`,
			SUM(CASE WHEN d.to_status IN (?, ?) THEN d.seconds_in_prev_status ELSE 0 END) AS seconds_up,
			SUM(CASE WHEN d.to_status = ? THEN d.seconds_in_prev_status ELSE 0 END) AS seconds_down
		`, autoDis, manDis, enabled).
		Joins("JOIN abilities AS a ON a.channel_id = d.channel_id").
		Where("a.model = ?", modelName).
		Where("d.created_at >= ?", since).
		Where("d.probe_only = ?", false).
		Group("a." + commonGroupCol).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	out := make(map[string]float64, len(rows))
	for _, r := range rows {
		total := r.SecondsUp + r.SecondsDown
		if total <= 0 {
			out[r.Group] = 100
			continue
		}
		out[r.Group] = float64(r.SecondsUp) * 100 / float64(total)
	}
	return out, nil
}

func sortDiagnosticStatRows(rows []*ChannelDiagnosticStatRow, orderBy string) {
	less := func(i, j int) bool { return rows[i].Transitions > rows[j].Transitions }
	switch orderBy {
	case "uptime":
		less = func(i, j int) bool { return rows[i].UptimePercent < rows[j].UptimePercent }
	case "downtime":
		less = func(i, j int) bool { return rows[i].SecondsDown > rows[j].SecondsDown }
	}
	sort.SliceStable(rows, less)
}

// PruneChannelDiagnosticsBefore deletes rows older than a cutoff (retention).
func PruneChannelDiagnosticsBefore(beforeTs int64) (int64, error) {
	res := DB.Where("created_at < ?", beforeTs).Delete(&ChannelDiagnostic{})
	return res.RowsAffected, res.Error
}

// DeleteChannelDiagnosticsByChannelIds drops the history of deleted channels. Ids
// are reused, so orphaned rows would otherwise surface as a new channel's history.
func DeleteChannelDiagnosticsByChannelIds(tx *gorm.DB, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	db := tx
	if db == nil {
		db = DB
	}
	const chunkSize = 200
	for start := 0; start < len(ids); start += chunkSize {
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		if err := db.Where("channel_id IN ?", ids[start:end]).Delete(&ChannelDiagnostic{}).Error; err != nil {
			return err
		}
	}
	return nil
}

// ChannelIdsWithRecentTransition returns the ids of channels that had a REAL
// status flip (probe-only retry rows excluded - those refresh created_at on
// every failed re-probe of an already-dead channel) since `since`. The status
// snapshot worker uses this to keep long-dead channels out of structural
// totals.
func ChannelIdsWithRecentTransition(since int64) map[int]bool {
	var ids []int
	err := DB.Model(&ChannelDiagnostic{}).
		Where("probe_only = ? AND created_at >= ?", false, since).
		Distinct().
		Pluck("channel_id", &ids).Error
	out := make(map[int]bool, len(ids))
	if err != nil {
		common.SysLog("channel diagnostics: recent transition query failed: " + err.Error())
		return out
	}
	for _, id := range ids {
		out[id] = true
	}
	return out
}
