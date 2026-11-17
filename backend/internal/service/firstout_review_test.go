//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service/openai_ws_v2"
	"github.com/stretchr/testify/require"
)

func TestFirstoutWSCommittedFrameRenewsReadDeadline(t *testing.T) {
	for _, opening := range []string{
		`{"type":"response.output_text.delta","delta":""}`,
		`{"type":"response.output_text.annotation.added","annotation":{"type":"url_citation","url":"https://example.invalid"}}`,
	} {
		t.Run(opening, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				up, client := newStagedPassthroughConn(), newStagedPassthroughConn()
				defer up.Close()
				defer client.Close()
				conn := &openAIWSPassthroughFirstOutputFrameConn{inner: up, activeReadTimeout: 3 * time.Second,
					resolveDeadline: func([]byte) openAIWSPassthroughFirstOutputDeadline {
						return openAIWSPassthroughFirstOutputDeadline{startedAt: start, timeout: time.Second}
					}}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan *openai_ws_v2.RelayExit, 1)
				go func() {
					_, exit := openai_ws_v2.Relay(ctx, client, conn, []byte(`{"type":"response.create","model":"fixture"}`), openai_ws_v2.RelayOptions{})
					done <- exit
				}()
				synctest.Wait()
				<-up.writes
				up.Send(opening)
				synctest.Wait()
				require.Equal(t, opening, string(<-client.writes), "relay commits the empty delta/annotation immediately")
				// Each gap exceeds the first-output limit; the sum also exceeds
				// the read-idle limit. An active read must renew on every frame.
				for _, frame := range []string{`{"type":"ping"}`, `{"type":"response.output_text.delta","delta":"answer"}`, `{"type":"response.completed","response":{"status":"completed"}}`} {
					time.Sleep(2 * time.Second)
					up.Send(frame)
					synctest.Wait()
					select {
					case exit := <-done:
						t.Fatalf("relay closed after committed prelude: %+v", exit)
					default:
					}
					require.Equal(t, frame, string(<-client.writes))
				}
				require.False(t, conn.deadlineState().armed)
				cancel()
				<-done
			})
		})
	}
}

// Put this above deliveryWriter to reproduce transport-independent 32 KiB
// slices reaching its bounded parser. Partial failures stop inside the JSON.
type firstoutChunkWriter struct {
	gin.ResponseWriter
	written, failAfter, maxChunk int
}

func (w *firstoutChunkWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		if w.failAfter > 0 && w.written >= w.failAfter {
			return total, io.ErrClosedPipe
		}
		size := min(len(p), 32*1024)
		if w.failAfter > 0 {
			size = min(size, w.failAfter-w.written)
		}
		time.Sleep(time.Millisecond)
		n, err := w.ResponseWriter.Write(p[:size])
		w.maxChunk = max(w.maxChunk, n)
		w.written += n
		total += n
		p = p[n:]
		if err != nil {
			return total, err
		}
		if n < size {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

func (w *firstoutChunkWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func TestFirstoutLargeClassifiedEventDelivery(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		for _, kind := range []string{"encrypted_reasoning", "tool_arguments"} {
			for _, fail := range []bool{false, true} {
				t.Run(fmt.Sprintf("passthrough=%v/%s/fail=%v", passthrough, kind, fail), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						start := time.Now()
						c, wire := compatStreamContext("/v1/responses", `{}`)
						state := resilience.NewRequestState()
						c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
						InstallDeliveryTracking(c, start)
						chunks := &firstoutChunkWriter{ResponseWriter: c.Writer}
						if fail {
							chunks.failAfter = 96 * 1024
						}
						c.Writer = chunks
						account := &Account{ID: 1, Platform: PlatformOpenAI}
						state.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "model"})
						svc := compatStreamService(nil, 1, 0, 0)
						svc.schedAttemptRecorder = NewSchedAttemptRecorderService(&fakeSchedAttemptStore{})
						capture := svc.beginSchedAttempt(c.Request.Context(), account, nil, 0, true)
						handle := capture.attempt.(*schedAttemptHandle)
						reader, writer := io.Pipe()
						defer reader.Close()
						defer writer.Close()
						done := make(chan error, 1)
						go func() {
							_, err := runBlankPlaceholderAttempt(svc, c, account, passthrough, reader, start)
							done <- err
						}()
						settled := false
						defer func() {
							if !settled {
								_ = writer.Close()
								_ = reader.Close()
								<-done // Allow error-frame writes to settle even after Fatal.
							}
						}()
						data := strings.Repeat("x", 128*1024)
						frame := firstoutWire("response.output_item.added", `,"item":{"type":"reasoning","encrypted_content":"`+data+`"}`)
						if kind == "tool_arguments" {
							frame = firstoutWire("response.function_call_arguments.delta", `,"delta":"`+data+`"`)
						}
						time.Sleep(100 * time.Millisecond)
						_, err := io.WriteString(writer, frame)
						require.NoError(t, err)
						// Let all successful/failed chunks settle without needing a
						// later small event to rescue the semantic delivery parser.
						time.Sleep(100 * time.Millisecond)
						synctest.Wait()
						require.Equal(t, 100, *handle.row.TTFOMs)
						require.Nil(t, handle.row.FirstAnswerMs)
						require.Equal(t, 32*1024, chunks.maxChunk)
						first := state.EndToEndFirstOutputMs()
						if fail {
							require.Nil(t, first, "a partially delivered event must not receive an output clock")
							require.Less(t, wire.Body.Len(), len(frame))
						} else {
							require.Equal(t, frame, wire.Body.String())
							require.NotNil(t, first)
							require.Greater(t, *first, int64(100), "use delivery time, not receipt time")
							require.Less(t, *first, int64(200))
						}
						_, err = io.WriteString(writer, firstoutWire("response.output_text.delta", `,"delta":"later answer"`)+firstoutWire("response.completed", `,"response":{"status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
						require.NoError(t, err)
						require.NoError(t, writer.Close())
						result := <-done
						settled = true
						if !fail {
							require.NoError(t, result)
						}
						require.Equal(t, first, state.EndToEndFirstOutputMs(), "later small events cannot shift the first-delivery clock")
					})
				})
			}
		}
	}
}
