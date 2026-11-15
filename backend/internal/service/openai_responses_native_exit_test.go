//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func m6Nonstream(t *testing.T, pass bool, body, contentType string) (*httptest.ResponseRecorder, *OpenAIUsage, error) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}, "Content-Length": {"99999"}}, Body: io.NopCloser(strings.NewReader(body))}
	account := newOpenAIRejectedFieldTestAccount()
	if pass {
		r, err := svc.handleNonStreamingResponsePassthrough(context.Background(), resp, c, account, "gpt-test", "gpt-test")
		if r != nil {
			return w, r.OpenAIUsage, err
		}
		return w, nil, err
	}
	r, err := svc.handleNonStreamingResponse(context.Background(), resp, c, account, "gpt-test", "gpt-test")
	if r != nil {
		return w, r.OpenAIUsage, err
	}
	return w, nil, err
}

func m6Frame(payload string) string { return "data: " + payload + "\n\n" }

func TestM6NonstreamBodyKindAndContentType(t *testing.T) {
	response := `{"id":"r1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"data: text; event: text"}]}],"usage":{"input_tokens":41,"output_tokens":7}}`
	sse := m6Frame(`{"type":"response.completed","response":` + response + `}`)
	multiline := "event: response.completed\r\ndata: {\r\ndata: \"response\":" + response + "}\r\n\r\n"
	for _, pass := range []bool{false, true} {
		for _, tc := range []struct{ name, body, ct string }{
			{"json", response, "application/json"},
			{"json_wrong_header", response, "text/event-stream"},
			{"json_no_header", response, ""},
			{"sse", sse, "text/event-stream"},
			{"sse_wrong_header", sse, "application/json"},
			{"sse_no_header", sse, ""},
			{"sse_multiline_crlf", multiline, "text/event-stream"},
		} {
			t.Run(fmt.Sprintf("%s/pass=%t", tc.name, pass), func(t *testing.T) {
				w, usage, err := m6Nonstream(t, pass, tc.body, tc.ct)
				t.Logf("status=%d content_type=%q usage=%+v body=%s error=%v", w.Code, w.Header().Get("Content-Type"), usage, w.Body.String(), err)
				require.NoError(t, err)
				require.NotNil(t, usage)
				require.Equal(t, 41, usage.InputTokens)
				require.Equal(t, 7, usage.OutputTokens)
				require.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
				require.Equal(t, fmt.Sprint(w.Body.Len()), w.Header().Get("Content-Length"))
				require.True(t, gjson.Valid(w.Body.String()))
				require.Contains(t, w.Body.String(), "data: text; event: text")
			})
		}
	}
}
