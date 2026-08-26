package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// x-newapi-dropped-params renders in the client as "X not supported by this
// model and were ignored", so a param the request never carried must not be
// listed: it reads as a complaint about settings the user did not send.
func TestDroppedParamsReportsOnlyPresentFields(t *testing.T) {
	override := map[string]interface{}{
		"operations": []interface{}{
			map[string]interface{}{"path": "repetition_penalty", "mode": "delete"},
			map[string]interface{}{"path": "min_p", "mode": "delete"},
			map[string]interface{}{"path": "top_a", "mode": "delete"},
		},
	}
	recorder := &paramOverrideAuditRecorder{}
	ctx := map[string]interface{}{paramOverrideContextAuditRecorder: recorder}

	body := []byte(`{"model":"glm-5.2","temperature":0.8,"repetition_penalty":1.05}`)
	out, err := ApplyParamOverride(body, override, ctx)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "repetition_penalty")

	assert.Equal(t, []string{"repetition_penalty"}, ExtractDroppedParamPaths(recorder.lines))
}
