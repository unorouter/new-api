package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const imageTaskTestUserID = 71

// newImageTaskTestDB gives each test its own task table so the wait loop reads
// real rows through the same query production uses.
func newImageTaskTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.Task{}))
	model.DB = database
	t.Cleanup(func() { model.DB = previousDB })
	return database
}

func newImageTaskTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{}`))
	common.SetContextKey(c, constant.ContextKeyUserId, imageTaskTestUserID)
	return c, recorder
}

func insertImageTask(t *testing.T, database *gorm.DB, taskID string, status model.TaskStatus, private model.TaskPrivateData, failReason string) *model.Task {
	t.Helper()
	task := model.Task{
		TaskID:      taskID,
		Platform:    constant.TaskPlatform("aihorde"),
		UserId:      imageTaskTestUserID,
		Status:      status,
		FailReason:  failReason,
		PrivateData: private,
	}
	require.NoError(t, database.Create(&task).Error)
	return &task
}

// A finished task must answer in the OpenAI image shape, and an inline data URI
// must come back as b64_json rather than a url the client cannot fetch.
func TestWaitImageTaskRendersTerminalResults(t *testing.T) {
	cases := []struct {
		name        string
		private     model.TaskPrivateData
		wantURLs    []string
		wantB64     []string
		wantCreated bool
	}{
		{
			name:        "https result",
			private:     model.TaskPrivateData{ResultURL: "https://r2.example.com/a.webp"},
			wantURLs:    []string{"https://r2.example.com/a.webp"},
			wantCreated: true,
		},
		{
			name:        "inline data uri becomes b64_json",
			private:     model.TaskPrivateData{ResultURL: "data:image/webp;base64,QUJD"},
			wantB64:     []string{"QUJD"},
			wantCreated: true,
		},
		{
			name: "multiple results are all returned",
			private: model.TaskPrivateData{
				ResultURLs: []string{"https://r2.example.com/a.webp", "https://r2.example.com/b.webp"},
			},
			wantURLs:    []string{"https://r2.example.com/a.webp", "https://r2.example.com/b.webp"},
			wantCreated: true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			database := newImageTaskTestDB(t)
			c, recorder := newImageTaskTestContext(t)
			task := insertImageTask(t, database, "task_"+testCase.name, model.TaskStatusSuccess, testCase.private, "")

			apiErr := waitImageTask(c, &taskSubmissionOutcome{Task: task}, &dto.ImageRequest{Model: "absolutereality:free"})

			require.Nil(t, apiErr)
			require.Equal(t, http.StatusOK, recorder.Code)
			var response dto.ImageResponse
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			require.Len(t, response.Data, len(testCase.wantURLs)+len(testCase.wantB64))
			for i, want := range testCase.wantURLs {
				assert.Equal(t, want, response.Data[i].Url)
				assert.Empty(t, response.Data[i].B64Json)
			}
			for i, want := range testCase.wantB64 {
				assert.Equal(t, want, response.Data[i].B64Json)
				assert.Empty(t, response.Data[i].Url)
			}
			if testCase.wantCreated {
				assert.NotZero(t, response.Created)
			}
		})
	}
}

// A failed task must surface the upstream reason to the caller as a 502, not as
// the bare 500 this bridge exists to remove.
func TestWaitImageTaskReportsFailureReason(t *testing.T) {
	database := newImageTaskTestDB(t)
	c, _ := newImageTaskTestContext(t)
	task := insertImageTask(t, database, "task_failed", model.TaskStatusFailure, model.TaskPrivateData{},
		"aihorde: no worker can fulfill this request")

	apiErr := waitImageTask(c, &taskSubmissionOutcome{Task: task}, &dto.ImageRequest{Model: "absolutereality:free"})

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
	assert.Contains(t, apiErr.Error(), "no worker can fulfill this request")
}

// A success carrying no image must not be reported as a successful generation.
func TestWaitImageTaskRejectsSuccessWithoutImage(t *testing.T) {
	database := newImageTaskTestDB(t)
	c, recorder := newImageTaskTestContext(t)
	task := insertImageTask(t, database, "task_empty", model.TaskStatusSuccess, model.TaskPrivateData{}, "")

	apiErr := waitImageTask(c, &taskSubmissionOutcome{Task: task}, &dto.ImageRequest{Model: "absolutereality:free"})

	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
	assert.Equal(t, http.StatusOK, recorder.Code, "no body should have been written")
}

// A client that hangs up ends the wait without writing an error: the task stays
// durable and nothing is owed to a caller that is gone.
func TestWaitImageTaskStopsWhenClientDisconnects(t *testing.T) {
	database := newImageTaskTestDB(t)
	c, _ := newImageTaskTestContext(t)
	requestContext, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(requestContext)
	task := insertImageTask(t, database, "task_pending", model.TaskStatusInProgress, model.TaskPrivateData{}, "")

	done := make(chan *types.NewAPIError, 1)
	go func() {
		done <- waitImageTask(c, &taskSubmissionOutcome{Task: task}, &dto.ImageRequest{Model: "absolutereality:free"})
	}()
	cancel()

	select {
	case apiErr := <-done:
		assert.Nil(t, apiErr)
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not stop after the client disconnected")
	}
}

// A locally rejected submission is deterministic on every channel, so it must not
// send the relay loop hunting for another one; an upstream fault must.
func TestImageTaskSubmitErrorClassifiesRetryability(t *testing.T) {
	local := imageTaskSubmitError(&taskdto.TaskError{Message: "bad prompt", StatusCode: http.StatusBadRequest, LocalError: true})
	require.NotNil(t, local)
	assert.Equal(t, http.StatusBadRequest, local.StatusCode)
	assert.True(t, types.IsSkipRetryError(local))

	upstream := imageTaskSubmitError(&taskdto.TaskError{Message: "upstream exploded", StatusCode: http.StatusBadGateway})
	require.NotNil(t, upstream)
	assert.Equal(t, http.StatusBadGateway, upstream.StatusCode)
	assert.False(t, types.IsSkipRetryError(upstream))
}

// relayHandler runs before InitChannelMeta, so info.ChannelMeta is still nil at
// dispatch time. Reading the channel type off the embedded pointer there panicked
// on every image request; the type must come from the request context instead.
func TestRelayHandlerDetectsImageTaskChannelBeforeChannelMetaExists(t *testing.T) {
	c, _ := newImageTaskTestContext(t)
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeAIHorde)
	info := &relaycommon.RelayInfo{}
	require.Nil(t, info.ChannelMeta, "fixture must reproduce the pre-init state")

	assert.True(t, IsImageTaskChannel(common.GetContextKeyInt(c, constant.ContextKeyChannelType)))
	assert.False(t, IsImageTaskChannel(common.GetContextKeyInt(c, constant.ContextKeyChannelType)+1))
}

// The task row must be stored under the plugin key, not the channel type number.
// GetTaskPlatform falls back to the channel type when neither task_plugin_key nor
// platform is set, and the background poller only advances rows whose platform is
// the plugin key, so a mismatch leaves the task QUEUED forever and the caller
// waiting on a status that can never change.
func TestImageTaskPluginKeyMatchesTheChannelTypeMapping(t *testing.T) {
	plugin, resolved := pluginruntime.DefaultRegistry.Generation().Get(imageTaskPluginKey)
	require.True(t, resolved, "the %q task plugin must be registered", imageTaskPluginKey)
	require.NotNil(t, plugin)
	assert.Equal(t, imageTaskPluginKey, plugin.Meta.Key)
	assert.Contains(t, plugin.Meta.ChannelTypes, constant.ChannelTypeAIHorde,
		"the plugin must claim the channel type the image dispatcher routes to it")
}
