//go:build unit

package admin

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProfitPreviewHandler(t *testing.T) {
	for _, tc := range []struct {
		query   string
		status  int
		ceiling float64
		passing int
	}{
		{"10?margin=0.25", 200, 1.5, 1}, {"10?margin=0.75", 200, 0.5, 0}, {"10", 200, 1.6, 1},
		{"10?margin=NaN", 400, 0, 0}, {"10?margin=Inf", 400, 0, 0}, {"10?margin=-0.1", 400, 0, 0},
		{"10?margin=1", 400, 0, 0}, {"10?margin=abc", 400, 0, 0}, {"bad?margin=0.2", 400, 0, 0}, {"0", 400, 0, 0}, {"99", 404, 0, 0},
	} {
		t.Run(tc.query, func(t *testing.T) {
			h := NewProfitPreviewHandler(service.NewProfitPreviewService(profitPreviewAccounts{}, profitPreviewGroups{}))
			router := gin.New()
			router.GET("/groups/:id", h.ProfitControlPreview)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", "/groups/"+tc.query, nil))
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status == 200 {
				var body struct {
					Data service.ProfitControlPreview `json:"data"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				require.Equal(t, 1, body.Data.Members)
				require.Equal(t, 1, body.Data.Schedulable)
				require.Equal(t, tc.passing, body.Data.Passing)
				require.InDelta(t, tc.ceiling, body.Data.Ceiling, 1e-12)
			}
		})
	}
}
