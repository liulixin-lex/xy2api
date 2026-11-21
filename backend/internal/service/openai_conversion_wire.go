package service

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type openAIConversionWire struct {
	wire, replay, first []byte // first is before same-account repairs; wire is the most recent send
	accountID           int64
	reverse             map[string]string
	inbound, tier       string
	scoped              bool
	path                conversionWirePath
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

// Capture output decoding with the actual bytes. On a replay, the ordinary
// builder must also reproduce the original pre-repair body before we substitute
// the last (possibly repaired) send. This catches unanticipated builder drift.
func (s *OpenAIGatewayService) prepareOpenAIConversionWire(ctx context.Context, c *gin.Context, req *http.Request, account *Account, inbound string) ([]byte, error) {
	state := openAIConversionRetryFrom(ctx)
	if state == nil {
		return nil, nil
	}
	var body []byte
	if req.GetBody != nil {
		if reader, err := req.GetBody(); err == nil {
			defer func() { _ = reader.Close() }()
			if b, readErr := io.ReadAll(reader); readErr == nil {
				body = b
			}
		}
	}
	if state.replay != nil {
		if len(body) == 0 || !bytes.Equal(body, state.first) {
			WriteOpenAIConversionRetryFailure(c, state.failure, "candidate_wire")
			return nil, state.failure.cause
		}
		body = state.replay
		state.replay = nil
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		req.ContentLength = int64(len(body))
		req.Header.Del("Content-Length")
		setCodexToolNameReverse(c, state.reverse)
		return body, nil
	}
	if state.first == nil || state.accountID != account.ID {
		state.first, state.accountID = body, account.ID
	}
	state.wire, state.inbound = body, inbound
	state.reverse = nil
	if names := codexToolNameReverseFromContext(c); len(names) != 0 {
		state.reverse = maps.Clone(names)
	}
	state.scoped = account.UsesOpenAICodexProtocol() && (gjson.GetBytes(body, "prompt_cache_key").String() != "" || gjson.GetBytes(body, "client_metadata").IsObject())
	state.path = s.conversionWirePath(ctx, account, codexAccountIdentitySource(c, account), state.scoped)
	if a, _ := ctx.Value(openAITierAttemptKey{}).(*openAITierAttempt); a != nil && a.decision != nil {
		state.tier = a.decision.OutboundTier
	}
	return body, nil
}
