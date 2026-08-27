package middleware

import "testing"

func TestExtractModelNameFromGeminiPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"plain model", "/v1beta/models/gemini-2.0-flash:generateContent", "gemini-2.0-flash"},
		{"streaming action", "/v1beta/models/gemini-2.0-flash:streamGenerateContent", "gemini-2.0-flash"},
		// A group suffix is part of the model name, so only the action's colon separates it.
		{"model with group suffix", "/v1beta/models/glm-5.3-flash:free:generateContent", "glm-5.3-flash:free"},
		{"v1 prefix", "/v1/models/glm-5.3-flash:free:generateContent", "glm-5.3-flash:free"},
		{"no action", "/v1beta/models/gemini-2.0-flash", "gemini-2.0-flash"},
		{"not a models path", "/v1/chat/completions", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractModelNameFromGeminiPath(tc.path); got != tc.want {
				t.Errorf("extractModelNameFromGeminiPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
