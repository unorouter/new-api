package aihorde

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ParseTaskResult maps the real AI Horde /status shapes onto TaskInfo. Bodies
// are captured from live aihorde.net responses.
func TestParseTaskResult(t *testing.T) {
	a := &TaskAdaptor{}

	cases := []struct {
		name       string
		body       string
		wantStatus string
		wantURL    string
		wantFail   bool
	}{
		{
			name:       "done with r2 url",
			body:       `{"done":true,"faulted":false,"is_possible":true,"finished":1,"processing":0,"waiting":0,"generations":[{"img":"https://r2.example.com/a.webp","seed":"12345","censored":false,"id":"g1"}]}`,
			wantStatus: model.TaskStatusSuccess,
			wantURL:    "https://r2.example.com/a.webp",
		},
		{
			name:       "done with inline base64",
			body:       `{"done":true,"is_possible":true,"generations":[{"img":"UklGRg==","seed":"1","censored":false}]}`,
			wantStatus: model.TaskStatusSuccess,
			wantURL:    "data:image/webp;base64,UklGRg==",
		},
		{
			name:       "faulted",
			body:       `{"done":false,"faulted":true,"is_possible":true,"message":"internal error"}`,
			wantStatus: model.TaskStatusFailure,
			wantFail:   true,
		},
		{
			name:       "impossible (no worker)",
			body:       `{"done":false,"faulted":false,"is_possible":false}`,
			wantStatus: model.TaskStatusFailure,
			wantFail:   true,
		},
		{
			name:       "in progress",
			body:       `{"done":false,"faulted":false,"is_possible":true,"processing":1,"waiting":0}`,
			wantStatus: model.TaskStatusInProgress,
		},
		{
			name:       "queued",
			body:       `{"done":false,"faulted":false,"is_possible":true,"processing":0,"waiting":1,"queue_position":275,"wait_time":1016}`,
			wantStatus: model.TaskStatusQueued,
		},
		{
			name:       "done but empty generations -> failure",
			body:       `{"done":true,"is_possible":true,"generations":[]}`,
			wantStatus: model.TaskStatusFailure,
			wantFail:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := a.ParseTaskResult([]byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.wantStatus, info.Status)
			if tc.wantURL != "" {
				assert.Equal(t, tc.wantURL, info.Url)
			}
			if tc.wantFail {
				assert.NotEmpty(t, info.Reason)
			}
		})
	}
}

// buildSubmit merges channel defaults under client overrides and always applies
// the uncensored flags. This is the contract that makes Horde serve NSFW output.
func TestBuildSubmit(t *testing.T) {
	a := &TaskAdaptor{
		config: &ChannelConfig{
			Models: map[string]ModelDefaults{
				"deliberate:free": {Width: 512, Height: 512, Steps: 20, CfgScale: 7, SamplerName: "k_euler_a", HordeModel: "Deliberate"},
			},
		},
	}

	t.Run("model defaults + horde model mapping", func(t *testing.T) {
		sub, err := a.buildSubmit(relaycommon.TaskSubmitReq{Prompt: "a cat"}, "deliberate:free")
		require.NoError(t, err)
		assert.Equal(t, []string{"Deliberate"}, sub.Models)
		assert.Equal(t, 512, sub.Params.Width)
		assert.Equal(t, 20, sub.Params.Steps)
		assert.Equal(t, "k_euler_a", sub.Params.SamplerName)
		// uncensored flags are non-negotiable
		assert.True(t, sub.NSFW)
		assert.False(t, sub.CensorNSFW)
		assert.False(t, sub.ReplacementFilter)
		assert.True(t, sub.R2)
	})

	t.Run("client size overrides + rounds to 64", func(t *testing.T) {
		sub, err := a.buildSubmit(relaycommon.TaskSubmitReq{Prompt: "x", Size: "770x513"}, "deliberate:free")
		require.NoError(t, err)
		assert.Equal(t, 768, sub.Params.Width)  // 770 -> 768
		assert.Equal(t, 512, sub.Params.Height) // 513 -> 512
	})

	t.Run("metadata overrides steps/cfg/seed/n + negative prompt", func(t *testing.T) {
		sub, err := a.buildSubmit(relaycommon.TaskSubmitReq{
			Prompt: "a dog",
			Metadata: map[string]any{
				"steps":           float64(30),
				"cfg_scale":       float64(9),
				"seed":            "42",
				"n":               float64(2),
				"negative_prompt": "blurry",
			},
		}, "deliberate:free")
		require.NoError(t, err)
		assert.Equal(t, 30, sub.Params.Steps)
		assert.Equal(t, float64(9), sub.Params.CfgScale)
		assert.Equal(t, "42", sub.Params.Seed)
		assert.Equal(t, 2, sub.Params.N)
		assert.Equal(t, "a dog ### blurry", sub.Prompt)
	})

	t.Run("empty prompt rejected", func(t *testing.T) {
		_, err := a.buildSubmit(relaycommon.TaskSubmitReq{Prompt: "  "}, "deliberate:free")
		assert.Error(t, err)
	})

	t.Run("unknown model falls back to raw name, no defaults", func(t *testing.T) {
		sub, err := a.buildSubmit(relaycommon.TaskSubmitReq{Prompt: "x"}, "some-model")
		require.NoError(t, err)
		assert.Equal(t, []string{"some-model"}, sub.Models)
		assert.Equal(t, 0, sub.Params.Width) // unset -> worker default
	})
}
