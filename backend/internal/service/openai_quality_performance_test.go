package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/redis/go-redis/v9" //nolint:depguard // Isolated performance fixture; production service uses the storage interface.
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// This fixture exercises actual Redis round trips and a local mock upstream.
// Repository tests separately verify the production Lua implementation.
type qualityPerformanceCache struct {
	stubGatewayCache
	rdb    *redis.Client
	prefix string
}

func (c *qualityPerformanceCache) GetSessionAccountID(ctx context.Context, g int64, k string) (int64, error) {
	return c.rdb.Get(ctx, c.prefix+fmt.Sprint(g)+k).Int64()
}
func (c *qualityPerformanceCache) SetSessionAccountID(ctx context.Context, g int64, k string, id int64, ttl time.Duration) error {
	return c.rdb.Set(ctx, c.prefix+fmt.Sprint(g)+k, id, ttl).Err()
}
func (c *qualityPerformanceCache) RefreshSessionTTL(ctx context.Context, g int64, k string, ttl time.Duration) error {
	return c.rdb.Expire(ctx, c.prefix+fmt.Sprint(g)+k, ttl).Err()
}
func (c *qualityPerformanceCache) ReadOpenAIQualityRouting(ctx context.Context, scope string, g int64, sticky []string) (*OpenAIQualityState, []int64, error) {
	keys := []string{c.prefix + scope}
	for _, k := range sticky {
		keys = append(keys, c.prefix+fmt.Sprint(g)+k)
	}
	values, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, nil, err
	}
	var state *OpenAIQualityState
	if raw, ok := values[0].(string); ok {
		state = &OpenAIQualityState{}
		if err := json.Unmarshal([]byte(raw), state); err != nil {
			return nil, nil, err
		}
	}
	ids := make([]int64, len(sticky))
	for i, v := range values[1:] {
		if raw, ok := v.(string); ok {
			ids[i], _ = strconv.ParseInt(raw, 10, 64)
		}
	}
	return state, ids, nil
}
func (*qualityPerformanceCache) UpdateOpenAIQualityRouting(context.Context, string, OpenAIQualityChange, time.Duration) (*OpenAIQualityState, error) {
	panic("healthy performance fixture must not observe a downgrade")
}

func TestOpenAIQualityHealthyPerformance(t *testing.T) {
	addr := os.Getenv("QUALITY_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set QUALITY_TEST_REDIS_ADDR for five-round Redis/mock-upstream comparison")
	}
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer func() { _ = rdb.Close() }()
	var calls atomic.Int64
	body := []byte(`{"model":"gpt-6-astra","prompt_cache_key":"perf-session","input":"healthy request"}`)
	var wireMismatch atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		wire, _ := io.ReadAll(r.Body)
		if string(wire) != string(body) {
			wireMismatch.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"gpt-6-astra","usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":800}}}`)
	}))
	defer upstream.Close()
	cache := &qualityPerformanceCache{rdb: rdb, prefix: fmt.Sprintf("quality-perf:%d:", time.Now().UnixNano())}
	defer func() {
		keys, _ := rdb.Keys(context.Background(), cache.prefix+"*").Result()
		if len(keys) > 0 {
			rdb.Del(context.Background(), keys...)
		}
	}()
	a := qualityTestAccount(1, upstream.URL)
	s := &OpenAIGatewayService{cache: cache, accountRepo: stubOpenAIAccountRepo{accounts: []Account{a}}, cfg: &config.Config{}}
	s.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	s.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = true
	s.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = true
	request := func() time.Duration {
		start := time.Now()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		c.Set("api_key", &APIKey{ID: 909})
		hash := s.GenerateSessionHash(c, body)
		selected, _, err := s.SelectAccountWithScheduler(c.Request.Context(), nil, "", hash, "gpt-6-astra", nil, OpenAIUpstreamTransportHTTPSSE, false)
		require.NoError(t, err)
		if selected.ReleaseFunc != nil {
			defer selected.ReleaseFunc()
		}
		beginUpstreamResponseModelObservation(c)
		req, err := s.buildUpstreamRequest(c.Request.Context(), c, selected.Account, body, "fixture", false, "perf-session", false)
		require.NoError(t, err)
		resp, err := upstream.Client().Do(req)
		require.NoError(t, err)
		wire, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.NoError(t, err)
		upstreamResponseModelObserverFromContext(c).ObserveOpenAI(wire, "")
		return time.Since(start)
	}
	for i := 0; i < 30; i++ {
		request()
	}
	const count = 300
	for round := 1; round <= 5; round++ {
		results := make(map[string]time.Duration)
		modes := []string{"off", "enforce"}
		if round%2 == 0 {
			modes = []string{"enforce", "off"}
		}
		for _, mode := range modes {
			s.cfg.Gateway.OpenAIQualityRouting.Mode = mode
			timings := make([]time.Duration, count)
			for i := range timings {
				timings[i] = request()
			}
			sort.Slice(timings, func(i, j int) bool { return timings[i] < timings[j] })
			results[mode] = timings[(count*95)/100]
		}
		delta := results["enforce"] - results["off"]
		t.Logf("HEALTHY_PERF round=%d samples=%d off_p95_us=%d enforce_p95_us=%d added_p95_us=%d", round, count, results["off"].Microseconds(), results["enforce"].Microseconds(), delta.Microseconds())
		require.LessOrEqual(t, delta, 2*time.Millisecond)
	}
	require.False(t, wireMismatch.Load(), "normal cache identity and body must remain byte-identical")
	require.Equal(t, int64(30+5*2*count), calls.Load(), "exactly one upstream call per request")
	t.Logf("HEALTHY_PERF upstream_calls=%d expected=%d normal_wire_identical=true synthetic_cached_tokens=800/1000", calls.Load(), 30+5*2*count)
}

func TestOpenAIQualityAbnormalCacheColdStart(t *testing.T) {
	seen := make(map[string]bool)
	var seenMu sync.Mutex
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		key := gjson.GetBytes(body, "prompt_cache_key").String()
		cached := 0
		seenMu.Lock()
		if seen[key] {
			cached = 800
		}
		seen[key] = true
		seenMu.Unlock()
		fmt.Fprintf(w, `{"model":"gpt-6-astra","usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":%d}}}`, cached)
	}))
	defer upstream.Close()
	a := qualityTestAccount(1, upstream.URL)
	s := &OpenAIGatewayService{cfg: &config.Config{}, cache: &stubGatewayCache{}, accountRepo: stubOpenAIAccountRepo{accounts: []Account{a}}}
	s.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	request := func() (int64, *gin.Context) {
		c, body, hash := qualityTestContext(s, 1, 0, "gpt-6-astra", "")
		selection, _, err := s.SelectAccountWithScheduler(c.Request.Context(), nil, "", hash, "gpt-6-astra", nil, OpenAIUpstreamTransportHTTPSSE, false)
		require.NoError(t, err)
		if selection.ReleaseFunc != nil {
			defer selection.ReleaseFunc()
		}
		beginUpstreamResponseModelObservation(c)
		req, err := s.buildUpstreamRequest(c.Request.Context(), c, &a, body, "fixture", false, "same-session", false)
		require.NoError(t, err)
		resp, err := upstream.Client().Do(req)
		require.NoError(t, err)
		wire, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.NoError(t, err)
		return gjson.GetBytes(wire, "usage.input_tokens_details.cached_tokens").Int(), c
	}
	first, _ := request()
	warm, c := request()
	upstreamResponseModelObserverFromContext(c).ObserveOpenAI([]byte(`{"model":"gpt-5.6-luna"}`), "")
	cold, _ := request()
	rewarm, _ := request()
	require.Equal(t, []int64{0, 800, 0, 800}, []int64{first, warm, cold, rewarm})
	require.Equal(t, int64(4), calls.Load())
	seenMu.Lock()
	require.Len(t, seen, 2)
	seenMu.Unlock()
	t.Logf("ABNORMAL_CACHE synthetic_cached_tokens=%v rotation_cold_starts=1 lost_cached_tokens=800 upstream_calls=%d", []int64{first, warm, cold, rewarm}, calls.Load())
}
