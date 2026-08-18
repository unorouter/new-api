package model

import "sort"

// ModelTrafficMetrics is per-model traffic computed from the log table for a
// single minute window. Percentiles are computed in Go to stay portable across
// SQLite, MySQL, and PostgreSQL (none agree on percentile syntax).
type ModelTrafficMetrics struct {
	RequestCount int
	ErrorCount   int
	P50LatencyMs int
	P95LatencyMs int
}

// CollectModelTrafficMetrics returns per-model real-traffic metrics for the
// half-open window [startTs, endTs). Excludes synthetic auto-tester rows
// (token_id == 0) so the numbers reflect real user traffic only.
func CollectModelTrafficMetrics(startTs, endTs int64) (map[string]*ModelTrafficMetrics, error) {
	out := map[string]*ModelTrafficMetrics{}

	type useTimeRow struct {
		ModelName string `gorm:"column:model_name"`
		UseTime   int    `gorm:"column:use_time"`
	}
	var consumeRows []useTimeRow
	err := LOG_DB.Table("logs").
		Select("model_name, use_time").
		Where("type = ? AND created_at >= ? AND created_at < ? AND token_id <> 0",
			LogTypeConsume, startTs, endTs).
		Scan(&consumeRows).Error
	if err != nil {
		return nil, err
	}

	perModelLatencies := map[string][]int{}
	for _, r := range consumeRows {
		if r.ModelName == "" {
			continue
		}
		m, ok := out[r.ModelName]
		if !ok {
			m = &ModelTrafficMetrics{}
			out[r.ModelName] = m
		}
		m.RequestCount++
		// use_time is in seconds in the log table; convert to ms for parity
		// with probe latency.
		perModelLatencies[r.ModelName] = append(perModelLatencies[r.ModelName], r.UseTime*1000)
	}

	for model, lats := range perModelLatencies {
		out[model].P50LatencyMs = percentile(lats, 50)
		out[model].P95LatencyMs = percentile(lats, 95)
	}

	type errRow struct {
		ModelName string `gorm:"column:model_name"`
		Cnt       int    `gorm:"column:cnt"`
	}
	var errRows []errRow
	err = LOG_DB.Table("logs").
		Select("model_name, COUNT(*) AS cnt").
		Where("type = ? AND created_at >= ? AND created_at < ?",
			LogTypeError, startTs, endTs).
		Group("model_name").
		Scan(&errRows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range errRows {
		if r.ModelName == "" {
			continue
		}
		m, ok := out[r.ModelName]
		if !ok {
			m = &ModelTrafficMetrics{}
			out[r.ModelName] = m
		}
		m.ErrorCount = r.Cnt
	}

	return out, nil
}

// percentile returns the nearest-rank percentile. Returns 0 for empty input.
func percentile(values []int, p int) int {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]int, len(values))
	copy(sorted, values)
	sort.Ints(sorted)
	idx := (p * len(sorted)) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
