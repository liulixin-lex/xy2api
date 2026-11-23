package service

import (
	"bytes"
	"context"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type openAICollabRequestKey struct{}

// The inbound owner survives account retries and optional child contexts.
// Mutable collaboration data is confined to the request goroutine.
type openAICollabRequest struct {
	state        *resilience.CollabRequestState
	settings     *resilience.Settings
	ingress      resilience.CollabIngressObservation
	delivery     *openAICollabDeliveryWriter
	attempt      *openAICollabAttempt
	gateway      *OpenAIGatewayService
	observation  *resilience.CollabObservationAccumulator
	send         *openAICollabSendObservation
	client       resilience.CollabClient
	versionState resilience.CollabVersionState
	version      string
}

// BeginOpenAICollabRequest inspects validated ingress before route selection.
// Ordinary requests retain their existing context and allocate no extra state.
func BeginOpenAICollabRequest(c *gin.Context, body []byte, userID, groupID int64) {
	if c == nil || c.Request == nil {
		return
	}
	ctx := c.Request.Context()
	if openAICollabRequestFrom(ctx) != nil {
		return
	}
	ingress := resilience.InspectCollabIngress(body, resilience.CollabIngressValidationPassed)
	countOpenAICollabIngress(c, body, ingress)
	if !ingress.Declared && !ingress.EncryptedAgent && !ingress.EncryptedTool {
		return
	}
	owner := resilience.StateFrom(ctx)
	if owner == nil {
		owner = resilience.NewRequestState()
		ctx = resilience.WithRequestState(ctx, owner)
	}
	settings := owner.CollabSettingsSnapshot()
	state := &resilience.CollabRequestState{
		UserID: userID, GroupID: groupID, Original: bytes.Clone(body),
		Role: resilience.CollabRoleUnknown, Kind: resilience.CollabRequestKindNormal,
		Order: settings.CollabOrder(), Execution: resilience.CollabExecutionUnknown,
		DeliveryHooks: &resilience.CollabDeliveryHooks{},
	}
	if !owner.SetCollabState(state) {
		return
	}
	request := &openAICollabRequest{state: state, settings: settings, ingress: ingress}
	request.initObservation(c)
	c.Request = c.Request.WithContext(context.WithValue(ctx, openAICollabRequestKey{}, request))
	request.delivery = &openAICollabDeliveryWriter{ResponseWriter: c.Writer, ctx: c.Request.Context(), hooks: state.DeliveryHooks}
	c.Writer = request.delivery
}

func openAICollabRequestFrom(ctx context.Context) *openAICollabRequest {
	if ctx == nil {
		return nil
	}
	request, _ := ctx.Value(openAICollabRequestKey{}).(*openAICollabRequest)
	return request
}
