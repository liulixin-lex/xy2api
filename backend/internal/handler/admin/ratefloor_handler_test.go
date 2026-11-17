package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type ratefloorMemberAdmin struct {
	service.AdminService
	calls  int
	pinned bool
}

func (s *ratefloorMemberAdmin) SetGroupMemberPinned(_ context.Context, groupID, accountID int64, pinned bool) error {
	s.calls++
	s.pinned = pinned
	return nil
}

func TestRatefloorMemberHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		body   string
		status int
	}{{`{"pinned":true}`, 200}, {`{"pinned":false}`, 200}, {`{}`, 400}, {`{"pinned":null}`, 400}, {`{"pinned":"true"}`, 400}} {
		t.Run(tc.body, func(t *testing.T) {
			svc := &ratefloorMemberAdmin{}
			h := &GroupHandler{adminService: svc}
			router := gin.New()
			router.PUT("/groups/:id/members/:account_id", h.SetMemberPinned)
			request := httptest.NewRequest(http.MethodPut, "/groups/7/members/42", bytes.NewBufferString(tc.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, tc.status, response.Code)
			if tc.status == 200 {
				require.Equal(t, 1, svc.calls)
				var body map[string]any
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
				data := body["data"].(map[string]any)
				require.Equal(t, svc.pinned, data["pinned"])
				require.EqualValues(t, 42, data["account_id"])
			} else {
				require.Zero(t, svc.calls)
			}
		})
	}
}

type ratefloorUpdateGroupRepo struct {
	service.AdminGroupRepository
	group  service.Group
	writes int
}

func (r *ratefloorUpdateGroupRepo) GetByID(context.Context, int64) (*service.Group, error) {
	g := r.group
	return &g, nil
}
func (r *ratefloorUpdateGroupRepo) Update(_ context.Context, g *service.Group) error {
	r.group = *g
	r.writes++
	return nil
}

func TestRatefloorRequestNullAndZero(t *testing.T) {
	floor := 0.4
	repo := &ratefloorUpdateGroupRepo{group: service.Group{ID: 7, Platform: service.PlatformOpenAI, RateMultiplier: 1, MinUpstreamRate: &floor}}
	svc := service.NewAdminService(nil, nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h := &GroupHandler{adminService: svc}
	router := gin.New()
	router.PUT("/groups/:id", h.Update)
	for _, tc := range []struct {
		body    string
		cleared bool
	}{{`{}`, false}, {`{"min_upstream_rate":null}`, false}, {`{"min_upstream_rate":0}`, true}} {
		request := httptest.NewRequest(http.MethodPut, "/groups/7", bytes.NewBufferString(tc.body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, 200, response.Code, response.Body.String())
		var payload struct {
			Data struct {
				MinUpstreamRate *float64 `json:"min_upstream_rate"`
			}
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
		if tc.cleared {
			require.Nil(t, repo.group.MinUpstreamRate)
			require.Nil(t, payload.Data.MinUpstreamRate)
		} else {
			require.NotNil(t, repo.group.MinUpstreamRate)
			require.Equal(t, 0.4, *repo.group.MinUpstreamRate)
			require.NotNil(t, payload.Data.MinUpstreamRate)
			require.Equal(t, 0.4, *payload.Data.MinUpstreamRate)
		}
	}
	require.Equal(t, 3, repo.writes)
}

type ratefloorPreviewGroups struct {
	service.GroupRepository
	platform string
}

func (r ratefloorPreviewGroups) GetByIDLite(context.Context, int64) (*service.Group, error) {
	return &service.Group{ID: 10, Platform: r.platform, RateMultiplier: 2, ProfitControlEnabled: true}, nil
}
func TestRatefloorPreviewZeroOnOtherPlatforms(t *testing.T) {
	for _, platform := range []string{service.PlatformAnthropic, service.PlatformGemini, service.PlatformGrok, service.PlatformAntigravity} {
		t.Run(platform, func(t *testing.T) {
			h := NewProfitPreviewHandler(service.NewProfitPreviewService(ratefloorPreviewAccounts{}, ratefloorPreviewGroups{platform: platform}))
			router := gin.New()
			router.GET("/groups/:id/profit-control-preview", h.ProfitControlPreview)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/groups/10/profit-control-preview?margin=0.2&floor=0", nil))
			require.Equal(t, 200, response.Code, response.Body.String())
		})
	}
}

type ratefloorPreviewAccounts struct{ service.AccountRepository }

func (ratefloorPreviewAccounts) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]service.Account, error) {
	return []service.Account{}, nil
}
