package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	middleware2 "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// asyncV2Harness runs the async handler on in-memory stand-ins for the Redis
// store and coordinator (their Redis behaviour is covered in the repository
// package) with a scripted gateway.
type asyncV2Harness struct {
	t        *testing.T
	tasks    *service.ImageTaskService
	handler  *AsyncImageHandler
	router   *gin.Engine
	executed atomic.Int32
	release  chan struct{}
	respond  func(c *gin.Context)
}

func newAsyncV2Harness(t *testing.T, executionTimeout time.Duration, limits service.ImageTaskLimits) *asyncV2Harness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
	tasks := service.NewImageTaskServiceWithUploader(store, nil, time.Hour, executionTimeout)
	tasks.Configure(service.ImageTaskDependencies{Coordinator: newAsyncImageMemoryCoordinator(), Limits: &limits})

	hr := &asyncV2Harness{t: t, tasks: tasks, release: make(chan struct{})}
	hr.respond = func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"created": 1, "data": []gin.H{{"url": "https://example.test/a.png"}}})
	}
	hr.handler = &AsyncImageHandler{tasks: tasks}
	hr.handler.execute = func(_ string, c *gin.Context) {
		hr.executed.Add(1)
		select {
		case <-hr.release:
		case <-c.Request.Context().Done():
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "upstream_error", "message": "upstream request failed: context canceled"}})
			return
		}
		hr.respond(c)
	}

	hr.router = gin.New()
	hr.router.Use(func(c *gin.Context) {
		groupID := int64(3)
		ctx := context.WithValue(c.Request.Context(), ctxkey.ClientRequestID, "cid-"+c.GetHeader("X-Test-Request"))
		c.Request = c.Request.WithContext(ctx)
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
			ID: 9, UserID: 7, GroupID: &groupID,
			Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowImageGeneration: true},
		})
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7, Concurrency: 1})
		c.Next()
	})
	hr.router.POST("/v1/images/generations/async", hr.handler.Submit)
	hr.router.POST("/v1/images/edits/async", hr.handler.Submit)
	hr.router.POST("/v1/images/generations", func(c *gin.Context) {
		if hr.handler.HandlePreferAsync(c) {
			return
		}
		c.JSON(http.StatusOK, gin.H{"sync": true})
	})
	hr.router.GET("/v1/images/tasks/:task_id", hr.handler.Get)
	hr.router.DELETE("/v1/images/tasks/:task_id", hr.handler.Delete)
	hr.router.GET("/v1/images/async/capabilities", hr.handler.Capabilities)
	return hr
}

func (hr *asyncV2Harness) do(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	hr.router.ServeHTTP(w, req)
	return w
}

func (hr *asyncV2Harness) task(id string) *service.ImageTask {
	task, err := hr.tasks.Get(context.Background(), service.ImageTaskOwner{UserID: 7, APIKeyID: 9}, id)
	require.NoError(hr.t, err)
	return task
}

func (hr *asyncV2Harness) waitStatus(id, status string) *service.ImageTask {
	var task *service.ImageTask
	require.Eventually(hr.t, func() bool {
		task = hr.task(id)
		return task.Status == status
	}, 3*time.Second, 5*time.Millisecond)
	return task
}

func decodeTaskBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	return body
}

func taskIDOf(t *testing.T, body map[string]any) string {
	t.Helper()
	id, ok := body["task_id"].(string)
	require.True(t, ok, "task_id in %v", body)
	return id
}

const asyncV2Body = `{"model":"gpt-image-2","prompt":"lighthouse"}`

func TestAsyncImageV2AcceptCarriesRequestIDAndBilling(t *testing.T) {
	hr := newAsyncV2Harness(t, time.Minute, service.DefaultImageTaskLimits())
	w := hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, map[string]string{"X-Test-Request": "a"})
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	body := decodeTaskBody(t, w)
	id := taskIDOf(t, body)
	require.Equal(t, "client:cid-a", body["request_id"])
	require.Equal(t, map[string]any{"status": "pending", "request_id": "client:cid-a"}, body["billing"])
	require.Equal(t, "/v1/images/tasks/"+id, w.Header().Get("Location"))

	running := hr.task(id)
	require.Equal(t, "gpt-image-2", running.Model)
	require.Equal(t, "generations", running.Endpoint)
	require.NotNil(t, running.HeartbeatAt)

	close(hr.release)
	done := hr.waitStatus(id, service.ImageTaskStatusCompleted)
	require.Equal(t, &service.ImageTaskBilling{Status: service.ImageTaskBillingPending, RequestID: "client:cid-a"}, done.Billing, "without a ledger lookup, successful generation does not prove settlement")
	require.NotNil(t, done.StartedAt)
}

func TestAsyncImageV2Idempotency(t *testing.T) {
	hr := newAsyncV2Harness(t, time.Minute, service.DefaultImageTaskLimits())
	headers := map[string]string{"Idempotency-Key": "job:42", "X-Test-Request": "first"}

	first := hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, headers)
	require.Equal(t, http.StatusAccepted, first.Code, first.Body.String())
	firstBody := decodeTaskBody(t, first)
	require.Equal(t, "job:42", firstBody["idempotency_key"])

	// The 202 was "lost": the client retries with the same key and body.
	headers["X-Test-Request"] = "retry"
	replay := hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, headers)
	require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
	require.Equal(t, "true", replay.Header().Get("Idempotent-Replayed"))
	replayBody := decodeTaskBody(t, replay)
	require.Equal(t, firstBody["task_id"], replayBody["task_id"])
	require.Equal(t, "client:cid-first", replayBody["request_id"], "the replay reports the original generation's request id")
	require.Equal(t, "cid-first", replay.Header().Get("X-Client-Request-ID"))
	require.Equal(t, firstBody["poll_url"], replayBody["poll_url"])

	changed := hr.do(http.MethodPost, "/v1/images/generations/async", `{"model":"gpt-image-2","prompt":"other"}`, headers)
	require.Equal(t, http.StatusUnprocessableEntity, changed.Code)
	require.Contains(t, changed.Body.String(), "idempotency_key_reused")

	close(hr.release)
	hr.waitStatus(taskIDOf(t, firstBody), service.ImageTaskStatusCompleted)
	require.EqualValues(t, 1, hr.executed.Load(), "one key, one generation")

	invalid := hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, map[string]string{"Idempotency-Key": "has space"})
	require.Equal(t, http.StatusBadRequest, invalid.Code)
}

func TestAsyncImageV2IdempotencyInProgressAndRelease(t *testing.T) {
	hr := newAsyncV2Harness(t, time.Minute, service.DefaultImageTaskLimits())
	scope := imageTaskIdempotencyScope(7, 9, "job:busy")
	fingerprint := imageTaskFingerprint("generations", "application/json", []byte(asyncV2Body))
	_, claimed, err := hr.tasks.ClaimIdempotency(context.Background(), scope, fingerprint)
	require.NoError(t, err)
	require.True(t, claimed)

	busy := hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, map[string]string{"Idempotency-Key": "job:busy"})
	require.Equal(t, http.StatusConflict, busy.Code)
	require.Equal(t, "1", busy.Header().Get("Retry-After"))
	require.Contains(t, busy.Body.String(), "idempotency_in_progress")
	require.Zero(t, hr.executed.Load())

	// Drain refuses before the key is claimed; the same key works afterwards.
	hr.handler.draining.Store(true)
	refused := hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, map[string]string{"Idempotency-Key": "job:later"})
	require.Equal(t, http.StatusServiceUnavailable, refused.Code)
	hr.handler.draining.Store(false)
	close(hr.release)
	later := hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, map[string]string{"Idempotency-Key": "job:later"})
	require.Equal(t, http.StatusAccepted, later.Code, later.Body.String())
	hr.waitStatus(taskIDOf(t, decodeTaskBody(t, later)), service.ImageTaskStatusCompleted)
}

func TestAsyncImageV2AdmissionLimit(t *testing.T) {
	limits := service.DefaultImageTaskLimits()
	limits.MaxInFlightPerKey = 1
	hr := newAsyncV2Harness(t, time.Minute, limits)

	first := hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, map[string]string{"Idempotency-Key": "k1"})
	require.Equal(t, http.StatusAccepted, first.Code)
	second := hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, map[string]string{"Idempotency-Key": "k2"})
	require.Equal(t, http.StatusTooManyRequests, second.Code)
	require.Equal(t, "5", second.Header().Get("Retry-After"))
	require.Contains(t, second.Body.String(), "too_many_tasks")

	close(hr.release)
	hr.waitStatus(taskIDOf(t, decodeTaskBody(t, first)), service.ImageTaskStatusCompleted)
	// The refused submission released its Idempotency-Key and its slot.
	retry := hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, map[string]string{"Idempotency-Key": "k2"})
	require.Equal(t, http.StatusAccepted, retry.Code, retry.Body.String())
	hr.waitStatus(taskIDOf(t, decodeTaskBody(t, retry)), service.ImageTaskStatusCompleted)
	require.Zero(t, hr.handler.running.Load())
}

func TestAsyncImageV2InstanceLimit(t *testing.T) {
	limits := service.DefaultImageTaskLimits()
	limits.MaxInFlightPerInstance = 1
	hr := newAsyncV2Harness(t, time.Minute, limits)
	require.Equal(t, http.StatusAccepted, hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, nil).Code)
	require.Equal(t, http.StatusTooManyRequests, hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, nil).Code)
	close(hr.release)
}

func TestAsyncImageV2PreferRespondAsync(t *testing.T) {
	hr := newAsyncV2Harness(t, time.Minute, service.DefaultImageTaskLimits())
	close(hr.release)

	w := hr.do(http.MethodPost, "/v1/images/generations", asyncV2Body, map[string]string{"Prefer": "wait=5, respond-async"})
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Equal(t, "respond-async", w.Header().Get("Preference-Applied"))
	body := decodeTaskBody(t, w)
	require.Equal(t, "/v1/images/tasks/"+taskIDOf(t, body), body["poll_url"])
	hr.waitStatus(taskIDOf(t, body), service.ImageTaskStatusCompleted)

	plain := hr.do(http.MethodPost, "/v1/images/generations", asyncV2Body, nil)
	require.Equal(t, http.StatusOK, plain.Code)
	require.JSONEq(t, `{"sync":true}`, plain.Body.String())

	disabled := &AsyncImageHandler{tasks: service.NewImageTaskServiceWithOptions(nil, time.Hour, time.Minute)}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(asyncV2Body))
	c.Request.Header.Set("Prefer", "respond-async")
	require.False(t, disabled.HandlePreferAsync(c), "without async the preference is ignored and the call stays synchronous")
}

func TestAsyncImageV2LongPollAndDelete(t *testing.T) {
	hr := newAsyncV2Harness(t, time.Minute, service.DefaultImageTaskLimits())
	accepted := hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, nil)
	require.Equal(t, http.StatusAccepted, accepted.Code)
	id := taskIDOf(t, decodeTaskBody(t, accepted))

	running := hr.do(http.MethodDelete, "/v1/images/tasks/"+id, "", nil)
	require.Equal(t, http.StatusConflict, running.Code, "deleting is not cancelling")

	var wg sync.WaitGroup
	var polled *httptest.ResponseRecorder
	var waited time.Duration
	wg.Add(1)
	go func() {
		defer wg.Done()
		start := time.Now()
		polled = hr.do(http.MethodGet, "/v1/images/tasks/"+id+"?wait=10", "", nil)
		waited = time.Since(start)
	}()
	time.Sleep(200 * time.Millisecond)
	close(hr.release)
	wg.Wait()
	require.Equal(t, http.StatusOK, polled.Code)
	require.Equal(t, service.ImageTaskStatusCompleted, decodeTaskBody(t, polled)["status"])
	require.Less(t, waited, 5*time.Second, "a long poll returns as soon as the task finishes")

	deleted := hr.do(http.MethodDelete, "/v1/images/tasks/"+id, "", nil)
	require.Equal(t, http.StatusOK, deleted.Code)
	require.Equal(t, true, decodeTaskBody(t, deleted)["deleted"])
	require.Equal(t, http.StatusNotFound, hr.do(http.MethodGet, "/v1/images/tasks/"+id, "", nil).Code)
}

func TestAsyncImageV2PollRateLimit(t *testing.T) {
	limits := service.DefaultImageTaskLimits()
	limits.PollRatePerSecond = 2
	hr := newAsyncV2Harness(t, time.Minute, limits)
	close(hr.release)
	accepted := hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, nil)
	id := taskIDOf(t, decodeTaskBody(t, accepted))
	codes := []int{}
	for i := 0; i < 4; i++ {
		codes = append(codes, hr.do(http.MethodGet, "/v1/images/tasks/"+id, "", nil).Code)
	}
	require.Contains(t, codes, http.StatusTooManyRequests)
}

func TestAsyncImageV2TimeoutAndShutdown(t *testing.T) {
	timeoutHarness := newAsyncV2Harness(t, 100*time.Millisecond, service.DefaultImageTaskLimits())
	accepted := timeoutHarness.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, nil)
	id := taskIDOf(t, decodeTaskBody(t, accepted))
	timedOut := timeoutHarness.waitStatus(id, service.ImageTaskStatusFailed)
	require.Equal(t, http.StatusGatewayTimeout, timedOut.HTTPStatus)
	require.Contains(t, string(timedOut.Error), `"code":"timeout"`)

	limits := service.DefaultImageTaskLimits()
	limits.DrainTimeout = 100 * time.Millisecond
	hr := newAsyncV2Harness(t, time.Minute, limits)
	accepted = hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, nil)
	id = taskIDOf(t, decodeTaskBody(t, accepted))

	start := time.Now()
	hr.handler.Drain()
	require.Less(t, time.Since(start), 5*time.Second)
	interrupted := hr.task(id)
	require.Equal(t, service.ImageTaskStatusFailed, interrupted.Status)
	require.Contains(t, string(interrupted.Error), `"code":"task_interrupted"`)
	require.Equal(t, "client:cid-", interrupted.Billing.RequestID)

	refused := hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, nil)
	require.Equal(t, http.StatusServiceUnavailable, refused.Code)
	require.Contains(t, refused.Body.String(), "not submitted")
}

func TestAsyncImageV2DrainWaitsForRunningTasks(t *testing.T) {
	hr := newAsyncV2Harness(t, time.Minute, service.DefaultImageTaskLimits())
	accepted := hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, nil)
	id := taskIDOf(t, decodeTaskBody(t, accepted))
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(hr.release)
	}()
	hr.handler.Drain()
	require.Equal(t, service.ImageTaskStatusCompleted, hr.task(id).Status, "a task that finishes within the drain timeout completes normally")
}

func TestAsyncImageV2Capabilities(t *testing.T) {
	hr := newAsyncV2Harness(t, time.Minute, service.DefaultImageTaskLimits())
	w := hr.do(http.MethodGet, "/v1/images/async/capabilities", "", nil)
	require.Equal(t, http.StatusOK, w.Code)
	body := decodeTaskBody(t, w)
	require.Equal(t, "image.async.capabilities", body["object"])
	require.Equal(t, true, body["enabled"])
	require.Equal(t, true, body["idempotency"])
	require.Equal(t, true, body["billing_in_task"])
	require.Equal(t, "openai", body["platform"])
	require.Equal(t, []any{"generations", "edits"}, body["endpoints"])
	require.EqualValues(t, 30, body["long_poll_max_seconds"])
	require.EqualValues(t, 2, body["max_in_flight_per_key"], "user concurrency 1 x2")
	require.EqualValues(t, 3600, body["task_ttl_seconds"])
	require.EqualValues(t, 60, body["execution_timeout_seconds"])
}

func TestImageTaskFingerprintIgnoresMultipartBoundary(t *testing.T) {
	form := func(boundary, prompt string) (string, []byte) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		require.NoError(t, w.SetBoundary(boundary))
		require.NoError(t, w.WriteField("model", "gpt-image-2"))
		require.NoError(t, w.WriteField("prompt", prompt))
		part, err := w.CreateFormFile("image", "a.png")
		require.NoError(t, err)
		_, _ = part.Write([]byte("png-bytes"))
		require.NoError(t, w.Close())
		return w.FormDataContentType(), buf.Bytes()
	}
	ct1, body1 := form("boundary-one", "cat")
	ct2, body2 := form("boundary-two", "cat")
	ct3, body3 := form("boundary-three", "dog")
	require.Equal(t, imageTaskFingerprint("edits", ct1, body1), imageTaskFingerprint("edits", ct2, body2))
	require.NotEqual(t, imageTaskFingerprint("edits", ct1, body1), imageTaskFingerprint("edits", ct3, body3))
	require.NotEqual(t, imageTaskFingerprint("edits", ct1, body1), imageTaskFingerprint("generations", ct1, body1))
	require.NotEqual(t,
		imageTaskFingerprint("generations", "application/json", []byte(`{"prompt":"a"}`)),
		imageTaskFingerprint("generations", "application/json", []byte(`{"prompt":"b"}`)))
}

func TestPreferRespondAsyncParsing(t *testing.T) {
	require.True(t, preferRespondAsync("respond-async"))
	require.True(t, preferRespondAsync("wait=10, Respond-Async"))
	require.True(t, preferRespondAsync("respond-async; foo=bar"))
	require.False(t, preferRespondAsync(""))
	require.False(t, preferRespondAsync("return=minimal"))
}

// asyncImageMemoryCoordinator mirrors the Redis coordinator's semantics.
type asyncImageMemoryCoordinator struct {
	mu       sync.Mutex
	idem     map[string]service.ImageTaskIdempotency
	inflight map[string]asyncImageMemoryInFlight
	locks    map[string]bool
	polls    map[string]int
}

type asyncImageMemoryInFlight struct {
	apiKeyID  int64
	heartbeat time.Time
}

func newAsyncImageMemoryCoordinator() *asyncImageMemoryCoordinator {
	return &asyncImageMemoryCoordinator{
		idem:     map[string]service.ImageTaskIdempotency{},
		inflight: map[string]asyncImageMemoryInFlight{},
		locks:    map[string]bool{},
		polls:    map[string]int{},
	}
}

func (c *asyncImageMemoryCoordinator) ClaimIdempotency(_ context.Context, scope, fingerprint string, _ time.Duration) (service.ImageTaskIdempotency, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.idem[scope]; ok {
		return existing, false, nil
	}
	c.idem[scope] = service.ImageTaskIdempotency{Fingerprint: fingerprint}
	return c.idem[scope], true, nil
}

func (c *asyncImageMemoryCoordinator) BindIdempotency(_ context.Context, scope string, state service.ImageTaskIdempotency, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.idem[scope] = state
	return nil
}

func (c *asyncImageMemoryCoordinator) ReleaseIdempotency(_ context.Context, scope string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.idem, scope)
	return nil
}

func (c *asyncImageMemoryCoordinator) AdmitInFlight(_ context.Context, apiKeyID int64, taskID string, limit int, now, liveSince time.Time) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	live := 0
	for _, item := range c.inflight {
		if item.apiKeyID == apiKeyID && !item.heartbeat.Before(liveSince) {
			live++
		}
	}
	if limit > 0 && live >= limit {
		return false, nil
	}
	c.inflight[taskID] = asyncImageMemoryInFlight{apiKeyID: apiKeyID, heartbeat: now}
	return true, nil
}

func (c *asyncImageMemoryCoordinator) TouchInFlight(_ context.Context, apiKeyID int64, taskID string, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.inflight[taskID]; ok {
		c.inflight[taskID] = asyncImageMemoryInFlight{apiKeyID: apiKeyID, heartbeat: now}
	}
	return nil
}

func (c *asyncImageMemoryCoordinator) ReleaseInFlight(_ context.Context, _ int64, taskID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.inflight, taskID)
	return nil
}

func (c *asyncImageMemoryCoordinator) StaleInFlight(_ context.Context, before time.Time, limit int) ([]service.ImageTaskInFlight, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []service.ImageTaskInFlight
	for id, item := range c.inflight {
		if item.heartbeat.Before(before) {
			out = append(out, service.ImageTaskInFlight{TaskID: id, APIKeyID: item.apiKeyID})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (c *asyncImageMemoryCoordinator) TryLock(_ context.Context, name string, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.locks[name] {
		return false, nil
	}
	c.locks[name] = true
	return true, nil
}

func (c *asyncImageMemoryCoordinator) AllowPoll(_ context.Context, apiKeyID int64, perSecond int, now time.Time) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := fmt.Sprintf("%d:%d", apiKeyID, now.Unix())
	c.polls[key]++
	return c.polls[key] <= perSecond, nil
}

// The accepted task must survive completion of the HTTP submission without
// depending on the removed controlled-scheduling request ledger.
func TestAsyncImageV2SubmitCancellationDoesNotCancelExecution(t *testing.T) {
	hr := newAsyncV2Harness(t, time.Minute, service.DefaultImageTaskLimits())
	submitCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(asyncV2Body)).WithContext(submitCtx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	hr.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	id := taskIDOf(t, decodeTaskBody(t, w))
	cancel()
	close(hr.release)
	task := hr.waitStatus(id, service.ImageTaskStatusCompleted)
	require.Equal(t, http.StatusOK, task.HTTPStatus)
	require.Equal(t, int32(1), hr.executed.Load())
}

type asyncImageBlockingStorage struct {
	entered chan struct{}
	release chan struct{}
}

func (s *asyncImageBlockingStorage) Save(ctx context.Context, key, _ string, _ []byte) (string, error) {
	close(s.entered)
	select {
	case <-s.release:
		return "https://cdn.test/" + key, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestAsyncImageV2HeartbeatCoversResultStorage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hr := newAsyncV2Harness(t, 10*time.Minute, service.DefaultImageTaskLimits())
		storage := &asyncImageBlockingStorage{entered: make(chan struct{}), release: make(chan struct{})}
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(storage.release) })
		store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
		hr.tasks = service.NewImageTaskServiceWithUploader(store, service.NewImageResultUploader(storage, "images/", 0, nil), time.Hour, 10*time.Minute)
		hr.tasks.Configure(service.ImageTaskDependencies{Coordinator: newAsyncImageMemoryCoordinator()})
		hr.handler.tasks = hr.tasks
		hr.respond = func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"b64_json": "aW1hZ2U="}}})
		}
		close(hr.release)
		w := hr.do(http.MethodPost, "/v1/images/generations/async", asyncV2Body, nil)
		require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
		id := taskIDOf(t, decodeTaskBody(t, w))
		<-storage.entered

		// Virtual time exercises multiple real heartbeat ticks while upload is
		// blocked beyond the stale threshold, without a multi-minute test.
		time.Sleep(service.ImageTaskStaleAfter + time.Minute)
		synctest.Wait()
		require.Zero(t, hr.tasks.Sweep(context.Background()), "an uploading task still belongs to a live instance")
		require.Equal(t, service.ImageTaskStatusProcessing, hr.task(id).Status)
		releaseOnce.Do(func() { close(storage.release) })
		synctest.Wait()
		require.Equal(t, service.ImageTaskStatusCompleted, hr.task(id).Status)
		require.Zero(t, hr.handler.running.Load())
	})
}
