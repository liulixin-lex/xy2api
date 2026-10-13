package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type imageTaskMemoryStore struct {
	mu      sync.Mutex
	task    *ImageTaskRecord
	tasks   map[string]*ImageTaskRecord
	ttl     time.Duration
	saveErr error
	getErr  error
}

func (s *imageTaskMemoryStore) Save(_ context.Context, task *ImageTaskRecord, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	if s.tasks == nil {
		s.tasks = map[string]*ImageTaskRecord{}
	}
	copy := cloneImageTaskRecord(task)
	s.task = copy
	s.tasks[task.ID] = copy
	s.ttl = ttl
	return nil
}

func (s *imageTaskMemoryStore) Get(_ context.Context, id string) (*ImageTaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	task := s.tasks[id]
	if task == nil {
		return nil, ErrImageTaskNotFound
	}
	return cloneImageTaskRecord(task), nil
}

func (s *imageTaskMemoryStore) Update(_ context.Context, id string, ttl time.Duration, mutate func(*ImageTaskRecord) error) (*ImageTaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := s.tasks[id]
	if task == nil {
		return nil, ErrImageTaskNotFound
	}
	copy := cloneImageTaskRecord(task)
	if err := mutate(copy); err != nil {
		if errors.Is(err, ErrImageTaskUnchanged) {
			return cloneImageTaskRecord(task), nil
		}
		return nil, err
	}
	s.tasks[id] = copy
	s.task = copy
	s.ttl = ttl
	return cloneImageTaskRecord(copy), nil
}

func (s *imageTaskMemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tasks, id)
	return nil
}

func cloneImageTaskRecord(task *ImageTaskRecord) *ImageTaskRecord {
	copy := *task
	copy.Result = append(json.RawMessage(nil), task.Result...)
	copy.Error = append(json.RawMessage(nil), task.Error...)
	copy.ObjectKeys = append([]string(nil), task.ObjectKeys...)
	if task.Billing != nil {
		billing := *task.Billing
		copy.Billing = &billing
	}
	return &copy
}

// imageTaskMemoryCoordinator mirrors the Redis coordinator's semantics.
type imageTaskMemoryCoordinator struct {
	mu       sync.Mutex
	idem     map[string]ImageTaskIdempotency
	inflight map[string]imageTaskMemoryInFlight
	locks    map[string]bool
	polls    map[string]int
}

type imageTaskMemoryInFlight struct {
	apiKeyID  int64
	heartbeat time.Time
}

func newImageTaskMemoryCoordinator() *imageTaskMemoryCoordinator {
	return &imageTaskMemoryCoordinator{
		idem:     map[string]ImageTaskIdempotency{},
		inflight: map[string]imageTaskMemoryInFlight{},
		locks:    map[string]bool{},
		polls:    map[string]int{},
	}
}

func (c *imageTaskMemoryCoordinator) ClaimIdempotency(_ context.Context, scope, fingerprint string, _ time.Duration) (ImageTaskIdempotency, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.idem[scope]; ok {
		return existing, false, nil
	}
	c.idem[scope] = ImageTaskIdempotency{Fingerprint: fingerprint}
	return c.idem[scope], true, nil
}

func (c *imageTaskMemoryCoordinator) BindIdempotency(_ context.Context, scope string, state ImageTaskIdempotency, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.idem[scope] = state
	return nil
}

func (c *imageTaskMemoryCoordinator) ReleaseIdempotency(_ context.Context, scope string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.idem, scope)
	return nil
}

func (c *imageTaskMemoryCoordinator) AdmitInFlight(_ context.Context, apiKeyID int64, taskID string, limit int, now, liveSince time.Time) (bool, error) {
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
	c.inflight[taskID] = imageTaskMemoryInFlight{apiKeyID: apiKeyID, heartbeat: now}
	return true, nil
}

func (c *imageTaskMemoryCoordinator) TouchInFlight(_ context.Context, apiKeyID int64, taskID string, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.inflight[taskID]; ok {
		c.inflight[taskID] = imageTaskMemoryInFlight{apiKeyID: apiKeyID, heartbeat: now}
	}
	return nil
}

func (c *imageTaskMemoryCoordinator) ReleaseInFlight(_ context.Context, _ int64, taskID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.inflight, taskID)
	return nil
}

func (c *imageTaskMemoryCoordinator) StaleInFlight(_ context.Context, before time.Time, limit int) ([]ImageTaskInFlight, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []ImageTaskInFlight
	for id, item := range c.inflight {
		if item.heartbeat.Before(before) {
			out = append(out, ImageTaskInFlight{TaskID: id, APIKeyID: item.apiKeyID})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (c *imageTaskMemoryCoordinator) TryLock(_ context.Context, name string, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.locks[name] {
		return false, nil
	}
	c.locks[name] = true
	return true, nil
}

func (c *imageTaskMemoryCoordinator) unlock(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.locks, name)
}

func (c *imageTaskMemoryCoordinator) AllowPoll(_ context.Context, apiKeyID int64, perSecond int, now time.Time) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := time.Unix(now.Unix(), 0).String() + string(rune(apiKeyID))
	c.polls[key]++
	return c.polls[key] <= perSecond, nil
}

type imageTaskStaticBilling struct {
	mu       sync.Mutex
	statuses map[string]string
	err      error
	calls    int
}

func (b *imageTaskStaticBilling) ImageTaskBillingStatus(_ context.Context, requestID string, _ int64) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.err != nil {
		return "", b.err
	}
	if status, ok := b.statuses[requestID]; ok {
		return status, nil
	}
	return ImageTaskBillingNotCharged, nil
}

func (b *imageTaskStaticBilling) set(requestID, status string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.statuses[requestID] = status
}

type imageTaskOpsRecorder struct {
	mu      sync.Mutex
	entries []*OpsInsertErrorLogInput
}

func (r *imageTaskOpsRecorder) RecordError(_ context.Context, entry *OpsInsertErrorLogInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, entry)
	return nil
}

// newV2ImageTaskService builds a fully wired service on memory fakes with a
// controllable clock and no real sleeping.
func newV2ImageTaskService(t *testing.T, uploader *ImageResultUploader) (*ImageTaskService, *imageTaskMemoryStore, *imageTaskMemoryCoordinator, *imageTaskStaticBilling, *imageTaskOpsRecorder, *time.Time) {
	t.Helper()
	store := &imageTaskMemoryStore{}
	coordinator := newImageTaskMemoryCoordinator()
	billing := &imageTaskStaticBilling{statuses: map[string]string{}}
	ops := &imageTaskOpsRecorder{}
	svc := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)
	limits := DefaultImageTaskLimits()
	svc.Configure(ImageTaskDependencies{Coordinator: coordinator, Billing: billing, Ops: ops, Limits: &limits})
	now := time.Unix(1_800_000_000, 0).UTC()
	svc.now = func() time.Time { return now }
	svc.sleep = func(context.Context, time.Duration) error { return nil }
	return svc, store, coordinator, billing, ops, &now
}

func TestImageTaskServiceLifecycleAndOwnership(t *testing.T) {
	store := &imageTaskMemoryStore{}
	svc := NewImageTaskServiceWithOptions(store, time.Hour, 10*time.Minute)
	owner := ImageTaskOwner{UserID: 7, APIKeyID: 9}

	created, err := svc.Create(context.Background(), owner)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusProcessing, created.Status)
	require.Equal(t, created.ID, created.TaskID)
	require.Equal(t, "image.generation.task", created.Object)
	require.Equal(t, time.Hour, store.ttl)
	require.Equal(t, owner.UserID, store.task.UserID)
	require.Equal(t, owner.APIKeyID, store.task.APIKeyID)

	_, err = svc.Get(context.Background(), ImageTaskOwner{UserID: 7, APIKeyID: 10}, created.ID)
	require.ErrorIs(t, err, ErrImageTaskNotFound)

	result := json.RawMessage(`{"created":123,"data":[{"url":"https://example.test/image.png"}]}`)
	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK, result))

	completed, err := svc.Get(context.Background(), owner, created.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusCompleted, completed.Status)
	require.Equal(t, http.StatusOK, completed.HTTPStatus)
	require.Equal(t, "https://example.test/image.png", completed.ImageURL)
	require.JSONEq(t, string(result), string(completed.Result))
	require.NotNil(t, completed.CompletedAt)
}

func TestImageTaskServiceInvalidResultBecomesFailed(t *testing.T) {
	store := &imageTaskMemoryStore{}
	svc := NewImageTaskServiceWithOptions(store, time.Hour, time.Minute)
	created, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2})
	require.NoError(t, err)

	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK, json.RawMessage(`not-json`)))
	got, err := svc.Get(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2}, created.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusFailed, got.Status)
	require.Equal(t, http.StatusBadGateway, got.HTTPStatus)
	require.Contains(t, string(got.Error), "non-JSON")
}

func TestImageTaskServiceMapsStoreFailures(t *testing.T) {
	store := &imageTaskMemoryStore{saveErr: errors.New("redis down")}
	svc := NewImageTaskService(store)

	_, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2})
	require.ErrorIs(t, err, ErrImageTaskUnavailable)
}

func TestImageTaskServiceV2CompletedIsCharged(t *testing.T) {
	svc, _, coordinator, _, _, _ := newV2ImageTaskService(t, nil)
	owner := ImageTaskOwner{UserID: 7, APIKeyID: 9}
	id := NewImageTaskID()
	require.NoError(t, svc.Admit(context.Background(), owner.APIKeyID, id, 4))
	created, err := svc.CreateTask(context.Background(), ImageTaskSpec{
		ID: id, Owner: owner, GroupID: 3, Platform: PlatformOpenAI, RequestID: "client:abc",
		Model: "gpt-image-2", Endpoint: "generations", SubmitPath: "/v1/images/generations/async", IdempotencyKey: "job-1",
	})
	require.NoError(t, err)
	require.Equal(t, id, created.ID)
	require.Equal(t, "client:abc", created.RequestID)
	require.Equal(t, &ImageTaskBilling{Status: ImageTaskBillingPending, RequestID: "client:abc"}, created.Billing)
	require.Equal(t, "gpt-image-2", created.Model)
	require.Equal(t, "generations", created.Endpoint)
	require.Equal(t, "job-1", created.IdempotencyKey)

	svc.Start(context.Background(), id)
	require.NoError(t, svc.Complete(context.Background(), id, http.StatusOK, json.RawMessage(`{"data":[{"url":"https://x.test/a.png"}]}`)))
	got, err := svc.Get(context.Background(), owner, id)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusCompleted, got.Status)
	require.Equal(t, &ImageTaskBilling{Status: ImageTaskBillingCharged, RequestID: "client:abc"}, got.Billing)
	require.NotNil(t, got.StartedAt)
	require.Empty(t, coordinator.inflight, "a finished task frees its admission slot")
}

func TestImageTaskServiceV2FailureBillingFromLedger(t *testing.T) {
	svc, store, _, billing, _, _ := newV2ImageTaskService(t, nil)
	owner := ImageTaskOwner{UserID: 7, APIKeyID: 9}

	// Upstream refusal: nothing billed.
	rejected, err := svc.CreateTask(context.Background(), ImageTaskSpec{Owner: owner, RequestID: "client:r1"})
	require.NoError(t, err)
	require.NoError(t, svc.Fail(context.Background(), rejected.ID, http.StatusBadRequest, json.RawMessage(`{"type":"invalid_request_error","code":"moderation_blocked","message":"no"}`)))
	got, err := svc.Get(context.Background(), owner, rejected.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskBillingNotCharged, got.Billing.Status)
	require.JSONEq(t, `{"type":"invalid_request_error","code":"moderation_blocked","message":"no"}`, string(got.Error), "upstream error object is kept as is")

	// Ledger still settling at finish: pending, refreshed on a later poll.
	billing.set("client:r2", ImageTaskBillingPending)
	slow, err := svc.CreateTask(context.Background(), ImageTaskSpec{Owner: owner, RequestID: "client:r2"})
	require.NoError(t, err)
	require.NoError(t, svc.FailWithCode(context.Background(), slow.ID, http.StatusGatewayTimeout, json.RawMessage(`{"type":"timeout_error","message":"t"}`), ImageTaskErrorTimeout))
	got, err = svc.Get(context.Background(), owner, slow.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskBillingPending, got.Billing.Status)
	require.Equal(t, ImageTaskErrorTimeout, imageTaskErrorCode(got.Error))

	billing.set("client:r2", ImageTaskBillingCharged)
	got, err = svc.Get(context.Background(), owner, slow.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskBillingCharged, got.Billing.Status)
	stored, err := store.Get(context.Background(), slow.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskBillingCharged, stored.Billing.Status, "the refreshed conclusion is persisted")

	// Lookup failure never invents an answer.
	billing.err = errors.New("db down")
	failed, err := svc.CreateTask(context.Background(), ImageTaskSpec{Owner: owner, RequestID: "client:r3"})
	require.NoError(t, err)
	require.NoError(t, svc.Fail(context.Background(), failed.ID, http.StatusBadGateway, nil))
	got, err = svc.Get(context.Background(), owner, failed.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskBillingPending, got.Billing.Status)
}

func TestImageTaskServiceTerminalStateIsFinal(t *testing.T) {
	svc, _, _, _, _, _ := newV2ImageTaskService(t, nil)
	owner := ImageTaskOwner{UserID: 7, APIKeyID: 9}
	created, err := svc.CreateTask(context.Background(), ImageTaskSpec{Owner: owner, RequestID: "client:t"})
	require.NoError(t, err)
	require.NoError(t, svc.FailWithCode(context.Background(), created.ID, http.StatusBadGateway, nil, ImageTaskErrorInterrupted))
	// A late success (e.g. after the sweeper concluded the task) is dropped.
	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK, json.RawMessage(`{"data":[{"url":"https://x.test/a.png"}]}`)))
	got, err := svc.Get(context.Background(), owner, created.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusFailed, got.Status)
	require.Empty(t, got.Result)
}

func TestImageTaskServiceHeartbeatAndSweeper(t *testing.T) {
	svc, store, coordinator, billing, ops, now := newV2ImageTaskService(t, nil)
	owner := ImageTaskOwner{UserID: 7, APIKeyID: 9}
	newTask := func(requestID string) string {
		id := NewImageTaskID()
		require.NoError(t, svc.Admit(context.Background(), owner.APIKeyID, id, 0))
		_, err := svc.CreateTask(context.Background(), ImageTaskSpec{ID: id, Owner: owner, RequestID: requestID, GroupID: 3,
			Platform: PlatformOpenAI, Model: "gpt-image-2", SubmitPath: "/v1/images/generations/async"})
		require.NoError(t, err)
		svc.Start(context.Background(), id)
		return id
	}
	dead := newTask("client:dead")
	alive := newTask("client:alive")
	billing.set("client:dead", ImageTaskBillingCharged)

	*now = now.Add(ImageTaskHeartbeatInterval)
	svc.Heartbeat(context.Background(), alive, owner.APIKeyID)
	*now = now.Add(ImageTaskStaleAfter - ImageTaskHeartbeatInterval + time.Second)
	svc.Heartbeat(context.Background(), alive, owner.APIKeyID)

	require.Equal(t, 1, svc.Sweep(context.Background()))
	got, err := svc.Get(context.Background(), owner, dead)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusFailed, got.Status)
	require.Equal(t, ImageTaskErrorInterrupted, imageTaskErrorCode(got.Error))
	require.Equal(t, &ImageTaskBilling{Status: ImageTaskBillingCharged, RequestID: "client:dead"}, got.Billing)
	require.Len(t, ops.entries, 1)
	require.Equal(t, "/v1/images/generations/async", ops.entries[0].RequestPath)
	require.Equal(t, "dead", ops.entries[0].ClientRequestID)
	require.Contains(t, ops.entries[0].ErrorMessage, ImageTaskErrorInterrupted)

	stillRunning, err := store.Get(context.Background(), alive)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusProcessing, stillRunning.Status)
	_, indexed := coordinator.inflight[alive]
	require.True(t, indexed)
	_, indexed = coordinator.inflight[dead]
	require.False(t, indexed)

	// Only one sweep at a time across instances.
	require.Equal(t, 0, svc.Sweep(context.Background()))
	coordinator.unlock("sweeper")

	// A heartbeat never revives a concluded task.
	svc.Heartbeat(context.Background(), dead, owner.APIKeyID)
	got, err = svc.Get(context.Background(), owner, dead)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusFailed, got.Status)
}

func TestImageTaskServiceAdmissionLimit(t *testing.T) {
	svc, _, _, _, _, now := newV2ImageTaskService(t, nil)
	require.NoError(t, svc.Admit(context.Background(), 9, "a", 2))
	require.NoError(t, svc.Admit(context.Background(), 9, "b", 2))
	require.ErrorIs(t, svc.Admit(context.Background(), 9, "c", 2), ErrImageTaskTooMany)
	require.NoError(t, svc.Admit(context.Background(), 10, "d", 2), "limits are per key")
	svc.ReleaseAdmission(context.Background(), 9, "a")
	require.NoError(t, svc.Admit(context.Background(), 9, "c", 2))
	// Tasks whose instance stopped heartbeating no longer count.
	*now = now.Add(ImageTaskStaleAfter + time.Second)
	require.NoError(t, svc.Admit(context.Background(), 9, "e", 2))

	require.Equal(t, 6, svc.MaxInFlightForKey(3))
	require.Equal(t, 2, svc.MaxInFlightForKey(1))
	require.Equal(t, 16, svc.MaxInFlightForKey(0))
}

func TestImageTaskServiceDeleteRemovesObjectsAndRecord(t *testing.T) {
	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	svc, store, _, _, _, _ := newV2ImageTaskService(t, uploader)
	owner := ImageTaskOwner{UserID: 7, APIKeyID: 9}
	created, err := svc.CreateTask(context.Background(), ImageTaskSpec{Owner: owner, RequestID: "client:d"})
	require.NoError(t, err)

	require.ErrorIs(t, svc.Delete(context.Background(), owner, created.ID), ErrImageTaskRunning)

	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK, json.RawMessage(`{"data":[{"b64_json":"`+b64+`"},{"b64_json":"`+b64+`"}]}`)))
	stored, err := store.Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"images/" + created.ID + "-0.png", "images/" + created.ID + "-1.png"}, stored.ObjectKeys)
	got, err := svc.Get(context.Background(), owner, created.ID)
	require.NoError(t, err)
	encoded, _ := json.Marshal(got)
	require.NotContains(t, string(encoded), "object_keys", "object keys stay private")

	require.ErrorIs(t, svc.Delete(context.Background(), ImageTaskOwner{UserID: 7, APIKeyID: 10}, created.ID), ErrImageTaskNotFound)
	require.NoError(t, svc.Delete(context.Background(), owner, created.ID))
	require.ElementsMatch(t, stored.ObjectKeys, storage.deleted)
	_, err = svc.Get(context.Background(), owner, created.ID)
	require.ErrorIs(t, err, ErrImageTaskNotFound)
}

func TestImageTaskServiceOffloadRetries(t *testing.T) {
	storage := &fakeImageStorage{failures: 2}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	svc, _, _, _, _, _ := newV2ImageTaskService(t, uploader)
	var waits []time.Duration
	svc.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	owner := ImageTaskOwner{UserID: 1, APIKeyID: 2}
	created, err := svc.CreateTask(context.Background(), ImageTaskSpec{Owner: owner, RequestID: "client:o"})
	require.NoError(t, err)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK, json.RawMessage(`{"data":[{"b64_json":"`+b64+`"}]}`)))
	got, err := svc.Get(context.Background(), owner, created.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusCompleted, got.Status)
	require.Equal(t, []time.Duration{time.Second, 3 * time.Second}, waits)
}

func TestUsageBillingRequestIDFromContext(t *testing.T) {
	require.Empty(t, UsageBillingRequestIDFromContext(context.Background()))
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "r-1")
	require.Equal(t, "local:r-1", UsageBillingRequestIDFromContext(ctx))
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "c-1")
	require.Equal(t, "client:c-1", UsageBillingRequestIDFromContext(ctx))
	require.Equal(t, resolveUsageBillingRequestID(ctx, ""), UsageBillingRequestIDFromContext(ctx))
}
