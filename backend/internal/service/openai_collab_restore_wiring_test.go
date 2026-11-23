//go:build unit

package service

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCollabWiringRestorationGeneration(t *testing.T) {
	enableCollabTest(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	BeginOpenAICollabRequest(c, []byte(collabWiringBody), 1, 1)
	_, attempt := armOpenAICollaboration(c, []byte(collabWiringBody), "gpt-5.4", true)
	require.NotNil(t, attempt)
	beginOpenAICollabResponse(c.Request.Context())
	beginOpenAICollabRestoration(c, false)
	_, err := restoreOpenAICollaboration(c, "response.function_call_arguments.delta", []byte(`{"type":"response.function_call_arguments.delta","item_id":"fc_1","encrypted_function_args":["message"]}`), true)
	require.NoError(t, err)
	beginOpenAICollabResponse(c.Request.Context())
	beginOpenAICollabRestoration(c, false)
	beginOpenAICollabRestoration(c, true)
	body, err := restoreOpenAICollaboration(c, "", []byte(`{"output":[`+collabWiringCall+`]}`), false)
	require.NoError(t, err)
	require.Equal(t, "[]", gjson.GetBytes(body, "output.0.encrypted_function_args").Raw, "a failed physical response must not poison the next one")
	require.EqualValues(t, 2, attempt.restorer.generation)
	require.Same(t, attempt.restorer, openAICollabRequestFrom(c.Request.Context()).state.Response.Restorer)
}

func TestCollabWiringRestorationUsesFlushedSnapshot(t *testing.T) {
	enableCollabTest(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	BeginOpenAICollabRequest(c, []byte(collabWiringBody), 1, 1)
	armOpenAICollaboration(c, []byte(collabWiringBody), "gpt-5.4", true)
	beginOpenAICollabResponse(c.Request.Context())
	beginOpenAICollabRestoration(c, false)
	done, err := restoreOpenAICollaboration(c, "response.output_item.done", []byte(`{"type":"response.output_item.done","item":`+collabWiringCall+`}`), true)
	require.NoError(t, err)
	c.Header("Content-Type", "text/event-stream")
	_, err = c.Writer.WriteString("event: response.output_item.done\ndata: " + string(done) + "\n\n")
	require.NoError(t, err)
	c.Writer.Flush()
	changed := strings.Replace(collabWiringCall, "hello world", "changed task", 1)
	completed, err := restoreOpenAICollaboration(c, "response.completed", []byte(`{"type":"response.completed","response":{"output":[`+changed+`]}}`), true)
	require.NoError(t, err)
	require.Equal(t, gjson.GetBytes(done, "item").Raw, gjson.GetBytes(completed, "response.output.0").Raw)
}
