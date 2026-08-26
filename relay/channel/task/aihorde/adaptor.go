package aihorde

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	taskcommon "github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

const clientAgent = "unorouter:1.0:https://unorouter.com"

// TaskAdaptor speaks the AI Horde v2 async image API. Submit ->
// POST /api/v2/generate/async -> {id}; poll -> GET /api/v2/generate/status/{id}
// -> {generations:[{img}]}. Per-model default params come from the channel's
// workflow_templates JSON (reused field), merged under client overrides.
type TaskAdaptor struct {
	taskcommon.BaseBilling

	channelType int
	apiKey      string
	baseURL     string
	config      *ChannelConfig
}

// ============================
// Init / Validate
// ============================

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.channelType = info.ChannelType
	a.baseURL = strings.TrimRight(info.ChannelBaseUrl, "/")
	if a.baseURL == "" {
		a.baseURL = constant.ChannelBaseURLs[constant.ChannelTypeAIHorde]
	}
	a.apiKey = info.ApiKey
}

func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	if err := relaycommon.ValidateBasicTaskRequest(c, info, constant.TaskActionGenerate); err != nil {
		return err
	}
	// Per-model defaults are optional; only load if present so a bare channel
	// (no workflow_templates) still works with client-supplied params.
	a.loadConfig(c)
	return nil
}

func (a *TaskAdaptor) loadConfig(c *gin.Context) {
	raw, _ := c.Get(string(constant.ContextKeyChannelWorkflowTemplates))
	rawStr, _ := raw.(string)
	if strings.TrimSpace(rawStr) == "" {
		return
	}
	cfg := &ChannelConfig{}
	if err := common.UnmarshalJsonStr(rawStr, cfg); err == nil && len(cfg.Models) > 0 {
		a.config = cfg
	}
}

// ============================
// Request build
// ============================

func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	return a.baseURL + "/api/v2/generate/async", nil
}

func (a *TaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	req.Header.Set("apikey", a.apiKey)
	req.Header.Set("Client-Agent", clientAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	taskReq, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil, err
	}
	body, err := a.buildSubmit(taskReq, info.UpstreamModelName)
	if err != nil {
		return nil, err
	}
	raw, err := common.Marshal(body)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(raw), nil
}

// buildSubmit merges per-model defaults (from channel config) under client
// overrides (from taskReq.Size + metadata), then applies the uncensored flags.
// Split out from BuildRequestBody so it is unit-testable without a gin context.
func (a *TaskAdaptor) buildSubmit(taskReq relaycommon.TaskSubmitReq, model string) (*hordeSubmit, error) {
	if strings.TrimSpace(taskReq.Prompt) == "" {
		return nil, errors.New("aihorde: prompt is required")
	}

	var def ModelDefaults
	hordeModel := model
	if a.config != nil {
		if d, ok := a.config.Models[model]; ok {
			def = d
			if strings.TrimSpace(d.HordeModel) != "" {
				hordeModel = d.HordeModel
			}
		}
	}

	p := hordeParams{
		Width:       roundTo64(def.Width),
		Height:      roundTo64(def.Height),
		Steps:       def.Steps,
		CfgScale:    def.CfgScale,
		SamplerName: def.SamplerName,
		Karras:      def.Karras,
		ClipSkip:    def.ClipSkip,
		N:           1,
	}

	// Client size (e.g. "768x512") overrides model default dimensions.
	if w, h := parseSize(taskReq.Size); w > 0 && h > 0 {
		p.Width = roundTo64(w)
		p.Height = roundTo64(h)
	}

	if taskReq.Metadata != nil {
		if n := metadataInt(taskReq.Metadata, "n"); n > 0 {
			p.N = n
		}
		if s := metadataInt(taskReq.Metadata, "steps"); s > 0 {
			p.Steps = s
		}
		if cfg := metadataFloat(taskReq.Metadata, "cfg_scale"); cfg > 0 {
			p.CfgScale = cfg
		}
		if sm, ok := taskReq.Metadata["sampler_name"].(string); ok && sm != "" {
			p.SamplerName = sm
		}
		if seed := metadataString(taskReq.Metadata, "seed"); seed != "" {
			p.Seed = seed
		}
	}

	prompt := taskReq.Prompt
	// OpenAI-style callers pass the negative prompt separately; Horde encodes it
	// inline as "prompt ### negative".
	if taskReq.Metadata != nil {
		if neg, ok := taskReq.Metadata["negative_prompt"].(string); ok && strings.TrimSpace(neg) != "" {
			prompt = prompt + " ### " + neg
		}
	}

	return &hordeSubmit{
		Prompt:            prompt,
		Params:            p,
		Models:            []string{hordeModel},
		NSFW:              true,
		CensorNSFW:        false,
		ReplacementFilter: false,
		R2:                true,
		SlowWorkers:       true,
	}, nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	resp, err := channel.DoTaskApiRequest(a, c, info, requestBody)
	// Horde's async submit returns 202 Accepted; the generic task pipeline only
	// treats 200 as success (relay_task.go). Normalize a 202 to 200 so DoResponse
	// runs; real errors (4xx/5xx) still flow through unchanged.
	if resp != nil && resp.StatusCode == http.StatusAccepted {
		resp.StatusCode = http.StatusOK
	}
	return resp, err
}

// ============================
// Response handling
// ============================

func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (taskID string, taskData []byte, taskErr *dto.TaskError) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		taskErr = service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
		return
	}
	if resp.StatusCode >= 400 {
		taskErr = service.TaskErrorWrapperLocal(fmt.Errorf("aihorde upstream %d: %s", resp.StatusCode, truncate(body, 400)), "upstream_error", resp.StatusCode)
		return
	}
	var sub hordeSubmitResp
	if err := common.Unmarshal(body, &sub); err != nil {
		taskErr = service.TaskErrorWrapper(err, "parse_submit_failed", http.StatusInternalServerError)
		return
	}
	if sub.ID == "" {
		taskErr = service.TaskErrorWrapperLocal(fmt.Errorf("aihorde: no task id in submit response: %s", truncate(body, 300)), "no_task_id", http.StatusInternalServerError)
		return
	}
	// Same envelope shape every task adapter returns to the client so polling
	// has a stable id reference.
	c.JSON(http.StatusOK, gin.H{
		"id":         info.PublicTaskID,
		"task_id":    info.PublicTaskID,
		"created_at": time.Now().Unix(),
		"model":      info.OriginModelName,
		"status":     "submitted",
	})
	return sub.ID, body, nil
}

// FetchTask GETs the terminal status endpoint (which also carries progress
// counters). The generic poller forwards the UPSTREAM Horde id as body["task_id"]
// (service/task_polling.go: task.GetUpstreamTaskID()).
func (a *TaskAdaptor) FetchTask(baseURL, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok || taskID == "" {
		return nil, errors.New("aihorde: invalid task_id")
	}
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = constant.ChannelBaseURLs[constant.ChannelTypeAIHorde]
	}
	url := base + "/api/v2/generate/status/" + taskID
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", key)
	req.Header.Set("Client-Agent", clientAgent)
	req.Header.Set("Accept", "application/json")
	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func (a *TaskAdaptor) ParseTaskResult(body []byte) (*relaycommon.TaskInfo, error) {
	var resp hordeStatusResp
	if err := common.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("aihorde: parse status: %w", err)
	}
	info := &relaycommon.TaskInfo{}

	switch {
	case resp.Faulted:
		info.Status = model.TaskStatusFailure
		info.Reason = defaultReason(resp.Message, "aihorde: request faulted")
	case !resp.IsPossible:
		info.Status = model.TaskStatusFailure
		info.Reason = defaultReason(resp.Message, "aihorde: no worker can fulfill this request")
	case resp.Done:
		urls := make([]string, 0, len(resp.Generations))
		for _, g := range resp.Generations {
			if g.Img == "" {
				continue
			}
			if strings.HasPrefix(g.Img, "http://") || strings.HasPrefix(g.Img, "https://") {
				urls = append(urls, g.Img)
			} else {
				// r2:false path: worker returned inline base64 webp.
				urls = append(urls, "data:image/webp;base64,"+g.Img)
			}
		}
		if len(urls) == 0 {
			info.Status = model.TaskStatusFailure
			info.Reason = "aihorde: done but no image returned"
		} else {
			info.Status = model.TaskStatusSuccess
			info.Url = urls[0]
			if len(urls) > 1 {
				info.Urls = urls
			}
			info.Progress = taskcommon.ProgressComplete
		}
	case resp.Processing > 0:
		info.Status = model.TaskStatusInProgress
		info.Progress = taskcommon.ProgressInProgress
	default:
		info.Status = model.TaskStatusQueued
		info.Progress = taskcommon.ProgressQueued
	}
	return info, nil
}

// ============================
// Misc adapter contract
// ============================

func (a *TaskAdaptor) GetModelList() []string {
	if a.config == nil {
		return nil
	}
	out := make([]string, 0, len(a.config.Models))
	for k := range a.config.Models {
		out = append(out, k)
	}
	return out
}

func (a *TaskAdaptor) GetChannelName() string { return "aihorde" }

// ConvertToOpenAIVideo lets the /v1/videos/:id fetch path return an OpenAI
// video-object shape. Image tasks have no native video DTO, so expose a minimal
// object keyed off the public task id + the R2 result url from PrivateData.
func (a *TaskAdaptor) ConvertToOpenAIVideo(task *model.Task) ([]byte, error) {
	status := "queued"
	switch task.Status {
	case model.TaskStatusSuccess:
		status = "completed"
	case model.TaskStatusFailure:
		status = "failed"
	case model.TaskStatusInProgress:
		status = "in_progress"
	}
	out := map[string]any{
		"id":         task.TaskID,
		"object":     "video",
		"status":     status,
		"progress":   task.Progress,
		"created_at": task.CreatedAt,
	}
	if url := task.GetResultURL(); url != "" {
		out["result_url"] = url
	}
	if task.FailReason != "" {
		out["error"] = task.FailReason
	}
	return common.Marshal(out)
}

// ============================
// Helpers
// ============================

// roundTo64 clamps a dimension to Horde's 64px grid (64..3072). Zero passes
// through so an unset dimension falls back to the worker default.
func roundTo64(v int) int {
	if v <= 0 {
		return 0
	}
	r := (v / 64) * 64
	if r < 64 {
		r = 64
	}
	if r > 3072 {
		r = 3072
	}
	return r
}

func parseSize(size string) (int, int) {
	if size == "" {
		return 0, 0
	}
	parts := strings.Split(strings.ToLower(size), "x")
	if len(parts) != 2 {
		return 0, 0
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, 0
	}
	return w, h
}

func metadataInt(meta map[string]any, key string) int {
	v, ok := meta[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i
		}
	}
	return 0
}

func metadataFloat(meta map[string]any, key string) float64 {
	v, ok := meta[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return f
		}
	}
	return 0
}

func metadataString(meta map[string]any, key string) string {
	if v, ok := meta[key].(string); ok {
		return strings.TrimSpace(v)
	}
	if n := metadataInt(meta, key); n > 0 {
		return strconv.Itoa(n)
	}
	return ""
}

func defaultReason(msg, fallback string) string {
	if strings.TrimSpace(msg) != "" {
		return msg
	}
	return fallback
}

func truncate(b []byte, n int) string {
	s := string(b)
	if len(s) <= n {
		return s
	}
	return s[:n]
}
