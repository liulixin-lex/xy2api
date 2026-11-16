//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestM8RejectedFieldUnsentRepairStaysFalse(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		for _, gate := range []string{"not_before", "budget_deadline", "sent"} {
			t.Run(fmt.Sprintf("passthrough=%t/%s", passthrough, gate), func(t *testing.T) {
				body := []byte(`{"model":"gpt-5.5","input":"hi","max_output_tokens":20}`)
				st := resilience.NewRequestState()
				ctx := resilience.WithRequestState(context.Background(), st)
				if gate == "budget_deadline" {
					settings := resilience.Defaults()
					started := time.Now().Add(-time.Duration(settings.Budget.PreOutputBudgetSeconds)*time.Second + 100*time.Millisecond)
					budget := resilience.NewBudget(ctx, &settings, resilience.KindText, started)
					st.SetBudget(budget)
					deadline, cancel := context.WithDeadline(ctx, budget.Deadline())
					defer cancel()
					ctx = deadline
				}
				st.SetLastUpstreamFailure(&UpstreamFailoverError{StatusCode: 429})
				c := newOpenAIRejectedFieldTestContext(body)
				c.Request = c.Request.WithContext(ctx)
				account := newOpenAIRejectedFieldTestAccount()
				account.Extra["openai_passthrough"] = passthrough
				response := newOpenAIRejectedFieldTestResponse(400, `{"error":{"code":"unsupported_parameter","param":"max_output_tokens","message":"Unsupported parameter: max_output_tokens"}}`)
				var once sync.Once
				response.Body = m6CloseFuncBody{Reader: response.Body, close: func() {
					once.Do(func() {
						switch gate {
						case "not_before":
							st.NoteAccountRetryNotBefore(account.ID, resilience.UpstreamRecoveryHint{NotBefore: time.Now().Add(310 * time.Second)})
						case "budget_deadline":
							<-ctx.Done()
						}
					})
				}}
				upstream := &httpUpstreamRecorder{responses: []*http.Response{response,
					newOpenAIRejectedFieldTestResponse(200, `{"id":"repaired","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)}}
				_, err := newOpenAIRejectedFieldTestService(upstream).Forward(ctx, c, account, body)
				if gate == "sent" {
					require.NoError(t, err)
					require.Len(t, upstream.requests, 2)
					require.False(t, gjson.GetBytes(upstream.bodies[1], "max_output_tokens").Exists())
				} else {
					require.Error(t, err)
					require.Len(t, upstream.requests, 1)
					var blocked *UpstreamFailoverError
					require.ErrorAs(t, err, &blocked)
					require.True(t, blocked.PreDispatchSelectionInvalidated)
				}
				events := currentOpsUpstreamEvents(c)
				require.Len(t, events, 1)
				require.Same(t, events[0], opsUpstreamEventsForTest(t, c)[0])
				require.Equal(t, "max_output_tokens", events[0].RejectedField)
				require.Equal(t, gate == "sent", events[0].Resent)
				require.Contains(t, resilience.SnapshotConversionNotices(ctx), apicompat.ConversionNotice{Field: "max_output_tokens", Action: "omit"})
				wire, marshalErr := json.Marshal(events[0])
				require.NoError(t, marshalErr)
				require.True(t, gjson.GetBytes(wire, "resent").Exists())
				var stored OpsUpstreamErrorEvent
				require.NoError(t, json.Unmarshal(wire, &stored))
				require.Equal(t, events[0].Resent, stored.Resent)
				require.Equal(t, "max_output_tokens", stored.RejectedField)
			})
		}
	}
}
