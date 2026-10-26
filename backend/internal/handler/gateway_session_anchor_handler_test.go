//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	middleware "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type sessionAnchorHandlerCacheKey struct {
	groupID int64
	hash    string
}

type sessionAnchorHandlerCache struct {
	service.GatewayCache
	bindings map[sessionAnchorHandlerCacheKey]int64
	reads    []sessionAnchorHandlerCacheKey
	writes   []sessionAnchorHandlerCacheKey
}

func (c *sessionAnchorHandlerCache) GetSessionAccountID(_ context.Context, groupID int64, hash string) (int64, error) {
	key := sessionAnchorHandlerCacheKey{groupID: groupID, hash: hash}
	c.reads = append(c.reads, key)
	if accountID, ok := c.bindings[key]; ok {
		return accountID, nil
	}
	return 0, service.ErrStickySessionNotFound
}

func (c *sessionAnchorHandlerCache) SetSessionAccountID(_ context.Context, groupID int64, hash string, accountID int64, _ time.Duration) error {
	key := sessionAnchorHandlerCacheKey{groupID: groupID, hash: hash}
	c.writes = append(c.writes, key)
	if c.bindings == nil {
		c.bindings = make(map[sessionAnchorHandlerCacheKey]int64)
	}
	c.bindings[key] = accountID
	return nil
}

func (*sessionAnchorHandlerCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

func (c *sessionAnchorHandlerCache) DeleteSessionAccountID(_ context.Context, groupID int64, hash string) error {
	delete(c.bindings, sessionAnchorHandlerCacheKey{groupID: groupID, hash: hash})
	return nil
}

type sessionAnchorHandlerUpstream struct{}

func (sessionAnchorHandlerUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body := `{"input_tokens":1}`
	if req != nil && req.URL != nil && strings.Contains(req.URL.Path, ":generateContent") {
		body = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (u sessionAnchorHandlerUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func TestGatewaySessionAnchorHandlerKeysShareNonzeroGroupAndBindings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		groupID   = int64(7301)
		accountID = int64(7302)
		apiKeyID  = int64(7303)
		userID    = int64(7304)
		clientIP  = "192.0.2.73"
		userAgent = "w3-test-client/1"
	)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformGemini, Status: service.StatusActive}
	account := &service.Account{
		ID: accountID, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-gemini-key", "intercept_warmup_requests": true},
		Concurrency: 1, Priority: 1, Status: service.StatusActive, Schedulable: true,
		AccountGroups: []service.AccountGroup{{AccountID: accountID, GroupID: groupID}},
	}
	sessionCache := &sessionAnchorHandlerCache{bindings: make(map[sessionAnchorHandlerCacheKey]int64)}
	schedulerSnapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	groupRepo := &fakeGroupRepo{group: group}
	upstream := sessionAnchorHandlerUpstream{}
	gatewayService := service.NewGatewayService(
		nil, groupRepo, nil, nil, nil, nil, nil, sessionCache, cfg, schedulerSnapshot,
		nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil,
		&service.TLSFingerprintProfileService{}, nil, nil, nil, nil, nil,
	)
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	concurrency := service.NewConcurrencyService(&fakeConcurrencyCache{})
	handler := &GatewayHandler{
		gatewayService:      gatewayService,
		geminiCompatService: service.NewGeminiMessagesCompatService(nil, groupRepo, sessionCache, schedulerSnapshot, nil, nil, upstream, nil, cfg),
		billingCacheService: billingCache,
		concurrencyHelper:   NewConcurrencyHelper(concurrency, SSEPingFormatNone, 0),
		maxAccountSwitches:  1, maxAccountSwitchesGemini: 1,
	}
	apiKey := &service.APIKey{
		ID: apiKeyID, UserID: userID, GroupID: new(int64), Group: group, Status: service.StatusActive,
		User: &service.User{ID: userID, Concurrency: 10, Balance: 100},
	}
	*apiKey.GroupID = groupID
	newContext := func(path, body string) (*gin.Context, *httptest.ResponseRecorder) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.RemoteAddr = clientIP + ":4321"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)
		req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, group))
		c.Request = req
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID, Concurrency: 10})
		return c, recorder
	}
	assertRead := func(t *testing.T, key sessionAnchorHandlerCacheKey) {
		t.Helper()
		require.Contains(t, sessionCache.reads, key)
	}

	countBody := `{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"Warmup"}]}`
	messagesBody := `{"model":"gemini-2.5-pro","max_tokens":256,"messages":[{"role":"user","content":"Warmup"}]}`
	geminiBody := `{"contents":[{"role":"user","parts":[{"text":"Warmup"}]}]}`
	parsed, err := service.ParseGatewayRequest(service.NewRequestBodyRef([]byte(countBody)), domain.PlatformAnthropic)
	require.NoError(t, err)
	parsed.GroupID = apiKey.GroupID
	parsed.SessionContext = &service.SessionContext{ClientIP: clientIP, UserAgent: userAgent, APIKeyID: apiKeyID}
	baseHash := gatewayService.GenerateSessionHash(parsed)
	require.NotEmpty(t, baseHash)
	key := sessionAnchorHandlerCacheKey{groupID: groupID, hash: "gemini:" + baseHash}

	countContext, countRecorder := newContext("/v1/messages/count_tokens", countBody)
	handler.CountTokens(countContext)
	require.Equal(t, http.StatusOK, countRecorder.Code, countRecorder.Body.String())
	assertRead(t, key)
	require.Contains(t, sessionCache.writes, key, "count_tokens selector should bind the stable key")
	require.Equal(t, accountID, sessionCache.bindings[key])

	messagesContext, messagesRecorder := newContext("/v1/messages", messagesBody)
	handler.Messages(messagesContext)
	require.Equal(t, http.StatusOK, messagesRecorder.Code, messagesRecorder.Body.String())
	assertRead(t, key)
	require.Equal(t, accountID, sessionCache.bindings[key], "Messages should reuse the count_tokens binding")

	geminiContext, geminiRecorder := newContext("/v1beta/models/gemini-2.5-pro:generateContent", geminiBody)
	geminiContext.Params = gin.Params{{Key: "modelAction", Value: "/gemini-2.5-pro:generateContent"}}
	handler.GeminiV1BetaModels(geminiContext)
	require.Equal(t, http.StatusOK, geminiRecorder.Code, geminiRecorder.Body.String())
	assertRead(t, key)
	require.Equal(t, accountID, sessionCache.bindings[key], "native Gemini fallback should retain the shared binding")

	parsedGemini, err := service.ParseGatewayRequest(service.NewRequestBodyRef([]byte(geminiBody)), domain.PlatformGemini)
	require.NoError(t, err)
	parsedGemini.GroupID = apiKey.GroupID
	parsedGemini.SessionContext = &service.SessionContext{ClientIP: clientIP, UserAgent: userAgent, APIKeyID: apiKeyID}
	require.Equal(t, baseHash, gatewayService.GenerateSessionHash(parsedGemini), "native Gemini text should generate the same base anchor")

	cliBody := `{"contents":[{"role":"user","parts":[{"text":"The project's temporary directory is: /Users/client/.gemini/tmp/f7851b009ed314d1baee62e83115f486160283f4a55a582d89fdac8b9fe3b740"}]}]}`
	cliContext, cliRecorder := newContext("/v1beta/models/gemini-2.5-pro:generateContent", cliBody)
	cliContext.Params = gin.Params{{Key: "modelAction", Value: "/gemini-2.5-pro:generateContent"}}
	cliContext.Request.Header.Set("x-gemini-api-privileged-user-id", "90785f52-8bbe-4b17-b111-a1ddea1636c3")
	cliHash := extractGeminiCLISessionHash(cliContext, []byte(cliBody))
	cliKey := sessionAnchorHandlerCacheKey{groupID: groupID, hash: "gemini:" + cliHash}
	readsBeforeCLI := len(sessionCache.reads)
	handler.GeminiV1BetaModels(cliContext)
	require.Equal(t, http.StatusOK, cliRecorder.Code, cliRecorder.Body.String())
	assertRead(t, cliKey)
	require.NotContains(t, sessionCache.reads[readsBeforeCLI:], key, "an explicit Gemini CLI session key must precede the fallback anchor")
}
