package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestResilienceExplainCapabilityQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		query, protocol string
		stream, invalid bool
	}{
		{},
		{query: "?protocol=responses&stream=false", protocol: "responses"},
		{query: "?protocol=chat&stream=true", protocol: "chat", stream: true},
		{query: "?protocol=responses", invalid: true},
		{query: "?stream=false", invalid: true},
		{query: "?protocol=messages&stream=false", invalid: true},
		{query: "?protocol=responses&stream=0", invalid: true},
		{query: "?protocol=responses&stream=", invalid: true},
		{query: "?protocol=&stream=false", invalid: true},
		{query: "?protocol=responses&protocol=chat&stream=false", invalid: true},
	} {
		t.Run(tc.query, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Params = gin.Params{{Key: "id", Value: "10"}}
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/resilience/groups/10/explain"+tc.query, nil)
			NewResilienceHandler(nil).Explain(c)
			protocol, stream, scoped := resilience.CapabilityRequestFrom(c.Request.Context())
			if tc.invalid {
				require.Equal(t, http.StatusBadRequest, w.Code)
				require.False(t, scoped)
			} else {
				require.Equal(t, http.StatusInternalServerError, w.Code, "a valid query reaches the unconfigured service")
				require.Equal(t, tc.protocol != "", scoped)
				require.Equal(t, tc.protocol, protocol)
				require.Equal(t, tc.stream, stream)
			}
		})
	}
}
