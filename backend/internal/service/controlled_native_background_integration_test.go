//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestReviewNativeStreamBackgroundRealStores(t *testing.T) {
	for _, finish := range []string{"completed", "admin_cancel", "lease_lost", "content_timeout", "client_deadline", "unknown"} {
		t.Run(finish, func(t *testing.T) {
			s, db, _, accounts := controlledIntegration(t, false)
			parent := context.Background()
			if finish == "client_deadline" {
				var cancel context.CancelFunc
				parent, cancel = context.WithTimeout(parent, 500*time.Millisecond)
				defer cancel()
			}
			ctx := WithNativeBackgroundExecution(NewControlledRequestContext(WithNativeStreamPolicy(parent, NativeStreamPolicy{Version: 1, Delivery: true}), "responses"))
			group := int64(7)
			r, on, err := s.loadPolicy(ctx, &group, "test-model", "")
			require.NoError(t, err)
			require.True(t, on)
			r.Profile = scheduling.LatencyProfile{Name: scheduling.AccountPoolProfileName, AttemptTimeoutMS: 3000, TotalBudgetMS: 6000, MinAttemptWindowMS: 50}
			if finish == "content_timeout" {
				r.Profile.AttemptTimeoutMS = 400
			}
			r.Ledger = scheduling.NewAttemptLedger(r.Policy.Retry, r.Profile, r.Started, r.ClientDeadline)
			t.Cleanup(r.Close)
			release, err := RetainControlledExecution(ctx)
			require.NoError(t, err)
			t.Cleanup(release)
			a := controlledPick(t, s, ctx, r, accounts[:1])
			var creates atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				creates.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, "{\"id\":\"resp_background\",\"object\":\"response\",\"background\":true,\"status\":\"queued\",\"output\":[]}")
			}))
			defer upstream.Close()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL, strings.NewReader("{\"stream\":false,\"background\":true}"))
			require.NoError(t, err)
			resp, err := s.roundTrip(req, a.ID, 1, upstream.Client().Do)
			require.NoError(t, err)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			c.Writer = &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
			_, err = nativeRelayService().handleNonStreamingResponsePassthrough(ctx, resp, c, a, "test-model", "test-model")
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.NoError(t, CommitControlledIdentity(ctx))
			d := r.currentDispatch
			require.NotNil(t, d)
			require.Equal(t, "resp_background", d.backgroundResponseID)
			poll := NativeBackgroundExecutionContext(ctx)
			require.NoError(t, poll.Err())
			r.Close()
			r.Close()
			require.NoError(t, poll.Err())
			var state string
			require.NoError(t, db.QueryRow("SELECT state FROM scheduling_attempts WHERE ticket_id=$1", d.ticket.TicketID).Scan(&state))
			require.NotEqual(t, "settled", state)
			require.Equal(t, 1, r.Ledger.Snapshot().Attempts)
			require.ErrorIs(t, r.Ledger.CanAttempt(2, 0, time.Now(), true), scheduling.ErrCommitted)
			second := NewControlledRequestContext(context.Background(), "responses")
			_, err = s.beginDispatch(second, a.ID, 1)
			require.ErrorIs(t, err, scheduling.ErrCapacity)
			var terminal []byte
			var finalErr error
			switch finish {
			case "completed":
				terminal = []byte("{\"id\":\"resp_background\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}")
			case "admin_cancel", "lease_lost":
				require.NoError(t, CancelControlledRequest(ctx, ControlledCancelReason(finish)))
				require.ErrorIs(t, poll.Err(), context.Canceled)
				terminal = []byte("{\"id\":\"resp_background\",\"status\":\"cancelled\",\"output\":[]}")
				finalErr = context.Canceled
			case "content_timeout", "client_deadline":
				select {
				case <-poll.Done():
				case <-time.After(time.Second):
					t.Fatal("queued EOF incorrectly disarmed original content/client deadline")
				}
				if finish == "content_timeout" {
					require.Equal(t, ControlledContentTimeout, ControlledStreamSnapshot(ctx).CancelReason)
				} else {
					require.ErrorIs(t, context.Cause(poll), context.DeadlineExceeded)
				}
				finalErr = context.Cause(poll)
			case "unknown":
				finalErr = fmt.Errorf("retrieval transport unavailable")
			}
			resultErr := FinishNativeBackground(ctx, terminal, finalErr)
			if finish == "completed" {
				require.NoError(t, resultErr)
			} else {
				require.Error(t, resultErr)
			}
			_ = FinishNativeBackground(ctx, terminal, finalErr)
			release()
			release()
			r.Close()
			var count int
			var outcome string
			require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts WHERE request_id=$1", r.ID).Scan(&count))
			require.Equal(t, 1, count)
			require.NoError(t, db.QueryRow("SELECT state,outcome FROM scheduling_attempts WHERE ticket_id=$1", d.ticket.TicketID).Scan(&state, &outcome))
			if finish == "completed" || finish == "admin_cancel" || finish == "lease_lost" {
				require.Equal(t, "settled", state)
			} else {
				require.NotEqual(t, "settled", state, "unverified remote completion must retain unknown execution capacity")
			}
			require.Len(t, r.history, 1)
			require.EqualValues(t, 1, creates.Load())
			require.Equal(t, 1, r.Ledger.Snapshot().Attempts)
			t.Logf("queued retained original ticket; finish=%s final_state=%s outcome=%s attempts=1 upstream_creates=1 middleware_close_did_not_release=true", finish, state, outcome)
		})
	}
}
