//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type collabTurnStateUpstream struct {
	state   string
	fail    bool
	headers http.Header
}

func (u *collabTurnStateUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.headers = req.Header.Clone()
	payload := map[string]any{"id": "resp_fixture", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "synthetic output"}}}}}
	if u.fail {
		payload["status"] = "failed"
		payload["error"] = map[string]any{"code": "server_error", "message": "synthetic"}
	}
	raw, _ := json.Marshal(payload)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}, "X-Codex-Turn-State": {u.state}}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
}
func (u *collabTurnStateUpstream) DoWithTLS(req *http.Request, p string, a int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, p, a, n)
}

func TestCollabAbandonedOptionalTurnStateProvenance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"ordinary", "withheld_optional", "delivered_optional", "delivered_optional_interrupted"} {
		t.Run(mode, func(t *testing.T) {
			optional := mode != "ordinary"
			delivered := mode == "delivered_optional" || mode == "delivered_optional_interrupted"
			cfg := &config.Config{}
			cfg.JWT.Secret = "synthetic"
			upstream := &collabTurnStateUpstream{}
			gateway := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
			gateway.StopOpenAICodexTicketHarvester()
			a := &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "synthetic", "base_url": "https://upstream.invalid"}}
			b := *a
			b.ID = 2
			raw := replayRequestBody(t, collabSafetyCase(t, "unnamed_task/pass_false/stream_false/attempts_1"))
			newContext := func() *gin.Context {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(raw))
				c.Request.Header.Set("session_id", "synthetic-turn-state")
				c.Set("api_key", &service.APIKey{ID: 7})
				return c
			}
			first := newContext()
			firstCtx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			first.Request = first.Request.WithContext(firstCtx)
			upstream.state = "blob-A"
			_, err := gateway.Forward(firstCtx, first, a, raw)
			require.NoError(t, err)

			failed := newContext()
			parent := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), parent)
			if optional {
				child := resilience.NewCollabChildState(parent)
				ctx = resilience.WithCollabAttempt(resilience.WithRequestState(ctx, child), resilience.CollabAttempt{Optional: true})
				child.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))
			}
			failed.Request = failed.Request.WithContext(ctx)
			upstream.state = "blob-B"
			upstream.fail = !delivered
			if optional {
				rescued := false
				failed.Request = failed.Request.WithContext(resilience.WithRequestState(context.Background(), parent))
				service.InstallDeliveryTracking(failed, time.Now())
				failed.Request = failed.Request.WithContext(ctx)
				withSeam(failed, func(c *gin.Context, seam *collabSeam, _ *collabWithheld) {
					rescued = true
					require.True(t, seam.writer.quiet, "real takeover suspended existing error")
					result, sendErr := gateway.Forward(ctx, c, &b, raw)
					if delivered {
						require.NoError(t, sendErr)
						require.Equal(t, collabSeamPassing, seam.writer.current())
						require.Positive(t, resilience.StateFrom(ctx).DeliveredBytes())
						c.Request = c.Request.WithContext(resilience.WithCollabAttempt(ctx, resilience.CollabAttempt{Optional: true, Delivered: true}))
						// The ladder commits delivered identity even if Forward was interrupted
						// after output, leaving no result for its settlement owner.
						if mode == "delivered_optional_interrupted" {
							result = nil
						}
						if persist := gateway.CommitCollabResponseIdentity(c, &b, result, ""); persist != nil {
							persist()
						}
					} else {
						require.Error(t, sendErr)
						require.True(t, seam.writer.withholding(), "failed optional response stays withheld")
					}
				}, seamTestExistingError(failed))
				require.True(t, rescued, "real seam takeover must execute optional send")
				if delivered {
					require.Equal(t, "blob-B", failed.Writer.Header().Get("x-codex-turn-state"))
				} else {
					assert.NotEqual(t, "blob-B", failed.Writer.Header().Get("x-codex-turn-state"), "optional header must not reach client")
				}
			} else {
				_, err = gateway.Forward(ctx, failed, &b, raw)
				require.Error(t, err)
				require.Equal(t, "blob-B", failed.Writer.Header().Get("x-codex-turn-state"))
			}
			// The next request returns to A with the blob A that the client retained.
			next := newContext()
			next.Request.Header.Set("x-codex-turn-state", "blob-A")
			nextCtx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			next.Request = next.Request.WithContext(nextCtx)
			upstream.state = "blob-A"
			upstream.fail = false
			_, err = gateway.Forward(nextCtx, next, a, raw)
			require.NoError(t, err)
			echo := upstream.headers.Get("x-codex-turn-state")
			t.Logf("optional=%t next_request_turn_state=%t", optional, echo != "")
			if optional && !delivered {
				assert.Equal(t, "blob-A", echo, "withheld optional failure must not overwrite delivered origin")
			} else {
				assert.Empty(t, echo, "ordinary B error delivered its header as today")
			}
		})
	}
}
