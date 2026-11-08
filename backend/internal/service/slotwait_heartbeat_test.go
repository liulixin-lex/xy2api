//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func (k *openAICompactSSEKeepalive) beat(generation uint64) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.beatLocked(generation)
}

func TestSlotWaitHeartbeatCoordinatesScheduledBeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newGWWriter()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		stop := StartPreOutputKeepalive(c, PreOutputProtocolChat, 50*time.Millisecond, 100*time.Millisecond)
		defer stop()
		time.Sleep(20 * time.Millisecond)
		handled, committed, err := WriteSlotWaitHeartbeat(c, []byte(":\n\n"), 20*time.Millisecond)
		require.True(t, handled)
		require.True(t, committed)
		require.NoError(t, err)
		handled, committed, err = WriteSlotWaitHeartbeat(c, []byte(":\n\n"), 20*time.Millisecond)
		require.True(t, handled)
		require.True(t, committed)
		require.NoError(t, err)
		require.Equal(t, ":\n\n", w.Body())
		time.Sleep(30 * time.Millisecond)
		synctest.Wait()
		require.Equal(t, ":\n\n", w.Body())
		time.Sleep(70 * time.Millisecond)
		synctest.Wait()
		require.Equal(t, ":\n\n:\n\n", w.Body())
	})
}

func TestSlotWaitHeartbeatShortWriteStopsHeartbeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newGWWriter()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Writer = &keepaliveShortWriter{ResponseWriter: c.Writer}
		stop := StartPreOutputKeepalive(c, PreOutputProtocolChat, time.Second, time.Second)
		defer stop()
		handled, committed, err := WriteSlotWaitHeartbeat(c, []byte(":\n\n"), time.Second)
		require.True(t, handled)
		require.True(t, committed)
		require.ErrorIs(t, err, io.ErrShortWrite)
		require.Equal(t, 1, OpenAICompactKeepaliveAdjustedWrittenSize(c))
		time.Sleep(3 * time.Second)
		synctest.Wait()
		require.Len(t, w.Body(), 1)
	})
}

func TestSlotWaitHeartbeatFutureInitialClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newGWWriter()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		stop := StartPreOutputKeepalive(c, PreOutputProtocolChat, 2*time.Second, time.Second)
		defer stop()
		_, err := writeOpenAIStreamKeepalive(c, c.Writer, []byte(": upstream\n\n"), false)
		require.NoError(t, err)
		handled, committed, err := WriteSlotWaitHeartbeat(c, []byte(":\n\n"), time.Second)
		require.True(t, handled)
		require.True(t, committed)
		require.NoError(t, err)
		require.Equal(t, ": upstream\n\n:\n\n", w.Body())
	})
}
