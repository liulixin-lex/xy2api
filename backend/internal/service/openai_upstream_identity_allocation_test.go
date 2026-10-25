//go:build unit

package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

// A reused Gin context hides first-insertion map growth after AllocsPerRun's
// warmup. Give every measured call a distinct request, prepared outside the
// measurement, and vary existing map occupancy across growth boundaries.
func TestOpenAIUpstreamIdentityPrepareAllocationBoundaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const runs = 64
	account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	session := ""
	bucket := 0
	for n := 0; bucket == 0; n++ {
		session = "v2:synthetic-l01-allocation-session-" + strconv.Itoa(n)
		bucket = openAIUpstreamIdentityCohort(session)
	}
	for _, state := range []string{"off", "control", "account_out_of_scope"} {
		for _, padding := range []int{0, 1, 3, 4, 5, 6, 7, 8, 9, 12, 13, 14, 15, 16, 24, 25, 26, 27, 28, 29, 32} {
			for _, retry := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/padding=%d/retry=%t", state, padding, retry), func(t *testing.T) {
					policyState := l01DisabledWireState{mode: "off"}
					wantArm, wantSkip := "none", "policy_off"
					switch state {
					case "control":
						policyState = l01DisabledWireState{master: true, mode: "uuid_fill", percent: bucket}
						wantArm, wantSkip = "off", "arm_off"
					case "account_out_of_scope":
						policyState = l01DisabledWireState{master: true, mode: "uuid_fill", percent: 100, accounts: []int64{999}}
						wantArm, wantSkip = "none", "account_out_of_scope"
					}
					// Publish outside measurement exactly as production does. Any
					// immutable request-cohort wrappers belong to this snapshot.
					svc := &OpenAIGatewayService{cfg: &config.Config{}}
					l01WireConfigure(t, policyState)(svc)
					policy := svc.cachePolicies.Snapshot()
					contexts := make([]*gin.Context, runs+1)
					for i := range contexts {
						c, _ := gin.CreateTestContext(httptest.NewRecorder())
						c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
						c.Set(cachePolicySnapshotGinKey, policy)
						c.Set(openAICacheSessionGinKey, session)
						if state == "control" {
							c.Set(openAICacheSessionProvenanceGinKey, true)
						}
						c.Set("api_key", &APIKey{ID: 99})
						for key := 0; key < padding; key++ {
							c.Set("synthetic-padding-"+strconv.Itoa(key), key)
						}
						if retry {
							d := prepareOpenAIUpstreamIdentity(c, account)
							require.Equal(t, wantArm, d.Arm)
							require.Equal(t, wantSkip, d.Skip)
						}
						contexts[i] = c
					}
					index := 0
					var decision openAIUpstreamIdentityDecision
					allocations := testing.AllocsPerRun(runs, func() {
						decision = prepareOpenAIUpstreamIdentity(contexts[index], account)
						index++
					})
					require.Equal(t, runs+1, index, "one fresh context per call, including warmup")
					require.Zero(t, allocations, "excluded requests must not allocate, even at map growth boundaries")
					require.Equal(t, wantArm, decision.Arm)
					require.Equal(t, wantSkip, decision.Skip)
					require.False(t, decision.eligible())
					for _, c := range contexts {
						keys := 3 + padding
						if state == "control" {
							keys++
						}
						require.Len(t, c.Keys, keys, "cohort preparation must not grow the Gin map")
						require.Same(t, policy, CachePolicyFromGin(c), "cohort wrapper preserves the captured snapshot")
						require.Same(t, policy, CaptureCachePolicy(c, nil), "retry capture preserves the original snapshot")
						treatment, fixed := cachePolicyL01CohortFromGin(c)
						require.False(t, treatment)
						require.Equal(t, state == "control", fixed, "policy-off and scope exclusions must not fix a cohort")
						require.NotContains(t, c.Keys, openAIUpstreamIdentityWireGinKey)
					}
				})
			}
		}
	}
}
