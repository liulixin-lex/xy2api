//go:build unit

package service

import (
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// RunCollabInterleavedSelection completes a second instance's pending placement
// at each storage boundary of the first request, including its trial operations.
func RunCollabInterleavedSelection(t *testing.T, cache GatewayCache, leases SchedTrialLeases, reset func(), arm func(int, func()), members func(int64) []string) {
	withResilienceSettings(t, nil)
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	for boundary := 1; boundary <= 16; boundary++ {
		outcome := func(on bool) string {
			reset()
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2), selectionFixtureAccount(3, 3))
			f.svc.cache = cache
			selectionSetTrial(f, 2, newSelectionTrialLeaseFake())
			selectionSetTrial(f, 3, newSelectionTrialLeaseFake())
			f.svc.selectionBoard.leases = leases
			a, session := collabAtomicRequest(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), "interleaved", on, false)
			key, member := "openai:"+session, TrialLeaseSessionMember(session)
			if on {
				binding := openAICollabBindingFrom(a)
				require.NoError(t, cache.SetSessionAccountID(a, 7, key, 1, time.Hour))
				require.NoError(t, binding.write(a, 0, time.Hour))
				key, member = binding.key, TrialLeaseSessionMember(binding.key)
			}
			b := collabAtomicContext(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), "interleaved", on)
			second, _, err := f.svc.SelectAccountWithSchedulerForCapability(b, ptrInt64Selection(7), "", session, "m", map[int64]struct{}{1: {}, 2: {}}, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true)
			require.NoError(t, err)
			require.EqualValues(t, 3, second.Account.ID)
			finish := func() { CompleteOpenAIStickyChange(b, 3, true, false) }
			arm(boundary, finish)
			first, _, err := f.svc.SelectAccountWithSchedulerForCapability(a, ptrInt64Selection(7), "", session, "m", map[int64]struct{}{1: {}, 3: {}}, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true)
			require.NoError(t, err)
			CompleteOpenAIStickyChange(a, first.Account.ID, true, false)
			first.ReleaseFunc()
			arm(0, nil)
			finish()
			second.ReleaseFunc()
			owner, err := cache.GetSessionAccountID(a, 7, key)
			require.NoError(t, err)
			var held []string
			for _, account := range []int64{2, 3} {
				for _, seat := range members(account) {
					held = append(held, fmt.Sprintf("%d:%s", account, strings.ReplaceAll(seat, member, "binding")))
				}
			}
			sort.Strings(held)
			if on {
				tree, err := cache.GetSessionAccountID(a, 7, "openai:"+session)
				require.NoError(t, err)
				require.EqualValues(t, 1, tree)
			}
			return fmt.Sprintf("selected=%d wait=%v owner=%d held=%v bypass=%t", first.Account.ID, first.WaitPlan, owner, held, resilience.StateFrom(a).StickyBypassChangesBinding())
		}
		require.Equal(t, outcome(false), outcome(true), "storage boundary %d", boundary)
	}
}
