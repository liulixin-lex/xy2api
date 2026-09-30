package admin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestNativeStreamGroupPolicyAtomicFeatureValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags scheduling.NativeStreamFeatures
		code  int
	}{
		{"delivery", scheduling.NativeStreamFeatures{Delivery: true}, 200},
		{"recovery", scheduling.NativeStreamFeatures{Delivery: true, Recovery: true}, 200},
		{"split_commit_boundary", scheduling.NativeStreamFeatures{Recovery: true}, 400},
		{"unimplemented_persistence", scheduling.NativeStreamFeatures{Delivery: true, Recovery: true, Persistence: true}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := scheduling.DefaultGroupPolicy(10)
			p.NativeStream = tc.flags
			raw, err := json.Marshal(map[string]any{"expected_version": 0, "policy": p})
			require.NoError(t, err)
			s := &groupSchedulingAdminStub{}
			w := schedulingTestCall(groupSchedulingRouter(s), http.MethodPut, "/groups/10", string(raw))
			require.Equal(t, tc.code, w.Code, w.Body.String())
			if tc.code == 200 {
				require.Equal(t, tc.flags, s.received.NativeStream)
			} else {
				require.Zero(t, s.groupWrites)
			}
		})
	}
}

func TestNativeStreamOldClientPreservesFlags(t *testing.T) {
	p := scheduling.DefaultGroupPolicy(10)
	p.NativeStream = scheduling.NativeStreamFeatures{Delivery: true, Recovery: true}
	s := &groupSchedulingAdminStub{record: scheduling.GroupPolicyRecord{Policy: p}}
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(validGroupPolicyJSON(t)), &body))
	policy, ok := body["policy"].(map[string]any)
	require.True(t, ok)
	delete(policy, "native_stream")
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	w := schedulingTestCall(groupSchedulingRouter(s), http.MethodPut, "/groups/10", string(raw))
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, p.NativeStream, s.received.NativeStream)
}
