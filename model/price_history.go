package model

import (
	"github.com/QuantumNous/new-api/common"
)

// LanePriceDay is the cheapest price one lane billed a model at on one UTC day.
// Ratio is model ratio times group ratio, so input USD per million is
// Ratio * 2, the same product the catalog prices a group with.
type LanePriceDay struct {
	Day             string  `gorm:"column:day"`
	ModelName       string  `gorm:"column:model_name"`
	Group           string  `gorm:"column:lane"`
	Ratio           float64 `gorm:"column:ratio"`
	CompletionRatio float64 `gorm:"column:completion_ratio"`
	Requests        int64   `gorm:"column:requests"`
}

// GetLanePriceDays reads per lane daily price minimums for [startTs, endTs)
// from the ClickHouse request log; the price a request was billed at is the
// only record of a lane once the sync deletes its channel. Fixed price models,
// free rows, user specific group ratios and auto-tester rows are left out, and
// a lane needs minRequests on a day so a probe or a one-off is never a price.
// A request billed from the upstream cost floor paid more than the ratio says,
// so it sets no price, and a day mostly billed that way sets none at all.
func GetLanePriceDays(startTs, endTs int64, minRequests int) ([]LanePriceDay, error) {
	if !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return nil, nil
	}
	var rows []LanePriceDay
	err := LOG_DB.Raw(`
SELECT toString(toDate(toDateTime(created_at))) AS day, model_name, `+"`group`"+` AS lane,
  minIf(r, f = 0) AS ratio, argMinIf(c, r, f = 0) AS completion_ratio, countIf(f = 0) AS requests
FROM (
  SELECT created_at, model_name, `+"`group`"+`,
    JSONExtractFloat(other, 'model_ratio') * JSONExtractFloat(other, 'group_ratio') AS r,
    JSONExtractFloat(other, 'completion_ratio') AS c,
    JSONExtractFloat(other, 'model_price') AS p,
    JSONHas(other, 'user_group_ratio') AS hu,
    JSONExtractFloat(other, 'user_group_ratio') AS u,
    JSONExtractBool(other, 'admin_info', 'billed_from_upstream_cost') AS f
  FROM logs
  WHERE type = ? AND created_at >= ? AND created_at < ? AND quota > 0 AND token_id <> 0 AND `+"`group`"+` <> ''
)
WHERE r > 0 AND p <= 0 AND (hu = 0 OR u < 0)
GROUP BY day, model_name, lane
HAVING requests >= ? AND countIf(f = 1) <= requests`, LogTypeConsume, startTs, endTs, minRequests).Scan(&rows).Error
	return rows, err
}
