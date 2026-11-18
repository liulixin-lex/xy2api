//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestSchedAttemptCensoringEndsAtCutoff(t *testing.T) {
	const tier = 14 * time.Second
	withResilienceSettings(t, func(cfg *resilience.Settings) {
		cfg.Budget.ModelOverrides = map[string]int{"wire-model": int(tier / time.Second)}
	})
	for _, tc := range []struct {
		name                     string
		receiptDelay, closeDelay time.Duration
	}{
		{"immediate", 0, 0}, {"slow_receipt", 2 * time.Second, 0}, {"slow_close", 0, 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const body = `{"model":"wire-model","input":"hello","stream":true}`
				c, _ := compatStreamContext("/v1/responses", body)
				account := rawChatCompletionsTestAccount()
				st, ctx := ownerFactsState(c.Request.Context(), account)
				c.Request = c.Request.WithContext(ctx)
				f := selectionNewSessionFixture()
				f.binding.owner, f.binding.generation, f.binding.avoidDelay = account.ID, "v1", tc.receiptDelay
				pendingOpenAIStickyChange(ctx).cache = f.binding
				r, w := io.Pipe()
				defer w.Close()
				respBody := &captureCloseBody{ReadCloser: r, close: func() { time.Sleep(tc.closeDelay) }}
				svc := compatStreamService(factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
					RecordOpsUpstreamSendBoundary(req.Context(), id)
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: respBody, Request: req}, nil
				}}, 1, 0, 0)
				recorder := NewSchedAttemptRecorderService(&fakeSchedAttemptStore{})
				var row schedquality.AttemptSample
				recorder.SetObserver(func(sample schedquality.AttemptSample) { row = sample })
				svc.SetSchedAttemptRecorder(recorder)
				_, err := svc.Forward(ctx, c, account, []byte(body))
				requireFirstOutputFailover(t, err)
				cutoff, ok := st.ConfirmedOwnerCutoff()
				require.True(t, ok)
				require.True(t, row.Warm && row.FullTierFirstOutputTimeout)
				require.Equal(t, tier, cutoff.FiredAt.Sub(row.StartedAt))
				require.Equal(t, tc.receiptDelay+tc.closeDelay, row.FinishedAt.Sub(cutoff.FiredAt))
				require.NotNil(t, row.CensoredMs)
				require.EqualValues(t, tier.Milliseconds(), *row.CensoredMs)

				row.Switched = true // The previous attempt in this session used another account.
				counts := schedquality.CountRuleBEvidence([]schedquality.RuleBSample{ruleBSample(row)}, schedquality.RuleBScope{GroupID: 7, AccountID: account.ID, Model: row.Model}, row.FinishedAt, row.StartedAt, 15)
				require.EqualValues(t, 1, counts.N)
				require.Zero(t, counts.K)
				require.EqualValues(t, 1, counts.X)
			})
		})
	}
}

func TestSchedAttemptHTTPStartMatchesHandoffClock(t *testing.T) {
	withResilienceSettings(t, nil)
	body := []byte(`{"model":"m","input":"` + strings.Repeat("a", 8<<20) + `","stream":true}`)
	for _, tc := range []struct {
		name   string
		delay  time.Duration
		faster bool
	}{
		{"just_before", time.Second - time.Nanosecond, true}, {"equal", time.Second, false}, {"just_after", time.Second + time.Nanosecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := selectionNewSessionFixture()
			svc := compatStreamService(factsUpstream{send: func(req *http.Request, id int64) (*http.Response, error) {
				RecordOpsUpstreamSendBoundary(req.Context(), id)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: http.NoBody, Request: req}, nil
			}}, 1, 0, 0)
			ctx := followState(t, f, true)
			acc := f.candidates[0]
			followSelect(svc, ctx, acc)
			change := pendingOpenAIStickyChange(ctx)
			require.Equal(t, stickyFollow, change.kind)
			st := resilience.StateFrom(ctx)
			st.SetSession(7, "session", nil)
			st.SetSchedSelection(resilience.SchedSelection{AccountID: acc.ID, Model: "m", HTTPStreamText: true})
			recorder := NewSchedAttemptRecorderService(&fakeSchedAttemptStore{})
			var row schedquality.AttemptSample
			recorder.SetObserver(func(sample schedquality.AttemptSample) { row = sample })
			svc.SetSchedAttemptRecorder(recorder)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://upstream.invalid/responses", bytes.NewReader(body))
			require.NoError(t, err)
			var a *schedAttemptCapture
			resp, err := svc.sendSchedHTTP(ctx, acc, req, "", body, true, &a)
			require.NoError(t, err)
			defer resp.Body.Close()
			at := a.started.Add(tc.delay)
			recorder.now = func() time.Time { return at.Add(time.Millisecond) }
			a.observeOutput([]byte(`{"type":"response.output_text.delta","delta":"hello"}`), at)
			a.observe([]byte(`{"type":"response.completed","response":{"status":"completed"}}`))
			result := &OpenAIForwardResult{}
			a.finish(result, nil)
			FinishOpenAIStickyForward(ctx, acc.ID, result, nil)

			require.Equal(t, schedquality.OutcomeOK, row.Outcome)
			require.NotNil(t, row.TTFOMs)
			require.True(t, change.targetUpstreamFirstValidKnown)
			require.Equal(t, tc.delay, change.targetUpstreamFirstValidDelay)
			require.Equal(t, tc.faster, f.binding.owner == acc.ID)
			require.Equal(t, tc.faster, int64(*row.TTFOMs) < change.ownerFirstOutputLimit.Milliseconds())
			require.Equal(t, a.started, row.StartedAt)
		})
	}
}

func TestSchedAttemptReusedWSStartsAtRecorderBegin(t *testing.T) {
	withResilienceSettings(t, nil)
	dialer := &errclassDialScript{conn: newStagedPassthroughConn()}
	svc, account, st, _ := errclassDialService(t, dialer)
	st.SetSession(7, "ws-session", nil)
	ctx := resilience.WithRequestState(context.Background(), st)
	recorder := NewSchedAttemptRecorderService(&fakeSchedAttemptStore{})
	var row schedquality.AttemptSample
	recorder.SetObserver(func(sample schedquality.AttemptSample) { row = sample })
	svc.SetSchedAttemptRecorder(recorder)

	acquire, obs := withSchedWSDialObservation(ctx, ctx, svc, account, true)
	_, _, _, err := dialOpenAIWSWithSchedCapture(acquire, dialer, "", nil, "")
	require.NoError(t, err)
	dialStart := obs.successStarted
	require.False(t, dialStart.IsZero())
	first := obs.beginSend([]byte(`{"type":"response.create","model":"synthetic-model","input":"first"}`), true)
	require.Equal(t, dialStart, row.StartedAt)
	first.finish(&OpenAIForwardResult{}, nil)

	payload := []byte(`{"type":"response.create","model":"synthetic-model","input":"` + strings.Repeat("x", 15<<20) + `"}`)
	// Observe Begin's own clock read so hashing cost and OS scheduling cannot
	// move the simulated output events across the millisecond test boundaries.
	var begin time.Time
	recorder.now = func() time.Time { begin = time.Now(); return begin }
	turn := obs.beginSend(payload, true)
	require.False(t, begin.IsZero(), "a turn without a new dial must use recorder.Begin")
	require.Equal(t, begin, row.StartedAt)
	require.EqualValues(t, 1, dialer.calls.Load())
	markSchedFirstEvent(turn, begin, new(50))
	turn.observeOutput([]byte(`{"type":"response.reasoning_text.delta","delta":"thinking"}`), begin.Add(14998*time.Millisecond))
	turn.observeOutput([]byte(`{"type":"response.output_text.delta","delta":"ok"}`), begin.Add(15020*time.Millisecond))
	recorder.now = func() time.Time { return begin.Add(15100 * time.Millisecond) }
	turn.finish(&OpenAIForwardResult{}, nil)
	require.Equal(t, schedquality.OutcomeOK, row.Outcome)
	require.Equal(t, new(50), row.FirstEventMs)
	require.Equal(t, new(14998), row.TTFOMs)
	require.Equal(t, new(15020), row.FirstAnswerMs)
	counts := schedquality.CountRuleBEvidence([]schedquality.RuleBSample{ruleBSample(row)}, schedquality.RuleBScope{GroupID: 7, AccountID: account.ID, Model: row.Model}, row.FinishedAt, begin, 15)
	require.EqualValues(t, 1, counts.N)
	require.Zero(t, counts.K)
}
