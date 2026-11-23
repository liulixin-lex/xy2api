//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type collabWiringDeliveryProbe struct {
	resilience.NoopCollabDeliveryConsumer
	prepared, delivered, completed []resilience.CollabDeliveryEvent
	panicPhase                     bool
}

func (p *collabWiringDeliveryProbe) Received(context.Context, resilience.CollabDeliveryEvent) {
	if p.panicPhase {
		panic("synthetic consumer")
	}
}
func (p *collabWiringDeliveryProbe) AboutToWrite(_ context.Context, e resilience.CollabDeliveryEvent) {
	if p.panicPhase {
		panic("synthetic consumer")
	}
	p.prepared = append(p.prepared, e)
}
func (p *collabWiringDeliveryProbe) Written(_ context.Context, e resilience.CollabDeliveryEvent) {
	if p.panicPhase {
		panic("synthetic consumer")
	}
	p.delivered = append(p.delivered, e)
}
func (p *collabWiringDeliveryProbe) Completed(_ context.Context, e resilience.CollabDeliveryEvent) {
	if p.panicPhase {
		panic("synthetic consumer")
	}
	p.completed = append(p.completed, e)
}

type collabWiringFaultWriter struct {
	gin.ResponseWriter
	writeErr, flushErr error
}

func (w *collabWiringFaultWriter) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.ResponseWriter.Write(p)
}
func (w *collabWiringFaultWriter) FlushError() error {
	if w.flushErr != nil {
		return w.flushErr
	}
	w.ResponseWriter.Flush()
	return nil
}

func TestCollabWiringFinalDeliveryFailures(t *testing.T) {
	const payload = `{"type":"response.output_item.done","item":{"type":"function_call","namespace":"collaboration","name":"send_message","arguments":"{}"}}`
	frame := "event: response.output_item.done\ndata: " + payload + "\n\n"
	for _, failure := range []string{"none", "write", "flush", "cancel"} {
		for split := 1; split < len(frame); split++ {
			ctx, cancel := context.WithCancel(context.Background())
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
			fault := &collabWiringFaultWriter{ResponseWriter: c.Writer}
			c.Writer = fault
			BeginOpenAICollabRequest(c, []byte(`{"tools":[{"type":"namespace","name":"collaboration","tools":[]}]}`), 1, 1)
			request := openAICollabRequestFrom(c.Request.Context())
			probe := &collabWiringDeliveryProbe{}
			request.state.DeliveryHooks.Register(&collabWiringDeliveryProbe{panicPhase: true})
			request.state.DeliveryHooks.Register(probe)
			beginOpenAICollabResponse(c.Request.Context())
			c.Header("Content-Type", "text/event-stream")
			_, err := c.Writer.Write([]byte(frame[:split]))
			require.NoError(t, err)
			if failure == "write" {
				fault.writeErr = io.ErrClosedPipe
			}
			_, err = c.Writer.Write([]byte(frame[split:]))
			if failure == "write" {
				require.ErrorIs(t, err, io.ErrClosedPipe)
			} else {
				require.NoError(t, err)
			}
			require.Empty(t, probe.delivered, "buffered writes are not flushed delivery")
			if failure == "flush" {
				fault.flushErr = errors.New("synthetic flush")
			}
			if failure == "cancel" {
				cancel()
			}
			c.Writer.Flush()
			FinishOpenAICollabDelivery(c)
			FinishOpenAICollabDelivery(c)
			require.Len(t, probe.completed, 1)
			if failure == "none" {
				require.Len(t, probe.delivered, 1)
				require.Equal(t, payload, probe.delivered[0].Payload)
				require.Equal(t, frame, rec.Body.String())
			} else {
				require.Empty(t, probe.delivered)
			}
			cancel()
		}
	}
}

func TestCollabWiringLocalGenerationIsNotExecutable(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	BeginOpenAICollabRequest(c, []byte(`{"tools":[{"type":"namespace","name":"collaboration","tools":[]}]}`), 1, 1)
	request := openAICollabRequestFrom(c.Request.Context())
	probe := &collabWiringDeliveryProbe{}
	request.state.DeliveryHooks.Register(probe)
	beginOpenAICollabResponse(c.Request.Context())
	BeginOpenAICollabLocalResponse(c.Request.Context())
	c.Header("Content-Type", "application/json")
	_, err := c.Writer.WriteString(`{"status":"completed","output":[]}`)
	require.NoError(t, err)
	FinishOpenAICollabDelivery(c)
	require.Len(t, probe.delivered, 1)
	require.False(t, probe.delivered[0].Executable)
	require.Equal(t, resilience.CollabGeneration(2), probe.delivered[0].Generation)
	require.Len(t, probe.completed, 2)
}

func BenchmarkCollabWiringOrdinaryIngress(b *testing.B) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	body := []byte(`{"model":"sample","input":[{"role":"user","content":"` + strings.Repeat("ordinary text ", 100) + `"}]}`)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		BeginOpenAICollabRequest(c, body, 1, 1)
	}
}
