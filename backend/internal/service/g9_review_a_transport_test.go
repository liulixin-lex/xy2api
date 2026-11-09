//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

type g9AuditAUpstream struct {
	httpUpstreamRecorder
	state        *resilience.RequestState
	observations []map[string]any
}

func (u *g9AuditAUpstream) Do(req *http.Request, p string, id int64, n int) (*http.Response, error) {
	tm := u.state.Timing()
	metric := map[string]int64{}
	for _, r := range resilience.Snapshot(time.Minute) {
		if r.Name == "late_attempt_start" && r.GroupID == u.state.GroupID() {
			metric[r.Label] += r.Count
		}
	}
	u.observations = append(u.observations, map[string]any{"late": *tm.LateAttemptCount, "no_timer": *tm.LateAttemptNoTimerCount, "metric": metric})
	return u.httpUpstreamRecorder.Do(req, p, id, n)
}
func (u *g9AuditAUpstream) DoWithTLS(req *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, p, id, n)
}

func TestG9AuditAOpenAITransport(t *testing.T) {
	withResilienceSettings(t, nil)
	idx := 0
	for _, mode := range []string{resilience.BudgetStartArrival, resilience.BudgetStartBodyEOF} {
		for _, pass := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				idx++
				req := newWebSearchReplayRequest(t)
				st := resilience.StateFrom(req.ctx)
				st.SetGroup(int64(94000 + idx))
				cfg := resilience.Defaults()
				cfg.Budget.BudgetStart = mode
				t0 := time.Now().Add(-239 * time.Second)
				st.SetBudget(resilience.NewBudget(context.Background(), &cfg, resilience.KindText, t0))
				st.MarkBodyReceived(t0.Add(10*time.Second), "body_eof", 100)
				st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 378, AttemptStart: time.Now(), Model: "gpt-5.5", Stream: stream})
				payload := `{"id":"resp_review_a","object":"response","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":2}}`
				ctype := "application/json"
				if stream {
					payload = "data: {\"type\":\"response.completed\",\"response\":" + payload + "}\n\n"
					ctype = "text/event-stream"
				}
				up := &g9AuditAUpstream{state: st, httpUpstreamRecorder: httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {ctype}}, Body: io.NopCloser(strings.NewReader(payload))}}}
				svc := newOpenAIImageGenerationControlTestService(nil)
				svc.httpUpstream = up
				account := webSearchReplayAccount(pass)
				body := []byte(fmt.Sprintf(`{"model":"gpt-5.5","stream":%t,"input":"hello"}`, stream))
				result, err := svc.Forward(req.ctx, req.c, account, body)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Len(t, up.observations, 1)
				observation := up.observations[0]
				require.EqualValues(t, 1, observation["late"])
				noTimer := int64(1)
				label := "false:" + mode
				if stream {
					noTimer = 0
					label = "true:" + mode
				}
				require.EqualValues(t, noTimer, observation["no_timer"])
				require.Equal(t, map[string]int64{label: 1}, observation["metric"])

				wire := []any{}
				for _, b := range up.bodies {
					var v any
					require.NoError(t, json.Unmarshal(b, &v))
					wire = append(wire, v)
				}
				out := map[string]any{"mode": mode, "passthrough": pass, "stream": stream, "upstream_count": len(up.requests), "wire": wire, "status": req.rec.Code, "body": req.rec.Body.String(), "attempts": st.Budget().Attempts(), "deadline_ms": st.Budget().Deadline().Sub(t0).Milliseconds(), "usage": result.Usage}
				raw, _ := json.Marshal(out)
				fmt.Println("REVIEW_A_BEHAVIOR", string(raw))
				obs, _ := json.Marshal(up.observations)
				fmt.Println("REVIEW_A_TRANSPORT_OBSERVATIONS", mode, pass, stream, string(obs))
			}
		}
	}
}

type g9AuditACancelBody struct {
	cancel context.CancelFunc
	state  *resilience.RequestState
	once   bool
	done   chan struct{}
}

func (b *g9AuditACancelBody) Read(_ []byte) (int, error) {
	if !b.once {
		b.once = true
		b.cancel()
		b.state.ResolveDisconnect()
		close(b.done)
	}
	return 0, io.EOF
}
func (b *g9AuditACancelBody) Close() error { return nil }

func TestG9AuditAAnthropicPassthroughDisconnect(t *testing.T) {
	withResilienceSettings(t, nil)
	old := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	resilience.SetUndeliveredAbuseCheck(func(int64) bool { return true })
	defer resilience.SetUndeliveredAbuseCheck(nil)
	c, _ := compatStreamContext("/v1/messages", `{}`)
	st := resilience.NewRequestState()
	t0 := time.Now().Add(-239 * time.Second)
	ctx, cancel := context.WithCancel(resilience.WithRequestState(context.Background(), st))
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	cfg := resilience.Defaults()
	cfg.Budget.BudgetStart = resilience.BudgetStartBodyEOF
	st.SetBudget(resilience.NewBudget(ctx, &cfg, resilience.KindText, t0))
	st.MarkBodyReceived(t0.Add(10*time.Second), "body_eof", 100)
	st.InitDelivery(ctx, t0, nil)
	st.SetAPIKey(19)
	st.SetGroup(95001)
	done := make(chan struct{})
	up := &g9AuditAUpstream{state: st, httpUpstreamRecorder: httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: &g9AuditACancelBody{cancel: cancel, state: st, done: done}}}}
	svc := newForwardPartialUsageServiceForTest(nil)
	svc.httpUpstream = up
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
	require.NoError(t, err)
	_, err = svc.Forward(ctx, c, newAnthropicAPIKeyAccountForTest(), parsed)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reader did not finish cancellation")
	}
	gone, delivered, abuse := st.Disconnect()
	require.True(t, gone)
	require.False(t, delivered)
	require.True(t, abuse)
	tm := st.Timing()
	obs, _ := json.Marshal(up.observations)
	fmt.Printf("REVIEW_A_ANTHROPIC before=%s after_late=%d after_no_timer=%d logs=%s err=%v\n", obs, *tm.LateAttemptCount, *tm.LateAttemptNoTimerCount, logs.String(), err)
	require.Contains(t, logs.String(), `"msg":"resilience.late_attempt_undelivered"`)
	require.Contains(t, logs.String(), `"late_attempt_no_timer_count":1`)
	require.EqualValues(t, 1, *tm.LateAttemptCount)
	require.EqualValues(t, 1, *tm.LateAttemptNoTimerCount, "a later response-stage timer must not erase the actual dispatch's no-timer snapshot used by G9")
}
