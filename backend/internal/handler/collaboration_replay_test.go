//go:build unit

package handler

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

type replayMemory struct {
	ownerFactsStore
	service.GatewayCache
	mu          sync.Mutex
	bindings    map[string]int64
	avoids      map[string]map[int64]time.Time
	avoidUntil  map[string]map[int64]time.Time
	failures    map[string]map[int64]int64
	accountBase int64
	effects     []string
}

type replayRandom struct {
	mu sync.Mutex
	r  *rand.Rand
}

func (r *replayRandom) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.r.Read(p)
}

func (m *replayMemory) GetSessionAccountID(_ context.Context, _ int64, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id := m.bindings[key]; id != 0 {
		return id, nil
	}
	return 0, service.ErrStickySessionNotFound
}
func (m *replayMemory) SetSessionAccountID(_ context.Context, _ int64, key string, id int64, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bindings[key] = id
	return nil
}
func (m *replayMemory) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}
func (m *replayMemory) DeleteSessionAccountID(_ context.Context, _ int64, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.bindings, key)
	m.effects = append(m.effects, "unbind:"+replayDigest([]byte(key)))
	return nil
}
func (m *replayMemory) IncrSessionFailure(_ context.Context, _ int64, key string, id int64, _ time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failures[key] == nil {
		m.failures[key] = map[int64]int64{}
	}
	m.failures[key][id]++
	m.effects = append(m.effects, "failure:"+replayAccount(id-m.accountBase)+":"+replayDigest([]byte(key)))
	return m.failures[key][id], nil
}
func (m *replayMemory) SessionFailures(_ context.Context, _ int64, key string) (map[int64]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int64]int64{}
	for id, n := range m.failures[key] {
		out[id] = n
	}
	return out, nil
}
func (m *replayMemory) ClearSessionFailure(_ context.Context, _ int64, key string, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.failures[key], id)
	return nil
}
func (m *replayMemory) ClearSessionFailuresBefore(ctx context.Context, group int64, key string, id int64, _ time.Time) error {
	return m.ClearSessionFailure(ctx, group, key, id)
}
func (m *replayMemory) AddSessionAvoid(_ context.Context, _ int64, key string, id int64, ttl time.Duration, _ ...time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.avoids[key] == nil {
		m.avoids[key] = map[int64]time.Time{}
	}
	m.avoids[key][id] = time.Now()
	m.effects = append(m.effects, "avoid:"+replayAccount(id-m.accountBase)+":"+replayDigest([]byte(key)))
	if m.avoidUntil[key] == nil {
		m.avoidUntil[key] = map[int64]time.Time{}
	}
	m.avoidUntil[key][id] = time.Now().Add(ttl)
	return nil
}
func (m *replayMemory) SessionAvoidWrittenAt(_ context.Context, _ int64, key string) (map[int64]time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int64]time.Time{}
	for id, at := range m.avoids[key] {
		if time.Now().Before(m.avoidUntil[key][id]) {
			out[id] = at
		}
	}
	return out, nil
}
func (m *replayMemory) SessionAvoid(ctx context.Context, group int64, key string) (map[int64]struct{}, error) {
	writes, err := m.SessionAvoidWrittenAt(ctx, group, key)
	out := map[int64]struct{}{}
	for id := range writes {
		out[id] = struct{}{}
	}
	return out, err
}
func (m *replayMemory) snapshot() ([]replayBinding, []replayBinding) {
	m.mu.Lock()
	defer m.mu.Unlock()
	bindings, avoids := []replayBinding{}, []replayBinding{}
	for key, id := range m.bindings {
		bindings = append(bindings, replayBinding{replayDigest([]byte(key)), replayAccount(id - m.accountBase)})
	}
	for key, ids := range m.avoids {
		for id := range ids {
			if !time.Now().Before(m.avoidUntil[key][id]) {
				continue
			}
			avoids = append(avoids, replayBinding{replayDigest([]byte(key)), replayAccount(id - m.accountBase)})
		}
	}
	less := func(values []replayBinding) {
		sort.Slice(values, func(i, j int) bool {
			if values[i].Key == values[j].Key {
				return values[i].Account < values[j].Account
			}
			return values[i].Key < values[j].Key
		})
	}
	less(bindings)
	less(avoids)
	return bindings, avoids
}

func replaySettings(c replayCase, snapshot resilience.Settings) resilience.Settings {
	s := snapshot.Clone()
	s.Budget.Enabled = c.Attempts != 0
	if c.Attempts != 0 {
		s.Budget.MaxAttempts = c.Attempts
	}
	// No timer-dependent heartbeat bytes. Explicit heartbeat fixtures cover
	// the committed-header case; hangs still exercise real deadline handling.
	s.Keepalive.Enabled = false
	s.Budget.FirstOutputTimeoutSeconds.Default = 2
	s.Budget.ModelOverrides = nil
	s.Budget.PreOutputBudgetSeconds = 30
	s.FCOCompat.Mode = resilience.FCOCompatModeReactive
	if c.MixedMarkers {
		s.CollaborationPlaintext.Enabled = true
		s.CollaborationPlaintext.Models = []string{"gpt-5.5"}
	}
	return s
}

func replayRequestBody(t *testing.T, c replayCase) []byte {
	t.Helper()
	body := map[string]any{"model": "gpt-5.5", "stream": c.Stream, "input": c.Items, "prompt_cache_key": "synthetic-thread"}
	if c.Route == "review" {
		body["model"] = "codex-auto-review"
	}
	if c.Pointer {
		body["previous_response_id"] = "resp_previous"
	}
	if c.Structured {
		body["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": "synthetic", "schema": map[string]any{"type": "object", "properties": map[string]any{}}}}
	}
	if c.MixedMarkers {
		body["tools"] = json.RawMessage(`[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"send_message","parameters":{"type":"object","properties":{"message":{"type":"string","encrypted":true},"subject":{"type":"string","encrypted":false}},"required":["message"],"additionalProperties":false}}]}]`)
	}
	if strings.HasPrefix(c.Name, "long_repair_chain/") {
		body["truncation"] = "auto"
		body["max_output_tokens"] = 128
		body["tools"] = []any{}
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	if c.Duplicate {
		raw = bytes.Replace(raw, []byte(`"id":"amsg_task"`), []byte(`"id":"amsg_task","id":"amsg_other"`), 1)
	}
	return raw
}

func runCollaborationReplay(t *testing.T, c replayCase, snapshot resilience.Settings) replayGolden {
	t.Helper()
	result := replayGolden{Case: c.Name, Turns: []replayOutcome{}}
	gin.SetMode(gin.TestMode)
	previousSettings := resilience.Current().Clone()
	resilience.Publish(replaySettings(c, snapshot))
	defer resilience.Publish(previousSettings)
	resilience.ProcessNegativeCache().InvalidateGroup(1)
	defer resilience.ProcessNegativeCache().InvalidateGroup(1)
	uuid.SetRand(&replayRandom{r: rand.New(rand.NewSource(1))})
	defer uuid.SetRand(nil)
	previousRandom := cryptorand.Reader
	cryptorand.Reader = &replayRandom{r: rand.New(rand.NewSource(2))}
	defer func() { cryptorand.Reader = previousRandom }()
	accounts := []service.Account{}
	// Isolate process-local learned capabilities with a fixed account pair per case.
	var accountBase int64
	for i, candidate := range collaborationReplayCorpus() {
		if candidate.Name == c.Name {
			accountBase = int64(2*i + 100)
			break
		}
	}
	for id := int64(1); id <= 2; id++ {
		extra := map[string]any{"openai_passthrough": c.Passthrough}
		if c.MixedMarkers {
			extra["openai_collaboration_plaintext"] = true
		}
		if c.Route == "chat_bridge" {
			extra["openai_responses_supported"] = false
		}
		accounts = append(accounts, service.Account{ID: id + accountBase, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: int(id), Concurrency: 2, Credentials: map[string]any{"api_key": "synthetic-key", "base_url": "https://fixture.invalid"}, Extra: extra})
	}
	repo := &openAIWSFailoverHandlerAccountRepoStub{accounts: accounts}
	memory := &replayMemory{accountBase: accountBase, bindings: map[string]int64{}, avoids: map[string]map[int64]time.Time{}, avoidUntil: map[string]map[int64]time.Time{}, failures: map[string]map[int64]int64{}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.JWT.Secret = "synthetic-replay-key"
	cfg.Gateway.MaxAccountSwitches = 1
	cfg.Default.RateMultiplier = 1
	upstream := &replayUpstream{c: c, accountBase: accountBase, counts: map[int64]int{}}
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	defer billing.Stop()
	rateLimit := service.NewRateLimitService(repo, nil, cfg, nil, nil)
	usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 16), unbilled: make(chan *service.GatewayUnbilledRequest, 16)}
	gateway := service.NewOpenAIGatewayService(repo, usage, nil, nil, nil, nil, memory, cfg, nil, nil, service.NewBillingService(cfg, nil), rateLimit, billing, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	gateway.StopOpenAICodexTicketHarvester()
	upstream.learning = func() []string { return replayLearning(gateway) }
	previousRuntime := service.Resilience()
	runtime := service.ProvideResilienceService(nil, memory, nil, nil, repo, nil, rateLimit, &service.GatewayService{}, gateway)
	defer func() { runtime.Stop(); service.SetResilienceInstance(previousRuntime) }()
	concurrency := &replaySlots{base: accountBase, firstWaiters: c.FirstWaiters, secondFull: c.SecondUnavailable, userWaiters: c.UserWaiters, active: map[int64]int{}}
	h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(concurrency), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	synctest.Test(t, func(t *testing.T) {
		records := &replayRecordStore{mode: c.RecordMode}
		if configureReplayRecords != nil {
			configureReplayRecords(t, gateway, records)
		}
		if os.Getenv("COLLAB_REPLAY_SETTINGS") != "" && c.RecordMode != "" {
			require.NotNil(t, configureReplayRecords, "record fault scenarios require the integration adapter")
		}
		if c.PendingSetup != "" && c.PendingSetup != "record_store_adapter" {
			if os.Getenv("COLLAB_REPLAY_SETTINGS") != "" {
				require.NotNil(t, configureReplayHistory, "history precondition requires the integration adapter: %s", c.PendingSetup)
			}
			if configureReplayHistory != nil {
				configureReplayHistory(t, gateway, c)
			}
		}
		if c.Pointer {
			require.NoError(t, gateway.BindOpenAIHTTPResponseOwner(context.Background(), 1, "resp_previous", 1, 1))
		}
		body := replayRequestBody(t, c)
		start := 0
		if c.Warmup {
			start = -1
		}
		for turn := start; turn < c.Turns; turn++ {
			if turn > 0 && c.WaitBeforeRetry {
				// Respect the existing session avoidance window before retrying.
				time.Sleep(time.Duration(snapshot.Session.AvoidSeconds+1) * time.Second)
			}
			if turn > 0 && strings.HasPrefix(c.Name, "new_task_after_local_ending/") {
				body = bytes.ReplaceAll(body, []byte("amsg_task"), []byte("amsg_new_task"))
			}
			if turn > 0 && c.ChildAfterRecovery {
				body = bytes.ReplaceAll(body, []byte("synthetic-thread"), []byte("synthetic-child"))
			}
			upstream.turn = turn
			memory.effects = []string{}
			before := len(upstream.sends)
			path := EndpointResponses
			if c.Route == "compact" {
				path += "/compact"
			}
			rec := httptest.NewRecorder()
			writer := &replayWriter{ResponseRecorder: rec, last: time.Now(), start: time.Now(), headerAt: -1, terminalAt: -1, acceptedAt: -1}
			timeout := 35 * time.Second
			if c.LittleTime {
				timeout = 50 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(logger.IntoContext(context.Background(), zap.NewNop()), timeout)
			upstream.cancel = cancel
			gc, _ := gin.CreateTestContext(writer)
			gc.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)).WithContext(ctx)
			gc.Request.Header.Set("Content-Type", "application/json")
			gc.Request.Header.Set("User-Agent", "codex_cli_rs/0.166.0")
			gc.Request.Header.Set("session_id", "synthetic-thread")
			if c.Identity == "child" {
				gc.Request.Header.Set("session_id", "synthetic-child")
				gc.Request.Header.Set("x-codex-turn-metadata", `{"thread_id":"synthetic-child","parent_thread_id":"synthetic-root","thread_source":"subagent"}`)
			}
			if turn > 0 && c.ChildAfterRecovery {
				gc.Request.Header.Set("session_id", "synthetic-child")
				gc.Request.Header.Set("x-codex-parent-thread-id", "synthetic-thread")
			}
			if c.Identity == "ambiguous" {
				gc.Request.Header.Del("session_id")
				gc.Request.Header.Set("x-codex-parent-thread-id", "synthetic-root")
				gc.Request.Header.Set("x-codex-turn-metadata", `{"parent_thread_id":"different-parent"}`)
			}
			if c.Route == "review" {
				gc.Request.Header.Set("x-openai-subagent", "review")
				gc.Request.Header.Set("x-codex-parent-thread-id", "synthetic-root")
			}
			group := int64(1)
			gc.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1, UserID: 1, GroupID: &group, Group: &service.Group{ID: group, Platform: service.PlatformOpenAI}, User: &service.User{ID: 1}})
			gc.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 0})
			if c.UserWaiters {
				gc.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 1})
			}
			// The actual request middleware owns budget creation and delivery
			// tracking; direct handler invocation would leave attempts unlimited.
			key, _ := gc.Get(string(middleware.ContextKeyAPIKey))
			subject, _ := gc.Get(string(middleware.ContextKeyUser))
			router := gin.New()
			router.Use(middleware.ClientRequestID())
			router.POST(path, func(current *gin.Context) {
				current.Set(string(middleware.ContextKeyAPIKey), key)
				current.Set(string(middleware.ContextKeyUser), subject)
				h.Responses(current)
				gc = current
			})
			router.ServeHTTP(writer, gc.Request)
			writer.note()
			cancel()
			billed := []string{}
			for len(usage.created) > 0 {
				row := <-usage.created
				billed = append(billed, replayAccount(row.AccountID-accountBase))
			}
			unbilled := len(usage.unbilled)
			for len(usage.unbilled) > 0 {
				<-usage.unbilled
			}
			if turn < 0 {
				continue
			}
			out := replayClientOutcome(rec)
			out.MaxIdleMillis, out.IdleLimitMillis = writer.maxIdle.Milliseconds(), 60000
			if c.LittleTime {
				out.IdleLimitMillis = timeout.Milliseconds()
			}
			out.Learning, out.RecordCalls = replayLearning(gateway), records.count()
			out.TerminalMillis, out.AcceptedMillis = writer.terminalAt, writer.acceptedAt
			out.HeadersBeforeTerminal, out.UserWaiters = writer.beforeTerminal, c.UserWaiters
			out.TerminalCounters = replayTerminalCounters(runtime)
			out.TerminalEffects = append([]string{}, memory.effects...)
			advice, err := json.Marshal(resilience.RetryAdviceFrom(gc.Request.Context()))
			require.NoError(t, err)
			out.RetryAdvice = replayDigest(advice)
			out.LocalEnding = strings.Contains(rec.Body.String(), "Gateway notice [")
			delivery := gc.Request.Header.Get("session_id")
			for _, item := range gjson.GetBytes(body, "input").Array() {
				if item.Get("type").String() == "agent_message" {
					delivery += ":" + item.Get("id").String()
				}
			}
			out.Delivery = replayDigest([]byte(delivery))
			out.UsageAccounts, out.UnbilledCount = billed, unbilled
			if streamError, ok := service.GetOpsStreamError(gc); ok {
				out.IntendedStatus = streamError.IntendedStatus
			}
			out.Sends = append([]replaySend{}, upstream.sends[before:]...)
			out.SendsPerAccount = map[string]int{"first": 0, "second": 0}
			for _, send := range out.Sends {
				out.SendsPerAccount[send.Account]++
			}
			out.Survived = []string{}
			if len(out.Sends) > 0 {
				out.Survived = out.Sends[len(out.Sends)-1].Survived
			}
			out.Bindings, out.Avoidance = memory.snapshot()
			out.RateLimited = []string{}
			for _, id := range repo.rateLimitedIDs {
				out.RateLimited = append(out.RateLimited, replayAccount(id-accountBase))
			}
			result.Turns = append(result.Turns, out)
		}
	})
	return result
}

func replayClientOutcome(rec *httptest.ResponseRecorder) replayOutcome {
	out := replayOutcome{Status: rec.Code, Protocol: "json", RetryAfter: rec.Header().Get("Retry-After"), RetryHeader: rec.Header().Get("x-should-retry"), Events: []string{}}
	body := rec.Body.String()
	failed := false
	inspect := func(raw string) {
		v := gjson.Parse(raw)
		kind := v.Get("type").String()
		if kind == "response.completed" || kind == "response.done" || v.Get("status").String() == "completed" {
			out.Success = true
			out.TerminalCount++
		}
		err := v.Get("error")
		if v.Get("response.error").Exists() {
			err = v.Get("response.error")
		}
		if err.Exists() || kind == "response.failed" || kind == "error" || v.Get("status").String() == "failed" {
			failed = true
			out.TerminalCount++
			out.Code = err.Get("code").String()
			out.ErrorType = err.Get("type").String()
			if out.Code == "" {
				out.Code = v.Get("code").String()
			}
		}
	}
	if strings.Contains(body, "data: ") {
		out.Protocol = "sse"
		for _, frame := range strings.SplitAfter(body, "\n\n") {
			if frame == "" {
				continue
			}
			out.Events = append(out.Events, replayDigest([]byte(frame)))
			for _, line := range strings.Split(frame, "\n") {
				if strings.HasPrefix(line, "data: ") {
					inspect(strings.TrimPrefix(line, "data: "))
				}
			}
		}
	} else {
		out.Events = append(out.Events, replayDigest([]byte(body)))
		inspect(body)
	}
	out.Success = out.Success && !failed && rec.Code < 400
	out.Retryable = !out.Success && out.RetryHeader != "false" && (rec.Code == 408 || rec.Code == 409 || rec.Code == 429 || rec.Code >= 500 || out.Code == "server_error")
	// Codex's SSE parser treats unknown failed-frame codes as retryable;
	// these terminal codes and explicit advice are the exceptions.
	if out.Protocol == "sse" && failed && out.RetryHeader != "false" {
		out.Retryable = out.Code != "invalid_prompt" && out.Code != "context_length_exceeded" && out.Code != "cyber_policy"
	}
	return out
}

func TestCollaborationReplayBaseline(t *testing.T) {
	// Re-enter the same instrumented binary with clean package globals on every run.
	if os.Getenv("COLLAB_REPLAY_CHILD") != "1" {
		executable, err := os.Executable()
		require.NoError(t, err)
		cmd := exec.Command(executable, "-test.run=^TestCollaborationReplayBaseline$", "-test.count=1")
		cmd.Env = append(os.Environ(), "COLLAB_REPLAY_CHILD=1")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "isolated replay: %s", output)
		return
	}
	quietReplayLogs(t)
	snapshot := resilience.Defaults()
	candidate := os.Getenv("COLLAB_REPLAY_SETTINGS")
	if candidate != "" {
		raw, err := os.ReadFile(candidate)
		require.NoError(t, err)
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		require.NoError(t, decoder.Decode(&snapshot), "unknown switches must fail until their implementation exists")
		require.NotEqual(t, "1", os.Getenv("UPDATE_COLLAB_REPLAY"), "candidate settings cannot replace baseline goldens")
	}
	var got []replayGolden
	for _, c := range collaborationReplayCorpus() {
		t.Run(c.Name, func(t *testing.T) { got = append(got, runCollaborationReplay(t, c, snapshot)) })
	}
	if t.Failed() {
		return
	}
	if os.Getenv("UPDATE_COLLAB_REPLAY") == "1" {
		raw, err := json.MarshalIndent(got, "", "  ")
		require.NoError(t, err)
		require.NoError(t, writeReplayGolden(append(raw, '\n')))
		return
	}
	raw, err := readReplayGolden()
	require.NoError(t, err)
	var want []replayGolden
	require.NoError(t, json.Unmarshal(raw, &want))
	if candidate == "" {
		require.NoError(t, checkReplayOff(want, got))
		require.NoError(t, checkReplayOn(snapshot, "ladder", want, got))
		require.NoError(t, checkReplayOn(snapshot, "lossless_first", want, got))
	} else {
		require.NoError(t, checkReplayOn(snapshot, os.Getenv("COLLAB_REPLAY_MODE"), want, got))
		require.NoError(t, checkHandledReplay(got))
	}
}

func quietReplayLogs(t *testing.T) {
	t.Helper()
	previousLog, previousSlog := log.Writer(), slog.Default()
	log.SetOutput(io.Discard)
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(previousSlog); log.SetOutput(previousLog) })
}
