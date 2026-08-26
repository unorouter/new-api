package model

import (
	"github.com/samber/lo"
	"gorm.io/gorm/clause"
)

// Per-ping status enum, matching the OpenStatus presentation taxonomy 1:1
// (success | degraded | error | empty). Stored as the lib's enum string at
// write time so reads need no derivation.
const (
	ModelStatusSuccess  = "success"
	ModelStatusDegraded = "degraded"
	ModelStatusError    = "error"
	ModelStatusEmpty    = "empty"
)

// ModelStatusPing is one snapshot per (model, minute). The single fact table
// for status. Status is computed at write time. Aggregations into 1m/5m/15m/
// 1h/1d buckets happen at read time via AggregateBuckets.
type ModelStatusPing struct {
	Model         string `json:"model" gorm:"type:varchar(255);primaryKey"`
	Timestamp     int64  `json:"timestamp" gorm:"bigint;primaryKey;index:idx_status_ts_model,priority:1"`
	Status        string `json:"status" gorm:"type:varchar(16);index"`
	UpChannels    int    `json:"up_channels"`
	TotalChannels int    `json:"total_channels"`
	LatencyMs     int    `json:"latency_ms"`
	RequestCount  int    `json:"request_count"`
	ErrorCount    int    `json:"error_count"`
	P50LatencyMs  int    `json:"p50_latency_ms"`
	P95LatencyMs  int    `json:"p95_latency_ms"`
}

func (ModelStatusPing) TableName() string {
	return "model_status_pings"
}

// ComputeModelStatus derives the per-minute status enum. Channel counts are
// the structural baseline ("degraded" = less than half the channels up), but
// real traffic overrides them: a minute of clean live requests is healthy no
// matter how many dead sibling channels drag the ratio down - long-disabled
// relay lanes otherwise pin a perfectly working model at "degraded" forever.
func ComputeModelStatus(upChannels, totalChannels, requestCount, errorCount int) string {
	switch {
	case totalChannels == 0:
		return ModelStatusEmpty
	case upChannels == 0:
		return ModelStatusError
	case requestCount > 0 && errorCount == 0:
		return ModelStatusSuccess
	case upChannels*2 < totalChannels:
		return ModelStatusDegraded
	default:
		return ModelStatusSuccess
	}
}

func InsertModelStatusPings(rows []*ModelStatusPing) error {
	if len(rows) == 0 {
		return nil
	}
	for _, chunk := range lo.Chunk(rows, 100) {
		err := DB.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "model"}, {Name: "timestamp"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"status", "up_channels", "total_channels", "latency_ms",
				"request_count", "error_count", "p50_latency_ms", "p95_latency_ms",
			}),
		}).Create(&chunk).Error
		if err != nil {
			return err
		}
	}
	return nil
}

func PruneModelStatusPingsBefore(beforeTs int64) error {
	return DB.Where("timestamp < ?", beforeTs).Delete(&ModelStatusPing{}).Error
}

// DeleteModelStatusPingsNotIn removes ping history for any model not in the
// given active set. No-op on an empty slice (see component sibling).
func DeleteModelStatusPingsNotIn(activeModels []string) error {
	if len(activeModels) == 0 {
		return nil
	}
	return DB.Where("model NOT IN ?", activeModels).
		Delete(&ModelStatusPing{}).Error
}

// LatestPingByModel returns the most recent ping per model (used by the
// /components endpoint to show "current status").
func LatestPingByModel() (map[string]*ModelStatusPing, error) {
	var latestTs int64
	if err := DB.Model(&ModelStatusPing{}).
		Select("MAX(timestamp)").
		Scan(&latestTs).Error; err != nil {
		return nil, err
	}
	if latestTs == 0 {
		return map[string]*ModelStatusPing{}, nil
	}
	var rows []*ModelStatusPing
	if err := DB.Where("timestamp = ?", latestTs).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]*ModelStatusPing, len(rows))
	for _, r := range rows {
		out[r.Model] = r
	}
	return out, nil
}

// BucketRow is one aggregated time window for one model. Lives only in
// memory (computed by AggregateBuckets).
//
// AVG() returns NUMERIC/DECIMAL on Postgres and FLOAT on SQLite/MySQL, so the
// latency averages MUST be float64 to scan portably. Callers round to int when
// rendering.
type BucketRow struct {
	BucketStart  int64   `gorm:"column:bucket_start"`
	Count        int     `gorm:"column:cnt"`
	Ok           int     `gorm:"column:ok"`
	Degraded     int     `gorm:"column:degraded"`
	ErrorCnt     int     `gorm:"column:err"`
	Empty        int     `gorm:"column:empty_cnt"`
	P95LatencyMs float64 `gorm:"column:avg_p95"`
	RequestSum   int     `gorm:"column:req_sum"`
	ErrorSum     int     `gorm:"column:err_sum"`
}

// AggregateBuckets groups pings into windows of bucketSeconds for one model
// since `since` (unix seconds, inclusive). Cross-DB compatible: uses
// SUM(CASE ...) instead of FILTER (Postgres-only) and integer truncation
// instead of date_trunc.
func AggregateBuckets(modelName string, bucketSeconds int64, since int64) ([]*BucketRow, error) {
	var rows []*BucketRow
	err := DB.Table("model_status_pings").
		Select(`
			(timestamp / ?) * ? AS bucket_start,
			COUNT(*) AS cnt,
			SUM(CASE WHEN status = 'success'  THEN 1 ELSE 0 END) AS ok,
			SUM(CASE WHEN status = 'degraded' THEN 1 ELSE 0 END) AS degraded,
			SUM(CASE WHEN status = 'error'    THEN 1 ELSE 0 END) AS err,
			SUM(CASE WHEN status = 'empty'    THEN 1 ELSE 0 END) AS empty_cnt,
			AVG(p95_latency_ms)  AS avg_p95,
			SUM(request_count)   AS req_sum,
			SUM(error_count)     AS err_sum
		`, bucketSeconds, bucketSeconds).
		Where("model = ? AND timestamp >= ?", modelName, since).
		Group("bucket_start").
		Order("bucket_start ASC").
		Scan(&rows).Error
	return rows, err
}

// bucketRowAll mirrors BucketRow but carries the model name for the batched
// scan. Internal type; callers receive map[string][]*BucketRow.
type bucketRowAll struct {
	Model        string  `gorm:"column:model"`
	BucketStart  int64   `gorm:"column:bucket_start"`
	Count        int     `gorm:"column:cnt"`
	Ok           int     `gorm:"column:ok"`
	Degraded     int     `gorm:"column:degraded"`
	ErrorCnt     int     `gorm:"column:err"`
	Empty        int     `gorm:"column:empty_cnt"`
	P95LatencyMs float64 `gorm:"column:avg_p95"`
	RequestSum   int     `gorm:"column:req_sum"`
	ErrorSum     int     `gorm:"column:err_sum"`
}

// AggregateBucketsAll returns bucketed aggregates for all models in a single
// query, keyed by model name. Replaces the N+1 per-model AggregateBuckets loop
// in the /page handler.
func AggregateBucketsAll(modelNames []string, bucketSeconds int64, since int64) (map[string][]*BucketRow, error) {
	out := map[string][]*BucketRow{}
	if len(modelNames) == 0 {
		return out, nil
	}
	var rows []*bucketRowAll
	err := DB.Table("model_status_pings").
		Select(`
			model,
			(timestamp / ?) * ? AS bucket_start,
			COUNT(*) AS cnt,
			SUM(CASE WHEN status = 'success'  THEN 1 ELSE 0 END) AS ok,
			SUM(CASE WHEN status = 'degraded' THEN 1 ELSE 0 END) AS degraded,
			SUM(CASE WHEN status = 'error'    THEN 1 ELSE 0 END) AS err,
			SUM(CASE WHEN status = 'empty'    THEN 1 ELSE 0 END) AS empty_cnt,
			AVG(p95_latency_ms)  AS avg_p95,
			SUM(request_count)   AS req_sum,
			SUM(error_count)     AS err_sum
		`, bucketSeconds, bucketSeconds).
		Where("model IN ? AND timestamp >= ?", modelNames, since).
		Group("model, bucket_start").
		Order("model ASC, bucket_start ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.Model] = append(out[r.Model], &BucketRow{
			BucketStart:  r.BucketStart,
			Count:        r.Count,
			Ok:           r.Ok,
			Degraded:     r.Degraded,
			ErrorCnt:     r.ErrorCnt,
			Empty:        r.Empty,
			P95LatencyMs: r.P95LatencyMs,
			RequestSum:   r.RequestSum,
			ErrorSum:     r.ErrorSum,
		})
	}
	return out, nil
}

// UptimeRow is one model's uptime counters over a window. Internal scan type.
type UptimeRow struct {
	Model    string `gorm:"column:model"`
	Ok       int    `gorm:"column:ok"`
	Degraded int    `gorm:"column:degraded"`
	ErrorCnt int    `gorm:"column:err"`
}

// UptimeByModelSince computes per-model uptime counters since `since` in one
// query. Caller converts to a 0-100 percentage. Replaces the N x computeUptime
// loop in /page and /components.
func UptimeByModelSince(modelNames []string, since int64) (map[string]float64, error) {
	out := map[string]float64{}
	if len(modelNames) == 0 {
		return out, nil
	}
	// No model filter in SQL: callers pass every component model anyway, and
	// a several-hundred-entry IN list pushes the planner off the timestamp
	// index into a full-table scan. Extra models (recently orphaned pings)
	// are dropped by the wanted-set check below.
	var rows []*UptimeRow
	err := DB.Table("model_status_pings").
		Select(`
			model,
			SUM(CASE WHEN status = 'success'  THEN 1 ELSE 0 END) AS ok,
			SUM(CASE WHEN status = 'degraded' THEN 1 ELSE 0 END) AS degraded,
			SUM(CASE WHEN status = 'error'    THEN 1 ELSE 0 END) AS err
		`).
		Where("timestamp >= ?", since).
		Group("model").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]struct{}, len(modelNames))
	for _, m := range modelNames {
		wanted[m] = struct{}{}
	}
	for _, r := range rows {
		if _, ok := wanted[r.Model]; !ok {
			continue
		}
		denom := r.Ok + r.Degraded + r.ErrorCnt
		if denom == 0 {
			out[r.Model] = 100
			continue
		}
		// Degraded minutes earn half credit: a day of nothing but degraded
		// capacity reads 50%, not a flawless-looking 100%.
		out[r.Model] = (float64(r.Ok) + float64(r.Degraded)/2) * 100 / float64(denom)
	}
	for _, m := range modelNames {
		if _, ok := out[m]; !ok {
			out[m] = 100
		}
	}
	return out, nil
}
