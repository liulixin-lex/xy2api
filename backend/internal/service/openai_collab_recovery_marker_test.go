//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollabOptionalLastResortCannotPublishRecoveryMarker(t *testing.T) {
	for _, optional := range []bool{false, true} {
		t.Run(fmt.Sprint(optional), func(t *testing.T) {
			env := newOpenAIRuntimeBlockEnv(t, lrAccountA)
			breakOpenAIModel(t, env, lrAccountA, 3, time.Now())
			child := resilience.NewCollabChildState(resilience.NewRequestState())
			ctx := resilience.WithCollabAttempt(resilience.WithRequestState(context.Background(), child), resilience.CollabAttempt{Optional: optional})
			gid := selectionTestGroupID
			selected, err := env.svc.SelectCollabAccount(ctx, &gid, "gpt-5.1", OpenAIEndpointCapabilityResponses, 0, nil)
			require.NoError(t, err)
			require.NotNil(t, selected)
			CombineSelectionRelease(selected, nil)()
			require.Equal(t, lrAccountA, selected.Account.ID)
			require.Equal(t, lrAccountA, child.LastResort())
			_, marked := env.svc.resilience.lastResortUsed.Load(lrAccountA)
			t.Logf("optional=%t last_resort_shared_marker=%t", optional, marked)
			assert.Equal(t, !optional, marked, "optional selection may keep request-local last resort, but not shared recovery authority")
		})
	}
}

func TestCollabOptionalLastResortCannotChangeLaterRecovery(t *testing.T) {
	for _, prior := range []string{"none", "optional", "ordinary"} {
		t.Run(prior, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				env := newOpenAIRuntimeBlockEnv(t, lrAccountA, lrAccountB)
				env.account(lrAccountA).Priority = 1
				env.account(lrAccountB).Priority = 5
				breakOpenAIModel(t, env, lrAccountA, 3, time.Now())
				breakOpenAIModel(t, env, lrAccountB, 3, time.Now())
				gid := selectionTestGroupID
				if prior != "none" {
					ctx := resilience.WithCollabAttempt(resilience.WithRequestState(t.Context(), resilience.NewCollabChildState(resilience.NewRequestState())), resilience.CollabAttempt{Optional: prior == "optional"})
					selected, err := env.svc.SelectCollabAccount(ctx, &gid, "gpt-5.1", OpenAIEndpointCapabilityResponses, 0, nil)
					require.NoError(t, err)
					require.NotNil(t, selected)
					require.Equal(t, lrAccountA, selected.Account.ID)
					CombineSelectionRelease(selected, nil)()
				}
				// Expire both model cooldowns. The next request is ordinarily admitted,
				// with no last-resort fallback or session.
				time.Sleep(time.Minute)
				state := resilience.NewRequestState()
				ctx := resilience.WithRequestState(t.Context(), state)
				selected, _, err := env.svc.SelectAccountWithSchedulerForCapability(ctx, &gid, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
				require.NoError(t, err)
				require.NotNil(t, selected)
				require.Equal(t, lrAccountA, selected.Account.ID)
				require.Zero(t, state.LastResort())
				CombineSelectionRelease(selected, nil)()

				// A concurrent request writes a transient pause after the final admission
				// check but before this request creates its outbound attempt profile.
				account := *env.account(lrAccountA)
				repo := &resilienceAccountRepo{account: &account}
				rl := NewRateLimitService(repo, nil, env.svc.cfg, nil, nil)
				rl.SetResilienceService(env.svc.resilience)
				env.svc.resilience.rateLimit = rl
				until := time.Now().Add(2 * time.Minute)
				reason := transientCooldownReason("TRANSIENT", "concurrent failure", time.Now())
				require.NoError(t, repo.SetTempUnschedulable(ctx, account.ID, until, reason))
				time.Sleep(time.Second)
				selected.Account.Credentials = map[string]any{"api_key": "synthetic", "base_url": "https://upstream.invalid"}
				env.svc.rateLimitService = rl
				env.svc.responseHeaderFilter = compileResponseHeaderFilter(env.svc.cfg)
				payload, err := json.Marshal(map[string]any{"id": "resp_ordinary", "status": "completed", "output": []any{}})
				require.NoError(t, err)
				env.svc.httpUpstream = &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(payload))}}
				body, err := json.Marshal(map[string]any{"model": "gpt-5.1", "stream": false, "input": []json.RawMessage{json.RawMessage(collabBatchTask)}})
				require.NoError(t, err)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
				result, err := env.svc.Forward(ctx, c, selected.Account, body)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.False(t, result.PartialFailure)
				require.True(t, state.AttemptProfile().AttemptStart.After(transientCooldownStartedAt(reason)))
				// The exact ordinary settlement success hook called by RecordUsage.
				env.svc.resilience.NoteAccountSuccess(ctx, selected.Account)
				fresh, err := repo.GetByID(t.Context(), account.ID)
				require.NoError(t, err)
				env.account(lrAccountA).TempUnschedulableUntil = fresh.TempUnschedulableUntil
				env.account(lrAccountA).TempUnschedulableReason = fresh.TempUnschedulableReason
				nextCtx := resilience.WithRequestState(t.Context(), resilience.NewRequestState())
				next, _, err := env.svc.SelectAccountWithSchedulerForCapability(nextCtx, &gid, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
				require.NoError(t, err)
				require.NotNil(t, next)
				CombineSelectionRelease(next, nil)()
				t.Logf("prior=%s clears=%d next_ordinary_account=%d", prior, repo.clearCalls, next.Account.ID)
				wantClears := 0
				wantID := lrAccountB
				if prior == "ordinary" {
					wantClears = 1
					wantID = lrAccountA
				}
				assert.Equal(t, wantClears, repo.clearCalls)
				assert.Equal(t, wantID, next.Account.ID)
			})
		})
	}
}
