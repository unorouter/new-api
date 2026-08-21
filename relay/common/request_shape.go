package common

import (
	"io"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Knobs a client can send that an upstream may reject. Recorded by name and
// value because a generic "invalid request parameters" names no field, so the
// log otherwise cannot say which one caused it.
var shapeScalarParams = []string{
	"temperature",
	"top_p",
	"top_k",
	"min_p",
	"top_a",
	"frequency_penalty",
	"presence_penalty",
	"repetition_penalty",
	"seed",
	"max_tokens",
	"max_completion_tokens",
	"reasoning_effort",
	"stream",
}

// Fields whose presence matters but whose contents do not, and which are large
// or private enough that logging them would be wrong.
// How many trailing roles to keep. The structural rejections this exists for
// are all visible at the end of the conversation.
const shapeRoleTailLen = 8

var shapePresenceOnly = []string{
	"stop",
	"logit_bias",
	"response_format",
	"tools",
	"tool_choice",
	"reasoning",
	"thinking",
	"chat_template_kwargs",
}

// DescribeRequestShape summarises the inbound request for an error log: which
// parameters were sent, and the role sequence of the conversation. Message text
// is never included, only the shape, since the failures this exists for are
// caused by parameters and message structure rather than content.
func DescribeRequestShape(c *gin.Context) map[string]interface{} {
	if c == nil {
		return nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil
	}
	body, err := io.ReadAll(common.NewReplayableBodyReader(storage))
	if err != nil || len(body) == 0 || !gjson.ValidBytes(body) {
		return nil
	}

	shape := make(map[string]interface{})
	params := make(map[string]interface{})
	for _, key := range shapeScalarParams {
		if v := gjson.GetBytes(body, key); v.Exists() {
			params[key] = v.Value()
		}
	}
	for _, key := range shapePresenceOnly {
		if gjson.GetBytes(body, key).Exists() {
			params[key] = "present"
		}
	}
	if len(params) > 0 {
		shape["params"] = params
	}

	// The role sequence catches the structural rejections a parameter list
	// cannot explain: a trailing assistant prefill, or two same-role turns in a
	// row that a strict upstream refuses.
	messages := gjson.GetBytes(body, "messages")
	if messages.IsArray() {
		roles := make([]string, 0, len(messages.Array()))
		imageParts := 0
		messages.ForEach(func(_, m gjson.Result) bool {
			roles = append(roles, m.Get("role").String())
			content := m.Get("content")
			if content.IsArray() {
				content.ForEach(func(_, p gjson.Result) bool {
					switch p.Get("type").String() {
					case "image_url", "image", "input_image":
						imageParts++
					}
					return true
				})
			}
			return true
		})
		// Only the tail is kept. Agent conversations reach several hundred turns,
		// and writing every role wrote kilobytes into each error row of the
		// largest table in the database to answer a question the last few
		// messages already answer: a trailing assistant prefill, or two same-role
		// turns a strict upstream refuses.
		if len(roles) > shapeRoleTailLen {
			shape["roles_tail"] = roles[len(roles)-shapeRoleTailLen:]
		} else {
			shape["roles"] = roles
		}
		shape["message_count"] = len(roles)
		if imageParts > 0 {
			shape["image_parts"] = imageParts
		}
	}
	return shape
}
