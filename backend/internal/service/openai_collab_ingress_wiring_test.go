//go:build unit

package service

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabWiringAuxiliaryRequestState(t *testing.T) {
	old := resilience.Current().Clone()
	t.Cleanup(func() { resilience.Publish(old) })
	for _, ladder := range []bool{false, true} {
		settings := resilience.Defaults()
		settings.Collab.Ladder = ladder
		resilience.Publish(settings)
		for _, kind := range []string{"ordinary", "reasoning", "compaction", "escaped_reasoning"} {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			body := []byte(`{"input":[{"role":"user","content":"hello"}]}`)
			if kind != "ordinary" {
				body = []byte(`{"input":[{"type":"` + kind + `","id":"sample-item","encrypted_content":"sample-cipher"}]}`)
			}
			if kind == "escaped_reasoning" {
				body = []byte(strings.NewReplacer("escaped_reasoning", "reasoning", "encrypted_content", `encr\u0079pted_content`).Replace(string(body)))
			}
			request, writer := c.Request, c.Writer
			BeginOpenAICollabRequest(c, body, 1, 1)
			state := openAICollabRequestFrom(c.Request.Context())
			if ladder && kind != "ordinary" {
				require.NotNil(t, state, kind)
				require.NotNil(t, state.observation, kind)
			} else {
				require.Nil(t, state, kind)
				require.Same(t, request, c.Request, kind)
				require.Same(t, writer, c.Writer, kind)
			}
		}
	}
}

func BenchmarkCollabWiringIngress(b *testing.B) {
	old := resilience.Current().Clone()
	b.Cleanup(func() { resilience.Publish(old) })
	settings := resilience.Defaults()
	resilience.Publish(settings)
	gin.SetMode(gin.ReleaseMode)
	for _, kind := range []string{"ordinary", "reasoning", "compaction"} {
		b.Run(kind, func(b *testing.B) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			body := []byte(`{"input":[{"role":"user","content":"hello"}]}`)
			if kind != "ordinary" {
				body = []byte(`{"input":[{"type":"` + kind + `","id":"sample-item","encrypted_content":"sample-cipher"}]}`)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				BeginOpenAICollabRequest(c, body, 1, 1)
			}
		})
	}
}
