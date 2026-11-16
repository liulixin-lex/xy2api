//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func finalF6BedrockFrame(payload string) []byte {
	headers := append([]byte{byte(len(":event-type"))}, []byte(":event-type")...)
	headers = append(headers, 7, 0, 5)
	headers = append(headers, []byte("chunk")...)
	body := []byte(`{"bytes":"` + base64.StdEncoding.EncodeToString([]byte(payload)) + `"}`)
	frame := make([]byte, 12)
	binary.BigEndian.PutUint32(frame[:4], uint32(16+len(headers)+len(body)))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(headers)))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	frame = append(frame, headers...)
	frame = append(frame, body...)
	return binary.BigEndian.AppendUint32(frame, crc32.ChecksumIEEE(frame))
}

type finalF6DisconnectedWriter struct {
	*httptest.ResponseRecorder
	writes int
}

func (w *finalF6DisconnectedWriter) Write(body []byte) (int, error) {
	w.writes++
	if w.writes > 1 {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseRecorder.Write(body)
}

func TestFinalF6BedrockRecordsFinalWireEffort(t *testing.T) {
	for _, tc := range []struct {
		name, model, want    string
		stream, disconnected bool
	}{
		{"sonnet55_json", "claude-sonnet-5-5", "high", false, false},
		{"old_model_strips_effort", "claude-sonnet-4-5", "", false, false},
		{"sonnet55_stream", "claude-sonnet-5-5", "high", true, false},
		{"sonnet55_disconnected_partial", "claude-sonnet-5-5", "high", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":64,"stream":%t,"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`, tc.model, tc.stream))
			rec := httptest.NewRecorder()
			var writer http.ResponseWriter = rec
			if tc.disconnected {
				writer = &finalF6DisconnectedWriter{ResponseRecorder: rec}
			}
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(raw))
			payload := []byte(`{"id":"msg_f6","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			if tc.stream {
				payload = finalF6BedrockFrame(`{"type":"message_start","message":{"id":"msg_f6","role":"assistant","content":[],"usage":{"input_tokens":1}}}`)
				payload = append(payload, finalF6BedrockFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)...)
				payload = append(payload, finalF6BedrockFrame(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`)...)
				if !tc.disconnected {
					payload = append(payload, finalF6BedrockFrame(`{"type":"message_stop"}`)...)
				}
			}
			up := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(payload))}}
			cfg := &config.Config{}
			svc := &GatewayService{cfg: cfg, httpUpstream: up, responseHeaderFilter: compileResponseHeaderFilter(cfg), rateLimitService: &RateLimitService{}}
			account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeBedrock, Credentials: map[string]any{"auth_mode": "apikey", "api_key": "fixture", "aws_region": "us-east-1"}}
			result, err := svc.Forward(context.Background(), c, account, &ParsedRequest{Model: tc.model, Stream: tc.stream, Body: NewRequestBodyRef(raw), OutputEffort: "high"})
			require.NoError(t, err)
			require.NotNil(t, result)
			wireEffort := gjson.GetBytes(up.lastBody, "output_config.effort").String()
			t.Logf("wire_effort=%q result_effort=%q partial_disconnect=%t usage=%d/%d", wireEffort, optionalStringValue(result.ReasoningEffort), result.ClientDisconnect, result.Usage.InputTokens, result.Usage.OutputTokens)
			require.Equal(t, tc.want, wireEffort)
			require.Equal(t, tc.want, optionalStringValue(result.ReasoningEffort))
			require.Equal(t, tc.disconnected, result.ClientDisconnect)
			require.Equal(t, 1, result.Usage.InputTokens)
			require.Equal(t, 1, result.Usage.OutputTokens)
			catalog := newStubPricingServiceFromJSON(t, fmt.Sprintf(`{%q:{"input_cost_per_token":0.001,"output_cost_per_token":0.002}}`, tc.model))
			billing := NewBillingService(cfg, catalog)
			svc.billingService = billing
			svc.resolver = NewModelPricingResolver(nil, billing)
			group := &Group{ID: 4, Platform: PlatformAnthropic, RateMultiplier: 1, ModelPricing: []ChannelModelPricing{{Models: []string{tc.model}, ReasoningEffortMultipliers: map[string]float64{"high": 2}}}}
			key := &APIKey{ID: 2, GroupID: &group.ID, Group: group}
			cost := svc.calculateTokenCost(context.Background(), result, key, tc.model, 1, time.Now())
			wantCost := 0.003
			if strings.EqualFold(tc.want, "high") {
				wantCost *= 2
			}
			require.InDelta(t, wantCost, cost.TotalCost, 1e-12)
		})
	}
}
