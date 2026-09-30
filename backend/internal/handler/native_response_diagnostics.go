package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/pkg/responseturn"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// NativeResponseDiagnostics is installed only behind the existing admin guard.
// Even there, a complete original scope is required; raw response bodies never
// leave this endpoint. It does not attach, schedule, cancel or settle anything.
func (h *OpenAIGatewayHandler) NativeResponseDiagnostics(c *gin.Context) {
	user, ue := strconv.ParseInt(c.Query("user_id"), 10, 64)
	key, ke := strconv.ParseInt(c.Query("api_key_id"), 10, 64)
	group, ge := strconv.ParseInt(c.DefaultQuery("group_id", "0"), 10, 64)
	id := strings.TrimSpace(c.Param("request_id"))
	if ue != nil || ke != nil || ge != nil || user <= 0 || key <= 0 || group < 0 || len(id) == 0 || len(id) > 256 {
		response.Error(c, http.StatusBadRequest, "A valid original user, API key, group and request ID are required")
		return
	}
	d, ok := service.LookupNativeStreamDiagnostics(service.NativeDiagnosticScope{UserID: user, APIKeyID: key, GroupID: group}, id)
	if !ok {
		response.Error(c, http.StatusNotFound, "Native stream measurements are unavailable or expired; historical timestamps are not inferred")
		return
	}
	out := gin.H{"delivery": d}
	if d.ResponseID != "" {
		scope := responseturn.Scope{UserID: user, APIKeyID: key, GroupID: group, Interface: "responses"}
		if turn, err := h.NativeResponseManager().Lookup(scope, d.ResponseID); err == nil {
			s := turn.Snapshot()
			out["recovery"] = gin.H{"state": s.State, "reason": s.Reason, "eligible": s.Recoverable, "unavailable_reason": s.RecoveryUnavailableReason, "offline_remaining_ms": s.OfflineRemaining.Milliseconds(), "offline_used_ms": s.OfflineUsed.Milliseconds(), "attachments": s.Attachments, "attachment_epoch": s.AttachmentEpoch, "replay_events": s.ReplayEvents, "cancel_requested": s.CancelRequested, "cancel_confirmed": s.CancelConfirmed, "journal_events": s.JournalEvents, "journal_bytes": s.JournalBytes, "owner_account_id": s.OwnerAccountID, "policy_version": s.PolicyVersion, "attached": s.Attached}
		}
	}
	response.Success(c, out)
}
