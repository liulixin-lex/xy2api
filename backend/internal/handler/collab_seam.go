package handler

import (
	"bytes"
	"sync/atomic"

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

// collabRescue attempts a takeover. It writes through the seam's writer and
// may call discard between attempts. Whether it worked is decided by what
// reached the client, never by what it returns.
type collabRescue func(c *gin.Context, seam *collabSeam, saved *collabWithheld)

// collabSeam is where the collaboration ladder takes over. The existing path
// runs to its end with all of its side effects at their own time. Only then,
// and only if it ended in an error before any output, may a takeover run.
// When nothing takes over, the error goes out exactly as it was written.
//
// finish has to run before the request's other deferred cleanup, so that the
// user slot, the balance reservation and the request record are still held.
type collabSeam struct {
	writer   *collabSeamWriter
	original gin.ResponseWriter
	body     []byte
	rescue   collabRescue
}

// armCollabSeam installs the seam for requests the ladder covers: native
// Responses requests of an official Codex client that carry ciphertext and
// no continuation pointer. For every other request it returns nil, and every
// method of a nil seam does nothing.
func (h *OpenAIGatewayHandler) armCollabSeam(c *gin.Context, body []byte, previousResponseID, platform string, legacyCompact bool) *collabSeam {
	if c == nil || c.Request == nil || c.Writer == nil || platform != service.PlatformOpenAI || previousResponseID != "" || legacyCompact {
		return nil
	}
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
	seam.writer = newCollabSeamWriter(c.Writer)
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
	if s.writer.withholding() {
		collabSeamWithheld.Add(1)
		if s.takeOver(c) {
			s.writer.release()
			return
		}
		// The existing path would have closed its heartbeat with this error.
		stopPreOutputKeepalive(c)
	}
	s.writer.release()
}

// takeOver reports whether output of a takeover reached the client. In every
// other case the saved error is back in place when it returns.
func (s *collabSeam) takeOver(c *gin.Context) (delivered bool) {
	if s.rescue == nil || c.Request == nil || c.Request.Context().Err() != nil {
		return false
	}
	saved := s.writer.suspend()
	if saved == nil {
		return false
	}
	// A takeover rewrites request-scoped keys and contexts. If it does not
	// deliver, later readers have to see the existing path's values. Pointed-to
	// state is shared, so a takeover uses its own request state.
	request, keys := c.Request, c.Copy().Keys
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.FromContext(request.Context()).Error("collab.takeover_panic", zap.Stack("stack"))
		}
		delivered = !s.writer.resume(saved)
		if !delivered {
			c.Request = request
			collabSeamSetKeys(c, keys)
		}
	}()
	s.rescue(c, s, saved)
	return false
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
