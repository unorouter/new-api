package dto

import (
	"encoding/json"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
)

//type OpenAIError struct {
//	Message string `json:"message"`
//	Type    string `json:"type"`
//	Param   string `json:"param"`
//	Code    any    `json:"code"`
//}

type OpenAIErrorWithStatusCode struct {
	Error      types.OpenAIError `json:"error"`
	StatusCode int               `json:"status_code"`
	LocalError bool
}

type GeneralErrorResponse struct {
	Error    json.RawMessage `json:"error"`
	Errors   json.RawMessage `json:"errors"`
	Message  string          `json:"message"`
	Msg      string          `json:"msg"`
	Err      string          `json:"err"`
	ErrorMsg string          `json:"error_msg"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	Detail   json.RawMessage `json:"detail,omitempty"`
	Header   struct {
		Message string `json:"message"`
	} `json:"header"`
	Response struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
}

func (e GeneralErrorResponse) TryToOpenAIError() *types.OpenAIError {
	var openAIError types.OpenAIError
	if len(e.Error) > 0 {
		err := kitutil.Unmarshal(e.Error, &openAIError)
		if err == nil && openAIError.Message != "" {
			return &openAIError
		}
	}
	return nil
}

func (e GeneralErrorResponse) ToMessage() string {
	if len(e.Error) > 0 {
		switch kitutil.GetJsonType(e.Error) {
		case "object":
			var openAIError types.OpenAIError
			err := kitutil.Unmarshal(e.Error, &openAIError)
			if err == nil && openAIError.Message != "" {
				return openAIError.Message
			}
		case "string":
			var msg string
			err := kitutil.Unmarshal(e.Error, &msg)
			if err == nil && msg != "" {
				return msg
			}
		default:
			return string(e.Error)
		}
	}
	// Error-array shape ({"errors":[{"message":...}]}), used by Runware among others.
	if len(e.Errors) > 0 && kitutil.GetJsonType(e.Errors) == "array" {
		var list []struct {
			Message string `json:"message"`
		}
		if err := kitutil.Unmarshal(e.Errors, &list); err == nil {
			for _, item := range list {
				if item.Message != "" {
					return item.Message
				}
			}
		}
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Msg != "" {
		return e.Msg
	}
	if e.Err != "" {
		return e.Err
	}
	if e.ErrorMsg != "" {
		return e.ErrorMsg
	}
	if len(e.Detail) > 0 {
		switch kitutil.GetJsonType(e.Detail) {
		case "string":
			var msg string
			if err := kitutil.Unmarshal(e.Detail, &msg); err == nil && msg != "" {
				return msg
			}
		default:
			// FastAPI-style validation errors come back as an array or object.
			// Return the raw JSON so the client sees what failed rather than a
			// blank "bad response" message.
			return string(e.Detail)
		}
	}
	if e.Header.Message != "" {
		return e.Header.Message
	}
	if e.Response.Error.Message != "" {
		return e.Response.Error.Message
	}
	return ""
}
