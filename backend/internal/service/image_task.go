package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const (
	ImageTaskStatusProcessing = "processing"
	ImageTaskStatusCompleted  = "completed"
	ImageTaskStatusFailed     = "failed"

	// Billing conclusions exposed on the task view (v2). "charged" means a usage
	// row with the task's request_id exists; "pending" means the gateway holds a
	// durable billing intent that has not settled yet (or the lookup could not
	// run) and the conclusion is refreshed on the next poll.
	ImageTaskBillingPending    = "pending"
	ImageTaskBillingCharged    = "charged"
	ImageTaskBillingNotCharged = "not_charged"

	// error.code values the gateway itself assigns. Upstream failures keep the
	// upstream error object unchanged.
	ImageTaskErrorTimeout       = "timeout"
	ImageTaskErrorInterrupted   = "task_interrupted"
	ImageTaskErrorStorageFailed = "result_storage_failed"

	defaultImageTaskTTL              = 24 * time.Hour
	defaultImageTaskExecutionTimeout = 30 * time.Minute

	// ImageTaskHeartbeatInterval is how often a running task proves its instance
	// is alive; a task whose heartbeat is older than ImageTaskStaleAfter is
	// concluded by the sweeper as task_interrupted.
	ImageTaskHeartbeatInterval = 15 * time.Second
	ImageTaskStaleAfter        = 2 * time.Minute
	imageTaskSweepInterval     = time.Minute
	imageTaskSweepBatch        = 100

	// An idempotency claim that never got bound to a task (instance died between
	// claim and create) expires quickly; a bound key lives as long as the task.
	imageTaskIdempotencyClaimTTL = 2 * time.Minute

	defaultImageTaskMaxInFlightPerInstance = 256
	defaultImageTaskLongPollMax            = 30 * time.Second
	defaultImageTaskPollRatePerSecond      = 10
	defaultImageTaskDrainTimeout           = 10 * time.Minute
)

var (
	ErrImageTaskNotFound    = infraerrors.New(http.StatusNotFound, "IMAGE_TASK_NOT_FOUND", "image task not found")
	ErrImageTaskForbidden   = infraerrors.New(http.StatusForbidden, "IMAGE_TASK_FORBIDDEN", "image task does not belong to this API key")
	ErrImageTaskUnavailable = infraerrors.New(http.StatusServiceUnavailable, "IMAGE_TASK_UNAVAILABLE", "image task storage is unavailable")
	// ErrImageTaskDraining is returned while the instance shuts down. Like every
	// pre-202 failure it means: no task, no generation, no usage.
	ErrImageTaskDraining = infraerrors.New(http.StatusServiceUnavailable, "IMAGE_TASK_UNAVAILABLE", "this instance is shutting down; the task was not submitted")
	ErrImageTaskRunning  = infraerrors.New(http.StatusConflict, "IMAGE_TASK_RUNNING", "image task is still processing; it can be deleted once it has finished")

	ErrImageTaskIdempotencyKeyInvalid = infraerrors.New(http.StatusBadRequest, "invalid_request_error", "Idempotency-Key must be 1-128 characters of A-Z a-z 0-9 . _ : -")
	ErrImageTaskIdempotencyInProgress = infraerrors.New(http.StatusConflict, "idempotency_in_progress", "a request with this Idempotency-Key is still being submitted")
	ErrImageTaskIdempotencyKeyReused  = infraerrors.New(http.StatusUnprocessableEntity, "idempotency_key_reused", "Idempotency-Key was already used with a different request body")
	ErrImageTaskTooMany               = infraerrors.New(http.StatusTooManyRequests, "too_many_tasks", "too many image tasks in progress; retry after some finish")
	ErrImageTaskPollRateLimited       = infraerrors.New(http.StatusTooManyRequests, "too_many_requests", "image task polling is limited to a few requests per second per key")

	// errImageTaskFinished makes a terminal write a no-op: a task that already
	// reached completed/failed never changes again.
	errImageTaskFinished = errors.New("image task already finished")
	// ErrImageTaskUnchanged lets an Update mutation skip the write.
	ErrImageTaskUnchanged = errors.New("image task unchanged")
)

// ImageTaskRecord is the private Redis representation of an asynchronous image
// request. Ownership fields are intentionally omitted from the public view.
type ImageTaskRecord struct {
	ID          string          `json:"id"`
	UserID      int64           `json:"user_id"`
	APIKeyID    int64           `json:"api_key_id"`
	Status      string          `json:"status"`
	HTTPStatus  int             `json:"http_status,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       json.RawMessage `json:"error,omitempty"`
	CreatedAt   int64           `json:"created_at"`
	CompletedAt *int64          `json:"completed_at,omitempty"`
	ExpiresAt   int64           `json:"expires_at"`

	// v2 fields. Tasks written by 0.2.6 simply lack them.
	GroupID        int64             `json:"group_id,omitempty"`
	Platform       string            `json:"platform,omitempty"`
	RequestID      string            `json:"request_id,omitempty"`
	Model          string            `json:"model,omitempty"`
	Endpoint       string            `json:"endpoint,omitempty"`
	SubmitPath     string            `json:"submit_path,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	InstanceID     string            `json:"instance_id,omitempty"`
	StartedAt      *int64            `json:"started_at,omitempty"`
	HeartbeatAt    *int64            `json:"heartbeat_at,omitempty"`
	Billing        *ImageTaskBilling `json:"billing,omitempty"`
	ObjectKeys     []string          `json:"object_keys,omitempty"`
}

// ImageTaskBilling tells the client whether the generation was billed. The
// request_id is the usage_logs.request_id of that generation.
type ImageTaskBilling struct {
	Status    string `json:"status"`
	RequestID string `json:"request_id,omitempty"`
}

// ImageTask is the API-safe task representation returned to callers.
type ImageTask struct {
	ID             string            `json:"id"`
	TaskID         string            `json:"task_id"`
	Object         string            `json:"object"`
	Status         string            `json:"status"`
	HTTPStatus     int               `json:"http_status,omitempty"`
	ImageURL       string            `json:"image_url,omitempty"`
	Result         json.RawMessage   `json:"result,omitempty"`
	Error          json.RawMessage   `json:"error,omitempty"`
	RequestID      string            `json:"request_id,omitempty"`
	Model          string            `json:"model,omitempty"`
	Endpoint       string            `json:"endpoint,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	Billing        *ImageTaskBilling `json:"billing,omitempty"`
	CreatedAt      int64             `json:"created_at"`
	StartedAt      *int64            `json:"started_at,omitempty"`
	HeartbeatAt    *int64            `json:"heartbeat_at,omitempty"`
	CompletedAt    *int64            `json:"completed_at,omitempty"`
	ExpiresAt      int64             `json:"expires_at"`
}

type ImageTaskOwner struct {
	UserID   int64
	APIKeyID int64
}

// ImageTaskSpec describes a task about to be accepted.
type ImageTaskSpec struct {
	ID             string
	Owner          ImageTaskOwner
	GroupID        int64
	Platform       string
	RequestID      string
	Model          string
	Endpoint       string
	SubmitPath     string
	IdempotencyKey string
}

type ImageTaskStore interface {
	Save(ctx context.Context, task *ImageTaskRecord, ttl time.Duration) error
	Get(ctx context.Context, id string) (*ImageTaskRecord, error)
	// Update reads, mutates and writes one task atomically: concurrent writers
	// (heartbeat, finish, sweeper on another instance) never overwrite each
	// other. mutate returning an error aborts the write and is returned as is.
	Update(ctx context.Context, id string, ttl time.Duration, mutate func(*ImageTaskRecord) error) (*ImageTaskRecord, error)
	Delete(ctx context.Context, id string) error
}

// ImageTaskIdempotency is the state behind one Idempotency-Key. TaskID is empty
// while the first submission is still between claim and task creation.
type ImageTaskIdempotency struct {
	Fingerprint string `json:"fingerprint"`
	TaskID      string `json:"task_id,omitempty"`
}

// ImageTaskInFlight identifies one running task in the cross-instance index.
type ImageTaskInFlight struct {
	TaskID   string
	APIKeyID int64
}

// ImageTaskCoordinator holds the cross-instance state of async image tasks:
// idempotency keys, the in-flight index used for admission and the sweeper,
// the sweeper lock and the polling rate limit. The Redis implementation lives
// in the repository package.
type ImageTaskCoordinator interface {
	// ClaimIdempotency stores {fingerprint} under scope when absent and returns
	// claimed=true; otherwise it returns the existing state.
	ClaimIdempotency(ctx context.Context, scope, fingerprint string, ttl time.Duration) (existing ImageTaskIdempotency, claimed bool, err error)
	BindIdempotency(ctx context.Context, scope string, state ImageTaskIdempotency, ttl time.Duration) error
	ReleaseIdempotency(ctx context.Context, scope string) error
	// AdmitInFlight adds the task to the in-flight index unless the key already
	// has limit live tasks (heartbeat at or after liveSince). limit <= 0 means
	// no per-key limit.
	AdmitInFlight(ctx context.Context, apiKeyID int64, taskID string, limit int, now, liveSince time.Time) (bool, error)
	TouchInFlight(ctx context.Context, apiKeyID int64, taskID string, now time.Time) error
	ReleaseInFlight(ctx context.Context, apiKeyID int64, taskID string) error
	StaleInFlight(ctx context.Context, before time.Time, limit int) ([]ImageTaskInFlight, error)
	TryLock(ctx context.Context, name string, ttl time.Duration) (bool, error)
	AllowPoll(ctx context.Context, apiKeyID int64, perSecond int, now time.Time) (bool, error)
}

// ImageTaskBillingLookup answers "was this request billed" from the gateway's
// own ledger: a usage_logs row means charged, an unsettled durable billing
// intent means pending, neither means not charged.
type ImageTaskBillingLookup interface {
	ImageTaskBillingStatus(ctx context.Context, requestID string, apiKeyID int64) (string, error)
}

// ImageTaskOpsRecorder receives failures the request pipeline cannot see
// (storage failure after a 2xx, interruption found by the sweeper).
type ImageTaskOpsRecorder interface {
	RecordError(ctx context.Context, entry *OpsInsertErrorLogInput) error
}

// ImageStorageResolver reports the currently effective object-storage binding.
// It exists so the async image feature can be switched on and off from the admin
// UI without a restart: the wiring below is fixed at startup, but the answer to
// "is object storage configured right now" is re-read (and cached) per call.
type ImageStorageResolver func() (uploader *ImageResultUploader, enabled bool)

// ImageTaskLimits are the operator-tunable bounds of the async image feature.
type ImageTaskLimits struct {
	// MaxInFlightPerKey caps running tasks per API key. 0 derives the cap from
	// the user's concurrency (x2, at least 2).
	MaxInFlightPerKey      int
	MaxInFlightPerInstance int
	LongPollMax            time.Duration
	PollRatePerSecond      int
	DrainTimeout           time.Duration
}

func DefaultImageTaskLimits() ImageTaskLimits {
	return ImageTaskLimits{
		MaxInFlightPerInstance: defaultImageTaskMaxInFlightPerInstance,
		LongPollMax:            defaultImageTaskLongPollMax,
		PollRatePerSecond:      defaultImageTaskPollRatePerSecond,
		DrainTimeout:           defaultImageTaskDrainTimeout,
	}
}

type ImageTaskService struct {
	store            ImageTaskStore
	uploader         *ImageResultUploader
	enabled          bool
	resolve          ImageStorageResolver
	ttl              time.Duration
	executionTimeout time.Duration

	coordinator ImageTaskCoordinator
	billing     ImageTaskBillingLookup
	ops         ImageTaskOpsRecorder
	limits      ImageTaskLimits
	instanceID  string
	now         func() time.Time
	sleep       func(context.Context, time.Duration) error
	// offloadBackoff is the wait before each retry of a failed object-storage
	// upload; len(offloadBackoff) retries follow the first attempt.
	offloadBackoff []time.Duration

	sweepStop chan struct{}
	sweepDone chan struct{}
	sweepOnce sync.Once
	stopOnce  sync.Once
}

func NewImageTaskService(store ImageTaskStore) *ImageTaskService {
	return NewImageTaskServiceWithOptions(store, defaultImageTaskTTL, defaultImageTaskExecutionTimeout)
}

func NewImageTaskServiceWithOptions(store ImageTaskStore, ttl, executionTimeout time.Duration) *ImageTaskService {
	if ttl <= 0 {
		ttl = defaultImageTaskTTL
	}
	if executionTimeout <= 0 {
		executionTimeout = defaultImageTaskExecutionTimeout
	}
	return &ImageTaskService{
		store:            store,
		ttl:              ttl,
		executionTimeout: executionTimeout,
		limits:           DefaultImageTaskLimits(),
		instanceID:       newImageTaskInstanceID(),
		now:              time.Now,
		sleep:            sleepContext,
		offloadBackoff:   []time.Duration{time.Second, 3 * time.Second, 9 * time.Second},
	}
}

// NewImageTaskServiceWithUploader 构造一个已启用的图片任务服务：结果会先经 uploader
// 转存到对象存储再落 Redis。uploader 为 nil 时不做转存（仅用于测试）。
func NewImageTaskServiceWithUploader(store ImageTaskStore, uploader *ImageResultUploader, ttl, executionTimeout time.Duration) *ImageTaskService {
	s := NewImageTaskServiceWithOptions(store, ttl, executionTimeout)
	s.uploader = uploader
	s.enabled = true
	return s
}

// NewImageTaskServiceWithResolver 构造一个由 resolver 决定启用状态的服务：
// 开关与凭证来自后台设置，保存后立即生效，无需重启。
func NewImageTaskServiceWithResolver(store ImageTaskStore, resolve ImageStorageResolver, ttl, executionTimeout time.Duration) *ImageTaskService {
	s := NewImageTaskServiceWithOptions(store, ttl, executionTimeout)
	s.resolve = resolve
	return s
}

// ImageTaskDependencies wires the v2 collaborators. Each one is optional: a
// nil coordinator disables idempotency, admission and the sweeper; a nil
// billing lookup leaves terminal tasks at billing "pending".
type ImageTaskDependencies struct {
	Coordinator ImageTaskCoordinator
	Billing     ImageTaskBillingLookup
	Ops         ImageTaskOpsRecorder
	Limits      *ImageTaskLimits
}

func (s *ImageTaskService) Configure(deps ImageTaskDependencies) *ImageTaskService {
	if s == nil {
		return nil
	}
	s.coordinator = deps.Coordinator
	s.billing = deps.Billing
	s.ops = deps.Ops
	if deps.Limits != nil {
		limits := *deps.Limits
		defaults := DefaultImageTaskLimits()
		if limits.MaxInFlightPerInstance <= 0 {
			limits.MaxInFlightPerInstance = defaults.MaxInFlightPerInstance
		}
		if limits.LongPollMax <= 0 {
			limits.LongPollMax = defaults.LongPollMax
		}
		if limits.LongPollMax > 60*time.Second {
			// Stay far below the 100–125 s proxy/CDN read timeouts.
			limits.LongPollMax = 60 * time.Second
		}
		if limits.PollRatePerSecond < 0 {
			limits.PollRatePerSecond = 0
		}
		if limits.DrainTimeout <= 0 {
			limits.DrainTimeout = defaults.DrainTimeout
		}
		s.limits = limits
	}
	return s
}

// current 返回当前生效的 uploader 与启用状态。
// 注入了 resolver 时以 resolver 为准（后台设置可热切换），否则回落到构造时固定的值。
func (s *ImageTaskService) current() (*ImageResultUploader, bool) {
	if s == nil {
		return nil, false
	}
	if s.resolve != nil {
		return s.resolve()
	}
	return s.uploader, s.enabled
}

// Enabled 表示异步图片任务功能是否可用（总开关 + 凭证齐全）。
// 关闭时 handler 直接返回 404，不创建任务、不写 Redis。
func (s *ImageTaskService) Enabled() bool {
	if s == nil || s.store == nil {
		return false
	}
	_, enabled := s.current()
	return enabled
}

// Pollable 表示已创建的任务能否被查询。
// 比 Enabled 弱：只要 store 可用即可，从而在功能被关掉后仍能取回进行中的任务结果。
func (s *ImageTaskService) Pollable() bool {
	return s != nil && s.store != nil
}

func (s *ImageTaskService) ExecutionTimeout() time.Duration {
	if s == nil || s.executionTimeout <= 0 {
		return defaultImageTaskExecutionTimeout
	}
	return s.executionTimeout
}

func (s *ImageTaskService) TTL() time.Duration {
	if s == nil || s.ttl <= 0 {
		return defaultImageTaskTTL
	}
	return s.ttl
}

func (s *ImageTaskService) Limits() ImageTaskLimits {
	if s == nil {
		return DefaultImageTaskLimits()
	}
	return s.limits
}

// SupportsIdempotency reports whether Idempotency-Key is honoured.
func (s *ImageTaskService) SupportsIdempotency() bool {
	return s != nil && s.coordinator != nil
}

// MaxInFlightForKey is the running-task cap for one API key.
func (s *ImageTaskService) MaxInFlightForKey(userConcurrency int) int {
	if s == nil {
		return 0
	}
	if s.limits.MaxInFlightPerKey > 0 {
		return s.limits.MaxInFlightPerKey
	}
	if userConcurrency > 0 {
		return max(2, userConcurrency*2)
	}
	return 16
}

// NewImageTaskID returns a 128-bit random task id.
func NewImageTaskID() string {
	return "imgtask_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// Create stores a task owned only by user and key (0.2.6 behaviour).
func (s *ImageTaskService) Create(ctx context.Context, owner ImageTaskOwner) (*ImageTask, error) {
	return s.CreateTask(ctx, ImageTaskSpec{Owner: owner})
}

func (s *ImageTaskService) CreateTask(ctx context.Context, spec ImageTaskSpec) (*ImageTask, error) {
	if s == nil || s.store == nil {
		return nil, ErrImageTaskUnavailable
	}
	if spec.ID == "" {
		spec.ID = NewImageTaskID()
	}
	now := s.now().UTC()
	task := &ImageTaskRecord{
		ID:             spec.ID,
		UserID:         spec.Owner.UserID,
		APIKeyID:       spec.Owner.APIKeyID,
		Status:         ImageTaskStatusProcessing,
		CreatedAt:      now.Unix(),
		ExpiresAt:      now.Add(s.ttl).Unix(),
		GroupID:        spec.GroupID,
		Platform:       spec.Platform,
		RequestID:      spec.RequestID,
		Model:          spec.Model,
		Endpoint:       spec.Endpoint,
		SubmitPath:     spec.SubmitPath,
		IdempotencyKey: spec.IdempotencyKey,
		InstanceID:     s.instanceID,
		HeartbeatAt:    unixPtr(now),
	}
	if spec.RequestID != "" {
		task.Billing = &ImageTaskBilling{Status: ImageTaskBillingPending, RequestID: spec.RequestID}
	}
	if err := s.store.Save(ctx, task, s.ttl); err != nil {
		return nil, ErrImageTaskUnavailable.WithCause(err)
	}
	return imageTaskToPublic(task), nil
}

// Discard removes a task that was stored but never started (the submission
// failed after creation). It is only used before any generation began.
func (s *ImageTaskService) Discard(ctx context.Context, id string) {
	if s == nil || s.store == nil {
		return
	}
	if err := s.store.Delete(ctx, id); err != nil {
		logger.L().Warn("image_task.discard_failed", zap.String("task_id", id), zap.Error(err))
	}
}

func (s *ImageTaskService) Get(ctx context.Context, owner ImageTaskOwner, id string) (*ImageTask, error) {
	task, err := s.getOwned(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	return imageTaskToPublic(s.refreshBilling(ctx, task)), nil
}

func (s *ImageTaskService) getOwned(ctx context.Context, owner ImageTaskOwner, id string) (*ImageTaskRecord, error) {
	if s == nil || s.store == nil {
		return nil, ErrImageTaskUnavailable
	}
	task, err := s.store.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		if errors.Is(err, ErrImageTaskNotFound) {
			return nil, ErrImageTaskNotFound
		}
		return nil, ErrImageTaskUnavailable.WithCause(err)
	}
	if task.UserID != owner.UserID || task.APIKeyID != owner.APIKeyID {
		// Do not reveal whether a random task ID exists for another caller.
		return nil, ErrImageTaskNotFound
	}
	return task, nil
}

// refreshBilling re-reads the ledger for a terminal task whose conclusion was
// still pending (an unsettled billing intent, or the lookup failed at finish).
func (s *ImageTaskService) refreshBilling(ctx context.Context, task *ImageTaskRecord) *ImageTaskRecord {
	if task == nil || task.Status == ImageTaskStatusProcessing || task.Billing == nil ||
		task.Billing.Status != ImageTaskBillingPending || task.Billing.RequestID == "" || s.billing == nil {
		return task
	}
	status, err := s.billing.ImageTaskBillingStatus(ctx, task.Billing.RequestID, task.APIKeyID)
	if err != nil || status == ImageTaskBillingPending {
		return task
	}
	updated, err := s.store.Update(ctx, task.ID, s.ttl, func(rec *ImageTaskRecord) error {
		if rec.Billing == nil || rec.Billing.Status != ImageTaskBillingPending {
			return ErrImageTaskUnchanged
		}
		rec.Billing.Status = status
		return nil
	})
	if err != nil {
		task.Billing.Status = status
		return task
	}
	return updated
}

// Start marks the task as running on this instance.
func (s *ImageTaskService) Start(ctx context.Context, id string) {
	if s == nil || s.store == nil {
		return
	}
	now := s.now().UTC()
	_, err := s.store.Update(ctx, id, s.ttl, func(rec *ImageTaskRecord) error {
		if rec.Status != ImageTaskStatusProcessing {
			return errImageTaskFinished
		}
		rec.StartedAt = unixPtr(now)
		rec.HeartbeatAt = unixPtr(now)
		rec.InstanceID = s.instanceID
		return nil
	})
	if err != nil && !errors.Is(err, errImageTaskFinished) {
		logger.L().Warn("image_task.start_store_failed", zap.String("task_id", id), zap.Error(err))
	}
}

// Heartbeat proves the task's instance is alive.
func (s *ImageTaskService) Heartbeat(ctx context.Context, id string, apiKeyID int64) {
	if s == nil || s.store == nil {
		return
	}
	now := s.now().UTC()
	_, err := s.store.Update(ctx, id, s.ttl, func(rec *ImageTaskRecord) error {
		if rec.Status != ImageTaskStatusProcessing {
			return errImageTaskFinished
		}
		rec.HeartbeatAt = unixPtr(now)
		rec.InstanceID = s.instanceID
		return nil
	})
	if err != nil && !errors.Is(err, errImageTaskFinished) {
		logger.L().Warn("image_task.heartbeat_store_failed", zap.String("task_id", id), zap.Error(err))
	}
	if s.coordinator != nil {
		if err := s.coordinator.TouchInFlight(ctx, apiKeyID, id, now); err != nil {
			logger.L().Warn("image_task.heartbeat_index_failed", zap.String("task_id", id), zap.Error(err))
		}
	}
}

// ClaimIdempotency claims an Idempotency-Key scope for a new submission, or
// returns the state left by the earlier submission with the same key.
func (s *ImageTaskService) ClaimIdempotency(ctx context.Context, scope, fingerprint string) (ImageTaskIdempotency, bool, error) {
	if s == nil || s.coordinator == nil {
		return ImageTaskIdempotency{}, true, nil
	}
	existing, claimed, err := s.coordinator.ClaimIdempotency(ctx, scope, fingerprint, imageTaskIdempotencyClaimTTL)
	if err != nil {
		return ImageTaskIdempotency{}, false, ErrImageTaskUnavailable.WithCause(err)
	}
	return existing, claimed, nil
}

// BindIdempotency records which task a claimed key produced. The binding
// lives as long as the task can be polled.
func (s *ImageTaskService) BindIdempotency(ctx context.Context, scope, fingerprint, taskID string) error {
	if s == nil || s.coordinator == nil {
		return nil
	}
	if err := s.coordinator.BindIdempotency(ctx, scope, ImageTaskIdempotency{Fingerprint: fingerprint, TaskID: taskID}, s.ttl); err != nil {
		return ErrImageTaskUnavailable.WithCause(err)
	}
	return nil
}

// ReleaseIdempotency frees a claimed key whose submission did not produce a
// task, so the client may retry with the same key.
func (s *ImageTaskService) ReleaseIdempotency(ctx context.Context, scope string) {
	if s == nil || s.coordinator == nil {
		return
	}
	if err := s.coordinator.ReleaseIdempotency(ctx, scope); err != nil {
		logger.L().Warn("image_task.idempotency_release_failed", zap.Error(err))
	}
}

// Admit reserves a running-task slot for the key across instances.
func (s *ImageTaskService) Admit(ctx context.Context, apiKeyID int64, taskID string, limit int) error {
	if s == nil || s.coordinator == nil {
		return nil
	}
	now := s.now().UTC()
	admitted, err := s.coordinator.AdmitInFlight(ctx, apiKeyID, taskID, limit, now, now.Add(-ImageTaskStaleAfter))
	if err != nil {
		return ErrImageTaskUnavailable.WithCause(err)
	}
	if !admitted {
		return ErrImageTaskTooMany
	}
	return nil
}

// ReleaseAdmission frees a slot reserved by Admit for a task that never ran.
func (s *ImageTaskService) ReleaseAdmission(ctx context.Context, apiKeyID int64, taskID string) {
	if s == nil || s.coordinator == nil {
		return
	}
	if err := s.coordinator.ReleaseInFlight(ctx, apiKeyID, taskID); err != nil {
		logger.L().Warn("image_task.release_in_flight_failed", zap.String("task_id", taskID), zap.Error(err))
	}
}

// AllowPoll applies the per-key polling rate limit. Coordinator errors fail
// open: polling must keep working when the limiter cannot be reached.
func (s *ImageTaskService) AllowPoll(ctx context.Context, apiKeyID int64) bool {
	if s == nil || s.coordinator == nil || s.limits.PollRatePerSecond <= 0 {
		return true
	}
	allowed, err := s.coordinator.AllowPoll(ctx, apiKeyID, s.limits.PollRatePerSecond, s.now())
	return err != nil || allowed
}

// Complete stores a successful generation. Images are offloaded to object
// storage first (retried with backoff); if every attempt fails the task fails
// with result_storage_failed. Billing is independently read from the ledger.
func (s *ImageTaskService) Complete(ctx context.Context, id string, statusCode int, result json.RawMessage) error {
	if !json.Valid(result) {
		return s.FailWithCode(ctx, id, http.StatusBadGateway, imageTaskErrorJSON("api_error", "upstream returned a non-JSON image response"), "")
	}
	uploader, _ := s.current()
	if s.resolve != nil && uploader == nil {
		// Settings can change after the 202. Never bypass required object
		// storage and persist an unbounded base64 response in Redis.
		failErr := s.finishFailed(ctx, id, http.StatusBadGateway,
			imageTaskCodedErrorJSON("api_error", ImageTaskErrorStorageFailed, "object storage became unavailable before the image result could be stored"), nil)
		s.recordOps(ctx, id, http.StatusBadGateway, ImageTaskErrorStorageFailed, "async image object storage became unavailable")
		return failErr
	}
	var keys []string
	if uploader != nil {
		var rewritten json.RawMessage
		var err error
		for attempt := 0; ; attempt++ {
			var attemptKeys []string
			rewritten, attemptKeys, err = uploader.RewriteWithKeys(ctx, id, result)
			for _, key := range attemptKeys {
				if !slices.Contains(keys, key) {
					keys = append(keys, key)
				}
			}
			if err == nil {
				break
			}
			if attempt >= len(s.offloadBackoff) {
				break
			}
			logger.L().Warn("image_task.offload_retry", zap.String("task_id", id), zap.Int("attempt", attempt+1), zap.Error(err))
			if sleepErr := s.sleep(ctx, s.offloadBackoff[attempt]); sleepErr != nil {
				break
			}
		}
		if err != nil {
			// 转存失败不回退存 base64，避免大 blob 撑爆 Redis：直接把任务标记为失败。
			logger.L().Error("image_task.offload_failed", zap.String("task_id", id), zap.Error(err))
			failErr := s.finishFailed(ctx, id, http.StatusBadGateway,
				imageTaskCodedErrorJSON("api_error", ImageTaskErrorStorageFailed, "the image was generated, but storing it to object storage failed; see billing.status for the billing outcome"),
				keys)
			s.recordOps(ctx, id, http.StatusBadGateway, ImageTaskErrorStorageFailed, "async image result could not be stored to object storage")
			return failErr
		}
		result = rewritten
	}
	billing := s.lookupBilling(ctx, id)
	_, err := s.finish(ctx, id, func(rec *ImageTaskRecord, now time.Time) {
		rec.Status = ImageTaskStatusCompleted
		rec.HTTPStatus = statusCode
		rec.Result = result
		rec.Error = nil
		rec.ObjectKeys = keys
		if rec.RequestID != "" {
			rec.Billing = &ImageTaskBilling{Status: billing, RequestID: rec.RequestID}
		}
	})
	return err
}

// Fail stores a failed generation (0.2.6 signature).
func (s *ImageTaskService) Fail(ctx context.Context, id string, statusCode int, taskErr json.RawMessage) error {
	return s.FailWithCode(ctx, id, statusCode, taskErr, "")
}

// FailWithCode stores a failed generation. code is one of the gateway error
// codes (timeout, task_interrupted, ...) or empty for an upstream failure; the
// billing conclusion is read from the ledger.
func (s *ImageTaskService) FailWithCode(ctx context.Context, id string, statusCode int, taskErr json.RawMessage, code string) error {
	if !json.Valid(taskErr) {
		taskErr = imageTaskErrorJSON("api_error", "image generation failed")
	}
	if code != "" {
		taskErr = withImageTaskErrorCode(taskErr, code)
	}
	return s.finishFailed(ctx, id, statusCode, taskErr, nil)
}

// finishFailed resolves billing and retains any partial uploads for DELETE.
func (s *ImageTaskService) finishFailed(ctx context.Context, id string, statusCode int, taskErr json.RawMessage, objectKeys []string) error {
	billing := s.lookupBilling(ctx, id)
	_, err := s.finish(ctx, id, func(rec *ImageTaskRecord, now time.Time) {
		rec.Status = ImageTaskStatusFailed
		rec.HTTPStatus = statusCode
		rec.Result = nil
		rec.Error = taskErr
		rec.ObjectKeys = objectKeys
		if rec.RequestID != "" {
			rec.Billing = &ImageTaskBilling{Status: billing, RequestID: rec.RequestID}
		}
	})
	return err
}

func (s *ImageTaskService) lookupBilling(ctx context.Context, id string) string {
	if s.billing == nil || s.store == nil {
		return ImageTaskBillingPending
	}
	task, err := s.store.Get(ctx, id)
	if err != nil || task.RequestID == "" {
		return ImageTaskBillingPending
	}
	status, err := s.billing.ImageTaskBillingStatus(ctx, task.RequestID, task.APIKeyID)
	if err != nil {
		logger.L().Warn("image_task.billing_lookup_failed", zap.String("task_id", id), zap.Error(err))
		return ImageTaskBillingPending
	}
	return status
}

func (s *ImageTaskService) finish(ctx context.Context, id string, apply func(*ImageTaskRecord, time.Time)) (*ImageTaskRecord, error) {
	if s == nil || s.store == nil {
		return nil, ErrImageTaskUnavailable
	}
	now := s.now().UTC()
	task, err := s.store.Update(ctx, id, s.ttl, func(rec *ImageTaskRecord) error {
		if rec.Status != ImageTaskStatusProcessing {
			return errImageTaskFinished
		}
		apply(rec, now)
		completedAt := now.Unix()
		rec.CompletedAt = &completedAt
		rec.ExpiresAt = now.Add(s.ttl).Unix()
		return nil
	})
	if err != nil {
		if errors.Is(err, errImageTaskFinished) {
			// Another writer (normally the sweeper after a lost heartbeat) already
			// concluded the task. The first conclusion stands.
			logger.L().Warn("image_task.late_result_dropped", zap.String("task_id", id))
			return nil, nil
		}
		if errors.Is(err, ErrImageTaskNotFound) {
			return nil, ErrImageTaskNotFound
		}
		return nil, ErrImageTaskUnavailable.WithCause(err)
	}
	s.ReleaseAdmission(context.WithoutCancel(ctx), task.APIKeyID, task.ID)
	logImageTaskFinished(task)
	return task, nil
}

// Delete removes a finished task's stored images and its record. A running
// task cannot be deleted: deleting is not cancelling, the generation continues
// and is billed regardless.
func (s *ImageTaskService) Delete(ctx context.Context, owner ImageTaskOwner, id string) error {
	task, err := s.getOwned(ctx, owner, id)
	if err != nil {
		return err
	}
	if task.Status == ImageTaskStatusProcessing {
		return ErrImageTaskRunning
	}
	if len(task.ObjectKeys) > 0 {
		uploader, _ := s.current()
		if uploader == nil || !uploader.CanDelete() {
			return ErrImageTaskUnavailable.WithCause(errors.New("object storage is not configured for deletes"))
		}
		if err := uploader.DeleteObjects(ctx, task.ObjectKeys); err != nil {
			return ErrImageTaskUnavailable.WithCause(err)
		}
	}
	if err := s.store.Delete(ctx, task.ID); err != nil {
		return ErrImageTaskUnavailable.WithCause(err)
	}
	logger.L().Info("image_task.deleted", zap.String("task_id", task.ID), zap.String("request_id", task.RequestID),
		zap.Int64("api_key_id", task.APIKeyID), zap.Int("objects", len(task.ObjectKeys)))
	return nil
}

// StartSweeper concludes tasks whose instance stopped heartbeating. Every
// instance runs it; a short Redis lock keeps one sweep at a time.
func (s *ImageTaskService) StartSweeper() {
	if s == nil || s.coordinator == nil || s.store == nil {
		return
	}
	s.sweepOnce.Do(func() {
		s.sweepStop = make(chan struct{})
		s.sweepDone = make(chan struct{})
		go func() {
			defer close(s.sweepDone)
			ticker := time.NewTicker(imageTaskSweepInterval)
			defer ticker.Stop()
			for {
				select {
				case <-s.sweepStop:
					return
				case <-ticker.C:
					ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
					s.Sweep(ctx)
					cancel()
				}
			}
		}()
	})
}

func (s *ImageTaskService) StopSweeper() {
	if s == nil || s.sweepStop == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.sweepStop)
		<-s.sweepDone
	})
}

// Sweep concludes stale tasks once. It returns how many were concluded.
func (s *ImageTaskService) Sweep(ctx context.Context) int {
	if s == nil || s.coordinator == nil || s.store == nil {
		return 0
	}
	locked, err := s.coordinator.TryLock(ctx, "sweeper", imageTaskSweepInterval-5*time.Second)
	if err != nil || !locked {
		return 0
	}
	now := s.now().UTC()
	staleBefore := now.Add(-ImageTaskStaleAfter)
	stale, err := s.coordinator.StaleInFlight(ctx, staleBefore, imageTaskSweepBatch)
	if err != nil {
		logger.L().Warn("image_task.sweep_index_failed", zap.Error(err))
		return 0
	}
	concluded := 0
	for _, item := range stale {
		task, err := s.store.Get(ctx, item.TaskID)
		if errors.Is(err, ErrImageTaskNotFound) || (err == nil && task.Status != ImageTaskStatusProcessing) {
			s.ReleaseAdmission(ctx, item.APIKeyID, item.TaskID)
			continue
		}
		if err != nil {
			continue
		}
		if !imageTaskHeartbeatStale(task, staleBefore) {
			continue
		}
		billing := ImageTaskBillingPending
		if task.RequestID != "" && s.billing != nil {
			if status, lookupErr := s.billing.ImageTaskBillingStatus(ctx, task.RequestID, task.APIKeyID); lookupErr == nil {
				billing = status
			}
		}
		taskErr := imageTaskCodedErrorJSON("api_error", ImageTaskErrorInterrupted, "the gateway instance running this task stopped before it finished")
		_, err = s.store.Update(ctx, task.ID, s.ttl, func(rec *ImageTaskRecord) error {
			if rec.Status != ImageTaskStatusProcessing || !imageTaskHeartbeatStale(rec, staleBefore) {
				return errImageTaskFinished
			}
			completedAt := now.Unix()
			rec.Status = ImageTaskStatusFailed
			rec.HTTPStatus = http.StatusBadGateway
			rec.Error = taskErr
			rec.CompletedAt = &completedAt
			rec.ExpiresAt = now.Add(s.ttl).Unix()
			if rec.RequestID != "" {
				rec.Billing = &ImageTaskBilling{Status: billing, RequestID: rec.RequestID}
			}
			return nil
		})
		if err != nil {
			continue
		}
		s.ReleaseAdmission(ctx, item.APIKeyID, item.TaskID)
		concluded++
		logger.L().Warn("image_task.interrupted",
			zap.String("task_id", task.ID), zap.String("request_id", task.RequestID),
			zap.Int64("user_id", task.UserID), zap.Int64("api_key_id", task.APIKeyID), zap.Int64("group_id", task.GroupID),
			zap.String("model", task.Model), zap.String("endpoint", task.Endpoint), zap.String("instance_id", task.InstanceID),
			zap.String("billing_status", billing))
		s.recordOpsForTask(ctx, task, http.StatusBadGateway, ImageTaskErrorInterrupted, "async image task interrupted: its gateway instance stopped heartbeating")
	}
	return concluded
}

func imageTaskHeartbeatStale(task *ImageTaskRecord, staleBefore time.Time) bool {
	last := task.CreatedAt
	if task.HeartbeatAt != nil {
		last = *task.HeartbeatAt
	}
	return last < staleBefore.Unix()
}

func (s *ImageTaskService) recordOps(ctx context.Context, id string, status int, code, message string) {
	if s == nil || s.ops == nil || s.store == nil {
		return
	}
	task, err := s.store.Get(ctx, id)
	if err != nil {
		return
	}
	s.recordOpsForTask(ctx, task, status, code, message)
}

// recordOpsForTask writes an ops_error_logs row for a failure that happened
// outside the request pipeline, so the admin error log shows it.
func (s *ImageTaskService) recordOpsForTask(ctx context.Context, task *ImageTaskRecord, status int, code, message string) {
	if s == nil || s.ops == nil || task == nil {
		return
	}
	entry := &OpsInsertErrorLogInput{
		ClientRequestID: strings.TrimPrefix(task.RequestID, "client:"),
		Platform:        task.Platform,
		Model:           task.Model,
		RequestedModel:  task.Model,
		RequestPath:     task.SubmitPath,
		ErrorPhase:      "internal",
		ErrorType:       "api_error",
		Severity:        "P1",
		StatusCode:      status,
		ErrorMessage:    message + " (" + code + ", task " + task.ID + ")",
		ErrorSource:     "gateway",
		ErrorOwner:      "platform",
		CreatedAt:       s.now().UTC(),
	}
	if task.UserID > 0 {
		entry.UserID = &task.UserID
	}
	if task.APIKeyID > 0 {
		entry.APIKeyID = &task.APIKeyID
	}
	if task.GroupID > 0 {
		entry.GroupID = &task.GroupID
	}
	if task.StartedAt != nil {
		duration := (s.now().Unix() - *task.StartedAt) * 1000
		entry.ResponseLatencyMs = &duration
	}
	if err := s.ops.RecordError(context.WithoutCancel(ctx), entry); err != nil {
		logger.L().Warn("image_task.ops_record_failed", zap.String("task_id", task.ID), zap.Error(err))
	}
}

func logImageTaskFinished(task *ImageTaskRecord) {
	if task == nil {
		return
	}
	billing, code := "", imageTaskErrorCode(task.Error)
	if task.Billing != nil {
		billing = task.Billing.Status
	}
	fields := []zap.Field{
		zap.String("task_id", task.ID), zap.String("request_id", task.RequestID),
		zap.Int64("user_id", task.UserID), zap.Int64("api_key_id", task.APIKeyID), zap.Int64("group_id", task.GroupID),
		zap.String("model", task.Model), zap.String("endpoint", task.Endpoint),
		zap.Int("http_status", task.HTTPStatus), zap.String("error_code", code), zap.String("billing_status", billing),
	}
	if task.StartedAt != nil && task.CompletedAt != nil {
		fields = append(fields, zap.Int64("duration_ms", (*task.CompletedAt-*task.StartedAt)*1000))
	}
	if task.Status == ImageTaskStatusCompleted {
		logger.L().Info("image_task.completed", fields...)
		return
	}
	logger.L().Warn("image_task.failed", fields...)
}

func imageTaskToPublic(task *ImageTaskRecord) *ImageTask {
	if task == nil {
		return nil
	}
	var billing *ImageTaskBilling
	if task.Billing != nil {
		copied := *task.Billing
		billing = &copied
	}
	return &ImageTask{
		ID:             task.ID,
		TaskID:         task.ID,
		Object:         "image.generation.task",
		Status:         task.Status,
		HTTPStatus:     task.HTTPStatus,
		ImageURL:       firstImageTaskURL(task.Result),
		Result:         task.Result,
		Error:          task.Error,
		RequestID:      task.RequestID,
		Model:          task.Model,
		Endpoint:       task.Endpoint,
		IdempotencyKey: task.IdempotencyKey,
		Billing:        billing,
		CreatedAt:      task.CreatedAt,
		StartedAt:      task.StartedAt,
		HeartbeatAt:    task.HeartbeatAt,
		CompletedAt:    task.CompletedAt,
		ExpiresAt:      task.ExpiresAt,
	}
}

func firstImageTaskURL(result json.RawMessage) string {
	if len(result) == 0 || !json.Valid(result) {
		return ""
	}
	var response struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if json.Unmarshal(result, &response) != nil || len(response.Data) == 0 {
		return ""
	}
	return strings.TrimSpace(response.Data[0].URL)
}

func imageTaskErrorJSON(errorType, message string) json.RawMessage {
	data, _ := json.Marshal(map[string]string{"type": errorType, "message": message})
	return data
}

func imageTaskCodedErrorJSON(errorType, code, message string) json.RawMessage {
	data, _ := json.Marshal(map[string]string{"type": errorType, "code": code, "message": message})
	return data
}

// withImageTaskErrorCode sets error.code on an error object, keeping every
// other field. A non-object error is wrapped.
func withImageTaskErrorCode(taskErr json.RawMessage, code string) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(taskErr, &fields) != nil || fields == nil {
		return imageTaskCodedErrorJSON("api_error", code, "image generation failed")
	}
	raw, _ := json.Marshal(code)
	fields["code"] = raw
	out, err := json.Marshal(fields)
	if err != nil {
		return taskErr
	}
	return out
}

func imageTaskErrorCode(taskErr json.RawMessage) string {
	if len(taskErr) == 0 {
		return ""
	}
	var fields struct {
		Code any `json:"code"`
	}
	if json.Unmarshal(taskErr, &fields) != nil {
		return ""
	}
	if code, ok := fields.Code.(string); ok {
		return code
	}
	return ""
}

// UsageBillingRequestIDFromContext returns the usage_logs.request_id a
// generation started from ctx will be billed under (client:<id> or local:<id>,
// the same precedence as resolveUsageBillingRequestID). Empty when ctx carries
// neither id, because the billed id would then be generated later.
func UsageBillingRequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, _ := ctx.Value(ctxkey.ClientRequestID).(string); strings.TrimSpace(id) != "" {
		return "client:" + strings.TrimSpace(id)
	}
	if id, _ := ctx.Value(ctxkey.RequestID).(string); strings.TrimSpace(id) != "" {
		return "local:" + strings.TrimSpace(id)
	}
	return ""
}

func unixPtr(t time.Time) *int64 {
	v := t.Unix()
	return &v
}

func newImageTaskInstanceID() string {
	host, _ := os.Hostname()
	var suffix [4]byte
	_, _ = rand.Read(suffix[:])
	host = strings.TrimSpace(host)
	if host == "" {
		host = "instance"
	}
	if len(host) > 48 {
		host = host[:48]
	}
	return host + "-" + hex.EncodeToString(suffix[:])
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
