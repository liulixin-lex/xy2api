//go:build unit

package handler

import (
	"context"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

func TestCollabFenceRetainedReleasePanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(resilience.WithRequestState(context.Background(), resilience.NewRequestState()))
	seam := &collabSeam{original: c.Writer, lossless: true, losslessDuration: time.Second}
	seam.writer = newCollabSeamWriter(c.Writer, seamTestAlways)
	c.Writer = seam.writer
	seam.rescue = func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
		c.Header("Content-Type", "text/event-stream")
		_, _ = c.Writer.WriteString(seamTestDelta)
		time.Sleep(20 * time.Millisecond)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("release panic reached request: %v", r)
		}
	}()
	before := resilience.CollabSafetySnapshot().ConsumerPanics
	seam.tryLossless(c, func() { panic("synthetic reservation storage failure") })
	require.Equal(t, before+1, resilience.CollabSafetySnapshot().ConsumerPanics)
}

func TestCollabWorkerAcceptancePanic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		before := resilience.CollabSafetySnapshot().ConsumerPanics
		ctx, stop := watchCollabTakeover(context.Background(), time.Second, func() bool { panic("synthetic acceptance callback") }, func(context.Context) bool { return false })
		defer stop()
		time.Sleep(time.Second)
		synctest.Wait()
		require.Error(t, ctx.Err())
		require.Equal(t, before+1, resilience.CollabSafetySnapshot().ConsumerPanics)
	})
}
