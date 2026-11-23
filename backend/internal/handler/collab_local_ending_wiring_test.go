//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

const collabLocalRejection = `{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_task"}}`

func collabLocalSettings() resilience.Settings {
	s := resilience.Defaults()
	s.Collab.Ladder = true
	s.Collab.LocalEnding.Mode = resilience.CollabLocalEndingAll
	return s
}

func TestCollabWiringLocalEndingWholeTurn(t *testing.T) {
	quietReplayLogs(t)
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprintf("capacity_%t", large), func(t *testing.T) {
			f := newCollabWiringFixture(t, collabLocalSettings(), func(*http.Request, int64) (*http.Response, error) {
				return replayResponse(400, "application/json", collabLocalRejection), nil
			})
			other := strings.ReplaceAll(strings.ReplaceAll(replayTask, "amsg_task", "amsg_other"), "cipher-task", "cipher-other")
			copiedCall := `{"type":"function_call","id":"item_copy","call_id":"call_copy","namespace":"collaboration","name":"send_message","arguments":"{\"message\":\"cipher-task\"}","encrypted_function_args":["message"]}`
			input := replayTask + `,` + other + `,` + copiedCall
			if large {
				input += `,{"type":"reasoning","id":"rs_sample","encrypted_content":"cipher-reasoning","summary":[]},{"type":"compaction","id":"cmp_sample","encrypted_content":"cipher-compaction"}`
			}
			body := []byte(`{"model":"gpt-5.5","input":[` + input + `]}`)
			var state *resilience.RequestState
			for i := 0; i < 3; i++ {
				state = collabLocalRequest(f, body, false, httptest.NewRecorder())
			}
			metadata, _ := state.ClaimGatewayRequest(false)
			items := state.CollabState().Items
			queries := make([]resilience.CollabRecordQuery, 0, len(items))
			for _, item := range items {
				queries = append(queries, resilience.CollabRecordQuery{Key: item.Key})
			}
			lookup := f.gateway.CollabRecords().LookupBatch(context.Background(), resilience.CollabRecordBatch{Scope: items[0].Key.Scope, Queries: queries, Dispositions: true, Records: true})
			if large {
				// Full-width digests plus two terminal observations per item exceed
				// the disposition partition. Never reserve or emit a partial notice.
				require.Len(t, items, 4)
				require.Equal(t, resilience.RequestFailed, metadata.Result)
				require.Zero(t, lookup.TreeLocalEndings)
				for _, q := range queries {
					require.Empty(t, lookup.Dispositions[q].Terminated)
				}
				return
			}
			require.Len(t, items, 2)
			require.Equal(t, resilience.RequestLocalEnded, metadata.Result)
			require.Equal(t, 1, lookup.TreeLocalEndings)
			event := resilience.CollabDigest("")
			for i, q := range queries {
				records := lookup.Dispositions[q].Terminated
				require.Len(t, records, 1)
				if event == "" {
					event = records[0].Event
				}
				require.Equal(t, event, records[0].Event, "one event reserves the entire turn")
				require.True(t, records[0].ExpiresAt.After(time.Now()))
				if i == 0 {
					require.Len(t, records[0].Copies, 1)
				}
				require.Empty(t, lookup.Records[q].Consumed)
			}
		})
	}
}

func TestCollabWiringLocalEndingExcludedRequests(t *testing.T) {
	quietReplayLogs(t)
	for _, extra := range []string{`,"text":{"format":{"type":"json_schema","name":"sample","schema":{"type":"object"}}}`, `,"previous_response_id":"resp_previous"`, `,"conversation":"conversation_sample"`} {
		t.Run(extra, func(t *testing.T) {
			f := newCollabWiringFixture(t, collabLocalSettings(), func(*http.Request, int64) (*http.Response, error) {
				return replayResponse(400, "application/json", collabLocalRejection), nil
			})
			for i := 0; i < 3; i++ {
				rec := httptest.NewRecorder()
				collabLocalRequest(f, []byte(`{"model":"gpt-5.5","input":[`+replayTask+`]`+extra+`}`), false, rec)
				require.NotContains(t, rec.Body.String(), "Gateway notice [")
			}
		})
	}
}

func collabLocalRequest(f *collabWiringFixture, body []byte, child bool, w http.ResponseWriter, contexts ...context.Context) *resilience.RequestState {
	router := gin.New()
	router.Use(middleware.ClientRequestID())
	var state *resilience.RequestState
	router.POST("/v1/responses", func(c *gin.Context) {
		group := int64(1)
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1, UserID: 1, GroupID: &group, Group: &service.Group{ID: group, Platform: service.PlatformOpenAI}, User: &service.User{ID: 1}})
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		f.handler.Responses(c)
		state = resilience.StateFrom(c.Request.Context())
	})
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
	if len(contexts) > 0 {
		req = req.WithContext(contexts[0])
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "codex_cli_rs/0.170.0")
	req.Header.Set("session_id", "sample-thread")
	if child {
		req.Header.Set("x-codex-turn-metadata", `{"thread_id":"sample-thread","parent_thread_id":"sample-parent","root_thread_id":"sample-parent"}`)
	}
	router.ServeHTTP(w, req)
	return state
}

type collabLocalFaultWriter struct {
	*httptest.ResponseRecorder
	mode                  string
	fail, writes, flushes int
	cancel                context.CancelFunc
}

func (w *collabLocalFaultWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.fail {
		switch w.mode {
		case "write":
			return 0, errors.New("synthetic local write failure")
		case "short":
			n, _ := w.ResponseRecorder.Write(p[:len(p)/2])
			return n, nil
		}
	}
	return w.ResponseRecorder.Write(p)
}

func (w *collabLocalFaultWriter) Flush() {
	w.flushes++
	if w.mode == "flush" && w.flushes == w.fail {
		w.cancel()
		return
	}
	w.ResponseRecorder.Flush()
}

func TestCollabWiringLocalEndingWriteFailures(t *testing.T) {
	quietReplayLogs(t)
	for _, stream := range []bool{false, true} {
		for _, child := range []bool{false, true} {
			for _, mode := range []string{"write", "short", "flush"} {
				frames := 1
				if stream {
					frames = 8
				}
				for pos := 1; pos <= frames; pos++ {
					t.Run(fmt.Sprintf("stream_%t/child_%t/%s/%d", stream, child, mode, pos), func(t *testing.T) {
						f := newCollabWiringFixture(t, collabLocalSettings(), func(*http.Request, int64) (*http.Response, error) {
							return replayResponse(400, "application/json", collabLocalRejection), nil
						})
						body := []byte(fmt.Sprintf(`{"model":"gpt-5.5","stream":%t,"input":[%s]}`, stream, replayTask))
						for i := 0; i < 2; i++ {
							collabLocalRequest(f, body, child, httptest.NewRecorder())
						}
						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()
						w := &collabLocalFaultWriter{ResponseRecorder: httptest.NewRecorder(), mode: mode, fail: pos, cancel: cancel}
						state := collabLocalRequest(f, body, child, w, ctx)
						meta, _ := state.ClaimGatewayRequest(false)
						require.Equal(t, resilience.RequestLocalUndeliverable, meta.Result)
						require.Equal(t, pos, w.writes, "stop at the first failed handoff")
						require.NotContains(t, w.Body.String(), "server_error")
						require.Empty(t, f.usage.created)
						var obs resilience.CollabObservation
						require.NoError(t, json.Unmarshal(meta.CollabObservation, &obs))
						require.Empty(t, obs.Events, "a failed notice cannot publish its event")
						key := state.CollabState().Items[0].Key
						query := resilience.CollabRecordQuery{Key: key}
						lookup := f.gateway.CollabRecords().LookupBatch(context.Background(), resilience.CollabRecordBatch{Scope: key.Scope, Queries: []resilience.CollabRecordQuery{query}, Records: true, Dispositions: true})
						require.Equal(t, 1, lookup.TreeLocalEndings, "failed notices keep their reservation")
						require.Empty(t, lookup.Records[query].Consumed)
					})
				}
			}
		}
	}
}

func TestCollabWiringLocalEndingReceiptReuse(t *testing.T) {
	quietReplayLogs(t)
	var sent []byte
	failing := true
	f := newCollabWiringFixture(t, collabLocalSettings(), func(req *http.Request, _ int64) (*http.Response, error) {
		sent, _ = io.ReadAll(req.Body)
		if failing {
			return replayResponse(400, "application/json", collabLocalRejection), nil
		}
		return replayResponse(200, "application/json", `{"id":"resp_sample","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`), nil
	})
	body := []byte(`{"model":"gpt-5.5","input":[` + replayTask + `]}`)
	var last *httptest.ResponseRecorder
	for i := 0; i < 3; i++ {
		last = httptest.NewRecorder()
		collabLocalRequest(f, body, true, last)
	}
	event := regexp.MustCompile(`cle_[a-f0-9]{32}`).FindString(last.Body.String())
	require.NotEmpty(t, event)
	failing = false
	settings := collabLocalSettings()
	settings.Collab.LocalEnding.Mode = resilience.CollabLocalEndingOff
	settings.Collab.Records = false
	resilience.Publish(settings)
	for _, receipt := range []string{"", event, "", "cle_" + strings.Repeat("0", 32)} {
		input := replayTask
		if receipt != "" {
			input += `,{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Gateway notice [` + receipt + `]."}]}`
		}
		collabLocalRequest(f, []byte(`{"model":"gpt-5.5","input":[`+input+`]}`), true, httptest.NewRecorder())
		if receipt == event {
			require.NotContains(t, string(sent), "cipher-task")
			require.Contains(t, string(sent), "already processed")
		} else {
			require.Contains(t, string(sent), "cipher-task")
		}
	}
	newItem := strings.Replace(replayTask, "amsg_task", "amsg_new_task", 1)
	collabLocalRequest(f, []byte(`{"model":"gpt-5.5","input":[`+newItem+`,{"role":"assistant","content":"Gateway notice [`+event+`]."}]}`), true, httptest.NewRecorder())
	require.Contains(t, string(sent), "cipher-task", "same ciphertext with a new item identity is a new delivery")
}

func TestCollabWiringLocalEnding(t *testing.T) {
	quietReplayLogs(t)
	for _, stream := range []bool{false, true} {
		for _, child := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream_%t/child_%t", stream, child), func(t *testing.T) {
				f := newCollabWiringFixture(t, collabLocalSettings(), func(*http.Request, int64) (*http.Response, error) {
					return replayResponse(400, "application/json", collabLocalRejection), nil
				})
				body := []byte(fmt.Sprintf(`{"model":"gpt-5.5","stream":%t,"input":[%s]}`, stream, replayTask))
				var local *resilience.RequestState
				for turn := 0; turn < 3; turn++ {
					rec := httptest.NewRecorder()
					local = collabLocalRequest(f, body, child, rec)
					meta, _ := local.ClaimGatewayRequest(false)
					if turn < 2 {
						require.NotContains(t, rec.Body.String(), "Gateway notice [")
						continue
					}
					require.Contains(t, rec.Body.String(), "Gateway notice [")
					require.NotContains(t, rec.Body.String(), `"usage"`)
					require.Equal(t, resilience.RequestLocalEnded, meta.Result)
					var obs resilience.CollabObservation
					require.NoError(t, json.Unmarshal(meta.CollabObservation, &obs))
					require.Equal(t, resilience.CollabFinalModeLocal, obs.FinalMode)
					require.Equal(t, 1, obs.LocalCount)
					require.Len(t, obs.Events, 1)
				}
				require.Empty(t, f.usage.created)
				require.Len(t, f.usage.unbilled, 3)
			})
		}
	}
}
