package service

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type cacheTraceReleaseReader struct {
	io.Reader
	release func()
	once    sync.Once
}

func (r *cacheTraceReleaseReader) Read(p []byte) (int, error) {
	r.once.Do(r.release)
	return r.Reader.Read(p)
}

// Exercise the actual HTTP invalid-encrypted-content retry while its first
// immutable body is still owned by the observation worker.
func TestCacheTraceHTTPRetryKeepsBothWireBodiesImmutable(t *testing.T) {
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "synthetic-retry-secret"}}
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	cfg.Gateway.CacheTrace.Workers = 1
	s := &OpenAIGatewayService{cfg: cfg}
	traceTestConfig(s)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := []byte(`{"model":"gpt-5.4","stream":false,"input":[{"type":"reasoning","encrypted_content":"retry-private-cipher","summary":[{"type":"summary_text","text":"keep"}]},{"role":"user","content":"` + strings.Repeat("x", 200000) + `"}]}`)
	original := bytes.Clone(body)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	group := int64(10)
	c.Set("api_key", &APIKey{ID: 1, GroupID: &group})
	account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic"}, Extra: map[string]any{"openai_responses_supported": true}}
	s.GenerateSessionHash(c, body)
	blocked, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	require.True(t, s.cacheTraceWorkerPool().submit(&cacheTraceJob{
		state: &cacheTraceAsync{}, service: s, policy: s.cachePolicies.Snapshot(), ingress: []byte("{}"), size: 2,
		apiKeyID: 1, session: "queue-sentinel", protocol: "responses", transport: "http", created: time.Now(),
		getBody: func() (io.ReadCloser, error) {
			close(blocked)
			<-release
			return io.NopCloser(strings.NewReader("{}")), nil
		},
	}))
	<-blocked
	var first *cacheTraceAsync
	failure := &cacheTraceReleaseReader{
		Reader: strings.NewReader(`{"error":{"code":"invalid_encrypted_content","type":"invalid_request_error","message":"The encrypted content could not be verified."}}`),
		release: func() {
			tr := cacheTraceFromGin(c)
			tr.mu.Lock()
			first = tr.async
			tr.mu.Unlock()
			unblock()
		},
	}
	up := &httpUpstreamSequenceRecorder{responses: []*http.Response{
		{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(failure)},
		{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"retry-ok","usage":{"input_tokens":12000,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`))},
	}}
	s.httpUpstream = up
	result, err := s.Forward(c.Request.Context(), c, account, body)
	require.NoError(t, err)
	require.Equal(t, 2, up.callCount)
	require.NotNil(t, first)
	awaitCacheTrace(t, s, c, result)
	require.Eventually(t, func() bool { return first.result.Load() != nil }, 5*time.Second, time.Millisecond)
	require.NotSame(t, first, result.CacheTrace.async)
	require.Nil(t, first.settled.Load(), "the rejected attempt cannot publish history")
	require.Equal(t, original, body, "retry patches must not mutate the ingress backing array")
	require.True(t, gjson.GetBytes(up.bodies[0], "input.0.encrypted_content").Exists())
	require.False(t, gjson.GetBytes(up.bodies[1], "input.0.encrypted_content").Exists())
	for i, state := range []*cacheTraceAsync{first, result.CacheTrace.async} {
		expected, _, _ := scanCachePrefix(up.bodies[i], "responses", cachePrefixProcessSeed, nil)
		require.Equal(t, expected.final, state.result.Load().chain.final)
		require.Equal(t, expected.blocks, state.result.Load().chain.blocks)
	}
}
