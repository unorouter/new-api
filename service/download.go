package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
)

const (
	mediaFetchTLSHandshakeTimeout   = 10 * time.Second
	mediaFetchResponseHeaderTimeout = 30 * time.Second
	mediaFetchTimeout               = 60 * time.Second

	maxMediaFetchesPerRequest    = 16
	maxMediaFetchBytesPerRequest = 128 << 20
)

var (
	errMediaFetchBudgetExceeded = errors.New("media fetch budget exceeded")

	mediaFetchBudgetMutex sync.Mutex
)

type mediaFetchBudget struct {
	fetches int
	bytes   int64
}

func newMediaFetchBudgetError(detail string) error {
	return types.NewErrorWithStatusCode(
		fmt.Errorf("%w: %s", errMediaFetchBudgetExceeded, detail),
		types.ErrorCodeInvalidRequest,
		http.StatusBadRequest,
		types.ErrOptionWithSkipRetry(),
	)
}

// reserveMediaFetch counts one URL download against the request's shared budget and
// returns how many bytes it may still read.
func reserveMediaFetch(c *gin.Context) (int64, error) {
	if c == nil {
		return maxMediaFetchBytesPerRequest, nil
	}
	mediaFetchBudgetMutex.Lock()
	defer mediaFetchBudgetMutex.Unlock()

	budget, _ := common.GetContextKeyType[*mediaFetchBudget](c, constant.ContextKeyMediaFetchBudget)
	if budget == nil {
		budget = &mediaFetchBudget{}
		common.SetContextKey(c, constant.ContextKeyMediaFetchBudget, budget)
	}
	if budget.fetches >= maxMediaFetchesPerRequest {
		return 0, newMediaFetchBudgetError(fmt.Sprintf("a request may fetch at most %d media URLs", maxMediaFetchesPerRequest))
	}
	remaining := maxMediaFetchBytesPerRequest - budget.bytes
	if remaining <= 0 {
		return 0, newMediaFetchBudgetError(fmt.Sprintf("media URLs in a request may total at most %dMB", maxMediaFetchBytesPerRequest>>20))
	}
	budget.fetches++
	return remaining, nil
}

func recordMediaFetchBytes(c *gin.Context, size int64) {
	if c == nil {
		return
	}
	mediaFetchBudgetMutex.Lock()
	defer mediaFetchBudgetMutex.Unlock()

	if budget, _ := common.GetContextKeyType[*mediaFetchBudget](c, constant.ContextKeyMediaFetchBudget); budget != nil {
		budget.bytes += size
	}
}

func requestContext(c *gin.Context) context.Context {
	if c == nil || c.Request == nil {
		return context.Background()
	}
	return c.Request.Context()
}

// WorkerRequest Worker请求的数据结构
type WorkerRequest struct {
	URL     string            `json:"url"`
	Key     string            `json:"key"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// DoWorkerRequest 通过Worker发送请求
func DoWorkerRequest(req *WorkerRequest) (*http.Response, error) {
	return doWorkerRequest(context.Background(), GetHttpClient(), req)
}

func doWorkerRequest(ctx context.Context, client *http.Client, req *WorkerRequest) (*http.Response, error) {
	if !system_setting.EnableWorker() {
		return nil, fmt.Errorf("worker not enabled")
	}
	if !system_setting.WorkerAllowHttpImageRequestEnabled && !strings.HasPrefix(req.URL, "https") {
		return nil, fmt.Errorf("only support https url")
	}

	// SSRF防护：验证请求URL
	fetchSetting := system_setting.GetFetchSetting()
	if err := common.ValidateURLWithFetchSetting(req.URL, fetchSetting.EnableSSRFProtection, fetchSetting.AllowPrivateIp, fetchSetting.DomainFilterMode, fetchSetting.IpFilterMode, fetchSetting.DomainList, fetchSetting.IpList, fetchSetting.AllowedPorts, fetchSetting.ApplyIPFilterForDomain); err != nil {
		return nil, fmt.Errorf("request reject: %v", err)
	}

	workerUrl := system_setting.WorkerUrl
	if !strings.HasSuffix(workerUrl, "/") {
		workerUrl += "/"
	}

	// 序列化worker请求数据
	workerPayload, err := common.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal worker payload: %v", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, workerUrl, bytes.NewBuffer(workerPayload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	return client.Do(httpReq)
}

func DoDownloadRequest(ctx context.Context, originUrl string, reason ...string) (resp *http.Response, err error) {
	if system_setting.EnableWorker() {
		common.SysLog(common.LogText("downloading file from worker: %s, reason: %s", originUrl, strings.Join(reason, ", ")))
		req := &WorkerRequest{
			URL: originUrl,
			Key: system_setting.WorkerValidKey,
		}
		client := *GetHttpClient()
		client.Timeout = mediaFetchTimeout
		return doWorkerRequest(ctx, &client, req)
	} else {
		// SSRF防护：验证请求URL（非Worker模式）
		if err := ValidateSSRFProtectedFetchURL(originUrl); err != nil {
			return nil, fmt.Errorf("request reject: %v", err)
		}

		common.SysLog(common.LogText("downloading from origin: %s, reason: %s", common.MaskSensitiveInfo(originUrl), strings.Join(reason, ", ")))
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, originUrl, nil)
		if err != nil {
			return nil, err
		}
		return getMediaFetchHTTPClient().Do(httpReq)
	}
}
