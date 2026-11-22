package service

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// wire and its output decoding table are immutable builder results. Repairs
// start from the corresponding Responses body before builder/prompt injection.
type conversionSendBody struct {
	wire, responsesInput []byte
	reverse              map[string]string
	// FCO repair retains final wire as responsesInput; rebuilding it must skip
	// group prompt injection, including after a conversion retry changes account.
	fcoRewritten bool
}

type openAIConversionWire struct {
	conversionSendBody
	first                 []byte // before same-account repairs
	pendingReplay, scoped bool
	accountID             int64
	inbound, tier         string
	path                  conversionWirePath
}

// These are effective body rewrites, not transport properties such as proxy,
// bearer or endpoint URL. Platform/type also determine the legacy cache filter.
type conversionWirePath struct {
	platform, kind, namespace, rotation       string
	passthrough, codex, stateless, imageAlias bool
}

func (s *OpenAIGatewayService) conversionWirePath(ctx context.Context, a, credential *Account, scoped bool) conversionWirePath {
	p := conversionWirePath{platform: a.Platform, kind: a.Type, passthrough: a.IsOpenAIPassthroughEnabled(),
		codex: a.UsesOpenAICodexProtocol(), stateless: a.UsesNativeCNResponses(), imageAlias: shouldAliasDeepSeekResponsesInputImages(a),
		rotation: s.qualityRotation(ctx, a)}
	if scoped {
		p.namespace = codexAccountIdentityNamespace(credential)
	}
	return p
}

func (s *OpenAIGatewayService) matchesConversionWire(ctx context.Context, a *Account, state *openAIConversionRetryState) bool {
	if state.fcoRewritten && !openAIFCOConversionReplayAllowed(ctx, a) {
		return false
	}
	credential := a
	if state.scoped && a.IsShadow() {
		var err error
		if credential, err = resolveCredentialAccount(ctx, s.accountRepo, a); err != nil {
			return false
		}
	}
	if s.conversionWirePath(ctx, a, credential, state.scoped) != state.path {
		return false
	}
	in, _ := ctx.Value(openAITierInputKey{}).(openAITierInput)
	d, err := s.openAITierDecisionForBody(context.WithValue(ctx, openAITierEndpointKey{}, APIProtocolResponses), a, state.model, in.candidate)
	return err == nil && d.OutboundTier == state.tier
}

// Only inspect potentially large metadata on the rare path that starts selection.
func (w *openAIConversionWire) startReplay() {
	w.pendingReplay = true
	w.scoped = w.path.codex && (gjson.GetBytes(w.wire, "prompt_cache_key").String() != "" || gjson.GetBytes(w.wire, "client_metadata").IsObject())
	if !w.scoped {
		w.path.namespace = ""
	}
}

// Capture output decoding with the actual bytes. On a replay, the ordinary
// builder must also reproduce the original pre-repair body before we substitute
// the last (possibly repaired) send. This catches unanticipated builder drift.
func (s *OpenAIGatewayService) prepareOpenAIConversionWire(ctx context.Context, c *gin.Context, req *http.Request, account *Account, inbound string, send conversionSendBody) (conversionSendBody, error) {
	state := openAIConversionRetryFrom(ctx)
	if state == nil {
		return send, nil
	}
	if state.pendingReplay {
		if len(send.wire) == 0 || !bytes.Equal(send.wire, state.first) ||
			state.fcoRewritten && !openAIFCOConversionReplayAllowed(ctx, account) {
			WriteOpenAIConversionRetryFailure(c, state.failure, "candidate_wire")
			return conversionSendBody{}, state.failure.cause
		}
		send = state.conversionSendBody
		body := send.wire
		state.pendingReplay = false
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		req.ContentLength = int64(len(body))
		req.Header.Del("Content-Length")
		setCodexToolNameReverse(c, send.reverse)
		return send, nil
	}
	if state.first == nil || state.accountID != account.ID {
		state.first, state.accountID = send.wire, account.ID
	}
	state.conversionSendBody, state.inbound = send, inbound
	state.path = s.conversionWirePath(ctx, account, codexAccountIdentitySource(c, account), account.UsesOpenAICodexProtocol())
	if a, _ := ctx.Value(openAITierAttemptKey{}).(*openAITierAttempt); a != nil && a.decision != nil {
		state.tier = a.decision.OutboundTier
	}
	return send, nil
}
