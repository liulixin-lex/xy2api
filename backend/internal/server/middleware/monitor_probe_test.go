package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/monitorprobe"
)

func runMonitorProbe(t *testing.T, header string) (probe bool, leaked string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(MonitorProbe())
	r.GET("/x", func(c *gin.Context) {
		probe = monitorprobe.IsProbe(c.Request.Context())
		leaked = c.Request.Header.Get(monitorprobe.HeaderName)
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if header != "" {
		req.Header.Set(monitorprobe.HeaderName, header)
	}
	r.ServeHTTP(httptest.NewRecorder(), req)
	return probe, leaked
}

func TestMonitorProbeMarksSignedRequest(t *testing.T) {
	probe, leaked := runMonitorProbe(t, monitorprobe.Sign(5, time.Now()))
	if !probe || leaked != "" {
		t.Fatalf("probe=%v leaked=%q; want true and empty", probe, leaked)
	}
}

func TestMonitorProbeIgnoresForgedHeaderButStripsIt(t *testing.T) {
	probe, leaked := runMonitorProbe(t, "v1.5.1800000000.deadbeef")
	if probe || leaked != "" {
		t.Fatalf("probe=%v leaked=%q; want false and empty", probe, leaked)
	}
}

func TestMonitorProbeWithoutHeader(t *testing.T) {
	if probe, _ := runMonitorProbe(t, ""); probe {
		t.Fatal("request without header marked as probe")
	}
}
