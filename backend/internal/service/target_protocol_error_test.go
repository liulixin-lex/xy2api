//go:build unit

package service

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

func TestM7ServiceErrorAfterHeartbeat(t *testing.T) {
	for _, protocol := range []string{PreOutputProtocolChat, PreOutputProtocolAnthropic, PreOutputProtocolResponses} {
		for _, stream := range []bool{false, true} {
			t.Run(protocol+map[bool]string{false: "/json", true: "/sse"}[stream], func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest("POST", "/", nil)
				if stream {
					c.Header("Content-Type", "text/event-stream")
					_, _ = c.Writer.Write([]byte(": ping\n\n"))
					c.Writer.Flush()
				}
				if !WriteTargetProtocolError(c, protocol, 400, "invalid_request_error", "bad_field", "input[1]", "bad input") {
					t.Fatal("unhandled")
				}
				body := rec.Body.String()
				if protocol == PreOutputProtocolAnthropic {
					if strings.Contains(body, `"param"`) || strings.Contains(body, `"code"`) {
						t.Fatal(body)
					}
				} else if !strings.Contains(body, `"param":"input[1]"`) {
					t.Fatal(body)
				}
				if stream {
					switch protocol {
					case PreOutputProtocolChat:
						if !strings.Contains(body, "data: [DONE]\n\n") || strings.Contains(body, "event:") {
							t.Fatal(body)
						}
					case PreOutputProtocolAnthropic:
						if !strings.Contains(body, "event: error\ndata: {\"error\"") || !strings.Contains(body, `"type":"error"`) {
							t.Fatal(body)
						}
					case PreOutputProtocolResponses:
						if strings.Count(body, "event: response.created\n") != 1 || strings.Count(body, "event: response.failed\n") != 1 {
							t.Fatal(body)
						}
					}
				} else if rec.Code != 400 {
					t.Fatal(rec.Code)
				}
				if WriteTargetProtocolError(c, protocol, 500, "server_error", "", "", "second") {
					t.Fatal("duplicate call claimed terminal")
				}
				if rec.Body.String() != body {
					t.Fatal("duplicate terminal")
				}
			})
		}
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/", nil)
	c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
	InstallDeliveryTracking(c, time.Now())
	c.Header("Content-Type", "text/event-stream")
	_, _ = c.Writer.Write([]byte("data: {\"type\":\"response.created\",\"sequence_number\":4,\"response\":{\"id\":\"resp_old\",\"model\":\"m\",\"created_at\":10}}\n\n"))
	_, _ = c.Writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"sequence_number\":5,\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}\n\n"))
	WriteTargetProtocolError(c, PreOutputProtocolResponses, 502, "server_error", "", "input", "failed")
	body := rec.Body.String()
	var failed gjson.Result
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") && gjson.Get(line[6:], "type").String() == "response.failed" {
			failed = gjson.Parse(line[6:])
		}
	}
	if failed.Get("response.id").String() != "resp_old" || failed.Get("sequence_number").Int() != 6 || failed.Get("response.usage.input_tokens").Int() != 7 || failed.Get("response.created_at").Int() != 10 || strings.Contains(body, "event: response.created") {
		t.Fatal(body)
	}
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.JSON(200, gin.H{"ok": true})
	before := rec.Body.String()
	if WriteTargetProtocolError(c, PreOutputProtocolChat, 500, "server_error", "", "", "later") {
		t.Fatal("written JSON claimed again")
	}
	if before != rec.Body.String() {
		t.Fatal("appended to JSON")
	}
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/", nil)
	ctx, cancel := context.WithCancel(c.Request.Context())
	cancel()
	c.Request = c.Request.WithContext(ctx)
	if WriteTargetProtocolError(c, PreOutputProtocolChat, 500, "server_error", "", "", "canceled") || rec.Body.Len() != 0 {
		t.Fatal("canceled request claimed")
	}
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/", nil)
	c.Header("Content-Type", "text/event-stream")
	_, _ = c.Writer.Write([]byte(": ping\n\n"))
	c.Writer = foundationFailedWriter{c.Writer}
	if !WriteTargetProtocolError(c, PreOutputProtocolChat, 500, "server_error", "", "", "write failure") || !ProtocolTerminalClaimed(c) || WriteTargetProtocolError(c, PreOutputProtocolChat, 500, "server_error", "", "", "duplicate") {
		t.Fatal("write failure claim semantics")
	}

}
