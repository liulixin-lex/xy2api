//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type fbGroupRepo struct {
	service.GroupRepository
	groups map[int64]*service.Group
}

func (r *fbGroupRepo) GetByIDLite(_ context.Context, id int64) (*service.Group, error) {
	g, ok := r.groups[id]
	if !ok {
		return nil, service.ErrGroupNotFound
	}
	cp := *g
	return &cp, nil
}

type fbAccountRepo struct {
	service.AccountRepository
	byGroup map[int64][]service.Account
}

func (r *fbAccountRepo) ListByGroup(_ context.Context, id int64) ([]service.Account, error) {
	return r.byGroup[id], nil
}

type fbStore struct {
	service.ResilienceStore
	keys map[int64]bool
}

func (s *fbStore) FallbackKey(_ context.Context, _ int64, k int64) (bool, error) {
	return s.keys[k], nil
}
func (s *fbStore) MarkFallbackKey(_ context.Context, _ int64, k int64, _ time.Duration) error {
	s.keys[k] = true
	return nil
}
func (s *fbStore) ClearFallbackKey(_ context.Context, _ int64, k int64) error {
	delete(s.keys, k)
	return nil
}
func (s *fbStore) MarkGroupStable(_ context.Context, _ int64, now time.Time) (time.Time, error) {
	return now, nil
}
func (s *fbStore) ClearGroupStable(context.Context, int64) error { return nil }

func TestAvailabilityFallbackMiddlewareMovesKey(t *testing.T) {
	withMiddlewareResilience(t, nil)
	fbID := int64(20)
	until := time.Now().Add(time.Minute)
	primary := &service.Group{ID: 10, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, AvailabilityFallbackGroupID: &fbID}
	fallback := &service.Group{ID: 20, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
	svc := service.NewResilienceService(nil, &fbStore{keys: map[int64]bool{}})
	svc.ConfigureFallbackGroups(&fbAccountRepo{byGroup: map[int64][]service.Account{
		10: {{ID: 1, Status: service.StatusActive, Schedulable: true, TempUnschedulableUntil: &until, TempUnschedulableReason: "balance:exhausted: x"}},
		20: {{ID: 2, Status: service.StatusActive, Schedulable: true}},
	}}, &fbGroupRepo{groups: map[int64]*service.Group{10: primary, 20: fallback}})
	service.SetResilienceInstance(svc)
	t.Cleanup(func() { service.SetResilienceInstance(nil) })

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	st := resilience.NewRequestState()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(resilience.WithRequestState(context.Background(), st))
	primaryID := int64(10)
	c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 5, GroupID: &primaryID, Group: primary})

	ResilienceAvailabilityFallback()(c)

	moved, ok := GetAPIKeyFromContext(c)
	require.True(t, ok)
	require.Equal(t, int64(20), *moved.GroupID)
	require.Equal(t, int64(20), moved.Group.ID)
	require.Equal(t, int64(10), st.OriginGroupID())
	require.Equal(t, int64(20), st.GroupID())

	// At most one fallback per request.
	ResilienceAvailabilityFallback()(c)
	again, _ := GetAPIKeyFromContext(c)
	require.Equal(t, int64(20), *again.GroupID)
}

// installFallbackFixture publishes a resilience service whose group 10 has no
// usable account and falls back to group 20.
func installFallbackFixture(t *testing.T) (primary *service.Group) {
	t.Helper()
	withMiddlewareResilience(t, nil)
	fbID := int64(20)
	until := time.Now().Add(time.Minute)
	primary = &service.Group{ID: 10, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, AvailabilityFallbackGroupID: &fbID}
	fallback := &service.Group{ID: 20, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
	svc := service.NewResilienceService(nil, &fbStore{keys: map[int64]bool{}})
	svc.ConfigureFallbackGroups(&fbAccountRepo{byGroup: map[int64][]service.Account{
		10: {{ID: 1, Status: service.StatusActive, Schedulable: true, TempUnschedulableUntil: &until, TempUnschedulableReason: "balance:exhausted: x"}},
		20: {{ID: 2, Status: service.StatusActive, Schedulable: true}},
	}}, &fbGroupRepo{groups: map[int64]*service.Group{10: primary, 20: fallback}})
	service.SetResilienceInstance(svc)
	t.Cleanup(func() { service.SetResilienceInstance(nil) })
	return primary
}

// The fallback group only routes the request: the key as authenticated stays
// available for billing, limits and permissions (fallback-billing design §2.1).
func TestAvailabilityFallbackMiddlewareRemembersTheAuthenticatedKey(t *testing.T) {
	primary := installFallbackFixture(t)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	st := resilience.NewRequestState()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(resilience.WithRequestState(context.Background(), st))
	primaryID := int64(10)
	authenticated := &service.APIKey{ID: 5, GroupID: &primaryID, Group: primary}
	c.Set(string(ContextKeyAPIKey), authenticated)

	ResilienceAvailabilityFallback()(c)

	moved, _ := GetAPIKeyFromContext(c)
	require.Equal(t, int64(20), *moved.GroupID)
	billing := service.BillingAPIKey(c.Request.Context(), moved)
	require.Same(t, authenticated, billing)
	require.Equal(t, int64(10), *billing.GroupID)
}

// Model listings describe the user's group and route nothing: they do not
// move to the fallback group (PM decision Q2).
func TestAvailabilityFallbackMiddlewareSkipsModelListings(t *testing.T) {
	primary := installFallbackFixture(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	primaryID := int64(10)
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
		c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 5, GroupID: &primaryID, Group: primary})
	}, ResilienceAvailabilityFallback())
	served := func(c *gin.Context) {
		key, _ := GetAPIKeyFromContext(c)
		c.String(http.StatusOK, "%d", *key.GroupID)
	}
	for _, path := range []string{"/v1/models", "/v1/models/:model", "/v1beta/models", "/v1beta/models/:model", "/models", "/models/:model", "/backend-api/codex/models", "/v1/images/batches/models"} {
		r.GET(path, served)
	}
	r.POST("/v1/responses", served)
	r.POST("/v1beta/models/*modelAction", served)

	for _, tc := range []struct {
		method, path, group string
	}{
		{http.MethodGet, "/v1/models", "10"},
		{http.MethodGet, "/v1/models/gpt-5.4", "10"},
		{http.MethodGet, "/v1beta/models", "10"},
		{http.MethodGet, "/v1beta/models/gemini-3-pro", "10"},
		{http.MethodGet, "/models", "10"},
		{http.MethodGet, "/models/gpt-5.4", "10"},
		{http.MethodGet, "/backend-api/codex/models", "10"},
		{http.MethodGet, "/v1/images/batches/models", "10"},
		{http.MethodPost, "/v1/responses", "20"},
		{http.MethodPost, "/v1beta/models/gemini-3-pro:generateContent", "20"},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		require.Equal(t, tc.group, rec.Body.String(), "%s %s", tc.method, tc.path)
	}
}

// The model allow-list is the user's permission: under an availability
// fallback it is the origin group's, not the serving group's (PM decision Q1).
func TestGroupModelAllowlistUnderFallbackUsesTheOriginGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originID := int64(10)
	authenticated := &service.APIKey{ID: 5, GroupID: &originID, Group: &service.Group{ID: originID, Platform: service.PlatformOpenAI,
		ModelAllowlist: service.GroupModelAllowlist{Enabled: true, Models: []string{"gpt-5.4"}}}}
	moved := service.CloneAPIKeyWithGroup(authenticated, &service.Group{ID: 20, Platform: service.PlatformOpenAI,
		ModelAllowlist: service.GroupModelAllowlist{Enabled: true, Models: []string{"gpt-5.5"}}})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		st := resilience.NewRequestState()
		st.MarkFallback(originID)
		ctx := resilience.WithRequestState(c.Request.Context(), st)
		service.RememberAvailabilityFallbackOrigin(ctx, authenticated)
		c.Request = c.Request.WithContext(ctx)
		c.Set(string(ContextKeyAPIKey), moved)
	}, GroupModelAllowlist())
	router.POST("/v1/responses", func(c *gin.Context) { c.Status(http.StatusOK) })

	post := func(model string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+model+`"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		return rec
	}
	require.Equal(t, http.StatusOK, post("gpt-5.4").Code, "allowed by the origin group")
	denied := post("gpt-5.5")
	require.Equal(t, http.StatusNotFound, denied.Code, "only the serving group allows it")
	require.Contains(t, denied.Body.String(), "gpt-5.4", "the listed models are the origin group's")
	require.NotContains(t, denied.Body.String(), "Available: gpt-5.5")
}
