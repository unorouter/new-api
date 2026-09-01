package reasoning

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Clients (SkyrimNet, OpenRouter-style `reasoning` objects) ask to disable
// reasoning on every model. On a model that cannot disable, that must fall
// back to the model's default rather than refuse the request: after the
// 2026-09-01 sync, upstream's refusal produced 210 Gemini 3.x flash 400s
// within 40 minutes.
func TestDisableOnNonDisableableModelFallsBackToDefault(t *testing.T) {
	disabled := Intent{Mode: ModeDisabled, Effort: EffortNone, Source: SourceExplicit}

	for _, model := range []string{"gemini-3.7-flash", "gemini-3.1-flash-lite", "gemini-2.5-pro"} {
		render, err := RenderGemini(model, disabled, nil, 0.8)
		require.NoErrorf(t, err, model)
		require.Nilf(t, render.Config, "%s must not carry a thinking config", model)
	}

	render, err := RenderClaude("claude-fable-5-1", disabled, nil, 0.8)
	require.NoError(t, err)
	require.Nil(t, render.Thinking)

	// A model that can disable still disables.
	flash, err := RenderGemini("gemini-2.5-flash", disabled, nil, 0.8)
	require.NoError(t, err)
	require.NotNil(t, flash.Config)
	require.NotNil(t, flash.Config.ThinkingBudget)
	require.Equal(t, 0, *flash.Config.ThinkingBudget)
}
