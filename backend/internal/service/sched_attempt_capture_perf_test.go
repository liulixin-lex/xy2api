//go:build unit

package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSchedCaptureBeginAcceptsOnlyParsedEffort(t *testing.T) {
	st := resilience.NewRequestState()
	st.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "gpt-5.4-high"})
	ctx := resilience.WithRequestState(context.Background(), st)
	rec := &captureServiceRecorder{}
	svc := &OpenAIGatewayService{schedAttemptRecorder: rec}
	account := &Account{ID: 1, Platform: PlatformOpenAI}
	// The scalar-only signature cannot inspect or reparse a request body.
	var begin func(context.Context, *Account, string, int, bool) *schedAttemptCapture = svc.beginSchedAttempt
	for _, effort := range []string{"", "low", "high", "max"} {
		a := begin(ctx, account, effort, 20<<20, true)
		require.NotNil(t, a)
		in := rec.starts[len(rec.starts)-1]
		require.Equal(t, effort, in.Effort, "including known empty effort; do not infer from the frozen model")
		require.Equal(t, 20<<20, in.BodyBytes)
		a.finish(nil, nil)
	}
}

// Compare the removed scan with the same Begin/Finish and recorder on both
// sides. The bounded fake records descriptors and outcomes; no network or DB.
func BenchmarkSchedCaptureBeginEffort(b *testing.B) {
	for _, size := range []int{512 << 10, 2 << 20, 20 << 20} {
		body := []byte(`{"model":"gpt-5.4","input":"` + strings.Repeat("x", size) + `","reasoning":{"effort":"high"}}`)
		for _, scan := range []bool{false, true} {
			b.Run(fmt.Sprintf("bytes%d/scan%t", size, scan), func(b *testing.B) {
				st := resilience.NewRequestState()
				st.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "gpt-5.4"})
				ctx := resilience.WithRequestState(context.Background(), st)
				rec := &captureServiceRecorder{}
				svc := &OpenAIGatewayService{schedAttemptRecorder: rec}
				account := &Account{ID: 1, Platform: PlatformOpenAI}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					effort := "high"
					if scan {
						effort = optionalStringValue(extractOpenAIReasoningEffortFromBody(body, gjson.GetBytes(body, "model").String()))
					}
					a := svc.beginSchedAttempt(ctx, account, effort, len(body), true)
					a.finish(nil, nil)
					rec.starts = rec.starts[:0]
					rec.samples = rec.samples[:0]
				}
			})
		}
	}
}
