package handler

import (
	"bytes"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"go.uber.org/zap"
)

var (
	collabCiphertextField = []byte(`"encrypted_content"`)
	collabEncryptedArgs   = []byte(`"encrypted_function_args"`)

	// collabSeamWithheld counts requests whose existing path ended in a
	// withheld error, rescued or not.
	collabSeamWithheld atomic.Int64
)

// collabRescue attempts a takeover. It starts with no writer above the seam's,
// no heartbeat and no terminal claim, and calls resetAttempt between attempts.
// Deadlines, waiters and its own request state are its responsibility. Whether
// it worked is decided by what reached the client, never by what it returns.
type collabRescue func(c *gin.Context, seam *collabSeam, saved *collabWithheld)

// collabRescuable returns, once when the seam is armed, the flag that the
// rescue keeps current: whether it could act on an error written now. Nothing
// else is withheld or delayed. The flag is read with locks of the response
// writers held, where calling out could deadlock.
type collabRescuable func(seam *collabSeam) *atomic.Bool

// collabSeam is where the collaboration ladder takes over. The existing path
// runs to its end with all of its side effects at their own time. Only then,
// and only if it ended in an error before any output, may a takeover run.
// When nothing takes over, the error goes out exactly as it was written.
//
// A withheld error is decided by beforeSettlement or, if the request is not
// settled, by finish. finish has to run before the request's other deferred
// cleanup, so that the user slot, the balance reservation and the request
// record are still held.
type collabSeam struct {
	writer           *collabSeamWriter
	original         gin.ResponseWriter
	body             []byte
	rescue           collabRescue
	heartbeats       service.DeliveryHeartbeatMark
	observer         *service.CollabSendObserver
	settle           func(*service.Account, *service.OpenAIForwardResult, error)
	localEnding      func(*gin.Context, *collabSeam, *collabWithheld, resilience.CollabEvidence)
	terminalRejected func(*gin.Context, resilience.CollabEvidence)
	started          time.Time
	idleDeadline     time.Time
	losslessDuration time.Duration
	lossless         bool
	progress         service.CollabLadderProgress
	optionalSpent    time.Duration
	rejected         map[service.CollabRejectedBody]bool
	deliveredAccount int64
	terminalRecorded bool
	localFinal       bool
}

// armCollabSeam installs the seam for requests the ladder covers: native
// Responses requests of an official Codex client that carry ciphertext and
// no continuation pointer, when a rescue is wired at all. For every other
// request it returns nil, and every method of a nil seam does nothing. It also
// returns nil when arming panics: the ladder is optional, the request is not.
func (h *OpenAIGatewayHandler) armCollabSeam(c *gin.Context, body []byte, previousResponseID, platform string, legacyCompact bool) (armed *collabSeam) {
	if c == nil || c.Request == nil || c.Writer == nil || platform != service.PlatformOpenAI || previousResponseID != "" || legacyCompact {
		return nil
	}
	if h.collabRescue == nil || h.collabRescuable == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.FromContext(c.Request.Context()).Error("collab.arm_panic", zap.Stack("stack"))
			armed = nil
		}
	}()
	if !resilience.StateFrom(c.Request.Context()).CollabSettingsSnapshot().CollabLadderOn() {
		return nil
	}
	if !openai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator")) {
		return nil
	}
	if !bytes.Contains(body, collabCiphertextField) && !bytes.Contains(body, collabEncryptedArgs) {
		return nil
	}
	seam := &collabSeam{original: c.Writer, body: body, rescue: h.collabRescue}
	seam.lossless = resilience.StateFrom(c.Request.Context()).CollabSettingsSnapshot().CollabLosslessFirstOn()
	seam.observer = service.NewLazyCollabSendObserver(body, service.CollabBodyFactsForRequest(c.Request.Context()))
	eligible := h.collabRescuable(seam)
	if eligible == nil {
		return nil
	}
	c.Request = c.Request.WithContext(service.WithCollabSendObserver(c.Request.Context(), seam.observer))
	seam.writer = newCollabSeamWriter(c.Writer, eligible)
	c.Writer = seam.writer
	return seam
}

// finish must be deferred directly, so that it sees a panic of the handler.
func (s *collabSeam) finish(c *gin.Context) {
	if s == nil {
		return
	}
	if recovered := recover(); recovered != nil {
		s.writer.release()
		panic(recovered)
	}
	s.writer.finishPending()
	if s.writer.withholding() {
		s.decide(c, true)
		return
	}
	s.writer.release()
}

// beforeSettlement decides a withheld error before the request's bill is
// written. That write can take seconds, which the client must not wait for,
// and once it claimed the request nothing can be taken over any more. A
// response with metered usage is billed as it is. One without can be taken
// over: then the rescue settles the request and it reports true.
func (s *collabSeam) beforeSettlement(c *gin.Context, result *service.OpenAIForwardResult) (rescued bool) {
	if s == nil {
		return false
	}
	s.writer.finishPending()
	if !s.writer.withholding() {
		return false
	}
	return s.decide(c, !collabSeamMetered(result))
}

func (s *collabSeam) decide(c *gin.Context, mayTakeOver bool) bool {
	collabSeamWithheld.Add(1)
	// What was rescuable when it was withheld may not be any more.
	if mayTakeOver && s.writer.intercepts() && s.takeOver(c) {
		s.writer.release()
		return true
	}
	// The existing path would have closed its heartbeat with this error.
	stopPreOutputKeepalive(c)
	s.recordTerminal(c)
	s.writer.release()
	return false
}

func collabSeamMetered(result *service.OpenAIForwardResult) bool {
	if result == nil {
		return false
	}
	u := result.Usage
	return result.ClientDisconnect ||
		u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheCreationInputTokens > 0 || u.CacheReadInputTokens > 0 ||
		u.CacheCreation5mTokens > 0 || u.CacheCreation1hTokens > 0 ||
		u.ImageInputTokens > 0 || u.ImageCacheReadTokens > 0 || u.ImageOutputTokens > 0 ||
		u.RawPromptTokens != nil && *u.RawPromptTokens > 0 ||
		result.ImageCount > 0 || result.VideoCount > 0 || result.SearchCount > 0 || result.WebSearchCalls > 0 ||
		result.AudioUsage != nil
}

func (s *collabSeam) recordTerminal(c *gin.Context) {
	if s.terminalRecorded || s.terminalRejected == nil || s.observer == nil {
		return
	}
	s.terminalRecorded = true
	defer func() { _ = recover() }()
	rejections := s.observer.Rejections()
	if len(rejections) != 0 {
		s.terminalRejected(c, rejections[len(rejections)-1].Evidence(s.observer.Body().Shape))
	}
}

// takeOver reports whether output of a takeover reached the client. In every
// other case the saved error is back in place when it returns.
//
// It does not start once the existing path has settled: that bill and request
// record are final, and a second upstream response could not be settled.
func (s *collabSeam) takeOver(c *gin.Context) (delivered bool) {
	if s.rescue == nil || c.Request == nil || c.Request.Context().Err() != nil {
		return false
	}
	if resilience.StateFrom(c.Request.Context()).GatewayRequestFinalized() {
		return false
	}
	// Snapshot first: stopping the heartbeat and clearing the claims below
	// must not be what the saved error's later readers see.
	original := c.Copy().Keys
	if s.idleDeadline.IsZero() {
		last := s.started
		if written := s.writer.lastWriteAt(); written.After(last) {
			last = written
		}
		s.idleDeadline = service.CollabClientIdleDeadline(c, last)
	}
	s.quiesce(c)
	recorded := opsRecordedStatus(c)
	saved := s.writer.suspend()
	if saved == nil {
		return false
	}
	s.heartbeats = service.MarkDeliveryHeartbeats(c)
	// A takeover rewrites request-scoped keys and contexts. If it does not
	// deliver, later readers have to see the existing path's values. Pointed-to
	// state is shared, so a takeover uses its own request state.
	request, keys, ginErrors := c.Request, original, c.Errors
	c.Errors = append(c.Errors[:0:0], c.Errors...)
	for i, entry := range c.Errors {
		if entry != nil {
			copied := *entry
			c.Errors[i] = &copied
		}
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.FromContext(request.Context()).Error("collab.takeover_panic", zap.Stack("stack"))
		}
		// Stop what the takeover started while its keys still name it, and
		// before the header map is put back.
		s.quiesce(c)
		delivered = !s.writer.resume(saved)
		c.Request = request
		if !delivered {
			s.heartbeats.Restore()
			collabSeamSetKeys(c, keys)
			c.Errors = ginErrors
			if !saved.committed && s.writer.ResponseWriter.Written() {
				collabSeamRecordLost(c, saved, recorded)
			}
		}
	}()
	collabSeamClearTerminal(c)
	c.Request = request.Clone(request.Context())
	s.rescue(c, s, saved)
	return false
}

// collabSeamRecordLost keeps a saved error on record when an attempt committed
// the response and then could not write: the error cannot reach the client any
// more, and the request must not pass for a recovered one.
func collabSeamRecordLost(c *gin.Context, saved *collabWithheld, status int) {
	var body []byte
	for _, op := range saved.ops {
		body = append(body, op.data...)
	}
	parsed := parseOpsErrorResponse(body)
	if status < http.StatusBadRequest {
		if !parsed.StreamFailure {
			return
		}
		if terminal, ok := service.OpsGatewayTerminalStatus(c); ok {
			parsed.StatusCode = terminal
		}
		status = inferStreamFailureStatus(c, parsed)
	}
	service.PromoteOpsStreamFailure(c, parsed.ErrorType, parsed.Code, parsed.Message, status)
}

// resetAttempt prepares for the next attempt of a takeover. It does nothing
// once output of the takeover has reached the client.
func (s *collabSeam) resetAttempt(c *gin.Context) {
	if s.writer.current() == collabSeamPassing {
		return
	}
	s.quiesce(c)
	s.heartbeats.Restore()
	s.writer.discard()
	collabSeamClearTerminal(c)
}

// quiesce stops every heartbeat that can write to the response and leaves the
// seam's writer on top. A stop waits for a beat in progress. Forgetting the
// stopped heartbeat also drops its converted-error state, which would swallow
// whatever is written next.
func (s *collabSeam) quiesce(c *gin.Context) {
	stopPreOutputKeepalive(c)
	service.StopOpenAICompactSSEKeepaliveCommitted(c)
	service.ForgetStoppedSSEKeepalive(c)
	c.Delete(ginKeyPreOutputKeepaliveStop)
	c.Writer = s.writer
}

// collabSeamClearTerminal makes the saved error's terminal claims provisional.
// They come back with the saved keys if the takeover does not deliver.
func collabSeamClearTerminal(c *gin.Context) {
	for _, key := range []string{
		resilience.GinKeyTerminalFinalized,
		service.ResponseCommittedKey,
		service.OpsStreamErrorKey,
		service.OpsStreamErrorsKey,
		service.OpsGatewayTerminalStatusKey,
		service.OpsUpstreamContentPolicyKey,
	} {
		c.Delete(key)
	}
}

// restore runs after the keepalive wrapper above the seam was removed, so
// that the middleware below finds its own writer again.
func (s *collabSeam) restore(c *gin.Context) {
	if s == nil {
		return
	}
	s.writer.release()
	if c.Writer == gin.ResponseWriter(s.writer) {
		c.Writer = s.original
	}
}

func collabSeamSetKeys(c *gin.Context, keys map[any]any) {
	for key := range c.Copy().Keys {
		if _, kept := keys[key]; !kept {
			c.Delete(key)
		}
	}
	for key, value := range keys {
		c.Set(key, value)
	}
}
