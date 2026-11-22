//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestFCOObservationFourPathsPreservesErrors(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range []string{"native", "passthrough", "chat", "bridge"} {
		t.Run(path, func(t *testing.T) {
			type result struct {
				client   string
				status   int
				count    int
				sequence uint64
				calls    int
				wire     string
			}
			run := func(itemType string) result {
				body := []byte(`{"model":"gpt-5.4","instructions":"test","stream":false,"input":[{"type":"` + itemType + `","namespace":"agent","name":"browser_info","output":"x"}]}`)
				payload := `{"error":{"message":"` + fcoRefusalPrefix + `"}}`
				c, rec := compatStreamContext("/v1/responses", string(body))
				state := resilience.NewRequestState()
				c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
				upstream := &capabilityForwardUpstream{&httpUpstreamRecorder{resp: &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}}}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				account := rawChatCompletionsTestAccount()
				var writes strings.Builder
				if path == "bridge" {
					_, _ = svc.proxyOpenAIWSHTTPBridgeTurn(c.Request.Context(), c, account, "test-key", body, len(body), "gpt-5.4", "", "", "", "", 1, func(message []byte) error { _, err := writes.Write(message); return err })
				} else {
					_, _ = forwardCapabilityAttempt(svc, c, path, account, body)
				}
				r := result{client: rec.Body.String() + writes.String(), status: rec.Code, sequence: state.OpsUpstreamEventSequence(), calls: len(upstream.bodies)}
				if len(upstream.bodies) > 0 {
					r.wire = string(upstream.bodies[0])
				}
				if raw, ok := c.Get(OpsUpstreamErrorsKey); ok {
					events := raw.([]*OpsUpstreamErrorEvent)
					r.count = len(events)
					for _, event := range events {
						require.NotEqual(t, "fco_standalone_rejected", event.Reason)
						require.Nil(t, event.FCOShape)
					}
				}
				return r
			}
			baseline := run("ordinary_output")
			observed := run("function_call_output")
			require.Equal(t, 1, observed.calls)
			require.Equal(t, baseline.calls, observed.calls)
			require.Equal(t, baseline.count, observed.count)
			require.Equal(t, baseline.sequence, observed.sequence)
			require.Equal(t, baseline.status, observed.status)
			require.Equal(t, baseline.client, observed.client)
			require.Contains(t, observed.wire, "function_call_output")
		})
	}
}
