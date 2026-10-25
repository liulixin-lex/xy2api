//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// l17Upstream returns a fresh response per call so the same fixture can be
// replayed by AllocsPerRun; recording is optional for the same reason.
type l17Upstream struct {
	statuses []int
	status   int
	body     string
	errBody  string
	record   bool
	reqs     []*http.Request
	bodies   [][]byte
}

func (u *l17Upstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	b, _ := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if u.record {
		u.reqs = append(u.reqs, req)
		u.bodies = append(u.bodies, b)
	}
	status := u.status
	if len(u.statuses) > 0 {
		status, u.statuses = u.statuses[0], u.statuses[1:]
	}
	body := u.body
	if status >= 400 {
		body = u.errBody
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func (u *l17Upstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

const (
	l17ChatResponse    = `{"id":"chatcmpl","object":"chat.completion","model":"grok-4.3","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2}}`
	l17CompactResponse = `{"id":"resp_grok_1","object":"response","status":"completed","model":"grok-4.5","output":[{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"grok-encrypted-state"},{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"summary text"}]}],"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14}}`
	l17RejectResponse  = `{"error":{"message":"Argument not supported: prompt_cache_key","type":"invalid_request_error","param":"prompt_cache_key"}}`
	l17APIKeyID        = 5101
)

func l17APIKeyAccount(id int64) *Account {
	return &Account{ID: id, Name: "grok-key", Platform: PlatformGrok, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "api-key"}}
}

func l17FreeOAuthAccount(id int64) *Account {
	account := healthyGrokOAuthGatewayTestAccount(id, "access-token")
	account.Credentials["subscription_tier"] = "free"
	return account
}

type l17State struct {
	name             string
	l17              bool
	kill             bool
	masterOff        bool
	stale            bool
	chat, compact    bool
	accounts         []int64
	l01              bool
	expectChat       bool
	expectCompact    bool
	rejectedAccounts []int64
}

// l17Snapshot publishes one real policy snapshot for the given state.
func l17Snapshot(t testing.TB, st l17State) *CachePolicySnapshot {
	t.Helper()
	repo := newPolicyTestStore()
	repo.rows[cachePolicySettings[0]] = fmt.Sprintf(`{"rev":1,"spec":{"enabled":%t}}`, !st.masterOff)
	ids := "[]"
	if len(st.accounts) > 0 {
		ids = strings.Join(strings.Fields(fmt.Sprint(st.accounts)), ",")
	}
	l01 := ""
	if st.l01 {
		l01 = `"mode":"uuid_fill","rollout_percent":100,`
	}
	repo.rows[cachePolicySettings[3]] = fmt.Sprintf(`{"rev":3,"spec":{%s"grok":{"chat_body_key":%t,"compact_identity":%t,"account_ids":%s}}}`, l01, st.chat, st.compact, ids)
	cfg := policyTestConfig()
	cfg.Gateway.CacheFeatures.KillSwitch = st.kill
	cfg.Gateway.CachePolicy.MaxStaleSeconds = 60
	clock := &cachePolicyStaleClock{now: time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC)}
	options := []CachePolicyOption{WithCachePolicyClock(clock.Now)}
	if st.l17 {
		options = append(options, WithCacheL17())
	}
	s := NewCachePolicyService(repo, cfg, options...)
	require.NoError(t, s.Refresh(context.Background()))
	if st.stale {
		setPolicyTestReadErr(repo, fmt.Errorf("settings unavailable"))
		clock.Advance(61 * time.Second)
		require.Error(t, s.Refresh(context.Background()))
		require.True(t, s.Snapshot().behaviorStale())
	}
	return s.Snapshot()
}

func l17On(name string) l17State {
	return l17State{name: name, l17: true, chat: true, compact: true, accounts: []int64{61, 62, 63}, expectChat: true, expectCompact: true}
}

// Every disabled state must equal the no-snapshot baseline arm byte for byte.
func l17OffStates() []l17State {
	kill := l17On("kill")
	kill.kill = true
	master := l17On("master_off")
	master.masterOff = true
	stale := l17On("stale")
	stale.stale = true
	noCap := l17On("l17_false")
	noCap.l17 = false
	empty := l17On("account_ids_empty")
	empty.accounts = nil
	scope := l17On("account_not_listed")
	scope.accounts = []int64{999}
	switches := l17On("switches_false")
	switches.chat, switches.compact = false, false
	rejected := l17On("account_rejected")
	rejected.rejectedAccounts = []int64{61, 62, 63}
	off := []l17State{kill, master, stale, noCap, empty, scope, switches, rejected}
	for i := range off {
		off[i].expectChat, off[i].expectCompact = false, false
	}
	return off
}

type l17Run struct {
	status int
	out    string
	err    error
	req    *http.Request
	body   []byte
}

func l17Context(path string, body []byte, apiKeyID int64, p *CachePolicySnapshot) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if apiKeyID > 0 {
		c.Set("api_key", &APIKey{ID: apiKeyID, Group: &Group{Platform: PlatformGrok}})
	}
	// The baseline arm stores a typed nil, so both arms perform identical
	// gin key writes and differ only in the published policy.
	c.Set(cachePolicySnapshotGinKey, p)
	return c, recorder
}

func l17Service(account *Account, upstream *l17Upstream) *OpenAIGatewayService {
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}}
	return &OpenAIGatewayService{httpUpstream: upstream, grokTokenProvider: NewGrokTokenProvider(repo, nil), accountRepo: repo}
}

func l17RawChat(t testing.TB, svc *OpenAIGatewayService, upstream *l17Upstream, account *Account, body []byte, apiKeyID int64, p *CachePolicySnapshot) l17Run {
	c, recorder := l17Context("/v1/chat/completions", body, apiKeyID, p)
	_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	run := l17Run{status: recorder.Code, out: recorder.Body.String(), err: err}
	if upstream.record && len(upstream.reqs) > 0 {
		run.req, run.body = upstream.reqs[len(upstream.reqs)-1], upstream.bodies[len(upstream.bodies)-1]
	}
	return run
}

func l17Compact(t testing.TB, svc *OpenAIGatewayService, upstream *l17Upstream, account *Account, body []byte, apiKeyID int64, p *CachePolicySnapshot) l17Run {
	c, recorder := l17Context("/v1/responses/compact", body, apiKeyID, p)
	_, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok", false, time.Now())
	run := l17Run{status: recorder.Code, out: recorder.Body.String(), err: err}
	if upstream.record && len(upstream.reqs) > 0 {
		run.req, run.body = upstream.reqs[len(upstream.reqs)-1], upstream.bodies[len(upstream.bodies)-1]
	}
	return run
}

// Raw chat body with a client key that the gateway must never forward. OAuth
// accounts use stop to take the raw xAI Chat fallback instead of the bridge.
var l17ChatBody = []byte(`{"model":"grok","messages":[{"role":"system","content":"stable prefix"},{"role":"user","content":"hi"}],"stream":false,"stop":"done","prompt_cache_key":"raw-client-cache-key"}`)

var l17CompactBodies = map[string][]byte{
	"no_tools":   []byte(`{"model":"grok","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"prompt_cache_key":"raw-client-key"}`),
	"with_tools": []byte(`{"model":"grok","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],"prompt_cache_key":"raw-client-key"}`),
}

func l17Accounts() map[string]func(int64) *Account {
	return map[string]func(int64) *Account{"apikey": l17APIKeyAccount, "oauth_free": l17FreeOAuthAccount}
}

// Compact responses carry a fresh random compaction item id per call.
var l17CompactionID = regexp.MustCompile(`cmp_[0-9a-f]{32}`)

func l17Expect(t *testing.T, base, got l17Run) {
	t.Helper()
	require.NoError(t, base.err)
	require.NoError(t, got.err)
	require.Equal(t, base.status, got.status)
	require.Equal(t, l17CompactionID.ReplaceAllString(base.out, "cmp_"), l17CompactionID.ReplaceAllString(got.out, "cmp_"))
}

func l17ApplyRejections(svc *OpenAIGatewayService, st l17State) {
	for _, id := range st.rejectedAccounts {
		svc.grokIdentityRuntime.reject(id, time.Now())
	}
}

// T-a: raw xAI Chat carries the scoped identity in the body only when on.
func TestGrokL17ChatBodyKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for kind, build := range l17Accounts() {
		for i, st := range append([]l17State{l17On("on")}, l17OffStates()...) {
			t.Run(kind+"/"+st.name, func(t *testing.T) {
				account := build(int64(61 + i%3))
				p := l17Snapshot(t, st)
				baseUp := &l17Upstream{status: 200, body: l17ChatResponse, record: true}
				base := l17RawChat(t, l17Service(account, baseUp), baseUp, account, l17ChatBody, l17APIKeyID, nil)
				up := &l17Upstream{status: 200, body: l17ChatResponse, record: true}
				svc := l17Service(account, up)
				l17ApplyRejections(svc, st)
				before := GrokIdentityFixAppliedTotal("chat_body_key")
				got := l17RawChat(t, svc, up, account, l17ChatBody, l17APIKeyID, p)
				l17Expect(t, base, got)
				require.True(t, strings.HasSuffix(got.req.URL.Path, "/chat/completions"), got.req.URL.Path)
				header := got.req.Header.Get(grokConversationIDHeader)
				require.NotEmpty(t, header)
				require.Equal(t, base.req.Header, got.req.Header, "headers never change")
				require.False(t, gjson.GetBytes(base.body, "prompt_cache_key").Exists())
				if !st.expectChat {
					require.Equal(t, base.body, got.body, "disabled state must be byte-identical")
					require.Equal(t, before, GrokIdentityFixAppliedTotal("chat_body_key"))
					return
				}
				model := strings.ToLower(gjson.GetBytes(got.body, "model").String())
				want := generateSessionUUID(fmt.Sprintf("grok-prompt-cache:v1:%d:%s:%s", l17APIKeyID, model, "raw-client-cache-key"))
				require.Equal(t, want, gjson.GetBytes(got.body, "prompt_cache_key").String())
				require.Equal(t, header, gjson.GetBytes(got.body, "prompt_cache_key").String())
				require.NotContains(t, string(got.body), "raw-client-cache-key")
				stripped, err := sjson.DeleteBytes(got.body, "prompt_cache_key")
				require.NoError(t, err)
				require.JSONEq(t, string(base.body), string(stripped), "only prompt_cache_key is added")
				require.Equal(t, before+1, GrokIdentityFixAppliedTotal("chat_body_key"))
			})
		}
	}
}

// An empty identity (no downstream API key) is still removed when on.
func TestGrokL17ChatBodyKeyEmptyIdentityStillStrips(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := l17APIKeyAccount(61)
	up := &l17Upstream{status: 200, body: l17ChatResponse, record: true}
	got := l17RawChat(t, l17Service(account, up), up, account, l17ChatBody, 0, l17Snapshot(t, l17On("on")))
	require.NoError(t, got.err)
	require.False(t, gjson.GetBytes(got.body, "prompt_cache_key").Exists())
	require.Empty(t, got.req.Header.Get(grokConversationIDHeader))
}

// T-b: compact carries the same identity in body and header, and never gains
// native tools, tool_choice or the Free-tier tool route.
func TestGrokL17CompactIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for kind, build := range l17Accounts() {
		for shape, body := range l17CompactBodies {
			for i, st := range append([]l17State{l17On("on")}, l17OffStates()...) {
				t.Run(kind+"/"+shape+"/"+st.name, func(t *testing.T) {
					account := build(int64(61 + i%3))
					p := l17Snapshot(t, st)
					baseUp := &l17Upstream{status: 200, body: l17CompactResponse, record: true}
					base := l17Compact(t, l17Service(account, baseUp), baseUp, account, body, l17APIKeyID, nil)
					up := &l17Upstream{status: 200, body: l17CompactResponse, record: true}
					svc := l17Service(account, up)
					l17ApplyRejections(svc, st)
					before := GrokIdentityFixAppliedTotal("compact_identity")
					got := l17Compact(t, svc, up, account, body, l17APIKeyID, p)
					l17Expect(t, base, got)
					require.False(t, gjson.GetBytes(base.body, "prompt_cache_key").Exists())
					require.Empty(t, base.req.Header.Get(grokConversationIDHeader))
					if !st.expectCompact {
						require.Equal(t, base.body, got.body, "disabled state must be byte-identical")
						require.Equal(t, base.req.Header, got.req.Header)
						require.Equal(t, before, GrokIdentityFixAppliedTotal("compact_identity"))
						return
					}
					identity := gjson.GetBytes(got.body, "prompt_cache_key").String()
					require.NotEmpty(t, identity)
					require.NotEqual(t, "raw-client-key", identity)
					require.Equal(t, identity, got.req.Header.Get(grokConversationIDHeader))
					require.Equal(t, gjson.GetBytes(base.body, "tools").Raw, gjson.GetBytes(got.body, "tools").Raw)
					require.Equal(t, gjson.GetBytes(base.body, "tool_choice").Raw, gjson.GetBytes(got.body, "tool_choice").Raw)
					require.NotContains(t, gjson.GetBytes(got.body, "tools").Raw, "web_search")
					stripped, err := sjson.DeleteBytes(got.body, "prompt_cache_key")
					require.NoError(t, err)
					require.JSONEq(t, string(base.body), string(stripped), "only prompt_cache_key is added")
					baseHeader := base.req.Header.Clone()
					baseHeader.Set(grokConversationIDHeader, identity)
					require.Equal(t, baseHeader, got.req.Header, "only X-Grok-Conv-Id is added")
					require.Equal(t, before+1, GrokIdentityFixAppliedTotal("compact_identity"))
				})
			}
		}
	}
}

// The two switches are independent of each other and of L-01.
func TestGrokL17SwitchesIndependent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := l17APIKeyAccount(61)
	for _, l01 := range []bool{false, true} {
		chatOnly := l17On("chat_only")
		chatOnly.compact, chatOnly.l01 = false, l01
		compactOnly := l17On("compact_only")
		compactOnly.chat, compactOnly.l01 = false, l01
		for _, st := range []l17State{chatOnly, compactOnly} {
			t.Run(fmt.Sprintf("%s/l01=%t", st.name, l01), func(t *testing.T) {
				p := l17Snapshot(t, st)
				require.Equal(t, l01, p.UpstreamIdentity().Enabled())
				up := &l17Upstream{status: 200, body: l17ChatResponse, record: true}
				chat := l17RawChat(t, l17Service(account, up), up, account, l17ChatBody, l17APIKeyID, p)
				require.NoError(t, chat.err)
				require.Equal(t, st.chat, gjson.GetBytes(chat.body, "prompt_cache_key").Exists())
				up = &l17Upstream{status: 200, body: l17CompactResponse, record: true}
				compact := l17Compact(t, l17Service(account, up), up, account, l17CompactBodies["no_tools"], l17APIKeyID, p)
				require.NoError(t, compact.err)
				require.Equal(t, st.compact, gjson.GetBytes(compact.body, "prompt_cache_key").Exists())
			})
		}
	}
}

func TestGrokL17AccessorsWithoutSnapshot(t *testing.T) {
	var p *CachePolicySnapshot
	require.False(t, p.GrokChatBodyKey(61))
	require.False(t, p.GrokCompactIdentity(61))
	require.Nil(t, CachePolicyFromGin(nil))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Nil(t, CachePolicyFromGin(c))
	svc := &OpenAIGatewayService{}
	account := l17APIKeyAccount(61)
	require.False(t, svc.grokIdentityFixEnabled(c, account, grokIdentityFixChatBodyKey))
	require.False(t, svc.grokIdentityFixEnabled(nil, account, grokIdentityFixCompactIdentity))
	on := l17Snapshot(t, l17On("on"))
	require.True(t, on.GrokChatBodyKey(61))
	require.True(t, on.GrokCompactIdentity(63))
	require.False(t, on.GrokChatBodyKey(64))
}

// §3.1: a 400 naming prompt_cache_key on an attempt where L-17 wrote the body
// key fails once with the baseline presentation, then the account is skipped.
func TestGrokL17UpstreamRejectionSkipsAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	type forward func(testing.TB, *OpenAIGatewayService, *l17Upstream, *Account, []byte, int64, *CachePolicySnapshot) l17Run
	cases := []struct {
		name string
		run  forward
		body []byte
		ok   string
	}{
		{"chat_body_key", l17RawChat, l17ChatBody, l17ChatResponse},
		{"compact_identity", l17Compact, l17CompactBodies["no_tools"], l17CompactResponse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := l17APIKeyAccount(61)
			p := l17Snapshot(t, l17On("on"))
			// Baseline presentation of the same upstream 400 without L-17.
			baseUp := &l17Upstream{status: 400, errBody: l17RejectResponse, record: true}
			base := tc.run(t, l17Service(account, baseUp), baseUp, account, tc.body, l17APIKeyID, nil)
			up := &l17Upstream{statuses: []int{400}, status: 200, body: tc.ok, errBody: l17RejectResponse, record: true}
			svc := l17Service(account, up)
			first := tc.run(t, svc, up, account, tc.body, l17APIKeyID, p)
			require.True(t, gjson.GetBytes(first.body, "prompt_cache_key").Exists())
			require.Equal(t, base.status, first.status)
			require.Equal(t, base.out, first.out)
			require.Equal(t, fmt.Sprint(base.err), fmt.Sprint(first.err))
			require.Len(t, up.reqs, 1, "no new retry")
			require.True(t, svc.grokIdentityRuntime.isRejected(account.ID, time.Now()))
			require.False(t, svc.openaiIdentityRuntime.isRejected(account.ID, time.Now()), "separate from L-01")
			// Afterwards the account's outbound equals the disabled state.
			offUp := &l17Upstream{status: 200, body: tc.ok, record: true}
			off := tc.run(t, l17Service(account, offUp), offUp, account, tc.body, l17APIKeyID, nil)
			second := tc.run(t, svc, up, account, tc.body, l17APIKeyID, p)
			require.NoError(t, second.err)
			require.Equal(t, off.body, second.body)
			require.Equal(t, off.req.Header, second.req.Header)
			// Another listed account is unaffected.
			other := l17APIKeyAccount(62)
			otherUp := &l17Upstream{status: 200, body: tc.ok, record: true}
			third := tc.run(t, l17Service(other, otherUp), otherUp, other, tc.body, l17APIKeyID, p)
			require.True(t, gjson.GetBytes(third.body, "prompt_cache_key").Exists())
		})
	}
}

// A client-owned key hitting the same 400 while L-17 is off is pure baseline.
func TestGrokL17RejectionIgnoredWhenNotApplied(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := l17APIKeyAccount(61)
	for _, st := range l17OffStates() {
		t.Run(st.name, func(t *testing.T) {
			p := l17Snapshot(t, st)
			baseUp := &l17Upstream{status: 400, errBody: l17RejectResponse, record: true}
			base := l17RawChat(t, l17Service(account, baseUp), baseUp, account, l17ChatBody, l17APIKeyID, nil)
			up := &l17Upstream{status: 400, errBody: l17RejectResponse, record: true}
			svc := l17Service(account, up)
			l17ApplyRejections(svc, st)
			got := l17RawChat(t, svc, up, account, l17ChatBody, l17APIKeyID, p)
			require.Equal(t, base.status, got.status)
			require.Equal(t, base.out, got.out)
			require.Equal(t, base.body, got.body)
			require.Equal(t, fmt.Sprint(base.err), fmt.Sprint(got.err))
			if len(st.rejectedAccounts) == 0 {
				require.False(t, svc.grokIdentityRuntime.isRejected(account.ID, time.Now()))
			}
		})
	}
}

func TestGrokL17RejectionExpiresAfter24h(t *testing.T) {
	var r openAIUpstreamIdentityRuntime
	now := time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC)
	require.True(t, r.reject(61, now))
	require.False(t, r.reject(61, now.Add(time.Hour)), "already skipped")
	require.True(t, r.isRejected(61, now.Add(openAIIdentityRejectTTL-time.Nanosecond)))
	require.False(t, r.isRejected(61, now.Add(openAIIdentityRejectTTL)))
	require.False(t, r.isRejected(62, now))
}

// Disabled states read one snapshot bool before any allocation: allocs/op of
// each state equals the no-snapshot arm in the same fixture. The arms
// alternate and each keeps its minimum over five rounds.
func TestGrokL17DisabledStatesZeroExtraAllocs(t *testing.T) {
	if l17RaceEnabled {
		t.Skip("allocs/op is not exact under the race detector")
	}
	gin.SetMode(gin.TestMode)
	paths := []struct {
		name    string
		account *Account
		body    []byte
		resp    string
		run     func(testing.TB, *OpenAIGatewayService, *l17Upstream, *Account, []byte, int64, *CachePolicySnapshot) l17Run
	}{
		{"chat/apikey", l17APIKeyAccount(61), l17ChatBody, l17ChatResponse, l17RawChat},
		{"compact/apikey", l17APIKeyAccount(61), l17CompactBodies["with_tools"], l17CompactResponse, l17Compact},
		{"compact/oauth_free", l17FreeOAuthAccount(61), l17CompactBodies["with_tools"], l17CompactResponse, l17Compact},
	}
	for _, path := range paths {
		for _, st := range l17OffStates() {
			t.Run(path.name+"/"+st.name, func(t *testing.T) {
				p := l17Snapshot(t, st)
				up := &l17Upstream{status: 200, body: path.resp}
				svc := l17Service(path.account, up)
				l17ApplyRejections(svc, st)
				arm := func(snapshot *CachePolicySnapshot) func() {
					return func() {
						if run := path.run(t, svc, up, path.account, path.body, l17APIKeyID, snapshot); run.err != nil || run.status != http.StatusOK {
							panic(fmt.Sprintf("forward failed: %v %d", run.err, run.status))
						}
					}
				}
				baseMin, stateMin := -1.0, -1.0
				for round := 0; round < 5; round++ {
					base := testing.AllocsPerRun(50, arm(nil))
					state := testing.AllocsPerRun(50, arm(p))
					if baseMin < 0 || base < baseMin {
						baseMin = base
					}
					if stateMin < 0 || state < stateMin {
						stateMin = state
					}
				}
				t.Logf("L17_ALLOCS path=%s state=%s baseline=%.0f disabled=%.0f", path.name, st.name, baseMin, stateMin)
				require.Equal(t, baseMin, stateMin)
			})
		}
	}
}

func TestCachePolicyL17HistoryAndLegacyFill(t *testing.T) {
	for _, field := range []string{"grok.chat_body_key", "grok.compact_identity", "grok.account_ids"} {
		require.True(t, cachePolicyHistoryField("upstream_identity", field), field)
	}
	repo := newPolicyTestStore()
	repo.rows[cachePolicySettings[3]] = `{"rev":7,"spec":{"mode":"off","account_ids":[],"rollout_percent":0}}`
	s := NewCachePolicyService(repo, policyTestConfig(), WithCacheL17())
	view, err := s.Get(context.Background(), "upstream_identity")
	require.NoError(t, err)
	require.EqualValues(t, 7, view.Stored.Rev)
	require.Equal(t, map[string]any{"chat_body_key": false, "compact_identity": false, "account_ids": []int64{}}, view.Stored.Spec["grok"])
	require.Equal(t, `{"rev":7,"spec":{"mode":"off","account_ids":[],"rollout_percent":0}}`, repo.rows[cachePolicySettings[3]], "never written back")
}

type l17AccountLookup struct{ calls [][]int64 }

func (l *l17AccountLookup) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	l.calls = append(l.calls, append([]int64{}, ids...))
	out := []*Account{}
	for _, id := range ids {
		switch id {
		case 701, 702:
			out = append(out, &Account{ID: id, Platform: PlatformGrok})
		case 501:
			out = append(out, &Account{ID: id, Platform: PlatformAnthropic})
		}
	}
	return out, nil
}

func TestCachePolicyL17Merge(t *testing.T) {
	repo := newPolicyTestStore()
	repo.rows[cachePolicySettings[3]] = `{"rev":2,"spec":{"grok":{"chat_body_key":true,"account_ids":[701]}}}`
	accounts := &l17AccountLookup{}
	s := NewCachePolicyService(repo, policyTestConfig(), WithCacheL17(), WithCachePolicyAccounts(accounts))
	ctx := context.Background()
	// Partial nested writes keep siblings; only added IDs are looked up.
	v, err := s.Validate(ctx, "upstream_identity", upstreamPolicyWrite(t, 2, `{"grok":{"account_ids":[701,702]}}`), nil)
	require.NoError(t, err)
	require.True(t, v.Valid, v.Errors)
	require.Equal(t, [][]int64{{702}}, accounts.calls)
	require.Equal(t, "enable", v.Direction)
	// Removal is a disable and never queries accounts.
	accounts.calls = nil
	v, err = s.Validate(ctx, "upstream_identity", upstreamPolicyWrite(t, 2, `{"grok":{"account_ids":[]}}`), nil)
	require.NoError(t, err)
	require.True(t, v.Valid)
	require.Equal(t, "disable", v.Direction)
	require.Empty(t, accounts.calls)
	for spec, want := range map[string]CachePolicyFieldError{
		`{"grok.chat_body_key":true}`:          {Field: "grok.chat_body_key", Code: "unknown_field"},
		`{"grok":{"other":true}}`:              {Field: "grok.other", Code: "unknown_field"},
		`{"grok":{"chat_body_key":null}}`:      {Field: "grok.chat_body_key", Code: "malformed_body"},
		`{"grok":true}`:                        {Field: "grok", Code: "malformed_body"},
		`{"grok":{"account_ids":[701,701]}}`:   {Field: "grok.account_ids", Code: "duplicate"},
		`{"grok":{"account_ids":[0]}}`:         {Field: "grok.account_ids", Code: "unknown_account"},
		`{"grok":{"compact_identity":"true"}}`: {Field: "grok.compact_identity", Code: "malformed_body"},
	} {
		v, err := s.Validate(ctx, "upstream_identity", upstreamPolicyWrite(t, 2, spec), nil)
		require.NoError(t, err, spec)
		require.False(t, v.Valid, spec)
		require.Equal(t, []CachePolicyFieldError{want}, v.Errors, spec)
	}
	// Without the capability, enabling is unsupported but turning off works.
	dormant := NewCachePolicyService(repo, policyTestConfig(), WithCachePolicyAccounts(accounts))
	v, err = dormant.Validate(ctx, "upstream_identity", upstreamPolicyWrite(t, 2, `{"grok":{"compact_identity":true,"chat_body_key":false}}`), nil)
	require.NoError(t, err)
	require.Equal(t, []CachePolicyFieldError{{Field: "grok.compact_identity", Code: "unsupported_in_build"}}, v.Errors)
	require.Equal(t, []CachePolicyDiff{{"grok.chat_body_key", true, false}}, v.Diff)
}
