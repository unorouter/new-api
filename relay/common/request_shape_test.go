package common

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func shapeFor(t *testing.T, body string) map[string]interface{} {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(body))
	c.Set(common.KeyRequestBody, []byte(body))
	return DescribeRequestShape(c)
}

// A generic upstream 400 names no field, so the log has to say which knobs were
// sent for the rejection to be diagnosable at all.
func TestRequestShapeRecordsParamsAndRoles(t *testing.T) {
	shape := shapeFor(t, `{"model":"m","temperature":0.8,"top_p":0.95,"frequency_penalty":0.5,
		"stop":["x"],"messages":[
		{"role":"user","content":"secret user text"},
		{"role":"assistant","content":"secret reply"}]}`)
	require.NotNil(t, shape)

	params := shape["params"].(map[string]interface{})
	assert.Equal(t, 0.8, params["temperature"])
	assert.Equal(t, 0.5, params["frequency_penalty"])
	// Large or private fields are recorded as present, never by value.
	assert.Equal(t, "present", params["stop"])

	assert.Equal(t, []string{"user", "assistant"}, shape["roles"])
	assert.Equal(t, 2, shape["message_count"])
}

// The whole point is diagnosing without exposing what people wrote.
func TestRequestShapeNeverLeaksMessageContent(t *testing.T) {
	shape := shapeFor(t, `{"messages":[{"role":"user","content":"secret user text"}]}`)
	require.NotNil(t, shape)
	assert.NotContains(t, common.MapToJsonStr(shape), "secret user text")
}

// Images reaching a text-only model is one of the failures this must explain.
func TestRequestShapeCountsImageParts(t *testing.T) {
	shape := shapeFor(t, `{"messages":[{"role":"user","content":[
		{"type":"text","text":"look"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`)
	require.NotNil(t, shape)
	assert.Equal(t, 1, shape["image_parts"])
	assert.NotContains(t, common.MapToJsonStr(shape), "base64")
}

// Agent conversations run to hundreds of turns, and the error log is the
// largest table in the database: the tail answers the structural questions
// without writing kilobytes per row.
func TestRequestShapeTruncatesLongRoleLists(t *testing.T) {
	body := `{"messages":[`
	for i := 0; i < 200; i++ {
		if i > 0 {
			body += ","
		}
		body += `{"role":"assistant","content":"x"},{"role":"tool","content":"y"}`
	}
	body += `]}`

	shape := shapeFor(t, body)
	require.NotNil(t, shape)
	assert.Equal(t, 400, shape["message_count"])
	assert.Nil(t, shape["roles"], "full list must not be written")
	assert.Len(t, shape["roles_tail"], shapeRoleTailLen)
	assert.Less(t, len(common.MapToJsonStr(shape)), 400, "row stays small")
}

func promptTextFor(t *testing.T, body string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", bytes.NewBufferString(body))
	return ExtractPromptText(c)
}

// A failed request never reaches usage accounting, so the error log is the only
// place its size can be recorded. Every request shape we relay must yield text,
// or the count silently reads zero for that vendor.
func TestExtractPromptTextCoversEveryRequestShape(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"openai chat", `{"messages":[{"role":"user","content":"hello world"}]}`, []string{"hello world"}},
		{"openai multipart", `{"messages":[{"role":"user","content":[{"type":"text","text":"part one"}]}]}`, []string{"part one"}},
		// Anthropic keeps system beside messages; on an RP request it is often the
		// largest single part, so missing it would undercount the most.
		{"anthropic system", `{"system":"you are a bot","messages":[{"role":"user","content":"hi"}]}`, []string{"you are a bot", "hi"}},
		{"anthropic system blocks", `{"system":[{"type":"text","text":"block sys"}],"messages":[]}`, []string{"block sys"}},
		{"gemini contents", `{"contents":[{"parts":[{"text":"gemini prompt"}]}]}`, []string{"gemini prompt"}},
		{"responses input", `{"input":[{"role":"user","content":"responses text"}]}`, []string{"responses text"}},
		{"legacy prompt", `{"prompt":"legacy text"}`, []string{"legacy text"}},
	}
	for _, tc := range cases {
		got := promptTextFor(t, tc.body)
		for _, w := range tc.want {
			assert.Contains(t, got, w, "%s", tc.name)
		}
	}
	assert.Empty(t, promptTextFor(t, `not json at all`))
	assert.Empty(t, ExtractPromptText(nil))
}
