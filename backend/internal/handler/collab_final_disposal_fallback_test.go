//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCollabFinalDisposalPreservesSavedTerminal(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(fmt.Sprint(local), func(t *testing.T) {
			c, wire := seamTestTracked()
			sends := 0
			withSeam(c, func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
				body := service.ParseCollabBody([]byte(`{"input":[{"type":"reasoning","id":"rs_a","encrypted_content":"opaque"}]}`))
				evidence := resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeVerified}
				ladder := service.CollabLadder{Body: body, First: 1, MaxAttempts: 1, Evidence: evidence}
				ladder.Select = func(context.Context, int64, map[int64]bool) (int64, func()) { return 1, func() {} }
				ladder.Send = func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
					sends++
					return evidence, false
				}
				ladder.BeforeDisposal = func(context.Context, resilience.CollabEvidence) bool {
					if !local || sends == 0 {
						return false
					}
					c.Header("Retry-After", "")
					c.Data(http.StatusOK, "application/json", []byte(seamTestJSON))
					return true
				}
				// Final disposal never sends again; fallback replays today's saved terminal.
				if !ladder.Run(c.Request.Context()) && body.LadderPolicy(ladder.LastEvidence, ladder.StopReason).LocalEnding {
					ladder.BeforeDisposal(c.Request.Context(), ladder.LastEvidence)
				}
			}, seamTestExistingError(c))
			require.Equal(t, 1, sends)
			if local {
				require.Equal(t, http.StatusOK, wire.Code)
				require.Equal(t, seamTestJSON, wire.Body.String())
			} else {
				require.Equal(t, http.StatusBadGateway, wire.Code)
				require.Equal(t, seamTestHTTPFail, wire.Body.String())
				require.Equal(t, "7", wire.Header().Get("Retry-After"))
			}
		})
	}
}
