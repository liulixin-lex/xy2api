//go:build unit

package service

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestM8ToolHistoryErrorIdentity(t *testing.T) {
	for _, tc := range []struct {
		status               int
		message, code, param string
		want                 bool
	}{
		{400, "No tool call found for function call output with call_id call_wrong at input[1]", "invalid_request_error", "input[1].call_id", true},
		{400, "No tool output found for function call call_1", "", "messages[2]", true},
		{400, "No tool call found for function call output", "", "", true},
		{409, "No tool output found for function call", "", "", false},
		{400, "prefix No tool output found for function call", "", "", false},
		{400, "No tool output found for function calls", "", "", false},
		{400, "No tool output found for function call", "bad_code", "input", false},
		{400, "No tool output found for function call", "", "input_extra", false},
	} {
		body, _ := json.Marshal(gin.H{"error": gin.H{"type": "invalid_request_error", "message": tc.message, "code": tc.code, "param": tc.param}})
		if got := isToolHistoryUpstreamClientError(tc.status, body); got != tc.want {
			t.Fatal(tc, got)
		}
		if tc.want {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			writeOpenAIUpstreamClientError(c, tc.status, body, SanitizeUpstreamErrorMessage(tc.message))
			if rec.Code != 400 || gjson.Get(rec.Body.String(), "error.message").String() != tc.message || gjson.Get(rec.Body.String(), "error.param").String() != tc.param {
				t.Fatal(rec.Code, rec.Body.String())
			}
		}
	}
	if isToolHistoryUpstreamClientError(400, []byte(`{"message":"No tool output found for function call"}`)) {
		t.Fatal("unstructured message")
	}
}
