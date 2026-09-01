package logger

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

const (
	loggerINFO  = "INFO"
	loggerWarn  = "WARN"
	loggerError = "ERR"
	loggerDebug = "DEBUG"
)

const maxLogCount = 1000000

var logCount int
var setupLogLock sync.Mutex
var setupLogWorking bool
var currentLogPath string
var currentLogPathMu sync.RWMutex
var currentLogFile *os.File

// Rotated oneapi-*.log files were never deleted, so a busy instance filled the
// disk. Retention is configurable via LOG_RETENTION_DAYS (0 disables cleanup).
var logRetentionDays = common.GetEnvOrDefault("LOG_RETENTION_DAYS", 7)

func GetCurrentLogPath() string {
	currentLogPathMu.RLock()
	defer currentLogPathMu.RUnlock()
	return currentLogPath
}

func SetupLogger() {
	defer func() {
		setupLogWorking = false
	}()
	if *common.LogDir != "" {
		ok := setupLogLock.TryLock()
		if !ok {
			log.Println("setup log is already working")
			return
		}
		defer func() {
			setupLogLock.Unlock()
		}()
		logPath := filepath.Join(*common.LogDir, fmt.Sprintf("oneapi-%s.log", time.Now().Format("20060102150405")))
		fd, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatal("failed to open log file")
		}
		currentLogPathMu.Lock()
		oldFile := currentLogFile
		currentLogPath = logPath
		currentLogFile = fd
		currentLogPathMu.Unlock()

		common.LogWriterMu.Lock()
		gin.DefaultWriter = io.MultiWriter(os.Stdout, fd)
		gin.DefaultErrorWriter = io.MultiWriter(os.Stderr, fd)
		if oldFile != nil {
			_ = oldFile.Close()
		}
		common.LogWriterMu.Unlock()

		cleanupOldLogs(*common.LogDir, logPath)
	}
}

func cleanupOldLogs(dir string, keepPath string) {
	if logRetentionDays <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("log cleanup: read dir failed: %s", err.Error())
		return
	}
	cutoff := time.Now().AddDate(0, 0, -logRetentionDays)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "oneapi-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		path := filepath.Join(dir, name)
		if path == keepPath {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(path); err != nil {
			log.Printf("log cleanup: remove %s failed: %s", name, err.Error())
		}
	}
}

func LogInfo(ctx context.Context, msg string) {
	logHelper(ctx, loggerINFO, msg)
}

func LogWarn(ctx context.Context, msg string, args ...any) {
	if len(args) > 0 {
		msg = fmt.Sprintf(msg, args...)
	}
	logHelper(ctx, loggerWarn, msg)
}

func LogError(ctx context.Context, msg string) {
	logHelper(ctx, loggerError, msg)
}

func LogDebug(ctx context.Context, msg string, args ...any) {
	if common.DebugEnabled {
		if len(args) > 0 {
			msg = fmt.Sprintf(msg, args...)
		}
		logHelper(ctx, loggerDebug, msg)
	}
}

func logHelper(ctx context.Context, level string, msg string) {
	var id any = "SYSTEM"
	if ctx != nil {
		if requestID := ctx.Value(common.RequestIdKey); requestID != nil {
			id = requestID
		}
	}
	now := time.Now()
	common.LogWriterMu.RLock()
	writer := gin.DefaultErrorWriter
	if level == loggerINFO {
		writer = gin.DefaultWriter
	}
	_, _ = fmt.Fprintf(writer, "[%s] %v | %s | %s \n", level, now.Format("2006/01/02 - 15:04:05"), id, msg)
	common.LogWriterMu.RUnlock()
	logCount++ // we don't need accurate count, so no lock here
	if logCount > maxLogCount && !setupLogWorking {
		logCount = 0
		setupLogWorking = true
		gopool.Go(func() {
			SetupLogger()
		})
	}
}

func LogQuota(quota int) string {
	// 新逻辑：根据额度展示类型输出
	q := float64(quota)
	switch operation_setting.GetQuotaDisplayType() {
	case operation_setting.QuotaDisplayTypeCNY:
		usd := q / common.QuotaPerUnit
		cny := usd * operation_setting.USDExchangeRate
		return fmt.Sprintf("¥%.6f quota", cny)
	case operation_setting.QuotaDisplayTypeCustom:
		usd := q / common.QuotaPerUnit
		rate := operation_setting.GetGeneralSetting().CustomCurrencyExchangeRate
		symbol := operation_setting.GetGeneralSetting().CustomCurrencySymbol
		if symbol == "" {
			symbol = "¤"
		}
		if rate <= 0 {
			rate = 1
		}
		v := usd * rate
		return fmt.Sprintf("%s%.6f quota", symbol, v)
	case operation_setting.QuotaDisplayTypeTokens:
		return fmt.Sprintf("%d quota points", quota)
	default: // USD
		return fmt.Sprintf("＄%.6f quota", q/common.QuotaPerUnit)
	}
}

func FormatQuota(quota int) string {
	q := float64(quota)
	switch operation_setting.GetQuotaDisplayType() {
	case operation_setting.QuotaDisplayTypeCNY:
		usd := q / common.QuotaPerUnit
		cny := usd * operation_setting.USDExchangeRate
		return fmt.Sprintf("¥%.6f", cny)
	case operation_setting.QuotaDisplayTypeCustom:
		usd := q / common.QuotaPerUnit
		rate := operation_setting.GetGeneralSetting().CustomCurrencyExchangeRate
		symbol := operation_setting.GetGeneralSetting().CustomCurrencySymbol
		if symbol == "" {
			symbol = "¤"
		}
		if rate <= 0 {
			rate = 1
		}
		v := usd * rate
		return fmt.Sprintf("%s%.6f", symbol, v)
	case operation_setting.QuotaDisplayTypeTokens:
		return fmt.Sprintf("%d", quota)
	default:
		return fmt.Sprintf("＄%.6f", q/common.QuotaPerUnit)
	}
}

// LogJson 仅供测试使用 only for test
func LogJson(ctx context.Context, msg string, obj any) {
	if !common.DebugEnabled {
		return
	}
	jsonStr, err := common.Marshal(obj)
	if err != nil {
		LogError(ctx, fmt.Sprintf("json marshal failed: %s", err.Error()))
		return
	}
	LogDebug(ctx, "%s | %s", msg, jsonStr)
}
