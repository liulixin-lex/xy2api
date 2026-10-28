//go:build unit

package resilience

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNumericModelErrorCodeMustMatchHTTPStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"google 400", 400, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"unknown model gemini-x"}}`, true},
		{"nested 404", 404, `{"response":{"error":{"code":404,"message":"model not found"}}}`, true},
		{"mismatched number", 400, `{"error":{"code":404,"message":"unknown model x"}}`, false},
		{"strong type", 400, `{"error":{"code":400,"type":"invalid_api_key","message":"unknown model x"}}`, false},
		{"param", 404, `{"error":{"code":404,"param":"tools","message":"model not found"}}`, false},
		{"semantic code", 400, `{"error":{"code":"invalid_api_key","message":"unknown model x"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, IsModelUnsupportedResponse(tc.status, []byte(tc.body)))
		})
	}
	require.False(t, IsOpenAICompatibleModelNotFound401([]byte(`{"error":{"code":401,"message":"model not found"}}`)))
}

func TestLegacy404ModelClassificationDoesNotExpandHardModelPredicate(t *testing.T) {
	message := "The model gpt-x does not exist"
	body := []byte(`{"error":{"message":"The model gpt-x does not exist"}}`)
	for _, outcome := range []Outcome{{StatusCode: 404, Message: message}, {StatusCode: 404, Body: body}} {
		result := Classify(outcome, nil)
		require.Equal(t, CategoryModelUnsupported, result.Category)
		require.Equal(t, "status_404_model", result.Reason)
	}
	require.False(t, IsModelUnsupportedResponse(404, body))
}
