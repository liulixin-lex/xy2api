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
	"strings"
	"sync"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/securityaudit"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/internal/testutil"
	"github.com/liulixin-lex/xy2api/internal/testutil/fcorelay"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	fcoE2EOutput = "  fixture\tpage\nwith <markup> & context  "
	fcoE2EUser   = `{ "type":"message", "role":"user", "content":[{"type":"input_text","text":"hello"}], "fixture_number":9007199254740993 }`
)

func fcoE2EItem(name, output string) string {
	return fmt.Sprintf(`{"type":"function_call_output","namespace":"agent","name":%q,"output":%q}`, name, output)
}

func fcoE2EBody(items ...string) string {
	return `{"model":"gpt-5.1","instructions":"fixture","stream":false,"input":[` + strings.Join(items, ",") + `]}`
}

func fcoE2EWrapped(name, output string) string {
	return "<external_" + name + ">" + output + "</external_" + name + ">"
}

// Reuse the scheduling capture's transport and handler. The relay sees the
// actual request body; only its successful SSE envelope needs an output item
// for the production Responses and Chat readers (as in the resilience tests).
type fcoE2EUpstream struct {
	captureUpstream
	accounts []int64
	urls     []string
	replies  [][]byte
}

func newFCOE2EUpstream(t *testing.T, version string, onSend func(int)) *fcoE2EUpstream {
	t.Helper()
	u := &fcoE2EUpstream{}
	relay := fcorelay.NewHandler(version)
	u.reply = func(n int, req *http.Request, body []byte) (*http.Response, error) {
		copyReader, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		copyBody, err := io.ReadAll(copyReader)
		_ = copyReader.Close()
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(body, copyBody) {
			return nil, fmt.Errorf("GetBody differs from the wire body")
		}
		u.urls = append(u.urls, req.URL.String())
		req.Body = io.NopCloser(bytes.NewReader(body))
		rec := httptest.NewRecorder()
		relay.ServeHTTP(rec, req)
		u.replies = append(u.replies, bytes.Clone(rec.Body.Bytes()))
		if onSend != nil {
			onSend(n)
		}
		resp := rec.Result()
		if resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			completed := strings.ReplaceAll(rec.Body.String(), `"status":"completed"}`, `"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture answer"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`)
			resp.Body = io.NopCloser(strings.NewReader(completed))
		}
		return resp, nil
	}
	return u
}

func (u *fcoE2EUpstream) Do(req *http.Request, proxy string, accountID int64, concurrency int) (*http.Response, error) {
	u.accounts = append(u.accounts, accountID)
	return u.captureUpstream.Do(req, proxy, accountID, concurrency)
}

func (u *fcoE2EUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

// Count the real adapter calls, without replacing moderation. Keyword+API
// mode lets a local moderation endpoint observe the actual semantic input
// after a non-matching keyword check. Keyword hits are observed in real logs.
// A keyword-only probe gets the same call and blocks on any vowel, so its log
// excerpt is the text the keyword check inspected.
type fcoE2EAudit struct {
	fcoAuditCapture
	engine      securityaudit.LegacyEngine
	probe       securityaudit.LegacyEngine
	probeLogs   chan service.ContentModerationLog
	mu          sync.Mutex
	inputs      []string
	keywords    []string
	logs        chan service.ContentModerationLog
	beforeCheck func()
}

func (a *fcoE2EAudit) Check(ctx context.Context, req securityaudit.Request) (*securityaudit.LegacyDecision, error) {
	_, _ = a.fcoAuditCapture.Check(ctx, req)
	if a.beforeCheck != nil {
		a.beforeCheck()
	}
	keyword, err := a.probeKeywordText(ctx, req)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.keywords = append(a.keywords, keyword)
	a.mu.Unlock()
	return a.engine.Check(ctx, req)
}

func (a *fcoE2EAudit) probeKeywordText(ctx context.Context, req securityaudit.Request) (string, error) {
	decision, err := a.probe.Check(ctx, req)
	if err != nil || decision == nil || !decision.Blocked {
		return "", err
	}
	select {
	case log := <-a.probeLogs:
		return log.InputExcerpt, nil
	case <-time.After(5 * time.Second):
		return "", fmt.Errorf("keyword probe log was not recorded")
	}
}

func (a *fcoE2EAudit) snapshotKeywords() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.keywords...)
}

func (a *fcoE2EAudit) snapshotInputs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.inputs...)
}

type fcoE2EModerationRepo struct {
	contentModerationHandlerTestRepo
	logs chan service.ContentModerationLog
}

func (r *fcoE2EModerationRepo) CreateLog(_ context.Context, log *service.ContentModerationLog) error {
	r.logs <- *log
	return nil
}

func newFCOE2EAudit(t *testing.T, keyword string) *fcoE2EAudit {
	t.Helper()
	a := &fcoE2EAudit{logs: make(chan service.ContentModerationLog, 16), probeLogs: make(chan service.ContentModerationLog, 16)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode moderation request: %v", err)
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		a.mu.Lock()
		a.inputs = append(a.inputs, payload.Input)
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"flagged":false,"category_scores":{}}]}`)
	}))
	t.Cleanup(server.Close)
	newEngine := func(logs chan service.ContentModerationLog, mode string, keywords ...string) securityaudit.LegacyEngine {
		cfg, err := json.Marshal(service.ContentModerationConfig{
			Enabled: true, Mode: service.ContentModerationModePreBlock,
			BaseURL: server.URL, APIKeys: []string{"sk-fixture"}, Model: "omni-moderation-latest",
			AllGroups: true, SampleRate: 100, BlockStatus: http.StatusForbidden, BlockMessage: "fixture keyword blocked",
			BlockedKeywords: keywords, KeywordBlockingMode: mode,
		})
		require.NoError(t, err)
		settings := &contentModerationHandlerSettingRepo{values: map[string]string{
			service.SettingKeyRiskControlEnabled: "true", service.SettingKeyContentModerationConfig: string(cfg),
		}}
		svc := service.NewContentModerationService(settings, &fcoE2EModerationRepo{logs: logs}, nil, nil, nil, nil, nil, nil)
		return securityaudit.NewLegacyModerationAdapter(svc)
	}
	a.engine = newEngine(a.logs, service.ContentModerationKeywordModeKeywordAndAPI, keyword)
	a.probe = newEngine(a.probeLogs, service.ContentModerationKeywordModeKeywordOnly, "a", "e", "i", "o", "u")
	return a
}

func newFCOE2EFixture(t *testing.T, mode, version, keyword string, account service.Account, onSend func(int), options ...captureOptions) (*gin.Engine, *fcoE2EUpstream, *fcoE2EAudit) {
	t.Helper()
	withHandlerResilience(t, func(s *resilience.Settings) {
		s.Enabled = true
		s.FCOCompat.Mode = mode
	})
	if account.Credentials == nil {
		account.Credentials = map[string]any{"api_key": "sk-fixture", "base_url": "https://relay.example.test"}
	}
	up := newFCOE2EUpstream(t, version, onSend)
	audit := newFCOE2EAudit(t, keyword)
	h, _, router := captureFixture(t, account, up, options...)
	h.securityAuditCoordinator = securityaudit.NewCoordinator(audit, nil)
	return router, up, audit
}

func requireFCOE2ESuccess(t *testing.T, rec *httptest.ResponseRecorder, chat bool) {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	path := "output.0.content.0.text"
	if chat {
		path = "choices.0.message.content"
	} else {
		require.Equal(t, "completed", gjson.GetBytes(rec.Body.Bytes(), "status").String())
	}
	require.Equal(t, "fixture answer", gjson.GetBytes(rec.Body.Bytes(), path).String(), rec.Body.String())
}

func requireFCOE2ERewrite(t *testing.T, before, after []byte, index int, name, output string) {
	t.Helper()
	path := fmt.Sprintf("input.%d", index)
	oldItem, newItem := gjson.GetBytes(before, path), gjson.GetBytes(after, path)
	require.Equal(t, "function_call_output", oldItem.Get("type").String())
	require.Equal(t, "message", newItem.Get("type").String())
	require.Equal(t, "user", newItem.Get("role").String())
	require.Equal(t, "input_text", newItem.Get("content.0.type").String())
	require.Equal(t, fcoE2EWrapped(name, output), newItem.Get("content.0.text").String())
	// Build the byte oracle from the observed replacement, not the production
	// normalizer: every byte outside that one item must survive the replay.
	want := append([]byte(nil), before[:oldItem.Index]...)
	want = append(want, []byte(newItem.Raw)...)
	want = append(want, before[oldItem.Index+len(oldItem.Raw):]...)
	require.Equal(t, want, after, "only the standalone item may change")
}

// requireFCOE2EAudit checks the one moderation call of a request: the text the
// keyword check inspected, and what the semantic endpoint received (nothing
// means it was not called).
func requireFCOE2EAudit(t *testing.T, audit *fcoE2EAudit, keyword string, semantic ...string) {
	t.Helper()
	require.Len(t, audit.snapshotCalls(), 1, "one real moderation call per HTTP request")
	require.Equal(t, []string{strings.Join(strings.Fields(keyword), " ")}, audit.snapshotKeywords())
	require.Equal(t, semantic, audit.snapshotInputs())
}

func TestFCOE2EReactiveAuditsOnce(t *testing.T) {
	router, up, audit := newFCOE2EFixture(t, resilience.FCOCompatModeReactive, fcorelay.VersionV0220, "never_matches", service.Account{}, nil)
	body := fcoE2EBody(fcoE2EUser, fcoE2EItem("browser_info", fcoE2EOutput))
	rec := captureRequest(router, "/v1/responses", body)
	requireFCOE2ESuccess(t, rec, false)
	require.Len(t, up.bodies, 2)
	require.Equal(t, []byte(body), up.bodies[0])
	requireFCOE2ERewrite(t, up.bodies[0], up.bodies[1], 1, "browser_info", fcoE2EOutput)
	require.Equal(t, up.accounts[0], up.accounts[1], "replay stays on the selected account")
	requireFCOE2EAudit(t, audit, fcoE2EWrapped("browser_info", fcoE2EOutput))
}

func TestFCOE2EKeywordBlocksBeforeSendAndSkipsHistory(t *testing.T) {
	for _, history := range []bool{false, true} {
		t.Run(fmt.Sprintf("history=%t", history), func(t *testing.T) {
			router, up, audit := newFCOE2EFixture(t, resilience.FCOCompatModeReactive, fcorelay.VersionV0220, "forbidden_marker", service.Account{}, nil)
			items := []string{fcoE2EUser, fcoE2EItem("browser_info", "page forbidden_marker")}
			if history {
				items = append(items, `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"earlier answer"}]}`, `{"role":"user","content":"new question"}`)
			}
			rec := captureRequest(router, "/v1/responses", fcoE2EBody(items...))
			if history {
				requireFCOE2ESuccess(t, rec, false)
				require.Len(t, up.bodies, 2)
				requireFCOE2ERewrite(t, up.bodies[0], up.bodies[1], 1, "browser_info", "page forbidden_marker")
				requireFCOE2EAudit(t, audit, "new question", "new question")
				return
			}
			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			require.Equal(t, "content_policy_violation", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
			require.Equal(t, "fixture keyword blocked", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
			require.Empty(t, up.bodies)
			requireFCOE2EAudit(t, audit, fcoE2EWrapped("browser_info", "page forbidden_marker"))
			select {
			case log := <-audit.logs:
				require.Equal(t, service.ContentModerationActionKeywordBlock, log.Action)
				require.Equal(t, fcoE2EWrapped("browser_info", "page forbidden_marker"), log.InputExcerpt)
			case <-time.After(5 * time.Second):
				t.Fatal("keyword audit log was not recorded")
			}
		})
	}
}

func TestFCOE2EProactive(t *testing.T) {
	for _, tc := range []struct{ version, name string }{
		{fcorelay.VersionV0220, "browser_info"}, {fcorelay.VersionV0224, "terminal_info"},
	} {
		t.Run(tc.version+"/"+tc.name, func(t *testing.T) {
			router, up, audit := newFCOE2EFixture(t, resilience.FCOCompatModeProactive, tc.version, "never_matches", service.Account{}, nil)
			body := fcoE2EBody(fcoE2EUser, fcoE2EItem(tc.name, fcoE2EOutput))
			requireFCOE2ESuccess(t, captureRequest(router, "/v1/responses", body), false)
			require.Len(t, up.bodies, 1)
			requireFCOE2ERewrite(t, []byte(body), up.bodies[0], 1, tc.name, fcoE2EOutput)
			requireFCOE2EAudit(t, audit, fcoE2EWrapped(tc.name, fcoE2EOutput))
		})
	}
}

func TestFCOE2EOffPreservesWireAndError(t *testing.T) {
	router, up, audit := newFCOE2EFixture(t, resilience.FCOCompatModeOff, fcorelay.VersionV0220, "fixture", service.Account{}, nil)
	body := fcoE2EBody(fcoE2EUser, fcoE2EItem("browser_info", fcoE2EOutput))
	rec := captureRequest(router, "/v1/responses", body)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Len(t, up.bodies, 1)
	require.Equal(t, []byte(body), up.bodies[0])
	// Off keeps the pre-existing behaviour: the gateway rebuilds the client
	// error from the upstream fields, so compare the fields, not whitespace.
	require.JSONEq(t, string(up.replies[0]), rec.Body.String(), "off returns the upstream error unchanged, without the new error code")
	require.Contains(t, rec.Body.String(), "function_call_output requires call_id on HTTP requests")
	require.False(t, gjson.GetBytes(rec.Body.Bytes(), "error.code").Exists())
	requireFCOE2EAudit(t, audit, "")
}

func TestFCOE2EUnsupportedName(t *testing.T) {
	router, up, audit := newFCOE2EFixture(t, resilience.FCOCompatModeReactive, fcorelay.VersionV0220, "never_matches", service.Account{}, nil)
	body := fcoE2EBody(fcoE2EUser, fcoE2EItem("zz_unknown", fcoE2EOutput))
	rec := captureRequest(router, "/v1/responses", body)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Len(t, up.bodies, 1)
	require.Equal(t, []byte(body), up.bodies[0])
	require.Equal(t, "upstream_standalone_tool_output_unsupported", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
	require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.False(t, gjson.GetBytes(rec.Body.Bytes(), "error.param").Exists())
	requireFCOE2EAudit(t, audit, "")
}

func TestFCOE2EAdminNames(t *testing.T) {
	for _, mode := range []string{resilience.FCOCompatModeReactive, resilience.FCOCompatModeProactive} {
		t.Run(mode, func(t *testing.T) {
			router, up, audit := newFCOE2EFixture(t, mode, fcorelay.VersionV0220, "never_matches", service.Account{}, nil)
			settings := resilience.Current().Clone()
			settings.FCOCompat.AdminNames = []string{"zz_unknown"}
			resilience.Publish(settings)
			body := fcoE2EBody(fcoE2EUser, fcoE2EItem("zz_unknown", fcoE2EOutput))
			requireFCOE2ESuccess(t, captureRequest(router, "/v1/responses", body), false)
			wantCalls := 1
			if mode == resilience.FCOCompatModeReactive {
				wantCalls = 2
				require.Equal(t, []byte(body), up.bodies[0])
			}
			require.Len(t, up.bodies, wantCalls)
			requireFCOE2ERewrite(t, []byte(body), up.bodies[wantCalls-1], 1, "zz_unknown", fcoE2EOutput)
			requireFCOE2EAudit(t, audit, fcoE2EWrapped("zz_unknown", fcoE2EOutput))
		})
	}
}

func TestFCOE2EAccountOverride(t *testing.T) {
	for _, mode := range []string{resilience.FCOCompatModeReactive, resilience.FCOCompatModeOff} {
		t.Run(mode, func(t *testing.T) {
			account := service.Account{Extra: map[string]any{"openai_fco_compat_mode": resilience.FCOCompatModeProactive}}
			router, up, audit := newFCOE2EFixture(t, mode, fcorelay.VersionV0220, "never_matches", account, nil)
			body := fcoE2EBody(fcoE2EUser, fcoE2EItem("browser_info", fcoE2EOutput))
			rec := captureRequest(router, "/v1/responses", body)
			require.Len(t, up.bodies, 1)
			if mode == resilience.FCOCompatModeOff {
				require.Equal(t, http.StatusBadRequest, rec.Code)
				require.Equal(t, []byte(body), up.bodies[0])
				require.JSONEq(t, string(up.replies[0]), rec.Body.String())
				requireFCOE2EAudit(t, audit, "")
				return
			}
			requireFCOE2ESuccess(t, rec, false)
			requireFCOE2ERewrite(t, []byte(body), up.bodies[0], 1, "browser_info", fcoE2EOutput)
			requireFCOE2EAudit(t, audit, fcoE2EWrapped("browser_info", fcoE2EOutput))
		})
	}
}

func TestFCOE2EGatingExclusions(t *testing.T) {
	for _, mode := range []string{resilience.FCOCompatModeReactive, resilience.FCOCompatModeProactive} {
		for _, kind := range []string{"oauth", "official"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				account := service.Account{
					ID: 9901, Name: "fixture", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
					Status: service.StatusActive, Schedulable: true, Concurrency: 2,
					Credentials: map[string]any{"api_key": "sk-fixture", "base_url": "https://api.openai.com/v1"},
				}
				if kind == "oauth" {
					account.Type = service.AccountTypeOAuth
					account.Credentials = map[string]any{"access_token": "fixture-token"}
				}
				router, up, audit := newFCOE2EFixture(t, mode, fcorelay.VersionV0220, "never_matches", account, nil,
					captureOptions{repo: &openAIWSUsageHandlerAccountRepoStub{account: account}})
				body := fcoE2EBody(fcoE2EUser, fcoE2EItem("browser_info", fcoE2EOutput))
				rec := captureRequest(router, "/v1/responses", body)
				require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
				require.Len(t, up.bodies, 1)
				// OAuth's existing request conversion sorts object members.
				require.JSONEq(t, gjson.Get(body, "input.1").Raw, gjson.GetBytes(up.bodies[0], "input.1").Raw)
				require.NotContains(t, rec.Body.String(), "upstream_standalone_tool_output_unsupported")
				require.Contains(t, rec.Body.String(), "function_call_output requires call_id on HTTP requests")
				requireFCOE2EAudit(t, audit, fcoE2EWrapped("browser_info", fcoE2EOutput))
			})
		}
	}
}

func TestFCOE2EDuplicateIngressMember(t *testing.T) {
	for _, mode := range []string{resilience.FCOCompatModeOff, resilience.FCOCompatModeReactive, resilience.FCOCompatModeProactive} {
		t.Run(mode, func(t *testing.T) {
			router, up, audit := newFCOE2EFixture(t, mode, fcorelay.VersionV0220, "fixture", service.Account{}, nil)
			body := `{"metadata":{"fixture":"first","fixture":"second"},` + fcoE2EBody(fcoE2EUser, fcoE2EItem("browser_info", fcoE2EOutput))[1:]
			rec := captureRequest(router, "/v1/responses", body)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Len(t, up.bodies, 1)
			require.Equal(t, gjson.Get(body, "input").Raw, gjson.GetBytes(up.bodies[0], "input").Raw)
			requireFCOE2EAudit(t, audit, "")
		})
	}
}

func TestFCOE2EIngressSnapshotSurvivesReload(t *testing.T) {
	for _, change := range []string{"clear names", "disable mode"} {
		t.Run(change, func(t *testing.T) {
			router, up, audit := newFCOE2EFixture(t, resilience.FCOCompatModeReactive, fcorelay.VersionV0220, "never_matches", service.Account{}, func(n int) {
				if n == 1 {
					next := resilience.Current().Clone()
					if change == "clear names" {
						next.FCOCompat.AdminNames = nil
					} else {
						next.FCOCompat.Mode = resilience.FCOCompatModeOff
					}
					resilience.Publish(next)
				}
			})
			settings := resilience.Current().Clone()
			settings.FCOCompat.AdminNames = []string{"zz_unknown"}
			resilience.Publish(settings)
			body := fcoE2EBody(fcoE2EUser, fcoE2EItem("zz_unknown", fcoE2EOutput))
			requireFCOE2ESuccess(t, captureRequest(router, "/v1/responses", body), false)
			require.Len(t, up.bodies, 2)
			require.Equal(t, []byte(body), up.bodies[0])
			requireFCOE2ERewrite(t, up.bodies[0], up.bodies[1], 1, "zz_unknown", fcoE2EOutput)
			requireFCOE2EAudit(t, audit, fcoE2EWrapped("zz_unknown", fcoE2EOutput))
		})
	}
}

func TestFCOE2EIngressSnapshotModeSurvivesReloadBeforeForward(t *testing.T) {
	t.Run("proactive snapshot remains enabled", func(t *testing.T) {
		router, up, audit := newFCOE2EFixture(t, resilience.FCOCompatModeProactive, fcorelay.VersionV0220, "never_matches", service.Account{}, nil)
		audit.beforeCheck = func() {
			next := resilience.Current().Clone()
			next.FCOCompat.Mode = resilience.FCOCompatModeOff
			resilience.Publish(next)
		}
		body := fcoE2EBody(fcoE2EUser, fcoE2EItem("browser_info", fcoE2EOutput))
		requireFCOE2ESuccess(t, captureRequest(router, "/v1/responses", body), false)
		require.Len(t, up.bodies, 1)
		requireFCOE2ERewrite(t, []byte(body), up.bodies[0], 1, "browser_info", fcoE2EOutput)
	})
	t.Run("off snapshot remains disabled", func(t *testing.T) {
		router, up, audit := newFCOE2EFixture(t, resilience.FCOCompatModeOff, fcorelay.VersionV0220, "never_matches", service.Account{}, nil)
		audit.beforeCheck = func() {
			next := resilience.Current().Clone()
			next.FCOCompat.Mode = resilience.FCOCompatModeProactive
			resilience.Publish(next)
		}
		body := fcoE2EBody(fcoE2EUser, fcoE2EItem("browser_info", fcoE2EOutput))
		rec := captureRequest(router, "/v1/responses", body)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		require.Len(t, up.bodies, 1)
		require.Equal(t, []byte(body), up.bodies[0])
		require.NotContains(t, rec.Body.String(), "upstream_standalone_tool_output_unsupported")
	})
}

func TestFCOE2EChatNativeInput(t *testing.T) {
	router, up, audit := newFCOE2EFixture(t, resilience.FCOCompatModeReactive, fcorelay.VersionV0220, "never_matches", service.Account{}, nil)
	body := fcoE2EBody(fcoE2EUser, fcoE2EItem("browser_info", fcoE2EOutput))
	requireFCOE2ESuccess(t, captureRequest(router, "/v1/chat/completions", body), true)
	require.Len(t, up.bodies, 2)
	require.Equal(t, gjson.Get(body, "input").Raw, gjson.GetBytes(up.bodies[0], "input").Raw)
	requireFCOE2ERewrite(t, up.bodies[0], up.bodies[1], 1, "browser_info", fcoE2EOutput)
	require.Equal(t, []string{"https://relay.example.test/v1/responses", "https://relay.example.test/v1/responses"}, up.urls)
	requireFCOE2EAudit(t, audit, fcoE2EWrapped("browser_info", fcoE2EOutput))
}

func TestFCOE2EWSHTTPBridgeTurnReplay(t *testing.T) {
	done := make(chan struct{})
	account := service.Account{Extra: map[string]any{
		"openai_apikey_responses_websockets_v2_enabled": true,
		"openai_apikey_responses_websockets_v2_mode":    service.OpenAIWSIngressModeHTTPBridge,
	}}
	router, up, audit := newFCOE2EFixture(t, resilience.FCOCompatModeReactive, fcorelay.VersionV0220, "never_matches", account, nil,
		captureOptions{requestDone: done, cache: testutil.NewRedisGatewayCache(t), configure: func(cfg *config.Config) {
			cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
		}})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = conn.CloseNow()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("websocket handler did not exit")
		}
	})
	turn := func(frame string) string {
		t.Helper()
		require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(frame)))
		for {
			_, event, readErr := conn.Read(ctx)
			require.NoError(t, readErr)
			require.NotEqual(t, "error", gjson.GetBytes(event, "type").String(), string(event))
			if gjson.GetBytes(event, "type").String() == "response.completed" {
				require.Equal(t, "fixture answer", gjson.GetBytes(event, "response.output.0.content.0.text").String())
				return gjson.GetBytes(event, "response.id").String()
			}
		}
	}
	first := `{"type":"response.create",` + fcoE2EBody(fcoE2EUser, fcoE2EItem("browser_info", fcoE2EOutput))[1:]
	responseID := turn(first)
	require.NotEmpty(t, responseID)
	turn(fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"input":[{"type":"message","role":"user","content":"follow-up question"}]}`, responseID))
	require.NoError(t, conn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("websocket handler did not finish")
	}
	// Reading captures after handler completion also synchronizes the transport
	// recorder; no sleeps or polling are needed to inspect either turn.
	require.Len(t, up.bodies, 4, "each turn has an independent same-account replay")
	for _, pair := range [][2]int{{0, 1}, {2, 3}} {
		requireFCOE2ERewrite(t, up.bodies[pair[0]], up.bodies[pair[1]], 1, "browser_info", fcoE2EOutput)
		require.Equal(t, up.accounts[0], up.accounts[pair[1]])
	}
	require.False(t, gjson.GetBytes(up.bodies[2], "previous_response_id").Exists())
	require.Len(t, gjson.GetBytes(up.bodies[2], "input").Array(), 3)
	require.Equal(t, "follow-up question", gjson.GetBytes(up.bodies[2], "input.2.content").String())
	require.Len(t, audit.snapshotCalls(), 2)
	require.Equal(t, []string{"first_turn", "subsequent_turn"}, []string{audit.snapshotCalls()[0].stage, audit.snapshotCalls()[1].stage})
	require.Equal(t, []string{strings.Join(strings.Fields(fcoE2EWrapped("browser_info", fcoE2EOutput)), " "), "follow-up question"}, audit.snapshotKeywords())
	require.Equal(t, []string{"follow-up question"}, audit.snapshotInputs())
}

// The semantic endpoint receives the user's words only, and in a tool loop the
// keyword check inspects just the items that are new in the request.
func TestFCOE2EAuditWindow(t *testing.T) {
	const (
		call   = `{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{}"}`
		result = `{"type":"function_call_output","call_id":"call_1","output":"tool result"}`
	)
	page, late := fcoE2EItem("browser_info", "turn page"), fcoE2EItem("terminal_info", "late term")
	for _, tc := range []struct {
		name     string
		items    []string
		keyword  string
		semantic []string
	}{
		{"context before the user message", []string{page, fcoE2EUser}, "hello " + fcoE2EWrapped("browser_info", "turn page"), []string{"hello"}},
		{"continuation", []string{page, fcoE2EUser, call, result}, "", nil},
		{"continuation with a new item", []string{page, fcoE2EUser, call, result, late}, fcoE2EWrapped("terminal_info", "late term"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, _, audit := newFCOE2EFixture(t, resilience.FCOCompatModeReactive, fcorelay.VersionV0224, "never_matches", service.Account{}, nil)
			requireFCOE2ESuccess(t, captureRequest(router, "/v1/responses", fcoE2EBody(tc.items...)), false)
			requireFCOE2EAudit(t, audit, tc.keyword, tc.semantic...)
		})
	}
}
