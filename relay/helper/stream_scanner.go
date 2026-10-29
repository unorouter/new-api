package helper

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/bytedance/gopkg/util/gopool"

	"github.com/gin-gonic/gin"
)

const (
	InitialScannerBufferSize    = 64 << 10  // 64KB (64*1024)
	DefaultMaxScannerBufferSize = 128 << 20 // 64MB (64*1024*1024) default SSE buffer size
	DefaultPingInterval         = 10 * time.Second
	// streamWriteTimeout bounds a write blocked on a client that stopped
	// reading so the unconditional wg.Wait() in cleanup can always finish.
	// Without it, a connected client with a full TCP buffer (no server
	// WriteTimeout) could hang the handler forever. StreamWriteBudget adds the
	// time a client reading at streamMinClientRate needs for the data.
	streamWriteTimeout  = 30 * time.Second
	streamMinClientRate = 8 << 10 // bytes per second
)

func getScannerBufferSize() int {
	if constant.StreamScannerMaxBufferMB > 0 {
		return constant.StreamScannerMaxBufferMB << 20
	}
	return DefaultMaxScannerBufferSize
}

// NewStreamScanner shares relay scanner configuration. Callers buffering bounded
// task state may additionally cap a line without increasing the configured limit.
func NewStreamScanner(reader io.Reader, maxBytes ...int) *bufio.Scanner {
	limit := getScannerBufferSize()
	if len(maxBytes) > 0 && maxBytes[0] > 0 {
		limit = min(limit, maxBytes[0])
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, min(InitialScannerBufferSize, limit)), limit)
	return scanner
}

func copyCodexSSEHeaders(c *gin.Context, resp *http.Response) {
	if c == nil || c.Writer == nil || resp == nil {
		return
	}
	// codex
	for _, name := range []string{"X-Reasoning-Included", "X-Codex-Turn-State"} {
		values := resp.Header.Values(name)
		if !service.ShouldCopyUpstreamHeader(c, name, values) {
			continue
		}
		for _, value := range values {
			if value != "" {
				c.Writer.Header().Add(name, value)
			}
		}
	}
}

// StreamWriteBudget gives the writes of one stream a deadline of
// streamWriteTimeout past the time a client reading at streamMinClientRate
// needs for everything written so far. A fixed deadline per write cut slow
// clients off in the middle of a multi-megabyte image event, and the timeout
// looked like a client disconnect. The budget carries over between writes
// because the kernel can still hold megabytes of earlier writes and wakes a
// blocked writer only after a large part of them drains. A client that stops
// reading is still cut off once the budget runs out. Callers serialize writes.
type StreamWriteBudget struct {
	drainBy time.Time
}

// Deadline returns the write deadline for n more bytes written at now.
func (b *StreamWriteBudget) Deadline(now time.Time, n int) time.Time {
	if b.drainBy.Before(now) {
		b.drainBy = now
	}
	b.drainBy = b.drainBy.Add(time.Duration(n) * time.Second / streamMinClientRate)
	return b.drainBy.Add(streamWriteTimeout)
}

// streamResponseWriter sets the connection write deadline from its budget
// before every write. Writers that don't support deadlines (e.g. httptest
// recorders) are silently ignored.
type streamResponseWriter struct {
	gin.ResponseWriter
	budget StreamWriteBudget
}

func (w *streamResponseWriter) Write(p []byte) (int, error) {
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(w.budget.Deadline(time.Now(), len(p)))
	return w.ResponseWriter.Write(p)
}

func (w *streamResponseWriter) WriteString(s string) (int, error) {
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(w.budget.Deadline(time.Now(), len(s)))
	return w.ResponseWriter.WriteString(s)
}

// Unwrap lets http.ResponseController reach the connection.
func (w *streamResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func StreamScannerHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, dataHandler func(data string, sr *StreamResult)) *types.NewAPIError {

	if resp == nil || dataHandler == nil {
		return nil
	}

	// Finish the request keepalive before changing headers. The scanner owns
	// subsequent ping and data writes through its shared write mutex.
	if writer, ok := c.Writer.(*streamKeepAliveWriter); ok {
		writer.stop()
	}

	// 无条件新建 StreamStatus
	info.StreamStatus = relaycommon.NewStreamStatus()

	ctx, cancel := context.WithCancel(context.Background())

	streamingTimeout := time.Duration(constant.StreamingTimeout) * time.Second
	// A 200 with an idle body is the failure mode the header timeout cannot
	// see, so until the first data line the idle budget is the short one.
	idleTimeout := firstDataTimeout(info)

	var (
		stopChan    = make(chan bool, 3) // 增加缓冲区避免阻塞
		scanner     = NewStreamScanner(resp.Body)
		ticker      = time.NewTicker(idleTimeout)
		pingTicker  *time.Ticker
		writeMutex  sync.Mutex     // Mutex to protect concurrent writes
		wg          sync.WaitGroup // 用于等待所有 goroutine 退出
		cleanupOnce sync.Once
		stopOnce    sync.Once
	)

	stop := func() {
		stopOnce.Do(func() {
			close(stopChan)
		})
	}

	generalSettings := operation_setting.GetGeneralSetting()
	pingEnabled := generalSettings.PingIntervalEnabled && !info.DisablePing
	pingInterval := time.Duration(generalSettings.PingIntervalSeconds) * time.Second
	if pingInterval <= 0 {
		pingInterval = DefaultPingInterval
	}

	if pingEnabled {
		pingTicker = time.NewTicker(pingInterval)
	}

	logger.LogDebug(c, "relay timeout seconds: %d", common.RelayTimeout)
	logger.LogDebug(c, "relay max idle conns: %d", common.RelayMaxIdleConns)
	logger.LogDebug(c, "relay max idle conns per host: %d", common.RelayMaxIdleConnsPerHost)
	logger.LogDebug(c, "streaming timeout seconds: %d", int64(streamingTimeout.Seconds()))
	logger.LogDebug(c, "ping interval seconds: %d", int64(pingInterval.Seconds()))

	cleanup := func() {
		cleanupOnce.Do(func() {
			cancel()
			stop()
			if resp.Body != nil {
				_ = resp.Body.Close()
			}

			ticker.Stop()
			if pingTicker != nil {
				pingTicker.Stop()
			}

			wg.Wait()
		})
	}
	// Ensure gin.Context is not returned to Gin's pool while any stream goroutine can still use it.
	defer cleanup()

	scanner.Split(bufio.ScanLines)
	copyCodexSSEHeaders(c, resp)
	if !info.ForceUpstreamStream {
		SetEventStreamHeaders(c)
	}

	ctx = context.WithValue(ctx, "stop_chan", stopChan)

	// Handle ping data sending with improved error handling
	if pingEnabled && pingTicker != nil {
		wg.Add(1)
		gopool.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					logger.LogError(c, common.LogText("ping goroutine panic: %v", r))
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("ping panic: %v", r))
					stop()
				}
				logger.LogDebug(c, "ping goroutine exited")
				wg.Done()
			}()

			// 添加超时保护，防止 goroutine 无限运行
			maxPingDuration := 30 * time.Minute // 最大 ping 持续时间
			pingTimeout := time.NewTimer(maxPingDuration)
			defer pingTimeout.Stop()

			// A ping is only for silence. Sent between live chunks it reached
			// JanitorAI mid reasoning, and its parser broke on it (2026-10-24).
			lastSize := -1
			for {
				select {
				case <-pingTicker.C:
					var err error
					skipped := false
					func() {
						writeMutex.Lock()
						defer writeMutex.Unlock()
						if size := c.Writer.Size(); size != lastSize {
							lastSize = size
							skipped = true
							return
						}
						err = PingData(c)
						lastSize = c.Writer.Size()
					}()
					if skipped {
						continue
					}
					if err != nil {
						logger.LogError(c, common.LogText("ping data error: %s", err.Error()))
						info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPingFail, err)
						return
					}
					logger.LogDebug(c, "ping data sent")
				case <-ctx.Done():
					return
				case <-stopChan:
					return
				case <-c.Request.Context().Done():
					// 监听客户端断开连接
					return
				case <-pingTimeout.C:
					logger.LogError(c, common.LogText("ping goroutine max duration reached"))
					return
				}
			}
		})
	}

	dataChan := make(chan string, 10)
	sr := newStreamResult(info.StreamStatus)
	// Who hung up first on a client_gone: read back in the log line at the select below.
	var lastUpstreamAt, lastClientWriteAt atomic.Int64
	var upstreamEnded atomic.Bool

	wg.Add(1)
	gopool.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				logger.LogError(c, common.LogText("data handler goroutine panic: %v", r))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("handler panic: %v", r))
			}
			stop()
			wg.Done()
		}()
		for data := range dataChan {
			sr.reset()
			func() {
				writeMutex.Lock()
				defer writeMutex.Unlock()
				dataHandler(data, sr)
				lastClientWriteAt.Store(time.Now().UnixNano())
			}()
			if sr.IsStopped() {
				return
			}
		}
	})

	// Scanner goroutine with improved error handling
	wg.Add(1)
	common.RelayCtxGo(ctx, func() {
		defer func() {
			upstreamEnded.Store(true)
			close(dataChan)
			if r := recover(); r != nil {
				logger.LogError(c, common.LogText("scanner goroutine panic: %v", r))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("scanner panic: %v", r))
			}
			stop()
			logger.LogDebug(c, "scanner goroutine exited")
			wg.Done()
		}()

		for scanner.Scan() {
			// 检查是否需要停止
			select {
			case <-stopChan:
				return
			case <-ctx.Done():
				return
			default:
			}

			ticker.Reset(idleTimeout)
			lastUpstreamAt.Store(time.Now().UnixNano())
			data := scanner.Text()
			logger.LogDebug(c, "stream scanner data: %s", common.ElideBase64(data))

			data, hasDataPrefix := strings.CutPrefix(data, "data:")
			data = strings.TrimSpace(data)
			if !hasDataPrefix && data != "[DONE]" {
				continue
			}
			if data == "" {
				continue
			}
			if data != "[DONE]" {
				info.SetFirstResponseTime()
				info.ReceivedResponseCount++
				idleTimeout = streamingTimeout

				select {
				case dataChan <- data:
				case <-ctx.Done():
					return
				case <-stopChan:
					return
				}
			} else {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
				logger.LogDebug(c, "received [DONE], stopping scanner")
				return
			}
		}

		// Closing the upstream body is part of stopping the scanner. Its EOF or
		// read error must not replace the actual stop reason or add a false error.
		if ctx.Err() != nil {
			return
		}
		if err := scanner.Err(); err != nil {
			if err != io.EOF {
				logger.LogError(c, common.LogText("scanner error: %s", err.Error()))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, err)
			}
		}
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
	})

	// 主循环等待完成或超时
	select {
	case <-ticker.C:
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonTimeout, nil)
	case <-stopChan:
		// EndReason already set by the goroutine that triggered stopChan
	case <-c.Request.Context().Done():
		// 客户端断开：立即 cleanup 关闭上游 resp.Body，解除 scanner 阻塞并让上游停止生成，
		// 避免为已放弃的请求继续消费上游 token。
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, c.Request.Context().Err())
		now := time.Now()
		since := func(at int64) string {
			if at == 0 {
				return "never"
			}
			return now.Sub(time.Unix(0, at)).Round(time.Millisecond).String()
		}
		channelID := 0
		if info.ChannelMeta != nil {
			channelID = info.ChannelId
		}
		logger.LogInfo(c, fmt.Sprintf("stream client gone: channel=%d after=%s since_upstream=%s since_client_write=%s upstream_ended=%t received=%d",
			channelID, now.Sub(info.StartTime).Round(time.Millisecond), since(lastUpstreamAt.Load()), since(lastClientWriteAt.Load()),
			upstreamEnded.Load(), info.ReceivedResponseCount))
	}

	cleanup()
	FlushMarkedEvents(c)
	// All stream goroutines have joined. Done from a successfully delivered
	// protocol completion can now correct a cancellation that won while its
	// callback was still running. Bare EOF and failed writes never qualify.
	if sr.done && info.StreamStatus.ResponseOutcome() == string(relaycommon.ResponseOutcomeCompleted) &&
		info.StreamStatus.EndReason == relaycommon.StreamEndReasonClientGone &&
		errors.Is(info.StreamStatus.EndError, context.Canceled) && !info.StreamStatus.HasErrors() {
		info.StreamStatus.EndReason = relaycommon.StreamEndReasonDone
		info.StreamStatus.EndError = nil
	}
	if info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors() {
		logger.LogInfo(c, common.LogText("stream ended: %s", info.StreamStatus.Summary()))
	} else {
		logger.LogError(c, common.LogText("stream ended: %s, received=%d", info.StreamStatus.Summary(), info.ReceivedResponseCount))
	}

	// If the stream ended abnormally and no tokens were ever sent to the client,
	// return a retriable channel error so the retry loop can try another channel.
	emptyEOF := info.StreamStatus.EndReason == relaycommon.StreamEndReasonEOF
	if (info.StreamStatus.IsRetriable() || emptyEOF) && info.ReceivedResponseCount == 0 {
		// 429, not 5xx: Cloudflare replaces a 502 body with its own error page and chat
		// frontends replace any 5xx body with their own generic string, so the caller
		// loses the reason either way. 429 is forwarded and rendered verbatim by both,
		// and retry is the correct action here. The channel: code carries the real
		// meaning for routing and disable classification.
		return types.NewErrorWithStatusCode(
			fmt.Errorf("the upstream provider closed the stream before sending anything: %s", info.StreamStatus.Summary()),
			"channel:stream_timeout_no_response",
			http.StatusTooManyRequests,
		)
	}

	return nil
}

// firstDataTimeout is the idle budget before the first data line: the global
// STREAM_FIRST_DATA_TIMEOUT, tightened by the user's first-token setting.
func firstDataTimeout(info *relaycommon.RelayInfo) time.Duration {
	seconds := constant.StreamFirstDataTimeout
	if info != nil {
		// A lane carrying cooldown strikes has failed recently and a strike is only
		// shed by a success, so it is on probation: spend a fraction of the budget
		// proving it before moving on. The full 60s on such a lane is what a user
		// experiences as a hang, and it is paid per attempt: one model had three
		// lanes that never answered, and a request burned 60s on each before giving
		// up, 122s of silence with nothing to show. Two strikes rather than one, so
		// a single blip on a healthy lane costs it nothing.
		if probation := constant.StreamFirstDataProbationTimeout; probation > 0 &&
			probation < seconds && info.ChannelMeta != nil && service.LaneStrikes(info.ChannelId) >= 2 {
			seconds = probation
		}
		if user := hosttypes.ClampFirstTokenSeconds(info.UserSetting.MaxFirstTokenSeconds); user > 0 && user < seconds {
			seconds = user
		}
	}
	if seconds <= 0 || seconds > constant.StreamingTimeout {
		seconds = constant.StreamingTimeout
	}
	return time.Duration(seconds) * time.Second
}
