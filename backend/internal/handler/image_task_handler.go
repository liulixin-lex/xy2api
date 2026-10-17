package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	pkghttputil "github.com/liulixin-lex/xy2api/internal/pkg/httputil"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	middleware2 "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"go.uber.org/zap"
)

// errImageTaskShutdown is the cancellation cause of tasks still running when
// the drain timeout of a graceful shutdown expires.
var errImageTaskShutdown = errors.New("gateway instance shutting down")

var imageTaskIdempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type AsyncImageHandler struct {
	tasks   *service.ImageTaskService
	openAI  *OpenAIGatewayHandler
	ops     *service.OpsService
	execute func(platform string, c *gin.Context)

	// Tasks running on this instance: counted for the per-instance cap, waited
	// for on shutdown, and cancellable when the drain timeout expires.
	running  atomic.Int64
	wg       sync.WaitGroup
	draining atomic.Bool
	mu       sync.Mutex
	cancels  map[string]context.CancelCauseFunc
}

func NewAsyncImageHandler(tasks *service.ImageTaskService, openAI *OpenAIGatewayHandler, ops *service.OpsService) *AsyncImageHandler {
	h := &AsyncImageHandler{tasks: tasks, openAI: openAI, ops: ops}
	h.execute = h.executeWithGateway
	return h
}

// enabled reports whether the async image task feature is available. Object
// storage is the enablement gate: without it the endpoints are fully disabled
// so that large base64 results never land in Redis.
func (h *AsyncImageHandler) enabled() bool {
	return h != nil && h.tasks != nil && h.tasks.Enabled()
}

// pollable reports whether task lookups can be served. It is deliberately weaker
// than enabled(): results already written to Redis stay readable after the
// feature is switched off, so an in-flight task is never stranded.
func (h *AsyncImageHandler) pollable() bool {
	return h != nil && h.tasks != nil && h.tasks.Pollable()
}

// HandlePreferAsync serves POST /v1/images/{generations,edits} carrying
// `Prefer: respond-async` (RFC 7240) as an async submission. It returns false,
// leaving the request to the synchronous handler, when the preference is
// absent or async tasks are not enabled — a preference may always be ignored,
// and the client tells the two apart by 202 versus 200.
func (h *AsyncImageHandler) HandlePreferAsync(c *gin.Context) bool {
	if h == nil || !preferRespondAsync(c.GetHeader("Prefer")) || !h.enabled() {
		return false
	}
	c.Header("Preference-Applied", "respond-async")
	h.Submit(c)
	return true
}

// Submit accepts the same payload as the synchronous Images endpoint and
// returns before the upstream image generation begins. Every failure before
// the 202 means: no task, no generation, no usage.
func (h *AsyncImageHandler) Submit(c *gin.Context) {
	if !h.enabled() {
		imageTaskJSONError(c, http.StatusNotFound, "not_found_error", "async image tasks are not enabled")
		return
	}
	if h.draining.Load() {
		imageTaskError(c, service.ErrImageTaskDraining)
		return
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.UserID <= 0 || apiKey.ID <= 0 {
		imageTaskError(c, service.ErrImageTaskForbidden)
		return
	}
	platform := ""
	if apiKey.Group != nil {
		platform = apiKey.Group.Platform
	}
	if platform != service.PlatformOpenAI && platform != service.PlatformGrok {
		imageTaskJSONError(c, http.StatusNotFound, "not_found_error", "Images API is not supported for this platform")
		return
	}
	if !service.GroupAllowsImageGeneration(apiKey.Group) {
		imageTaskJSONError(c, http.StatusForbidden, "permission_error", service.ImageGenerationPermissionMessage())
		return
	}
	if h == nil || h.tasks == nil || h.execute == nil {
		imageTaskError(c, service.ErrImageTaskUnavailable)
		return
	}

	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idempotencyKey != "" && !imageTaskIdempotencyKeyPattern.MatchString(idempotencyKey) {
		imageTaskError(c, service.ErrImageTaskIdempotencyKeyInvalid)
		return
	}

	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			imageTaskJSONError(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		imageTaskJSONError(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		imageTaskJSONError(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}
	if asyncImageRequestStreams(c.GetHeader("Content-Type"), body) {
		imageTaskJSONError(c, http.StatusBadRequest, "invalid_request_error", "streaming image requests cannot be submitted as asynchronous tasks")
		return
	}
	model, err := h.validateRequest(c, platform, body)
	if err != nil {
		imageTaskJSONError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	endpoint := imageTaskEndpoint(c.Request.URL.Path)

	// Idempotency: a replay returns the first task and never starts a second
	// generation, so it is resolved before moderation and admission.
	var idemScope, fingerprint string
	if idempotencyKey != "" && h.tasks.SupportsIdempotency() {
		idemScope = imageTaskIdempotencyScope(apiKey.UserID, apiKey.ID, idempotencyKey)
		fingerprint = imageTaskFingerprint(endpoint, c.GetHeader("Content-Type"), body)
		existing, claimed, claimErr := h.tasks.ClaimIdempotency(c.Request.Context(), idemScope, fingerprint)
		if claimErr != nil {
			imageTaskError(c, claimErr)
			return
		}
		if !claimed {
			h.replayIdempotent(c, apiKey, existing, fingerprint)
			return
		}
	} else if idempotencyKey != "" {
		logger.L().Warn("image_task.idempotency_unavailable", zap.Int64("api_key_id", apiKey.ID))
	}
	submitted := false
	defer func() {
		if !submitted && idemScope != "" {
			h.tasks.ReleaseIdempotency(context.WithoutCancel(c.Request.Context()), idemScope)
		}
	}()

	// Admission: per-instance cap first (cheap, local), then the per-key cap
	// shared by all instances. xy2api resilience: admission runs before the
	// content audit so a rejected re-submission costs no moderation call
	// (design §9.6); a failed audit releases the admission below.
	if h.running.Add(1) > int64(h.tasks.Limits().MaxInFlightPerInstance) {
		h.running.Add(-1)
		c.Header("Retry-After", "5")
		imageTaskError(c, service.ErrImageTaskTooMany)
		return
	}
	defer func() {
		if !submitted {
			h.running.Add(-1)
		}
	}()
	concurrency := 0
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok {
		concurrency = subject.Concurrency
	}
	taskID := service.NewImageTaskID()
	if err := h.tasks.Admit(c.Request.Context(), apiKey.ID, taskID, h.tasks.MaxInFlightForKey(concurrency)); err != nil {
		if errors.Is(err, service.ErrImageTaskTooMany) {
			c.Header("Retry-After", "5")
		}
		imageTaskError(c, err)
		return
	}
	defer func() {
		if !submitted {
			h.tasks.ReleaseAdmission(context.WithoutCancel(c.Request.Context()), apiKey.ID, taskID)
		}
	}()

	if !h.checkSecurityAuditBeforeSubmit(c, apiKey, platform, body) {
		return
	}

	requestID := service.UsageBillingRequestIDFromContext(c.Request.Context())
	job := newAsyncImageJob(c, body, h.tasks.ExecutionTimeout())
	var groupID int64
	if apiKey.GroupID != nil {
		groupID = *apiKey.GroupID
	}
	spec := service.ImageTaskSpec{
		ID:         taskID,
		Owner:      service.ImageTaskOwner{UserID: apiKey.UserID, APIKeyID: apiKey.ID},
		GroupID:    groupID,
		Platform:   platform,
		RequestID:  requestID,
		Model:      model,
		Endpoint:   endpoint,
		SubmitPath: c.Request.URL.Path,
	}
	if idemScope != "" {
		spec.IdempotencyKey = idempotencyKey
	}
	task, err := h.tasks.CreateTask(c.Request.Context(), spec)
	if err != nil {
		job.cancel(nil)
		imageTaskError(c, err)
		return
	}
	if idemScope != "" {
		if err := h.tasks.BindIdempotency(c.Request.Context(), idemScope, fingerprint, task.ID); err != nil {
			// Without the binding a retry could start a second generation; the
			// task has not started, so withdraw it and report "not submitted".
			job.cancel(nil)
			h.tasks.Discard(context.WithoutCancel(c.Request.Context()), task.ID)
			imageTaskError(c, err)
			return
		}
	}

	pollURL := imageTaskPollURL(c.Request.URL.Path, task.ID)
	c.Header("Cache-Control", "no-store")
	c.Header("Location", pollURL)
	c.Header("Retry-After", "3")
	c.JSON(http.StatusAccepted, imageTaskAcceptedBody(task, pollURL))

	submitted = true
	h.wg.Add(1)
	h.registerCancel(task.ID, job.cancel)
	logger.L().Info("image_task.submitted",
		zap.String("task_id", task.ID), zap.String("request_id", requestID),
		zap.Int64("user_id", apiKey.UserID), zap.Int64("api_key_id", apiKey.ID), zap.Int64("group_id", groupID),
		zap.String("model", model), zap.String("endpoint", endpoint), zap.Bool("idempotent", idemScope != ""))
	go h.run(task.ID, apiKey.ID, platform, job)
}

// replayIdempotent answers a submission whose Idempotency-Key was seen before.
func (h *AsyncImageHandler) replayIdempotent(c *gin.Context, apiKey *service.APIKey, existing service.ImageTaskIdempotency, fingerprint string) {
	if existing.Fingerprint != fingerprint {
		imageTaskError(c, service.ErrImageTaskIdempotencyKeyReused)
		return
	}
	if existing.TaskID == "" {
		c.Header("Retry-After", "1")
		imageTaskError(c, service.ErrImageTaskIdempotencyInProgress)
		return
	}
	task, err := h.tasks.Get(c.Request.Context(), service.ImageTaskOwner{UserID: apiKey.UserID, APIKeyID: apiKey.ID}, existing.TaskID)
	if err != nil {
		imageTaskError(c, err)
		return
	}
	pollURL := imageTaskPollURL(c.Request.URL.Path, task.ID)
	c.Header("Cache-Control", "no-store")
	c.Header("Location", pollURL)
	c.Header("Idempotent-Replayed", "true")
	if task.Status == service.ImageTaskStatusProcessing {
		c.Header("Retry-After", "3")
	}
	if task.RequestID != "" {
		// The replay belongs to the original generation; its request id is the
		// one usage was (or will be) recorded under.
		c.Header("X-Client-Request-ID", strings.TrimPrefix(task.RequestID, "client:"))
	}
	c.JSON(http.StatusOK, imageTaskReplayBody(task, pollURL))
}

func (h *AsyncImageHandler) checkSecurityAuditBeforeSubmit(c *gin.Context, apiKey *service.APIKey, platform string, body []byte) bool {
	if h == nil || h.openAI == nil {
		return true
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		imageTaskJSONError(c, http.StatusInternalServerError, "api_error", "User context not found")
		return false
	}
	model := ""
	moderationBody := body
	if platform == service.PlatformGrok {
		parsed := service.ParseGrokMediaRequest(c.GetHeader("Content-Type"), body)
		model, moderationBody = parsed.Model, parsed.ModerationBody()
	} else if h.openAI.gatewayService != nil {
		parsed, err := h.openAI.gatewayService.ParseOpenAIImagesRequest(c, body)
		if err != nil {
			imageTaskJSONError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
			return false
		}
		model, moderationBody = parsed.Model, parsed.ModerationBody()
	}
	if len(moderationBody) == 0 {
		c.Set(securityAuditCompletedContextKey, true)
		return true
	}
	reqLog := requestLogger(c, "handler.async_image.security_audit",
		zap.Int64("user_id", subject.UserID), zap.Int64("api_key_id", apiKey.ID), zap.String("model", model))
	decision := h.openAI.checkSecurityAudit(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIImages, model, moderationBody)
	if decision != nil && !decision.AllowNextStage {
		h.openAI.openAISecurityAuditError(c, decision)
		return false
	}
	return true
}

// Get returns the task. `?wait=N` long-polls: the response is held until the
// task finishes or N seconds (capped, default cap 30) pass.
func (h *AsyncImageHandler) Get(c *gin.Context) {
	// Polling deliberately does not require the feature to be enabled, only that
	// the task store is reachable. Turning the switch off in the admin UI must not
	// strand tasks that were already accepted — their results are still in Redis
	// and their submitters are still polling.
	if !h.pollable() {
		imageTaskJSONError(c, http.StatusNotFound, "not_found_error", "async image tasks are not enabled")
		return
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.UserID <= 0 || apiKey.ID <= 0 {
		imageTaskError(c, service.ErrImageTaskForbidden)
		return
	}
	if !h.tasks.AllowPoll(c.Request.Context(), apiKey.ID) {
		c.Header("Retry-After", "1")
		imageTaskError(c, service.ErrImageTaskPollRateLimited)
		return
	}
	owner := service.ImageTaskOwner{UserID: apiKey.UserID, APIKeyID: apiKey.ID}
	task, err := h.tasks.Get(c.Request.Context(), owner, c.Param("task_id"))
	if err != nil {
		imageTaskError(c, err)
		return
	}
	if wait := h.longPollWait(c.Query("wait")); wait > 0 && task.Status == service.ImageTaskStatusProcessing {
		deadline := time.NewTimer(wait)
		ticker := time.NewTicker(time.Second)
	waitLoop:
		for {
			select {
			case <-c.Request.Context().Done():
				deadline.Stop()
				ticker.Stop()
				return
			case <-deadline.C:
				break waitLoop
			case <-ticker.C:
				next, err := h.tasks.Get(c.Request.Context(), owner, task.ID)
				if err != nil {
					break waitLoop
				}
				task = next
				if task.Status != service.ImageTaskStatusProcessing {
					break waitLoop
				}
			}
		}
		deadline.Stop()
		ticker.Stop()
	}
	c.Header("Cache-Control", "no-store")
	if task.Status == service.ImageTaskStatusProcessing {
		c.Header("Retry-After", "3")
	}
	c.JSON(http.StatusOK, task)
}

func (h *AsyncImageHandler) longPollWait(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 0
	}
	wait := time.Duration(seconds) * time.Second
	if limit := h.tasks.Limits().LongPollMax; wait > limit {
		wait = limit
	}
	return wait
}

// Delete removes a finished task's stored images and its record (v2). It is
// not a cancel: a running task returns 409 and keeps running.
func (h *AsyncImageHandler) Delete(c *gin.Context) {
	if !h.pollable() {
		imageTaskJSONError(c, http.StatusNotFound, "not_found_error", "async image tasks are not enabled")
		return
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.UserID <= 0 || apiKey.ID <= 0 {
		imageTaskError(c, service.ErrImageTaskForbidden)
		return
	}
	if !h.tasks.AllowPoll(c.Request.Context(), apiKey.ID) {
		c.Header("Retry-After", "1")
		imageTaskError(c, service.ErrImageTaskPollRateLimited)
		return
	}
	id := strings.TrimSpace(c.Param("task_id"))
	if err := h.tasks.Delete(c.Request.Context(), service.ImageTaskOwner{UserID: apiKey.UserID, APIKeyID: apiKey.ID}, id); err != nil {
		imageTaskError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"id": id, "task_id": id, "object": "image.generation.task.deleted", "deleted": true})
}

// Capabilities reports what this gateway offers the calling key, so clients
// can adopt v2 features without probing (v2).
func (h *AsyncImageHandler) Capabilities(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.UserID <= 0 || apiKey.ID <= 0 {
		imageTaskError(c, service.ErrImageTaskForbidden)
		return
	}
	platform := ""
	if apiKey.Group != nil {
		platform = apiKey.Group.Platform
	}
	supported := platform == service.PlatformOpenAI || platform == service.PlatformGrok
	enabled := h.enabled() && supported && service.GroupAllowsImageGeneration(apiKey.Group) && !h.draining.Load()
	concurrency := 0
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok {
		concurrency = subject.Concurrency
	}
	limits := service.DefaultImageTaskLimits()
	if h != nil && h.tasks != nil {
		limits = h.tasks.Limits()
	}
	endpoints := []string{}
	if supported {
		endpoints = []string{"generations", "edits"}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"object":                    "image.async.capabilities",
		"version":                   2,
		"enabled":                   enabled,
		"platform":                  platform,
		"endpoints":                 endpoints,
		"prefer_respond_async":      true,
		"idempotency":               h != nil && h.tasks != nil && h.tasks.SupportsIdempotency(),
		"billing_in_task":           true,
		"delete":                    true,
		"long_poll_max_seconds":     int(limits.LongPollMax / time.Second),
		"poll_rate_per_second":      limits.PollRatePerSecond,
		"max_in_flight_per_key":     h.maxInFlightForKey(concurrency),
		"task_ttl_seconds":          int(h.taskTTL() / time.Second),
		"execution_timeout_seconds": int(h.executionTimeout() / time.Second),
	})
}

func (h *AsyncImageHandler) maxInFlightForKey(concurrency int) int {
	if h == nil || h.tasks == nil {
		return 0
	}
	return h.tasks.MaxInFlightForKey(concurrency)
}

func (h *AsyncImageHandler) taskTTL() time.Duration {
	if h == nil || h.tasks == nil {
		return 0
	}
	return h.tasks.TTL()
}

func (h *AsyncImageHandler) executionTimeout() time.Duration {
	if h == nil || h.tasks == nil {
		return 0
	}
	return h.tasks.ExecutionTimeout()
}

// Drain is called on SIGTERM before infrastructure shuts down: new
// submissions get 503 (not submitted), running tasks get up to the configured
// drain timeout to finish, and whatever is left is cancelled and concluded as
// task_interrupted with a billing conclusion.
func (h *AsyncImageHandler) Drain() {
	if h == nil {
		return
	}
	h.draining.Store(true)
	timeout := service.DefaultImageTaskLimits().DrainTimeout
	if h.tasks != nil {
		timeout = h.tasks.Limits().DrainTimeout
	}
	running := h.running.Load()
	if running > 0 {
		logger.L().Info("image_task.drain_started", zap.Int64("running", running), zap.Duration("timeout", timeout))
	}
	if waitGroupWithTimeout(&h.wg, timeout) {
		return
	}
	h.mu.Lock()
	remaining := len(h.cancels)
	for _, cancel := range h.cancels {
		cancel(errImageTaskShutdown)
	}
	h.mu.Unlock()
	logger.L().Warn("image_task.drain_timeout", zap.Int("cancelled", remaining))
	waitGroupWithTimeout(&h.wg, 15*time.Second)
}

func waitGroupWithTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (h *AsyncImageHandler) registerCancel(taskID string, cancel context.CancelCauseFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancels == nil {
		h.cancels = make(map[string]context.CancelCauseFunc)
	}
	h.cancels[taskID] = cancel
}

func (h *AsyncImageHandler) unregisterCancel(taskID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.cancels, taskID)
}

// validateRequest checks the payload the way the synchronous handler will and
// returns the requested model.
func (h *AsyncImageHandler) validateRequest(c *gin.Context, platform string, body []byte) (string, error) {
	if h.openAI == nil || h.openAI.gatewayService == nil {
		// No gateway parser (tests, degraded wiring): the synchronous handler
		// still validates during execution; read the model only to label the task.
		return service.ParseGrokMediaRequest(c.GetHeader("Content-Type"), body).Model, nil
	}
	if platform == service.PlatformGrok {
		parsed := service.ParseGrokMediaRequest(c.GetHeader("Content-Type"), body)
		if strings.TrimSpace(parsed.Model) == "" {
			return "", errors.New("model is required")
		}
		return parsed.Model, nil
	}
	parsed, err := h.openAI.gatewayService.ParseOpenAIImagesRequest(c, body)
	if err != nil {
		return "", err
	}
	if parsed.Stream {
		return "", errors.New("streaming image requests cannot be submitted as asynchronous tasks")
	}
	return parsed.Model, nil
}

func (h *AsyncImageHandler) executeWithGateway(platform string, c *gin.Context) {
	if h.openAI == nil {
		imageTaskJSONError(c, http.StatusServiceUnavailable, "api_error", "image gateway is unavailable")
		return
	}
	if platform == service.PlatformGrok {
		h.openAI.GrokImages(c)
		return
	}
	h.openAI.Images(c)
}

func (h *AsyncImageHandler) run(taskID string, apiKeyID int64, platform string, job *asyncImageJob) {
	defer h.wg.Done()
	defer h.running.Add(-1)
	defer h.unregisterCancel(taskID)
	defer job.cancel(nil)
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.L().Error("image_task.execution_panicked", zap.String("task_id", taskID), zap.Any("panic", recovered))
			h.failTask(taskID, http.StatusInternalServerError, imageTaskErrorPayload("api_error", "image generation task panicked"), "")
		}
	}()

	h.tasks.Start(context.Background(), taskID)
	logger.L().Info("image_task.started", zap.String("task_id", taskID), zap.Int64("api_key_id", apiKeyID))
	stopHeartbeat := h.startHeartbeat(taskID, apiKeyID)
	// Upload retries are part of the running task. Keep proving liveness until
	// the terminal state is stored, and stop on panic as well as normal return.
	defer stopHeartbeat()

	// Run the gateway call under the same ops capture the request pipeline
	// uses, so a failed task lands in ops_error_logs like a synchronous call.
	capture := acquireOpsCaptureWriter(job.ctx.Writer)
	defer releaseOpsCaptureWriter(capture)
	capture.setContext(job.ctx)
	job.ctx.Writer = capture
	h.execute(platform, job.ctx)
	capture.finalizeCapture()

	body := bytes.TrimSpace(job.recorder.Body.Bytes())
	statusCode := job.recorder.Code
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	ctxErr := job.ctx.Request.Context().Err()
	cause := context.Cause(job.ctx.Request.Context())
	succeeded := statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices && len(body) > 0 && json.Valid(body)

	switch {
	case errors.Is(cause, errImageTaskShutdown) && !succeeded:
		h.failTask(taskID, http.StatusServiceUnavailable,
			imageTaskCodedErrorPayload("api_error", service.ImageTaskErrorInterrupted, "the gateway instance shut down before the task finished"),
			service.ImageTaskErrorInterrupted)
	case errors.Is(ctxErr, context.DeadlineExceeded) && !succeeded:
		h.failTask(taskID, http.StatusGatewayTimeout,
			imageTaskCodedErrorPayload("timeout_error", service.ImageTaskErrorTimeout, "image generation task timed out"),
			service.ImageTaskErrorTimeout)
	case succeeded:
		if err := h.tasks.Complete(context.Background(), taskID, statusCode, json.RawMessage(body)); err != nil {
			logger.L().Error("image_task.complete_store_failed", zap.String("task_id", taskID), zap.Error(err))
		}
	case statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices:
		h.failTask(taskID, http.StatusBadGateway, imageTaskErrorPayload("api_error", "upstream returned an invalid image response"), "")
	default:
		h.failTask(taskID, statusCode, extractImageTaskError(body), "")
	}

	// The ops row carries the submit path, so async failures are told apart
	// from synchronous ones in the admin error log.
	if job.submitPath != "" {
		job.ctx.Request.URL.Path = job.submitPath
	}
	if h.ops != nil {
		recordOpsErrorAfterRequest(job.ctx, h.ops, capture)
	}
}

func (h *AsyncImageHandler) startHeartbeat(taskID string, apiKeyID int64) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(service.ImageTaskHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				h.tasks.Heartbeat(ctx, taskID, apiKeyID)
				cancel()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-done
		})
	}
}

func (h *AsyncImageHandler) failTask(taskID string, statusCode int, taskErr json.RawMessage, code string) {
	if err := h.tasks.FailWithCode(context.Background(), taskID, statusCode, taskErr, code); err != nil {
		logger.L().Error("image_task.failure_store_failed", zap.String("task_id", taskID), zap.Error(err))
	}
}

// asyncImageJob is the detached request a task executes: it outlives the
// submit request, carries its own cancellation context, and records the
// synchronous handler's response.
type asyncImageJob struct {
	ctx        *gin.Context
	recorder   *httptest.ResponseRecorder
	cancel     context.CancelCauseFunc
	submitPath string
}

func newAsyncImageJob(c *gin.Context, body []byte, timeoutDuration time.Duration) *asyncImageJob {
	base := context.WithoutCancel(c.Request.Context())
	causeCtx, cancelCause := context.WithCancelCause(base)
	executionCtx, cancelTimeout := context.WithTimeout(causeCtx, timeoutDuration)
	request := c.Request.Clone(executionCtx)
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	request.ContentLength = int64(len(body))
	request.URL.Path = strings.TrimSuffix(request.URL.Path, "/async")

	taskCtx := c.Copy()
	recorder := httptest.NewRecorder()
	recorderCtx, _ := gin.CreateTestContext(recorder)
	taskCtx.Writer = recorderCtx.Writer
	taskCtx.Request = request
	var once sync.Once
	return &asyncImageJob{
		ctx:        taskCtx,
		recorder:   recorder,
		submitPath: c.Request.URL.Path,
		cancel: func(cause error) {
			if cause != nil {
				cancelCause(cause)
				return
			}
			once.Do(func() {
				cancelTimeout()
				cancelCause(nil)
			})
		},
	}
}

func asyncImageRequestStreams(contentType string, body []byte) bool {
	if isMultipartImagesContentType(contentType) {
		return false
	}
	var envelope struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &envelope) == nil && envelope.Stream
}

func imageTaskPollURL(submitPath, taskID string) string {
	if strings.HasPrefix(submitPath, "/v1/") {
		return "/v1/images/tasks/" + taskID
	}
	return "/images/tasks/" + taskID
}

func imageTaskEndpoint(path string) string {
	if strings.Contains(path, "/images/edits") {
		return "edits"
	}
	return "generations"
}

func imageTaskAcceptedBody(task *service.ImageTask, pollURL string) gin.H {
	body := gin.H{
		"id":         task.ID,
		"task_id":    task.TaskID,
		"object":     task.Object,
		"status":     task.Status,
		"created_at": task.CreatedAt,
		"expires_at": task.ExpiresAt,
		"poll_url":   pollURL,
	}
	if task.RequestID != "" {
		body["request_id"] = task.RequestID
	}
	if task.IdempotencyKey != "" {
		body["idempotency_key"] = task.IdempotencyKey
	}
	if task.Billing != nil {
		body["billing"] = task.Billing
	}
	return body
}

// imageTaskReplayBody is the full current task view plus poll_url.
func imageTaskReplayBody(task *service.ImageTask, pollURL string) map[string]any {
	encoded, _ := json.Marshal(task)
	var body map[string]any
	_ = json.Unmarshal(encoded, &body)
	if body == nil {
		body = map[string]any{}
	}
	body["poll_url"] = pollURL
	return body
}

func preferRespondAsync(header string) bool {
	for _, part := range strings.Split(header, ",") {
		token := strings.TrimSpace(part)
		if i := strings.IndexAny(token, ";="); i >= 0 {
			token = strings.TrimSpace(token[:i])
		}
		if strings.EqualFold(token, "respond-async") {
			return true
		}
	}
	return false
}

func imageTaskIdempotencyScope(userID, apiKeyID int64, key string) string {
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%d:%d:%s", userID, apiKeyID, hex.EncodeToString(sum[:]))
}

// imageTaskFingerprint identifies "the same request" for Idempotency-Key. JSON
// bodies hash as sent. Multipart bodies hash their parts (name, filename,
// content) so a client that rebuilds the form with a new boundary on retry
// still matches.
func imageTaskFingerprint(endpoint, contentType string, body []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(endpoint))
	_, _ = h.Write([]byte{0})
	if mediaType, params, err := mime.ParseMediaType(contentType); err == nil && strings.EqualFold(mediaType, "multipart/form-data") && params["boundary"] != "" {
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		parts := 0
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				if parts > 0 {
					return hex.EncodeToString(h.Sum(nil))
				}
				break
			}
			if err != nil {
				break
			}
			content, readErr := io.ReadAll(part)
			if readErr != nil {
				break
			}
			partSum := sha256.Sum256(content)
			fmt.Fprintf(h, "%s\x00%s\x00%x\x00", part.FormName(), part.FileName(), partSum)
			parts++
		}
		// Unparseable multipart: fall back to the raw bytes below.
		h = sha256.New()
		_, _ = h.Write([]byte(endpoint))
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func extractImageTaskError(body []byte) json.RawMessage {
	if json.Valid(body) {
		var envelope struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil && len(envelope.Error) > 0 && json.Valid(envelope.Error) {
			return envelope.Error
		}
		return json.RawMessage(body)
	}
	return imageTaskErrorPayload("api_error", "image generation failed")
}

func imageTaskErrorPayload(errorType, message string) json.RawMessage {
	data, _ := json.Marshal(gin.H{"type": errorType, "message": message})
	return data
}

func imageTaskCodedErrorPayload(errorType, code, message string) json.RawMessage {
	data, _ := json.Marshal(gin.H{"type": errorType, "code": code, "message": message})
	return data
}

func imageTaskError(c *gin.Context, err error) {
	status := infraerrors.Code(err)
	code := infraerrors.Reason(err)
	message := infraerrors.Message(err)
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if strings.TrimSpace(code) == "" {
		code = "IMAGE_TASK_ERROR"
	}
	imageTaskJSONError(c, status, code, message)
}

func imageTaskJSONError(c *gin.Context, status int, code, message string) {
	c.Header("Cache-Control", "no-store")
	c.JSON(status, gin.H{"error": gin.H{"type": code, "code": code, "message": message}})
}
