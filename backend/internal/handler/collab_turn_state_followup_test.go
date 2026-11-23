//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A borrowed response is the one the client received. The client returns its
// turn-state on the next request, and the gateway must forward it unchanged to
// the account that minted it.
func TestCollabBorrowedTurnStateFollowup(t *testing.T) {
	quietReplayLogs(t)
	settings := resilience.Defaults()
	settings.Collab.Ladder, settings.Collab.LosslessFirst, settings.Collab.RecordRouting, settings.Collab.TreeAffinity = true, true, true, true
	settings.CollaborationPlaintext.Enabled = true
	settings.CollaborationPlaintext.Models = []string{"gpt-5.5"}
	var accounts []int64
	var outbound []string
	fixture := newCollabWiringFixture(t, settings, func(req *http.Request, account int64) (*http.Response, error) {
		accounts = append(accounts, account)
		outbound = append(outbound, req.Header.Get("x-codex-turn-state"))
		response := replayResponse(200, "application/json", `{"id":"resp_sample","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`)
		response.Header.Set("X-Codex-Turn-State", fmt.Sprintf("blob-%d", account))
		return response, nil
	})
	fixture.repo.accounts[0].Extra["openai_collaboration_plaintext"] = false
	second := fixture.repo.accounts[0]
	second.ID, second.Priority = 72, 2
	second.Extra = map[string]any{"openai_collaboration_plaintext": true}
	fixture.repo.accounts = append(fixture.repo.accounts, second)
	declaration := `{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"send_message","parameters":{"type":"object","properties":{"message":{"type":"string","encrypted":true}},"required":["message"],"additionalProperties":false}}]}`
	_, state := fixture.request([]byte(`{"model":"gpt-5.5","tools":[` + declaration + `],"input":[` + replayTask + `,` + replayQuestion + `]}`))
	scope := state.CollabState().Items[0].Key.Scope
	parent := state.CollabState().Thread
	child := fixture.gateway.CollabDigest(resilience.CollabDigestDomainThread, "sample-child")
	require.True(t, fixture.gateway.CollabTreeAffinity().RefreshTreeMembership(context.Background(), resilience.CollabTreeHint{Scope: scope, Thread: child, Parent: parent, AccountID: 71}))
	key := resilience.CollabDeliveryKey{Scope: scope, Thread: child, Item: "item", Delivery: "delivery", Digest: "ciphertext"}
	record := resilience.CollabTerminatedRecord{Key: key, Parent: parent, Event: fixture.gateway.CollabDigest(resilience.CollabDigestDomainEvent, "sample-event"), ExpiresAt: time.Now().Add(time.Hour)}
	_, reserved := fixture.gateway.CollabRecords().ReserveLocalEnding(context.Background(), []resilience.CollabTerminatedRecord{record})
	require.True(t, reserved)
	require.True(t, fixture.gateway.CollabRecords().Terminated(context.Background(), record))
	notice, _ := json.Marshal(map[string]any{"type": "agent_message", "author": "sample-child", "recipient": "sample-thread", "content": []any{map[string]string{"type": "input_text", "text": "Message Type: FINAL_ANSWER\nTask name: task\nSender: sample-child\nPayload:\nGateway notice [sample-event]. Unreadable task."}}})
	rec, _ := fixture.request([]byte(`{"model":"gpt-5.5","tools":[` + declaration + `],"input":[` + string(notice) + `,` + replayQuestion + `]}`))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []int64{71, 72}, accounts, "the notice is served by the borrowed plaintext account")
	require.Equal(t, "blob-72", rec.Header().Get("x-codex-turn-state"))

	fixture.repo.accounts[0].Schedulable = false
	next := collabTurnStateFollowupRequest(fixture, []byte(`{"model":"gpt-5.5","tools":[`+declaration+`],"input":[`+replayQuestion+`]}`), "blob-72")
	require.Equal(t, http.StatusOK, next.Code)
	require.Equal(t, []int64{71, 72, 72}, accounts)
	require.Equal(t, "blob-72", outbound[2], "the borrowed account receives the turn-state it minted")

	counts, err := fixture.handler.concurrencyHelper.concurrencyService.GetAccountConcurrencyBatch(context.Background(), []int64{71, 72})
	require.NoError(t, err)
	require.Zero(t, counts[71])
	require.Zero(t, counts[72])
	require.Len(t, fixture.usage.created, 3)
	require.Empty(t, fixture.usage.unbilled)
}

func collabTurnStateFollowupRequest(f *collabWiringFixture, body []byte, turnState string) *httptest.ResponseRecorder {
	router := gin.New()
	router.Use(middleware.ClientRequestID())
	router.POST("/v1/responses", func(c *gin.Context) {
		group := int64(1)
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1, UserID: 1, GroupID: &group, Group: &service.Group{ID: group, Platform: service.PlatformOpenAI}, User: &service.User{ID: 1}})
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		f.handler.Responses(c)
	})
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "codex_cli_rs/0.170.0")
	req.Header.Set("session_id", "sample-thread")
	req.Header.Set("x-codex-turn-state", turnState)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

type collabPreludeTurnStateUpstream struct {
	state   string
	stream  bool
	fail    bool
	headers http.Header
}

func (u *collabPreludeTurnStateUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.headers = req.Header.Clone()
	header := http.Header{"Content-Type": {"application/json"}}
	if u.state != "" {
		header.Set("x-codex-turn-state", u.state)
	}
	payload := map[string]any{"id": "resp_a", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "synthetic"}}}}}
	raw, _ := json.Marshal(payload)
	if u.stream {
		header.Set("Content-Type", "text/event-stream")
		if u.fail {
			raw = []byte(seamTestCreated + seamTestFailed)
		} else {
			event, _ := json.Marshal(map[string]any{"type": "response.completed", "response": payload})
			raw = []byte(seamTestCreated + seamTestDelta + "event: response.completed\ndata: " + string(event) + "\n\n")
		}
	}
	return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(bytes.NewReader(raw))}, nil
}

func (u *collabPreludeTurnStateUpstream) DoWithTLS(req *http.Request, p string, a int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, p, a, n)
}

// The turn-state origin follows the header the client actually received. When
// an earlier response already sent its headers (and A's turn-state), a takeover
// adds only body output, so A stays the origin whatever B returned.
func TestCollabDeliveredTurnStateFollowsCommittedHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, committed := range []bool{false, true} {
		for _, mode := range []string{"ordinary", "delivered_optional", "withheld_optional"} {
			for _, upstreamHeader := range []bool{false, true} {
				t.Run(fmt.Sprintf("committed_%t/%s/header_%t", committed, mode, upstreamHeader), func(t *testing.T) {
					cfg := &config.Config{}
					cfg.JWT.Secret = "synthetic"
					upstream := &collabPreludeTurnStateUpstream{state: "blob-A"}
					gateway := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
					gateway.StopOpenAICodexTicketHarvester()
					a := &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "synthetic", "base_url": "https://upstream.invalid"}}
					b := *a
					b.ID = 2
					body := replayRequestBody(t, collabSafetyCase(t, "unnamed_task/pass_false/stream_true/attempts_1"))
					plain := replayRequestBody(t, collabSafetyCase(t, "unnamed_task/pass_false/stream_false/attempts_1"))
					newContext := func(raw []byte) (*gin.Context, *httptest.ResponseRecorder) {
						rec := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(rec)
						c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(raw))
						c.Request.Header.Set("session_id", "synthetic-prelude-turn-state")
						c.Set("api_key", &service.APIKey{ID: 7})
						c.Request = c.Request.WithContext(resilience.WithRequestState(context.Background(), resilience.NewRequestState()))
						return c, rec
					}
					first, _ := newContext(plain)
					_, err := gateway.Forward(first.Request.Context(), first, a, plain)
					require.NoError(t, err)

					c, rec := newContext(body)
					service.InstallDeliveryTracking(c, time.Now())
					parentCtx := c.Request.Context()
					child := resilience.NewCollabChildState(resilience.StateFrom(parentCtx))
					ctx := resilience.WithCollabAttempt(resilience.WithRequestState(parentCtx, child), resilience.CollabAttempt{Optional: mode != "ordinary"})
					child.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))
					upstream.stream, upstream.fail, upstream.state = true, mode == "withheld_optional", ""
					if upstreamHeader {
						upstream.state = "blob-B"
					}
					called := false
					withSeam(c, func(c *gin.Context, seam *collabSeam, saved *collabWithheld) {
						called = true
						require.Equal(t, committed, saved.committed)
						c.Request = c.Request.WithContext(ctx)
						result, sendErr := gateway.Forward(ctx, c, &b, body)
						if mode == "withheld_optional" {
							require.Error(t, sendErr)
							require.NotEqual(t, collabSeamPassing, seam.writer.current())
							require.Zero(t, child.DeliveredBytes())
							return
						}
						require.NoError(t, sendErr)
						require.Equal(t, collabSeamPassing, seam.writer.current())
						require.Positive(t, child.DeliveredBytes())
						if mode == "delivered_optional" {
							c.Request = c.Request.WithContext(resilience.WithCollabAttempt(ctx, resilience.CollabAttempt{Optional: true, Delivered: true}))
							if persist := gateway.CommitCollabResponseIdentity(c, &b, result, ""); persist != nil {
								persist()
							}
						}
					}, func() {
						if !committed {
							seamTestExistingError(c)()
							return
						}
						// This prelude is already on the wire when the existing failure is withheld.
						c.Header("Content-Type", "text/event-stream")
						c.Header("x-codex-turn-state", "blob-A")
						_, err := c.Writer.WriteString(seamTestCreated)
						require.NoError(t, err)
						c.Writer.Flush()
						_, err = c.Writer.WriteString(seamTestFailed)
						require.NoError(t, err)
					})
					require.True(t, called)
					wireHeader := rec.Result().Header.Get("x-codex-turn-state")
					if committed {
						require.Equal(t, "blob-A", wireHeader, "the client keeps the header sent first")
					}

					next, _ := newContext(plain)
					next.Request.Header.Set("x-codex-turn-state", "blob-A")
					upstream.stream, upstream.fail, upstream.state = false, false, "blob-A"
					_, err = gateway.Forward(next.Request.Context(), next, a, plain)
					require.NoError(t, err)
					want := "blob-A"
					if !committed && mode != "withheld_optional" && upstreamHeader {
						want = ""
						require.Equal(t, "blob-B", wireHeader)
					}
					assert.Equal(t, want, upstream.headers.Get("x-codex-turn-state"), "the origin matches the header the client received")
				})
			}
		}
	}
}
