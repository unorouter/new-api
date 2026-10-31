package service

import (
	"sort"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"golang.org/x/sync/singleflight"
)

const (
	priceHistoryDays = 14
	// A drop is a new low against the previous week, so the window reaches a week
	// further back than the days it reports on.
	priceLowWindowDays = 7
	priceBestDays      = 7
	// A lane needs this many billed requests on a day to set a price; probes and a
	// stray request on a lane the sync deletes an hour later stay out.
	priceMinRequests = 50
	// Cheap lanes come and go with every full sync, which made a plain day over day
	// drop as common as a rise; only a fall under the whole previous week counts.
	priceDropMinPct     = 10
	priceDropMinHistory = 5
	priceDropLimit      = 100
	priceTodayTTL       = 30 * time.Minute
)

type PricePoint struct {
	Day             string  `json:"day"`
	Group           string  `json:"group"`
	Ratio           float64 `json:"ratio"`
	CompletionRatio float64 `json:"completion_ratio"`
	Requests        int64   `json:"requests"`
}

type ModelPriceHistory struct {
	ModelName string `json:"model_name"`
	// Cheapest lane that billed real traffic in the last week, live or not.
	Best *PricePoint  `json:"best,omitempty"`
	Days []PricePoint `json:"days" validate:"required"`
}

type PriceDrop struct {
	ModelName       string  `json:"model_name"`
	Vendor          string  `json:"vendor"`
	Day             string  `json:"day"`
	Group           string  `json:"group"`
	Ratio           float64 `json:"ratio"`
	CompletionRatio float64 `json:"completion_ratio"`
	PreviousRatio   float64 `json:"previous_ratio"`
	DropPct         float64 `json:"drop_pct"`
}

type PriceDropsResponse struct {
	Drops []PriceDrop `json:"drops" validate:"required"`
}

type priceSnapshot struct {
	models map[string]*ModelPriceHistory
	drops  []PriceDrop
}

var (
	priceMu sync.Mutex
	// Finished UTC days never change, so each is read from ClickHouse once per replica.
	priceDays     = map[string][]model.LanePriceDay{}
	priceSnap     *priceSnapshot
	priceBuiltAt  time.Time
	priceBuilds   singleflight.Group
	emptyPriceSet = &priceSnapshot{models: map[string]*ModelPriceHistory{}}
)

// StartPriceHistory reads the cold three weeks in the background, about 40s of
// ClickHouse, so the first visitor after a rollout does not wait on it.
func StartPriceHistory() {
	if !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return
	}
	go func() {
		if _, err, _ := priceBuilds.Do("price", buildPriceSnapshot); err != nil {
			common.SysError("price history build failed: " + err.Error())
		}
	}()
}

func GetModelPriceHistory(modelName string) (*ModelPriceHistory, error) {
	snap, err := currentPriceSnapshot()
	if err != nil {
		return nil, err
	}
	if h, ok := snap.models[modelName]; ok {
		return h, nil
	}
	return &ModelPriceHistory{ModelName: modelName, Days: []PricePoint{}}, nil
}

func GetPriceDrops() (*PriceDropsResponse, error) {
	snap, err := currentPriceSnapshot()
	if err != nil {
		return nil, err
	}
	return &PriceDropsResponse{Drops: snap.drops}, nil
}

// currentPriceSnapshot serves the last build and refreshes it in the background
// once it is older than priceTodayTTL; only a replica with no build yet waits.
func currentPriceSnapshot() (*priceSnapshot, error) {
	if !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return emptyPriceSet, nil
	}
	priceMu.Lock()
	snap, builtAt := priceSnap, priceBuiltAt
	priceMu.Unlock()
	if snap != nil {
		if time.Since(builtAt) > priceTodayTTL {
			go priceBuilds.Do("price", buildPriceSnapshot)
		}
		return snap, nil
	}
	built, err, _ := priceBuilds.Do("price", buildPriceSnapshot)
	if err != nil {
		return nil, err
	}
	return built.(*priceSnapshot), nil
}

func buildPriceSnapshot() (any, error) {
	now := time.Now().UTC()
	today := now.Truncate(24 * time.Hour)
	first := today.AddDate(0, 0, -(priceHistoryDays + priceLowWindowDays))

	priceMu.Lock()
	missingFrom := time.Time{}
	for d := first; d.Before(today); d = d.AddDate(0, 0, 1) {
		if _, ok := priceDays[d.Format(time.DateOnly)]; !ok {
			missingFrom = d
			break
		}
	}
	priceMu.Unlock()

	fetched := map[string][]model.LanePriceDay{}
	if !missingFrom.IsZero() {
		rows, err := model.GetLanePriceDays(missingFrom.Unix(), today.Unix(), priceMinRequests)
		if err != nil {
			return nil, err
		}
		for d := missingFrom; d.Before(today); d = d.AddDate(0, 0, 1) {
			fetched[d.Format(time.DateOnly)] = []model.LanePriceDay{}
		}
		for _, r := range rows {
			fetched[r.Day] = append(fetched[r.Day], r)
		}
	}
	todayRows, err := model.GetLanePriceDays(today.Unix(), now.Unix()+1, priceMinRequests)
	if err != nil {
		return nil, err
	}

	priceMu.Lock()
	for day, rows := range fetched {
		priceDays[day] = rows
	}
	firstKey := first.Format(time.DateOnly)
	all := make([]model.LanePriceDay, 0)
	for day, rows := range priceDays {
		if day < firstKey {
			delete(priceDays, day)
			continue
		}
		all = append(all, rows...)
	}
	priceMu.Unlock()
	all = append(all, todayRows...)

	snap := summarizePrices(all, today)
	meta := buildRankingModelMeta()
	for i := range snap.drops {
		snap.drops[i].Vendor = modelMeta(snap.drops[i].ModelName, meta).vendor
	}
	priceMu.Lock()
	priceSnap, priceBuiltAt = snap, time.Now()
	priceMu.Unlock()
	return snap, nil
}

func summarizePrices(rows []model.LanePriceDay, today time.Time) *priceSnapshot {
	cheapest := map[string]map[string]PricePoint{}
	best := map[string]PricePoint{}
	bestFrom := today.AddDate(0, 0, -(priceBestDays - 1)).Format(time.DateOnly)
	for _, r := range rows {
		p := PricePoint{Day: r.Day, Group: r.Group, Ratio: r.Ratio, CompletionRatio: r.CompletionRatio, Requests: r.Requests}
		days, ok := cheapest[r.ModelName]
		if !ok {
			days = map[string]PricePoint{}
			cheapest[r.ModelName] = days
		}
		if cur, ok := days[r.Day]; !ok || p.Ratio < cur.Ratio {
			days[r.Day] = p
		}
		if r.Day < bestFrom {
			continue
		}
		// Equal prices keep the most recent day, the lane most likely still around.
		if cur, ok := best[r.ModelName]; !ok || p.Ratio < cur.Ratio || (p.Ratio == cur.Ratio && p.Day > cur.Day) {
			best[r.ModelName] = p
		}
	}

	reportFrom := today.AddDate(0, 0, -(priceHistoryDays - 1)).Format(time.DateOnly)
	snap := &priceSnapshot{models: map[string]*ModelPriceHistory{}, drops: []PriceDrop{}}
	for name, days := range cheapest {
		series := make([]PricePoint, 0, len(days))
		for _, p := range days {
			series = append(series, p)
		}
		sort.Slice(series, func(i, j int) bool { return series[i].Day < series[j].Day })

		for i, p := range series {
			if p.Day < reportFrom {
				continue
			}
			day, _ := time.Parse(time.DateOnly, p.Day)
			weekStart := day.AddDate(0, 0, -priceLowWindowDays).Format(time.DateOnly)
			low, seen := 0.0, 0
			for _, prev := range series[:i] {
				if prev.Day < weekStart {
					continue
				}
				if seen == 0 || prev.Ratio < low {
					low = prev.Ratio
				}
				seen++
			}
			if seen < priceDropMinHistory || p.Ratio > low*(1-priceDropMinPct/100.0) {
				continue
			}
			snap.drops = append(snap.drops, PriceDrop{
				ModelName:       name,
				Day:             p.Day,
				Group:           p.Group,
				Ratio:           p.Ratio,
				CompletionRatio: p.CompletionRatio,
				PreviousRatio:   low,
				DropPct:         100 * (1 - p.Ratio/low),
			})
		}

		h := &ModelPriceHistory{ModelName: name, Days: []PricePoint{}}
		for _, p := range series {
			if p.Day >= reportFrom {
				h.Days = append(h.Days, p)
			}
		}
		if b, ok := best[name]; ok {
			h.Best = &b
		}
		snap.models[name] = h
	}
	sort.Slice(snap.drops, func(i, j int) bool {
		if snap.drops[i].Day != snap.drops[j].Day {
			return snap.drops[i].Day > snap.drops[j].Day
		}
		return snap.drops[i].DropPct > snap.drops[j].DropPct
	})
	if len(snap.drops) > priceDropLimit {
		snap.drops = snap.drops[:priceDropLimit]
	}
	return snap
}
