package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/monitorprobe"
)

func runMonitorProbe(t *testing.T, prepare func(*http.Request)) (probe bool, leaked []string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(MonitorProbe())
	r.GET("/x", func(c *gin.Context) {
		probe = monitorprobe.IsProbe(c.Request.Context())
		leaked = c.Request.Header.Values(monitorprobe.HeaderName)
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	prepare(req)
	r.ServeHTTP(httptest.NewRecorder(), req)
	return probe, leaked
}

func TestMonitorProbeMarksRequestSignedForItsOwnKey(t *testing.T) {
	cases := map[string]func(*http.Request, string){
		"bearer":         func(r *http.Request, k string) { r.Header.Set("Authorization", "Bearer "+k) },
		"x-api-key":      func(r *http.Request, k string) { r.Header.Set("x-api-key", k) },
		"x-goog-api-key": func(r *http.Request, k string) { r.Header.Set("x-goog-api-key", k) },
	}
	for name, setKey := range cases {
		probe, leaked := runMonitorProbe(t, func(r *http.Request) {
			setKey(r, "sk-site")
			r.Header.Set(monitorprobe.HeaderName, monitorprobe.Sign(5, "sk-site", time.Now()))
		})
		if !probe || len(leaked) != 0 {
			t.Fatalf("%s: probe=%v leaked=%q; want true and none", name, probe, leaked)
		}
	}
}

func TestMonitorProbeRejectsSignatureForAnotherKey(t *testing.T) {
	probe, leaked := runMonitorProbe(t, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer sk-site")
		r.Header.Set(monitorprobe.HeaderName, monitorprobe.Sign(5, "sk-third-party", time.Now()))
	})
	if probe || len(leaked) != 0 {
		t.Fatalf("probe=%v leaked=%q; want false and none", probe, leaked)
	}
}

func TestMonitorProbeIgnoresForgedHeaderButStripsIt(t *testing.T) {
	probe, leaked := runMonitorProbe(t, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer sk-site")
		r.Header.Set(monitorprobe.HeaderName, "v1.5.1800000000.deadbeef")
	})
	if probe || len(leaked) != 0 {
		t.Fatalf("probe=%v leaked=%q; want false and none", probe, leaked)
	}
}

func TestMonitorProbeStripsEmptyAndRepeatedValues(t *testing.T) {
	probe, leaked := runMonitorProbe(t, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer sk-site")
		r.Header[http.CanonicalHeaderKey(monitorprobe.HeaderName)] = []string{"", monitorprobe.Sign(5, "sk-site", time.Now())}
	})
	if probe || len(leaked) != 0 {
		t.Fatalf("probe=%v leaked=%q; want false and none", probe, leaked)
	}
}

func TestMonitorProbeWithoutHeader(t *testing.T) {
	if probe, _ := runMonitorProbe(t, func(r *http.Request) { r.Header.Set("Authorization", "Bearer sk-site") }); probe {
		t.Fatal("request without header marked as probe")
	}
}
