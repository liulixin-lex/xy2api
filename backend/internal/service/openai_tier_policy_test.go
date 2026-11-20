//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func explicitTierGroup(fast, ultra string) *Group {
	return &Group{ID: 4, Platform: PlatformOpenAI, Hydrated: true, Status: StatusActive, OpenAITierPolicyVersion: OpenAITierPolicyExplicit, OpenAIFastMode: fast, OpenAIUltrafastMode: ultra}
}

func TestOpenAITierGroupMatrix(t *testing.T) {
	const i, on, off = OpenAITierModeInherit, OpenAITierModeForceOn, OpenAITierModeForceOff
	inputs := []string{"", "null", `"default"`, `"auto"`, `"scale"`, `"flex"`, `"fast"`, `"priority"`, `"ultrafast"`}
	rows := []struct {
		fast, ultra string
		want        []string
	}{
		{i, i, []string{"", "", "default", "auto", "scale", "flex", "priority", "priority", "ultrafast"}},
		{off, i, []string{"default", "default", "default", "default", "default", "flex", "default", "default", "ultrafast"}},
		{i, off, []string{"default", "default", "default", "default", "default", "flex", "priority", "priority", "default"}},
		{off, off, []string{"default", "default", "default", "default", "default", "flex", "default", "default", "default"}},
		{on, i, nil}, {on, off, nil}, {i, on, nil}, {off, on, nil},
	}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	s := &OpenAIGatewayService{}
	for _, row := range rows {
		ctx := context.WithValue(context.Background(), ctxkey.Group, explicitTierGroup(row.fast, row.ultra))
		for n, input := range inputs {
			frame := `{"type":"response.create"}`
			if input != "" {
				frame = `{"type":"response.create","service_tier":` + input + `}`
			}
			want := "priority"
			if row.want != nil {
				want = row.want[n]
			} else if row.ultra == on {
				want = "ultrafast"
			}
			body, err := s.applyOpenAIFastPolicyToBody(ctx, account, "gpt-6.2-sol", []byte(frame))
			require.NoError(t, err)
			require.Equal(t, want, gjson.GetBytes(body, "service_tier").String(), "%s/%s/%s", row.fast, row.ultra, input)
			wire, blocked, err := s.applyOpenAIFastPolicyToWSResponseCreate(ctx, account, "gpt-6.2-sol", []byte(frame))
			require.NoError(t, err)
			require.Nil(t, blocked)
			require.Equal(t, body, wire)
		}
		for _, bad := range []string{`""`, `"typo"`, "1", "true", "{}", "[]"} {
			_, err := s.applyOpenAIFastPolicyToBody(ctx, account, "gpt-6.2-sol", []byte(`{"service_tier":`+bad+`}`))
			var invalid *ErrInvalidOpenAIServiceTier
			require.ErrorAs(t, err, &invalid)
		}
	}
}

func TestOpenAITierGroupFallbackPolicyAndAccountEvidence(t *testing.T) {
	origin := explicitTierGroup("inherit", "force_off")
	serving := explicitTierGroup("inherit", "force_on")
	serving.ID = 2
	ctx := WithBillingAPIKeySnapshot(context.WithValue(context.Background(), ctxkey.Group, serving), &APIKey{Group: origin})
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"plan_type": "pro"}}
	s := &OpenAIGatewayService{}
	_, err := s.resolveOpenAITier(ctx, account, "gpt-6.2-sol", []byte("{}"))
	var unavailable *UpstreamFailoverError
	require.ErrorAs(t, err, &unavailable)
	require.Equal(t, GatewayFailureReason(openAITierRouteConflict), unavailable.Reason)
	ctx = context.WithValue(context.Background(), ctxkey.Group, serving)
	_, err = s.resolveOpenAITier(ctx, account, "gpt-6.2-sol", []byte("{}"))
	require.ErrorAs(t, err, &unavailable)
	require.Equal(t, GatewayFailureReason(openAITierUnavailable), unavailable.Reason)
	manifest := UpstreamModelMetadataSnapshot{SyncedAt: time.Now().UTC().Format(time.RFC3339), Models: map[string]UpstreamModelMetadata{"gpt-6.2-sol": {TierCredentialFingerprint: capabilityFingerprint(account), CodexToolCapabilities: map[string]json.RawMessage{"service_tiers": json.RawMessage(`[{"id":"ultrafast"}]`)}}}}
	account.SetUpstreamModelMetadataSnapshot(manifest)
	d, err := s.resolveOpenAITier(ctx, account, "gpt-6.2-sol", []byte("{}"))
	require.NoError(t, err)
	require.Equal(t, "account_manifest", d.CapabilitySource)
	manifest.SyncedAt = time.Now().Add(-2 * 24 * time.Hour).UTC().Format(time.RFC3339)
	account.SetUpstreamModelMetadataSnapshot(manifest)
	_, err = s.resolveOpenAITier(ctx, account, "gpt-6.2-sol", []byte("{}"))
	require.ErrorAs(t, err, &unavailable)
}

func TestOpenAITierForwardRealBody(t *testing.T) {
	for _, tc := range []struct {
		path                 string
		passthrough, rawChat bool
		body                 string
	}{
		{"/v1/responses", false, false, `{"model":"gpt-6.2-sol","input":"hi","service_tier":"fast"}`},
		{"/v1/responses", true, false, `{"model":"gpt-6.2-sol","input":"hi","service_tier":"fast"}`},
		{"/v1/responses/compact", false, false, `{"model":"gpt-6.2-sol","input":[{"role":"user","content":"hi"}],"service_tier":"fast"}`},
		{"/v1/responses", false, true, `{"model":"gpt-6.2-sol","input":"hi","service_tier":"fast"}`},
		{"/v1/chat/completions", false, false, `{"model":"gpt-6.2-sol","messages":[{"role":"user","content":"hi"}],"service_tier":"fast"}`},
		{"/v1/chat/completions", false, true, `{"model":"gpt-6.2-sol","messages":[{"role":"user","content":"hi"}],"service_tier":"fast"}`},
		{"/v1/messages", false, false, `{"model":"gpt-6.2-sol","messages":[{"role":"user","content":"hi"}],"max_tokens":12}`},
		{"/v1/messages", false, true, `{"model":"gpt-6.2-sol","messages":[{"role":"user","content":"hi"}],"max_tokens":12}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"test fixture stop"}}`))}}
			s := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: up}
			a := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{"api_key": "test"}, Extra: map[string]any{"openai_responses_supported": !tc.rawChat, "openai_passthrough": tc.passthrough}}
			body := []byte(tc.body)
			ctx := context.WithValue(context.Background(), ctxkey.Group, explicitTierGroup("inherit", "force_on"))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(body)).WithContext(ctx)
			var err error
			switch tc.path {
			case "/v1/messages":
				_, err = s.ForwardAsAnthropic(ctx, c, a, body, "", "")
			case "/v1/chat/completions":
				_, err = s.ForwardAsChatCompletions(ctx, c, a, body, "", "")
			default:
				_, err = s.Forward(ctx, c, a, body)
			}
			require.Error(t, err)
			require.NotEmpty(t, up.lastBody, "%v", err)
			require.Equal(t, "ultrafast", gjson.GetBytes(up.lastBody, "service_tier").String())
		})
	}
}

func TestOpenAITierDeclaresRawResponseEvidence(t *testing.T) {
	o := &upstreamResponseModelObserver{}
	o.ObserveOpenAI([]byte(`{"type":"response.created","response":{"id":"r","model":"gpt-6","service_tier":"default"}}`), "")
	o.ObserveOpenAI([]byte(`{"type":"response.completed","response":{"id":"r","service_tier":"ultrafast"},"service_tier":"vendor_unknown"}`), "")
	r := &OpenAIForwardResult{}
	o.attachTierEvidence(r)
	require.True(t, r.ResponseTierConflict)
	require.Empty(t, r.UpstreamResponseServiceTier)
	require.Equal(t, []string{"ultrafast", "vendor_unknown"}, r.ResponseTierEvidence)
}

func TestOpenAITierCatalogScopesMappedTextAndClearsUnavailableDefaults(t *testing.T) {
	ctx := context.Background()
	strict := explicitTierGroup("inherit", "force_on")
	body := []byte(`{"models":[{"slug":"gpt-image-1","default_service_tier":"original"},{"slug":"gpt-audio","default_service_tier":"original"},{"slug":"gpt-alias","default_service_tier":"original"},{"slug":"gpt-6.2-sol","service_tiers":[{"id":"ultrafast"}],"default_service_tier":"ultrafast"}]}`)
	account := Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-alias": "other-vendor-model"}}}
	limited, err := restrictCodexTierCatalog(ctx, nil, body, strict, []Account{account}, nil, true)
	require.NoError(t, err)
	require.Equal(t, []any{"gpt-image-1", "gpt-audio", "gpt-alias"}, gjson.GetBytes(limited, "models.#.slug").Value())
	require.Equal(t, []any{"original", "original", "original"}, gjson.GetBytes(limited, "models.#.default_service_tier").Value())
	follow, err := restrictCodexTierCatalog(ctx, nil, body, explicitTierGroup("inherit", "inherit"), nil, nil, true)
	require.NoError(t, err)
	require.Equal(t, "null", gjson.GetBytes(follow, "models.3.default_service_tier").Raw)
	require.Empty(t, gjson.GetBytes(follow, "models.3.service_tiers").Array())
}

type openAITierTestDialer struct {
	openAIWSQueueDialer
	hints chan string
}

func (d *openAITierTestDialer) Dial(ctx context.Context, url string, headers http.Header, proxy string) (openAIWSClientConn, int, http.Header, error) {
	d.hints <- headers.Get(openAICodexRoutingHintHeader)
	return d.openAIWSQueueDialer.Dial(ctx, url, headers, proxy)
}

func TestOpenAITierWSRouteChangesPerTurn(t *testing.T) {
	for _, mode := range []string{OpenAIWSIngressModePassthrough, OpenAIWSIngressModeCtxPool} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			first, second := newStagedPassthroughConn(), newStagedPassthroughConn()
			cfg := passthroughLifecycleConfig()
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 5
			svc := newPassthroughLifecycleService(cfg, first)
			dialer := &openAITierTestDialer{openAIWSQueueDialer: openAIWSQueueDialer{conns: []openAIWSClientConn{&gpt61ValidationWSConn{first}, &gpt61ValidationWSConn{second}}}, hints: make(chan string, 8)}
			svc.openaiWSPassthroughDialer = dialer
			if mode == OpenAIWSIngressModeCtxPool {
				pool := newOpenAIWSConnPool(cfg)
				pool.setClientDialerForTest(dialer)
				svc.openaiWSPool = pool
				defer pool.Close()
			}
			acct := passthroughLifecycleAccount()
			acct.Type = AccountTypeOAuth
			acct.Extra["openai_oauth_responses_websockets_v2_mode"] = mode
			acct.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{SyncedAt: time.Now().UTC().Format(time.RFC3339), Models: map[string]UpstreamModelMetadata{"gpt-6.2-sol": {TierCredentialFingerprint: capabilityFingerprint(acct), CodexToolCapabilities: map[string]json.RawMessage{"service_tiers": json.RawMessage(`[{"id":"ultrafast"}]`)}}}})
			turns := make(chan *OpenAIForwardResult, 4)
			server, serverErr := startPassthroughLifecycleServerWithHooks(t, ctx, svc, acct, func(*gin.Context) *OpenAIWSIngressHooks {
				return &OpenAIWSIngressHooks{PolicyContext: func(ctx context.Context, turn int) context.Context {
					mode := OpenAITierModeForceOn
					if turn > 1 {
						mode = OpenAITierModeForceOff
					}
					return context.WithValue(ctx, ctxkey.Group, explicitTierGroup("inherit", mode))
				}, AfterTurn: func(_ int, result *OpenAIForwardResult, _ error) { turns <- result }}
			})
			defer server.Close()
			client := dialPassthroughLifecycleClientWithPayload(t, server, `{"type":"response.create","model":"gpt-6.2-sol","input":"hello"}`)
			defer func() { _ = client.CloseNow() }()
			for i, upstream := range []*stagedPassthroughConn{first, second} {
				if i == 1 {
					err := client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","previous_response_id":"resp_tier_0","input":"next"}`))
					require.NoError(t, err)
				}
				var wire []byte
				select {
				case wire = <-upstream.writes:
				case err := <-serverErr:
					t.Fatalf("turn %d ended before write: %v", i+1, err)
				case <-time.After(3 * time.Second):
					t.Fatalf("turn %d missing upstream frame", i+1)
				}
				tier := []string{"ultrafast", "default"}[i]
				require.Equal(t, tier, gjson.GetBytes(wire, "service_tier").String())
				require.Equal(t, []string{"model=gpt-6.2-sol;tier=ultrafast", "model=gpt-6.2-sol"}[i], <-dialer.hints)
				if i == 1 {
					if mode == OpenAIWSIngressModeCtxPool {
						// With store=false the existing pool replays complete input when
						// a change of request settings starts a fresh upstream chain.
						require.False(t, gjson.GetBytes(wire, "previous_response_id").Exists())
						require.Contains(t, gjson.GetBytes(wire, "input").Raw, "hello")
						require.Contains(t, gjson.GetBytes(wire, "input").Raw, "next")
					} else {
						require.Equal(t, "resp_tier_0", gjson.GetBytes(wire, "previous_response_id").String())
					}
					require.Empty(t, first.writes)
				}
				upstream.Send(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_tier_%d","model":"gpt-6.2-sol","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`, i))
				frame, err := readPassthroughLifecycleFrame(t, client, 2*time.Second)
				require.NoError(t, err)
				require.Equal(t, "response.completed", gjson.GetBytes(frame, "type").String())
				select {
				case result := <-turns:
					require.NotNil(t, result)
					require.NotNil(t, result.TierDecision)
					require.Equal(t, tier, result.TierDecision.OutboundTier)
					require.Equal(t, "missing", result.TierDecision.ClientPresence)
				case <-time.After(2 * time.Second):
					t.Fatal("missing turn usage")
				}
			}
		})
	}
}

func TestOpenAITierTerminalRefusalOnlyMarksSentTier(t *testing.T) {
	ctx := context.Background()
	account := capabilityTestAccount(1)
	repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
	s := &OpenAIGatewayService{accountRepo: repo}
	wire := []byte(`{"model":"gpt-6.2-sol","service_tier":"ultrafast","stream":true}`)
	observe := func(events ...string) {
		o := beginUpstreamResponseModelObservation(nil)
		s.bindOpenAITierRefusal(ctx, o, account, "gpt-6.2-sol", "ultrafast", APIProtocolResponses)
		for _, event := range events {
			o.ObserveOpenAI([]byte(event), "")
		}
		o.finishTierRefusal()
	}
	const refusal = `{"type":"response.failed","response":{"status":"failed","id":"resp_1","error":{"code":"model_unsupported","param":"service_tier","message":"service_tier ultrafast is not supported"}}}`
	const success = `{"type":"response.completed","response":{"status":"completed","id":"resp_1"}}`
	observe(refusal, success)
	observe(`{"type":"error","status":429,"error":{"code":"rate_limit_exceeded","param":"service_tier","message":"service_tier ultrafast unsupported"}}`)
	observed, err := repo.GetByID(ctx, 1)
	require.NoError(t, err)
	require.Nil(t, observed.CapabilityMarked("gpt-6.2-sol", "responses", "tier:ultrafast", time.Now()))
	observe(refusal)
	learn := s.capabilityFailureLearner(ctx, account, wire, "responses")
	require.NotNil(t, learn)
	learn(400, []byte(refusal))
	observed, err = repo.GetByID(ctx, 1)
	require.NoError(t, err)
	require.NotNil(t, observed.CapabilityMarked("gpt-6.2-sol", "responses", "tier:ultrafast", time.Now()))
	for _, shape := range []string{"tier:priority", "tier:default", "stream", "*"} {
		require.Nil(t, observed.CapabilityMarked("gpt-6.2-sol", "responses", shape, time.Now()))
	}
	require.Nil(t, observed.CapabilityMarked("gpt-6.2-sol", "chat", "tier:ultrafast", time.Now()))
}

func TestOpenAITierMessagesRejectsInvalidFieldBeforeDispatch(t *testing.T) {
	for _, rawChat := range []bool{false, true} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		ctx := context.WithValue(context.Background(), ctxkey.Group, explicitTierGroup("inherit", "force_on"))
		raw := []byte(`{"model":"gpt-6.2-sol","messages":[{"role":"user","content":"hi"}],"service_tier":false}`)

		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(raw)).WithContext(ctx)
		acct := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}, Extra: map[string]any{"openai_responses_supported": !rawChat}}
		up := &httpUpstreamRecorder{}
		gw := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: up}
		_, err := gw.ForwardAsAnthropic(ctx, c, acct, raw, "", "")
		require.Error(t, err)
		require.Equal(t, 400, rec.Code)
		require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
		require.Empty(t, up.lastBody)
	}
}
