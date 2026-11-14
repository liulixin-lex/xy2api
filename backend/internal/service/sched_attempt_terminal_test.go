//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestSchedAttemptTerminalRetainsEvidence(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		first, cancel, timeout bool
		err                    error
		event                  string
		outcome                schedquality.Outcome
	}{
		{name: "complete", first: true, outcome: 1},
		{name: "post_output_failure", first: true, err: errors.New("read failed"), outcome: 3},
		{name: "cancel_before_output", cancel: true, outcome: 5},
		{name: "cancel_after_output", first: true, cancel: true, outcome: 5},
		{name: "deadline_is_failure", err: context.DeadlineExceeded, outcome: 3},
		{name: "internal_cancel_is_failure", err: context.Canceled, outcome: 3},
		{name: "first_output_deadline", timeout: true, outcome: 2},
		{name: "post_output_timeout", first: true, timeout: true, outcome: 3},
		{name: "max_output_tokens_is_success", first: true, event: `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, outcome: 1},
		{name: "content_filter_is_success", first: true, event: `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`, outcome: 1},
		{name: "unknown_incomplete_is_failure", first: true, event: `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"unknown"}}}`, outcome: 3},
		{name: "upstream_cancel_is_failure", event: `{"type":"response.cancelled"}`, outcome: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := resilience.NewRequestState()
			st.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "fixture"})
			client, cancel := context.WithCancel(context.Background())
			defer cancel()
			st.InitDelivery(client, time.Now(), nil)
			recorder := NewSchedAttemptRecorderService(&fakeSchedAttemptStore{})
			var rows []schedquality.AttemptSample
			recorder.SetObserver(func(row schedquality.AttemptSample) { rows = append(rows, row) })
			svc := &OpenAIGatewayService{schedAttemptRecorder: recorder}
			a := svc.beginSchedAttempt(resilience.WithRequestState(context.Background(), st), &Account{ID: 1, Platform: PlatformOpenAI}, nil, 0, true)
			a.noteUsage(&OpenAIUsage{InputTokens: 100, CacheReadInputTokens: 80, CacheCreationInputTokens: 20})
			if tc.first {
				a.MarkFirstOutput(a.started.Add(10 * time.Millisecond))
			}
			if tc.cancel {
				cancel()
			}
			if tc.event != "" {
				a.observe([]byte(tc.event))
			}
			if tc.timeout {
				a.timeout(resilience.TimeoutPlan{})
			} else {
				a.finish(nil, tc.err)
			}
			a.finish(nil, nil)
			require.Len(t, rows, 1)
			row := rows[0]
			require.Equal(t, tc.outcome, row.Outcome)
			require.Equal(t, tc.outcome == 5, row.ClientCanceled)
			require.Equal(t, 0, *row.InputTokens)
			require.Equal(t, 80, *row.CacheReadTokens)
			require.Equal(t, 20, *row.CacheCreationTokens)
			require.Equal(t, 100, *row.PromptTokens)
			require.Equal(t, tc.first, row.TTFOMs != nil)
		})
	}
}
