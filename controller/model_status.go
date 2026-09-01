package controller

import (
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"

	"github.com/go-fuego/fuego"
	"golang.org/x/sync/singleflight"
)

// ----- Response DTOs (match the OpenStatus lib's TypeScript types verbatim) -----

// ComponentDTO maps to the StatusComponent props on the lib side.
type ComponentDTO struct {
	Id             int     `json:"id"`
	Name           string  `json:"name"`
	Description    string  `json:"description"`
	GroupId        *int    `json:"group_id,omitempty"`
	Status         string  `json:"status"` // success|degraded|error|empty
	UpChannels     int     `json:"up_channels"`
	TotalChannels  int     `json:"total_channels"`
	ProbeLatencyMs int     `json:"probe_latency_ms"`
	Uptime24h      float64 `json:"uptime_24h"`
	Uptime30d      float64 `json:"uptime_30d"`
	OpenIncidentId *int    `json:"open_incident_id,omitempty"`
	SampledAt      int64   `json:"sampled_at"`
}

// EventDTO matches StatusBarData.events item: {id, name, type, from, to, isAggregated?}.
// `From`/`To` are RFC3339 (ISO 8601) strings so the JS side can `new Date(s)` /
// `dayjs(s)` directly without a millisecond multiplication step at the boundary.
type EventDTO struct {
	Id           int     `json:"id"`
	Name         string  `json:"name"`
	Type         string  `json:"type"`
	From         string  `json:"from"`
	To           *string `json:"to"`
	IsAggregated bool    `json:"is_aggregated,omitempty"`
}

// BarSegmentDTO is one stacked segment within a single bucket; heights sum to 100.
type BarSegmentDTO struct {
	Status string `json:"status"`
	Height int    `json:"height"`
}

// CardItemDTO is one row in the hover card breakdown.
type CardItemDTO struct {
	Status string `json:"status"`
	Value  string `json:"value"`
}

// StatusBarDataDTO matches StatusBarData[] verbatim. `Day` is an ISO timestamp
// of the bucket start; the lib accepts any ISO string (not just calendar days).
type StatusBarDataDTO struct {
	Day    string          `json:"day"`
	Bar    []BarSegmentDTO `json:"bar" validate:"required"`
	Card   []CardItemDTO   `json:"card" validate:"required"`
	Events []EventDTO      `json:"events" validate:"required"`
}

// CompactBarDTO carries one model's buckets and its incident overlay. Events
// are a sparse map keyed by bucket index (string for JSON) -> incident ids that
// overlap that bucket. Incident metadata lives once at the top level.
type CompactBarDTO struct {
	Buckets [][7]int         `json:"buckets"`
	Events  map[string][]int `json:"events,omitempty"`
}

// CompactPageDTO is the compact wire format for /page when ?compact=1. Cuts
// payload ~10x by dropping per-bucket strings, RFC3339 timestamps, and empty
// arrays. Client reconstructs StatusBarData[] from this.
type CompactPageDTO struct {
	Components  []ComponentDTO            `json:"components"`
	Incidents   []EventDTO                `json:"incidents"`
	BucketStart int64                     `json:"bucket_start"`
	BucketSec   int64                     `json:"bucket_sec"`
	BucketCount int                       `json:"bucket_count"`
	Bars        map[string]*CompactBarDTO `json:"bars"`
}

// ----- Bucket parsing -----

var bucketSeconds = map[string]int64{
	"1m":  60,
	"5m":  300,
	"15m": 900,
	"1h":  3600,
	"1d":  86400,
}

func resolveBucketSeconds(bucket string) int64 {
	if v, ok := bucketSeconds[bucket]; ok {
		return v
	}
	return bucketSeconds["15m"]
}

// coarsenBucketToCap: the payload scales with modelCount x (window/bucketSec),
// and every public model is aggregated per request. Cap the total buckets by
// widening the bucket until the window fits under maxBucketsPerModel, so a
// fine bucket over a long window can't materialize a multi-GB response.
const maxBucketsPerModel = 1500

var bucketLadder = []int64{60, 300, 900, 3600, 86400}

// The windows the status page actually offers. Requests snap up to the nearest
// one before the cache key is built.
var hourLadder = []int{1, 6, 24, 168, 720}

// snapHours collapses an arbitrary hours value onto hourLadder. The response is
// already size-capped by coarsenBucketToCap, but hours reaches the cache key
// verbatim, so 720 distinct values are 720 distinct keys and every one of them
// is a full aggregation over every public model. Snapping keeps that at five.
func snapHours(hours int) int {
	for _, step := range hourLadder {
		if hours <= step {
			return step
		}
	}
	return hourLadder[len(hourLadder)-1]
}

func coarsenBucketToCap(bucketSec int64, windowSec int64) int64 {
	for _, step := range bucketLadder {
		if step < bucketSec {
			continue
		}
		if windowSec/step <= maxBucketsPerModel {
			return step
		}
	}
	return bucketLadder[len(bucketLadder)-1]
}

// Short in-process cache. Snapshots refresh once a minute upstream, so a 1min
// TTL collapses concurrent public reads onto a single aggregation without
// serving visibly stale bars. Keyed by (compact, bucket, hours).
const statusPageCacheTTL = time.Minute

type statusPageCacheEntry struct {
	payload   any
	expiresAt time.Time
}

var (
	statusPageCacheMu sync.Mutex
	statusPageCache   = map[string]statusPageCacheEntry{}

	// The TTL alone does not collapse concurrent readers: it only stops work that
	// starts after a result is stored. This aggregation takes ~12s over every
	// public model, so on a cold key every request arriving inside that window
	// starts its own copy, and an anonymous caller can force arbitrarily many by
	// firing in parallel. singleflight makes the first caller do the work and the
	// rest wait on its result, which is what the cache was always assumed to do.
	statusPageGroup singleflight.Group
)

func statusPageCacheGet(key string) (any, bool) {
	statusPageCacheMu.Lock()
	defer statusPageCacheMu.Unlock()
	e, ok := statusPageCache[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expiresAt) {
		delete(statusPageCache, key)
		return nil, false
	}
	return e.payload, true
}

func statusPageCacheSet(key string, payload any) {
	statusPageCacheMu.Lock()
	defer statusPageCacheMu.Unlock()
	// Expired entries have to be dropped, not just ignored on read. The route is
	// public and the key is (bucket, hours), so a caller walking hours=1..720 mints
	// a distinct entry per request; at ~1.25MB per payload that reaches gigabytes
	// against a 2Gi limit while every entry is already stale. Sweeping here keeps
	// the map to what was actually requested within one TTL.
	now := time.Now()
	for k, e := range statusPageCache {
		if now.After(e.expiresAt) {
			delete(statusPageCache, k)
		}
	}
	statusPageCache[key] = statusPageCacheEntry{
		payload:   payload,
		expiresAt: now.Add(statusPageCacheTTL),
	}
}

// The uptime percentages are rolling averages over 24h/30d of pings; the 30d
// window covers nearly the whole ping table (17M+ rows), so computing them per
// request turned every /components poll into a full-table aggregate. Serving
// them minutes stale is imperceptible while the status bars stay per-minute
// fresh through the page cache above.
type uptimeCacheEntry struct {
	at  time.Time
	key string
	val map[string]float64
	// Held for the duration of one refresh so concurrent callers wait for it
	// instead of each running their own. Nil when no refresh is in flight.
	inflight *sync.WaitGroup
}

var (
	uptimeCacheMu  sync.Mutex
	uptime24hCache uptimeCacheEntry
	uptime30dCache uptimeCacheEntry
)

// cachedUptimeSince serves the window from cache, and on a miss lets exactly one
// caller run the aggregate while the rest wait for its result.
//
// The mutex used to cover only the map read and the map write, with the query
// itself running unlocked. That is a cache stampede: when the TTL lapsed every
// concurrent request saw a stale entry and all of them ran the full aggregate
// at once. Measured 2026-08-31 on the node1 saturation alert -- four concurrent
// copies, parallel workers spilling to disk (BufFileRead), 2.3 of 4 cores in one
// pod. Both callers are PUBLIC unauthenticated endpoints (/pricing/catalog and
// /model_status/components), so the concurrency is whatever the internet sends.
func cachedUptimeSince(cache *uptimeCacheEntry, ttl time.Duration, modelNames []string, since int64) (map[string]float64, error) {
	key := fmt.Sprintf("%d", len(modelNames))
	for {
		uptimeCacheMu.Lock()
		if cache.key == key && time.Since(cache.at) < ttl {
			v := cache.val
			uptimeCacheMu.Unlock()
			return v, nil
		}
		if wg := cache.inflight; wg != nil {
			// Someone else is already computing this window. Wait for them and
			// re-check rather than issuing a duplicate aggregate.
			uptimeCacheMu.Unlock()
			wg.Wait()
			continue
		}
		wg := &sync.WaitGroup{}
		wg.Add(1)
		cache.inflight = wg
		uptimeCacheMu.Unlock()

		val, err := model.UptimeByModelSince(modelNames, since)

		uptimeCacheMu.Lock()
		cache.inflight = nil
		if err == nil {
			cache.at, cache.key, cache.val = time.Now(), key, val
		}
		uptimeCacheMu.Unlock()
		wg.Done()

		if err != nil {
			return nil, err
		}
		return val, nil
	}
}

// cachedUptimes24 is the 24h window alone, for callers that would otherwise pay
// for a 30-day aggregate they discard. The returned map is the CACHED one, shared
// with every other caller: read it, never write to it.
func cachedUptimes24(modelNames []string) (map[string]float64, error) {
	return cachedUptimeSince(&uptime24hCache, 5*time.Minute, modelNames, time.Now().Unix()-24*60*60)
}

func cachedUptimes(modelNames []string) (map[string]float64, map[string]float64, error) {
	now := time.Now().Unix()
	u24, err := cachedUptimeSince(&uptime24hCache, 5*time.Minute, modelNames, now-24*60*60)
	if err != nil {
		return nil, nil, err
	}
	u30, err := cachedUptimeSince(&uptime30dCache, time.Hour, modelNames, now-30*24*60*60)
	if err != nil {
		return nil, nil, err
	}
	return u24, u30, nil
}

// ----- Handlers -----

// GET /api/model_status/components
func GetModelStatusComponents(c fuego.ContextNoBody) (*dto.Response[[]ComponentDTO], error) {
	comps, err := model.GetAllPublicModelStatusComponents()
	if err != nil {
		return dto.Fail[[]ComponentDTO](err.Error())
	}
	latest, err := model.LatestPingByModel()
	if err != nil {
		return dto.Fail[[]ComponentDTO](err.Error())
	}
	modelNames := make([]string, 0, len(comps))
	componentIds := make([]int, 0, len(comps))
	for _, comp := range comps {
		modelNames = append(modelNames, comp.ModelName)
		componentIds = append(componentIds, comp.Id)
	}

	uptime24h, uptime30d, err := cachedUptimes(modelNames)
	if err != nil {
		return dto.Fail[[]ComponentDTO](err.Error())
	}
	openIncidents, err := model.OpenIncidentsByComponent(componentIds)
	if err != nil {
		return dto.Fail[[]ComponentDTO](err.Error())
	}

	items := make([]ComponentDTO, 0, len(comps))
	for _, comp := range comps {
		dto := ComponentDTO{
			Id:          comp.Id,
			Name:        comp.ModelName,
			Description: comp.Description,
			GroupId:     comp.GroupId,
			Status:      model.ModelStatusEmpty,
		}
		if p, ok := latest[comp.ModelName]; ok {
			dto.Status = p.Status
			dto.UpChannels = p.UpChannels
			dto.TotalChannels = p.TotalChannels
			dto.ProbeLatencyMs = p.LatencyMs
			dto.SampledAt = p.Timestamp
		}
		dto.Uptime24h = uptime24h[comp.ModelName]
		dto.Uptime30d = uptime30d[comp.ModelName]
		if id, ok := openIncidents[comp.Id]; ok {
			idCopy := id
			dto.OpenIncidentId = &idCopy
		}
		items = append(items, dto)
	}
	return dtoOk(items)
}

// GET /api/model_status/buckets?model=X&bucket=15m&hours=24
func GetModelStatusBuckets(c fuego.ContextWithParams[dto.GetModelStatusBucketsParams]) (*dto.Response[[]StatusBarDataDTO], error) {
	p, _ := dto.ParseParams[dto.GetModelStatusBucketsParams](c)
	if p.Model == "" {
		return dto.Fail[[]StatusBarDataDTO]("model is required")
	}
	hours := p.Hours
	if hours <= 0 {
		hours = 24
	}
	hours = snapHours(hours)
	// Same cap as page_compact: this route is public and hours reaches 720, so an
	// uncapped 1m bucket asks for 43,200 buckets per model in one anonymous request.
	bucketSec := coarsenBucketToCap(resolveBucketSeconds(p.Bucket), int64(hours)*60*60)
	since := time.Now().Unix() - int64(hours)*60*60

	rows, err := model.AggregateBuckets(p.Model, bucketSec, since)
	if err != nil {
		return dto.Fail[[]StatusBarDataDTO](err.Error())
	}

	// Look up the component once for incident overlay. Missing component =>
	// no events (model not yet probed).
	var componentId int
	if comp, _ := model.GetComponentByModel(p.Model); comp != nil {
		componentId = comp.Id
	}

	// Incidents for the WHOLE window in one query, then bucketed in memory.
	// This used to call ListIncidentsByComponentBetween once per bucket, and the
	// cost tracked bucket count rather than data volume: 24h at 15m is 96 buckets
	// and took 52s, while 720h at 1d is 30 buckets and took 10s -- more data, less
	// time. Each lookup is only ~0.3ms in the database; the rest was waiting for a
	// pool slot, 96 times, on a public uncached route where every anonymous caller
	// could tie up that many connections at once.
	var windowIncidents []*model.ModelStatusIncident
	if componentId != 0 && len(rows) > 0 {
		windowStart := rows[0].BucketStart
		windowEnd := rows[len(rows)-1].BucketStart + bucketSec
		windowIncidents, _ = model.ListIncidentsByComponentBetween(componentId, windowStart, windowEnd)
	}

	items := make([]StatusBarDataDTO, 0, len(rows))
	for _, r := range rows {
		bucketEnd := r.BucketStart + bucketSec
		item := StatusBarDataDTO{
			Day:    time.Unix(r.BucketStart, 0).UTC().Format(time.RFC3339),
			Bar:    buildBarSegments(r),
			Card:   buildCardItems(r),
			Events: []EventDTO{},
		}
		// Same overlap test the per-bucket query used (started_at < to AND
		// (resolved_at IS NULL OR resolved_at >= from)): an incident belongs to
		// this bucket if it began before the bucket ended and had not resolved
		// before the bucket started. A nil ResolvedAt is still open, so it
		// overlaps every bucket after it began.
		for _, inc := range windowIncidents {
			if inc.StartedAt >= bucketEnd {
				continue
			}
			if inc.ResolvedAt == nil || *inc.ResolvedAt >= r.BucketStart {
				item.Events = append(item.Events, incidentToEvent(inc))
			}
		}
		items = append(items, item)
	}
	return dtoOk(items)
}

// buildBarSegments converts a BucketRow into bar segments summing to 100.
// Zero-height segments are omitted to keep the JSON small.
func buildBarSegments(r *model.BucketRow) []BarSegmentDTO {
	if r.Count == 0 {
		return []BarSegmentDTO{{Status: model.ModelStatusEmpty, Height: 100}}
	}
	pct := func(n int) int { return (n * 100) / r.Count }
	segs := []BarSegmentDTO{}
	if h := pct(r.Ok); h > 0 {
		segs = append(segs, BarSegmentDTO{Status: model.ModelStatusSuccess, Height: h})
	}
	if h := pct(r.Degraded); h > 0 {
		segs = append(segs, BarSegmentDTO{Status: model.ModelStatusDegraded, Height: h})
	}
	if h := pct(r.ErrorCnt); h > 0 {
		segs = append(segs, BarSegmentDTO{Status: model.ModelStatusError, Height: h})
	}
	if h := pct(r.Empty); h > 0 {
		segs = append(segs, BarSegmentDTO{Status: model.ModelStatusEmpty, Height: h})
	}
	// Pad the last segment so heights sum to exactly 100 (rounding fix).
	if len(segs) > 0 {
		var sum int
		for _, s := range segs {
			sum += s.Height
		}
		if diff := 100 - sum; diff != 0 {
			segs[len(segs)-1].Height += diff
		}
	}
	return segs
}

// buildCardItems formats per-bucket metrics for the hover card.
func buildCardItems(r *model.BucketRow) []CardItemDTO {
	items := []CardItemDTO{}
	if r.Ok > 0 {
		items = append(items, CardItemDTO{
			Status: model.ModelStatusSuccess,
			Value:  fmt.Sprintf("%d min", r.Ok),
		})
	}
	if r.Degraded > 0 {
		items = append(items, CardItemDTO{
			Status: model.ModelStatusDegraded,
			Value:  fmt.Sprintf("%d min", r.Degraded),
		})
	}
	if r.ErrorCnt > 0 {
		items = append(items, CardItemDTO{
			Status: model.ModelStatusError,
			Value:  fmt.Sprintf("%d min", r.ErrorCnt),
		})
	}
	if r.RequestSum > 0 || r.ErrorSum > 0 {
		latency := ""
		if r.P95LatencyMs > 0 {
			latency = fmt.Sprintf(" / p95 %s", formatMs(int(r.P95LatencyMs)))
		}
		items = append(items, CardItemDTO{
			Status: model.ModelStatusSuccess,
			Value:  fmt.Sprintf("%d req / %d err%s", r.RequestSum, r.ErrorSum, latency),
		})
	}
	return items
}

func formatMs(ms int) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

func incidentToEvent(inc *model.ModelStatusIncident) EventDTO {
	var to *string
	if inc.ResolvedAt != nil {
		s := time.Unix(*inc.ResolvedAt, 0).UTC().Format(time.RFC3339)
		to = &s
	}
	return EventDTO{
		Id:   inc.Id,
		Name: inc.Title,
		Type: inc.EventType,
		From: time.Unix(inc.StartedAt, 0).UTC().Format(time.RFC3339),
		To:   to,
	}
}

// GET /api/model_status/incidents?since=...&until=...&model=...
func GetModelStatusIncidents(c fuego.ContextWithParams[dto.GetModelStatusIncidentsParams]) (*dto.Response[[]EventDTO], error) {
	p, _ := dto.ParseParams[dto.GetModelStatusIncidentsParams](c)
	now := time.Now().Unix()
	since := p.Since
	if since == 0 {
		since = now - 24*60*60
	}
	until := p.Until
	if until == 0 {
		until = now
	}
	rows, err := model.ListIncidentsBetween(since, until, p.Model)
	if err != nil {
		return dto.Fail[[]EventDTO](err.Error())
	}
	out := make([]EventDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, incidentToEvent(r))
	}
	return dtoOk(out)
}

// GET /api/model_status/page_compact?bucket=1m&hours=24
// Compact wire format: bucket counters as fixed-length int tuples keyed by
// position. Client reconstructs StatusBarData[] from this. ~10x smaller than
// /page (15 MB -> ~1.5 MB) for 1m x 24h x 83 models.
func GetModelStatusPageCompact(c fuego.ContextWithParams[dto.GetModelStatusPageParams]) (*dto.Response[CompactPageDTO], error) {
	p, _ := dto.ParseParams[dto.GetModelStatusPageParams](c)

	hours := p.Hours
	if hours <= 0 {
		hours = 24
	}
	hours = snapHours(hours)
	bucketSec := coarsenBucketToCap(resolveBucketSeconds(p.Bucket), int64(hours)*60*60)

	cacheKey := fmt.Sprintf("compact|%d|%d", bucketSec, hours)
	if cached, ok := statusPageCacheGet(cacheKey); ok {
		if page, ok := cached.(CompactPageDTO); ok {
			return dtoOk(page)
		}
	}

	// Only one caller per key builds; the rest block here and share its result.
	built, err, _ := statusPageGroup.Do(cacheKey, func() (any, error) {
		// Re-check inside the flight: the winner of a race that just finished may
		// have populated the cache while this closure was being scheduled.
		if cached, ok := statusPageCacheGet(cacheKey); ok {
			if page, ok := cached.(CompactPageDTO); ok {
				return page, nil
			}
		}
		return buildCompactPage(bucketSec, hours, cacheKey)
	})
	if err != nil {
		return dto.Fail[CompactPageDTO](err.Error())
	}
	page, ok := built.(CompactPageDTO)
	if !ok {
		return dto.Fail[CompactPageDTO]("status page build returned an unexpected type")
	}
	return dto.Ok(page)
}

// WarmStatusPageCache keeps the status page's cache populated so no public
// request ever pays for the aggregation.
//
// The build takes ~12s across every public model. singleflight stops a cold key
// from being built more than once per process, but the cache is in-process and
// three pods serve this route, so a cold key still costs three builds and the
// first caller after each expiry still waits. Warming ahead of the TTL means the
// request path is always a cache hit and cold builds stop existing rather than
// merely being deduplicated.
//
// Only the five windows the status page offers are warmed, not all twenty
// reachable keys: the rest are only produced by a caller passing an unusual
// bucket, which snapHours and coarsenBucketToCap already collapse onto a bounded
// set, and building them on a timer would spend more than it saves.
//
// Runs on every pod, master and slave alike, because the cache it fills is
// per-process. Gating this on IsMasterNode would leave the two slaves cold and
// defeat the point.
func WarmStatusPageCache() {
	warm := func() {
		for _, w := range statusPageWarmWindows {
			bucketSec := coarsenBucketToCap(resolveBucketSeconds(w.bucket), int64(w.hours)*60*60)
			key := fmt.Sprintf("compact|%d|%d", bucketSec, w.hours)
			// Through singleflight so a warm tick and a live request that race
			// share one build rather than doubling the work they exist to avoid.
			if _, err, _ := statusPageGroup.Do(key, func() (any, error) {
				return buildCompactPage(bucketSec, w.hours, key)
			}); err != nil {
				common.SysLog("status page warm failed for " + key + ": " + err.Error())
			}
		}
	}
	warm()
	ticker := time.NewTicker(statusPageWarmInterval)
	for range ticker.C {
		warm()
	}
}

// Refresh ahead of statusPageCacheTTL so an entry is replaced before it expires
// and a request never lands on a gap.
const statusPageWarmInterval = 45 * time.Second

// The windows BUCKET_OPTIONS offers in the frontend status page.
var statusPageWarmWindows = []struct {
	bucket string
	hours  int
}{
	{"1m", 1},
	{"5m", 6},
	{"15m", 24},
	{"1h", 168},
	{"1d", 720},
}

func buildCompactPage(bucketSec int64, hours int, cacheKey string) (CompactPageDTO, error) {
	comps, err := model.GetAllPublicModelStatusComponents()
	if err != nil {
		return CompactPageDTO{}, err
	}
	latest, err := model.LatestPingByModel()
	if err != nil {
		return CompactPageDTO{}, err
	}

	now := time.Now().Unix()
	since := (now - int64(hours)*60*60) / bucketSec * bucketSec
	bucketCount := int((now-since)/bucketSec) + 1

	modelNames := make([]string, 0, len(comps))
	for _, comp := range comps {
		modelNames = append(modelNames, comp.ModelName)
	}

	uptime24h, uptime30d, err := cachedUptimes(modelNames)
	if err != nil {
		return CompactPageDTO{}, err
	}
	bucketsByModel, err := model.AggregateBucketsAll(modelNames, bucketSec, since)
	if err != nil {
		return CompactPageDTO{}, err
	}

	allIncidents, _ := model.ListIncidentsBetween(since, now, "")
	incByComp := map[int][]*model.ModelStatusIncident{}
	for _, inc := range allIncidents {
		incByComp[inc.ComponentId] = append(incByComp[inc.ComponentId], inc)
	}

	page := CompactPageDTO{
		Components:  make([]ComponentDTO, 0, len(comps)),
		BucketStart: since,
		BucketSec:   bucketSec,
		BucketCount: bucketCount,
		Bars:        map[string]*CompactBarDTO{},
	}

	for _, comp := range comps {
		cdto := ComponentDTO{
			Id:          comp.Id,
			Name:        comp.ModelName,
			Description: comp.Description,
			GroupId:     comp.GroupId,
			Status:      model.ModelStatusEmpty,
		}
		if ping, ok := latest[comp.ModelName]; ok {
			cdto.Status = ping.Status
			cdto.UpChannels = ping.UpChannels
			cdto.TotalChannels = ping.TotalChannels
			cdto.ProbeLatencyMs = ping.LatencyMs
			cdto.SampledAt = ping.Timestamp
		}
		cdto.Uptime24h = uptime24h[comp.ModelName]
		cdto.Uptime30d = uptime30d[comp.ModelName]
		for _, inc := range incByComp[comp.Id] {
			if inc.ResolvedAt == nil {
				id := inc.Id
				cdto.OpenIncidentId = &id
				break
			}
		}
		page.Components = append(page.Components, cdto)

		rows := bucketsByModel[comp.ModelName]
		rowByStart := make(map[int64]*model.BucketRow, len(rows))
		for _, r := range rows {
			rowByStart[r.BucketStart] = r
		}

		buckets := make([][7]int, bucketCount)
		for i := 0; i < bucketCount; i++ {
			ts := since + int64(i)*bucketSec
			if r, ok := rowByStart[ts]; ok {
				buckets[i] = [7]int{r.Ok, r.Degraded, r.ErrorCnt, r.Empty, r.RequestSum, r.ErrorSum, int(r.P95LatencyMs)}
			}
		}

		bar := &CompactBarDTO{Buckets: buckets}
		if incs := incByComp[comp.Id]; len(incs) > 0 {
			ev := map[string][]int{}
			for i := 0; i < bucketCount; i++ {
				ts := since + int64(i)*bucketSec
				bucketEnd := ts + bucketSec
				var ids []int
				for _, inc := range incs {
					if inc.StartedAt < bucketEnd && (inc.ResolvedAt == nil || *inc.ResolvedAt >= ts) {
						ids = append(ids, inc.Id)
					}
				}
				if len(ids) > 0 {
					ev[fmt.Sprintf("%d", i)] = ids
				}
			}
			if len(ev) > 0 {
				bar.Events = ev
			}
		}
		page.Bars[comp.ModelName] = bar
	}

	for _, inc := range allIncidents {
		page.Incidents = append(page.Incidents, incidentToEvent(inc))
	}

	statusPageCacheSet(cacheKey, page)
	return page, nil
}

// dtoOk wraps a successful response. Local helper so handlers stay terse.
func dtoOk[T any](v T) (*dto.Response[T], error) {
	return dto.Ok(v)
}
