//go:build unit

package handler

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type placeholderExpireOnRead struct {
	io.Reader
	expire func()
}

func (r *placeholderExpireOnRead) Read(p []byte) (int, error) {
	if r.expire != nil {
		r.expire()
		r.expire = nil
	}
	return r.Reader.Read(p)
}

func TestResponsesBlankPlaceholderOutcomeLoggedAtHandlerExit(t *testing.T) {
	withHandlerResilience(t, nil)
	const blank = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_usage\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\" \"}\n\n"
	for _, tc := range []struct {
		name, body, outcome string
		marked, expire      bool
	}{
		{name: "no blank no event", body: ptUsageOpening + ptUsageCompleted},
		{name: "blank success", body: blank + ptUsageCompleted, marked: true, outcome: "completed"},
		{name: "blank failed", body: blank + ptUsageFailed, marked: true, outcome: "selection_exhausted"},
		{name: "blank budget exhausted", body: blank + ptUsageFailed, marked: true, expire: true, outcome: "budget_exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(logger.IntoContext(context.Background(), zap.New(core)), st)
			upstream := &ptUsageUpstream{body: func() io.ReadCloser {
				if tc.expire {
					// The already-started upstream attempt outlives the pre-output
					// budget. The normal failover check must supply the final cause.
					return io.NopCloser(&placeholderExpireOnRead{Reader: strings.NewReader(tc.body), expire: func() {
						st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now().Add(-time.Hour)))
					}})
				}
				return io.NopCloser(strings.NewReader(tc.body))
			}}
			h := newResponsesUsageHandler(t, &gwUsageLogRepo{}, upstream, 0, true)
			servePassthroughResponses(h, ctx, httptest.NewRecorder())
			events := logs.FilterMessage("resilience.blank_placeholder_outcome").All()
			if !tc.marked {
				require.Empty(t, events)
				return
			}
			require.Len(t, events, 1)
			fields := events[0].ContextMap()
			require.EqualValues(t, 0, fields["placeholder_timeouts"])
			require.Equal(t, tc.outcome, fields["final_outcome"])
			cause := ""
			if tc.expire {
				cause = resilience.ExhaustedDeadline
			}
			require.Equal(t, cause, fields["budget_cause"])
			require.Equal(t, tc.outcome, *st.RecoveryOutcome(), "observation must not overwrite handler outcome")
		})
	}
}
