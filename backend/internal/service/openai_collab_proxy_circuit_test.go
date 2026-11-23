//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type collabProxyReadError struct{}

func (collabProxyReadError) Read([]byte) (int, error) { return 0, errors.New("synthetic read failure") }

func TestCollabOptionalProxyCircuitDoesNotChangeSharedState(t *testing.T) {
	for _, mode := range []string{"missing_terminal", "scan_error", "completed"} {
		completed := mode == "completed"
		for _, optional := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/optional_%t", mode, optional), func(t *testing.T) {
				first, second := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 5)
				first.Credentials = map[string]any{"api_key": "synthetic", "base_url": "https://synthetic.invalid"}
				proxyID := int64(91)
				first.ProxyID = &proxyID
				first.Proxy = &Proxy{ID: proxyID, Protocol: "http", Host: "proxy.invalid", Port: 8080, Status: StatusActive}
				f := newSelectionRuntimeFixture(t, first, second)
				f.svc.cfg.Gateway.StreamDataIntervalTimeout = 0
				f.svc.responseHeaderFilter = compileResponseHeaderFilter(f.svc.cfg)
				circuit := f.svc.getOpenAIProxyStreamCircuit()
				now := time.Now()
				tripped, _ := circuit.recordFailure(proxyID, now.Add(-5*time.Second))
				require.False(t, tripped)
				before := circuit.entries[proxyID]

				parent := resilience.NewRequestState()
				requestCtx := resilience.WithRequestState(context.Background(), parent)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(requestCtx)
				InstallDeliveryTracking(c, now)
				attemptCtx := requestCtx
				if optional {
					child := resilience.NewCollabChildState(parent)
					settings := parent.CollabSettingsSnapshot().Clone()
					child.SetBudget(resilience.NewBudget(requestCtx, &settings, resilience.KindText, now))
					attemptCtx = WithCollabAttemptIsolation(resilience.WithRequestState(requestCtx, child))
				}
				attemptCtx = resilience.WithCollabAttempt(attemptCtx, resilience.CollabAttempt{Optional: optional})
				admitted, err := f.svc.SelectCollabAccount(attemptCtx, ptrInt64Selection(7), "m", OpenAIEndpointCapabilityResponses, 0, nil)
				require.NoError(t, err)
				require.NotNil(t, admitted)
				require.EqualValues(t, 1, admitted.Account.ID)

				var wire bytes.Buffer
				events := []map[string]any{
					{"type": "response.created", "response": map[string]any{"id": "resp_fixture"}},
					{"type": "response.output_text.delta", "delta": "hello"},
				}
				if completed {
					events = append(events, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_fixture", "status": "completed"}})
				}
				for _, event := range events {
					frame, err := json.Marshal(event)
					require.NoError(t, err)
					_, err = fmt.Fprintf(&wire, "data: %s\n\n", frame)
					require.NoError(t, err)
				}
				var upstreamBody io.Reader = &wire
				if mode == "scan_error" {
					upstreamBody = io.MultiReader(upstreamBody, collabProxyReadError{})
				}
				f.svc.httpUpstream = &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(upstreamBody)}}
				body, err := json.Marshal(map[string]any{"model": "m", "stream": true, "input": []json.RawMessage{json.RawMessage(collabBatchTask)}})
				require.NoError(t, err)
				c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body)).WithContext(attemptCtx)
				_, err = f.svc.Forward(attemptCtx, c, &first, body)
				CombineSelectionRelease(admitted, nil)()
				require.Contains(t, recorder.Body.String(), "hello")
				if completed {
					require.NoError(t, err)
				} else if mode == "scan_error" {
					require.ErrorContains(t, err, "synthetic read failure")
				} else {
					require.ErrorContains(t, err, "missing terminal")
				}
				after, exists := circuit.entries[proxyID]
				if optional {
					assert.True(t, exists, "optional attempt may not alter shared proxy history")
					assert.True(t, before == after, "optional attempt changed shared proxy entry")
				}
				if completed {
					circuit.recordFailure(proxyID, time.Now())
				}
				blocked := circuit.isBlocked(proxyID, time.Now())
				ordinary := resilience.WithRequestState(t.Context(), resilience.NewRequestState())
				next, _, err := f.svc.SelectAccountWithSchedulerForCapability(ordinary, ptrInt64Selection(7), "", "", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
				require.NoError(t, err)
				require.NotNil(t, next)
				CombineSelectionRelease(next, nil)()
				if optional {
					want := int64(1)
					if completed {
						want = 2
					}
					assert.EqualValues(t, want, next.Account.ID)
				} else if completed {
					assert.False(t, exists)
					assert.EqualValues(t, 1, next.Account.ID)
				} else {
					assert.True(t, blocked)
					assert.Equal(t, 2, after.failureCount)
					assert.EqualValues(t, 2, next.Account.ID)
				}
			})
		}
	}
}
