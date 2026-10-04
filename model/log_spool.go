package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LogSpool holds log and audit rows ClickHouse refused, until the drain loop
// lands them. CreatedAt is the spool time, not the row's.
type LogSpool struct {
	Id        int64  `gorm:"primaryKey;autoIncrement"`
	Target    string `gorm:"type:text;not null"`
	DedupKey  string `gorm:"type:text;not null;index"`
	Payload   string `gorm:"type:jsonb;not null"`
	CreatedAt int64  `gorm:"type:bigint;not null;index"`
	Attempts  int    `gorm:"type:integer;not null;default:0"`
}

func (LogSpool) TableName() string {
	return "log_spool"
}

type clickHouseAuditRow struct {
	AuditLog     `gorm:"embedded"`
	EncodedOther string `gorm:"column:other;type:json"`
}

const (
	logSpoolInsertTimeout  = 10 * time.Second
	logSpoolDrainInterval  = 5 * time.Second
	logSpoolDepthInterval  = time.Minute
	logSpoolBatchSize      = 1000
	logSpoolClickHouseWait = 30 * time.Second
	// A timed out async insert can still be flushed by the server afterwards.
	logSpoolMinAge    = 30
	logSpoolDrainLock = 7342190451
)

func logSpoolEnabled() bool {
	return common.UsingLogDatabase(common.DatabaseTypeClickHouse) && common.UsingMainDatabase(common.DatabaseTypePostgreSQL)
}

func migrateLogSpool() error {
	if !logSpoolEnabled() {
		return nil
	}
	return DB.AutoMigrate(&LogSpool{})
}

func logRowDedupKey(log *Log) (string, error) {
	encoded, err := common.Marshal([]any{
		"logs", log.RequestId, log.CreatedAt, log.Type, log.Content, log.Quota, log.PromptTokens,
		log.CompletionTokens, log.ChannelId, log.TokenId, log.ModelName, log.Other, log.UserId,
		log.Username, log.TokenName, log.UseTime, log.IsStream, log.Group, log.UpstreamRequestId,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func spoolLogRow(table string, row any) error {
	if !logSpoolEnabled() {
		return errors.New("log spool requires a PostgreSQL main database")
	}
	var dedupKey string
	switch r := row.(type) {
	case *Log:
		key, err := logRowDedupKey(r)
		if err != nil {
			return err
		}
		dedupKey = key
	case *clickHouseAuditRow:
		dedupKey = r.EventId
	default:
		return fmt.Errorf("unsupported spool row %T", row)
	}
	payload, err := common.Marshal(row)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), logSpoolInsertTimeout)
	defer cancel()
	return DB.WithContext(ctx).Create(&LogSpool{
		Target:    table,
		DedupKey:  dedupKey,
		Payload:   string(payload),
		CreatedAt: time.Now().Unix(),
	}).Error
}

// StartLogSpoolDrain replays spooled rows into ClickHouse on the master node.
func StartLogSpoolDrain() {
	if !common.IsMasterNode || !logSpoolEnabled() {
		return
	}
	go func() {
		ticker := time.NewTicker(logSpoolDrainInterval)
		defer ticker.Stop()
		lastDepth := time.Now()
		for range ticker.C {
			for drainLogSpoolBatch() >= logSpoolBatchSize {
			}
			if time.Since(lastDepth) >= logSpoolDepthInterval {
				lastDepth = time.Now()
				var depth int64
				if err := DB.Model(&LogSpool{}).Count(&depth).Error; err != nil {
					common.SysError("failed to count log spool: " + err.Error())
				} else if depth > 0 {
					common.SysLog(fmt.Sprintf("log spool depth: %d", depth))
				}
			}
		}
	}()
}

// drainLogSpoolBatch returns how many spool rows it took, so a full batch is
// followed by another one in the same tick.
func drainLogSpoolBatch() int {
	taken := 0
	err := DB.Transaction(func(tx *gorm.DB) error {
		var locked bool
		if err := tx.Raw("SELECT pg_try_advisory_xact_lock(?)", logSpoolDrainLock).Scan(&locked).Error; err != nil {
			return err
		}
		if !locked {
			return nil
		}
		var batch []LogSpool
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("created_at <= ?", time.Now().Unix()-logSpoolMinAge).
			Order("id").Limit(logSpoolBatchSize).Find(&batch).Error; err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		// Identical log rows share a key and are counted as a multiset, so all
		// spooled copies of a key must be in the same batch.
		keys := make([]string, 0, len(batch))
		ids := make([]int64, 0, len(batch))
		for _, row := range batch {
			keys = append(keys, row.DedupKey)
			ids = append(ids, row.Id)
		}
		var siblings []LogSpool
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("dedup_key IN ? AND id NOT IN ?", keys, ids).Find(&siblings).Error; err != nil {
			return err
		}
		batch = append(batch, siblings...)
		taken = len(batch)

		byTarget := map[string][]LogSpool{}
		for _, row := range batch {
			byTarget[row.Target] = append(byTarget[row.Target], row)
		}
		var drained []int64
		inserted, skipped := 0, 0
		for _, target := range []string{"logs", "audit_logs"} {
			rows := byTarget[target]
			if len(rows) == 0 {
				continue
			}
			done, n, err := replaySpoolRows(target, rows)
			if err != nil {
				common.SysError(fmt.Sprintf("log spool drain of %s failed, %d rows kept: %v", target, len(rows), err))
				failed := make([]int64, 0, len(rows))
				for _, row := range rows {
					failed = append(failed, row.Id)
				}
				if len(drained) > 0 {
					if err := tx.Where("id IN ?", drained).Delete(&LogSpool{}).Error; err != nil {
						return err
					}
				}
				taken = 0
				return tx.Model(&LogSpool{}).Where("id IN ?", failed).
					UpdateColumn("attempts", gorm.Expr("attempts + 1")).Error
			}
			drained = append(drained, done...)
			inserted += n
			skipped += len(done) - n
		}
		if len(drained) > 0 {
			if err := tx.Where("id IN ?", drained).Delete(&LogSpool{}).Error; err != nil {
				return err
			}
		}
		common.SysLog(fmt.Sprintf("log spool drained %d rows: %d inserted, %d already in ClickHouse", len(drained), inserted, skipped))
		return nil
	})
	if err != nil {
		common.SysError("log spool drain failed: " + err.Error())
		return 0
	}
	return taken
}

// replaySpoolRows inserts the rows ClickHouse does not have yet and returns the
// spool ids that are safe to delete plus the number of rows inserted.
func replaySpoolRows(target string, rows []LogSpool) ([]int64, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), logSpoolClickHouseWait)
	defer cancel()
	minCreated, maxCreated := int64(0), int64(0)
	var done []int64
	switch target {
	case "logs":
		var pending []*Log
		var pendingKeys []string
		requestIds := map[string]struct{}{}
		for _, row := range rows {
			var log Log
			if err := common.UnmarshalJsonStr(row.Payload, &log); err != nil {
				common.SysError(fmt.Sprintf("log spool row %d is unreadable and stays spooled: %v", row.Id, err))
				continue
			}
			pending = append(pending, &log)
			pendingKeys = append(pendingKeys, row.DedupKey)
			done = append(done, row.Id)
			requestIds[log.RequestId] = struct{}{}
			if minCreated == 0 || log.CreatedAt < minCreated {
				minCreated = log.CreatedAt
			}
			maxCreated = max(maxCreated, log.CreatedAt)
		}
		if len(pending) == 0 {
			return done, 0, nil
		}
		ids := make([]string, 0, len(requestIds))
		for id := range requestIds {
			ids = append(ids, id)
		}
		var existing []*Log
		if err := LOG_DB.WithContext(ctx).Table("logs").
			Where("created_at >= ? AND created_at <= ? AND request_id IN ?", minCreated, maxCreated, ids).
			Find(&existing).Error; err != nil {
			return nil, 0, err
		}
		present := map[string]int{}
		for _, log := range existing {
			key, err := logRowDedupKey(log)
			if err != nil {
				return nil, 0, err
			}
			present[key]++
		}
		var missing []*Log
		for i, log := range pending {
			if present[pendingKeys[i]] > 0 {
				present[pendingKeys[i]]--
				continue
			}
			missing = append(missing, log)
		}
		if len(missing) > 0 {
			if err := LOG_DB.WithContext(syncInsertContext(ctx)).Table("logs").Create(&missing).Error; err != nil {
				return nil, 0, err
			}
		}
		return done, len(missing), nil
	case "audit_logs":
		var pending []*clickHouseAuditRow
		eventIds := make([]string, 0, len(rows))
		for _, row := range rows {
			var audit clickHouseAuditRow
			if err := common.UnmarshalJsonStr(row.Payload, &audit); err != nil {
				common.SysError(fmt.Sprintf("log spool row %d is unreadable and stays spooled: %v", row.Id, err))
				continue
			}
			pending = append(pending, &audit)
			done = append(done, row.Id)
			eventIds = append(eventIds, audit.EventId)
			if minCreated == 0 || audit.CreatedAt < minCreated {
				minCreated = audit.CreatedAt
			}
			maxCreated = max(maxCreated, audit.CreatedAt)
		}
		if len(pending) == 0 {
			return done, 0, nil
		}
		var existing []string
		if err := LOG_DB.WithContext(ctx).Table("audit_logs").
			Where("created_at >= ? AND created_at <= ? AND event_id IN ?", minCreated, maxCreated, eventIds).
			Pluck("event_id", &existing).Error; err != nil {
			return nil, 0, err
		}
		present := make(map[string]bool, len(existing))
		for _, id := range existing {
			present[id] = true
		}
		var missing []*clickHouseAuditRow
		for _, audit := range pending {
			if present[audit.EventId] {
				continue
			}
			present[audit.EventId] = true
			missing = append(missing, audit)
		}
		if len(missing) > 0 {
			if err := LOG_DB.WithContext(syncInsertContext(ctx)).Table("audit_logs").Create(&missing).Error; err != nil {
				return nil, 0, err
			}
		}
		return done, len(missing), nil
	}
	return nil, 0, fmt.Errorf("unknown spool target %q", target)
}

// The replayed rows must be committed before their spool rows are deleted.
func syncInsertContext(ctx context.Context) context.Context {
	return clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert": 0,
	}))
}
