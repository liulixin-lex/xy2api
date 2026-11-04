package admin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type gateV3PolicyRepo struct {
	service.GroupQualityGateRepository
	policies service.GroupQualityGatePolicies
}

func (r *gateV3PolicyRepo) LoadPolicies(context.Context) (service.GroupQualityGatePolicies, error) {
	return r.policies, nil
}
func (r *gateV3PolicyRepo) CompareAndSwapPolicies(_ context.Context, expected int, p service.GroupQualityGatePolicies) (service.GroupQualityGatePolicies, error) {
	if expected != r.policies.Version {
		return r.policies, service.ErrGroupQualityGateVersionConflict
	}
	r.policies = p
	return p, nil
}

type gateV3Groups struct{ service.GroupRepository }

func (gateV3Groups) GetByID(context.Context, int64) (*service.Group, error) {
	return &service.Group{ID: 7, Platform: service.PlatformOpenAI}, nil
}
func TestGroupQualityV3PolicyHTTP(t *testing.T) {
	repo := &gateV3PolicyRepo{policies: service.GroupQualityGatePolicies{Groups: map[string]service.GroupQualityGatePolicy{"7": {Enabled: true, MaxErrorRate: .17, MaxWarmTTFTP90Ms: 31000, FastWarmTTFTP90Ms: 9000, PinnedAccountIDs: []int64{99}}}}}
	h := NewGroupQualityGateHandler(service.NewGroupQualityGateService(repo, nil, gateV3Groups{}))
	router := gin.New()
	router.PUT("/policies/:group_id", h.UpdatePolicy)
	router.GET("/policies", h.Policies)
	put := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("PUT", "/policies/7", strings.NewReader(body)))
		return w
	}
	w := put(`{"expected_version":0,"policy":{"enabled":false}}`)
	require.Equal(t, 400, w.Code)
	require.Contains(t, w.Body.String(), `"field":"v3"`)
	w = put(`{"expected_version":0,"policy":{"max_error_rate":"ignored legacy value","pinned_account_ids":"ignored","v3":{"enabled":false,"max_ttft_p90_seconds":15,"max_error_rate":0.1,"min_cache_keep_rate":0.5,"strict":false}}}`)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.True(t, repo.policies.Groups["7"].Enabled)
	require.Equal(t, .17, repo.policies.Groups["7"].MaxErrorRate)
	require.Equal(t, []int64{99}, repo.policies.Groups["7"].PinnedAccountIDs)
	w = put(`{"expected_version":0,"policy":{"v3":{"enabled":true,"max_ttft_p90_seconds":15,"max_error_rate":0.1,"min_cache_keep_rate":0.5}}}`)
	require.Equal(t, 409, w.Code)
	for _, value := range []string{"4", "61", "15.5"} {
		w = put(`{"expected_version":1,"policy":{"v3":{"enabled":true,"max_ttft_p90_seconds":` + value + `,"max_error_rate":0.1,"min_cache_keep_rate":0.5}}}`)
		require.Equal(t, 400, w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/policies", nil))
	require.Equal(t, 200, w.Code)
	var envelope struct {
		Data service.GroupQualityGatePolicies `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	require.False(t, envelope.Data.Groups["7"].V3.Enabled)
}
