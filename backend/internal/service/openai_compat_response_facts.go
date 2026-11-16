package service

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

type openAICompatBufferedOutcome struct {
	facts    OpenAIForwardResult
	terminal OpenAIResponsesTerminal
	payload  []byte
}

func observeOpenAICompatFrame(c *gin.Context, resp *http.Response, frame openAICompatSSEFrame, usage *OpenAIUsage, facts *OpenAIForwardResult) OpenAIResponsesTerminal {
	data := []byte(frame.Data)
	terminal := ClassifyOpenAIResponsesTerminal(frame.EventType, data)
	// A transport event name such as "message" does not override the payload.
	if terminal.Reason == "type_status_conflict" && !ClassifyOpenAIResponsesTerminal(frame.EventType, nil).Seen {
		terminal = ClassifyOpenAIResponsesTerminal("", data)
	}
	// Baseline readers skip unparseable extensions; malformed terminal candidates
	// remain failures. Known business DTO errors are checked by each converter.
	if !terminal.Seen {
		terminal.Failure = false
	}
	source := "upstream_sse_progressive"
	if terminal.Seen && terminal.Valid {
		source = "upstream_sse_terminal"
	}
	if observed, present := openAIResponsesUsageEvidence(data, source); present {
		if terminal.Seen && terminal.Valid {
			if openAIUsageHasTokens(&observed) || !openAIUsageHasTokens(usage) {
				*usage = observed
			}
		} else {
			mergeOpenAIUsageNonZero(usage, observed)
		}
		usage.InputTokensPresent = usage.InputTokensPresent || observed.InputTokensPresent
		usage.OutputTokensPresent = usage.OutputTokensPresent || observed.OutputTokensPresent
		usage.CacheReadInputTokensPresent = usage.CacheReadInputTokensPresent || observed.CacheReadInputTokensPresent
		usage.CacheCreationInputTokensPresent = usage.CacheCreationInputTokensPresent || observed.CacheCreationInputTokensPresent
		facts.UsagePresent, facts.UsageSource = true, source
	}
	if terminal.Seen {
		facts.TerminalSeen, facts.TerminalValid = true, terminal.Valid
	}
	if terminal.Seen && terminal.Valid && !terminal.Failure && c != nil && c.Request != nil && !strings.Contains(c.Request.URL.Path, "/messages") {
		recordConvertedTerminalMapping(c.Request.Context(), "responses", "chat", terminal.Status, gjson.GetBytes(data, "response.incomplete_details.reason").String())
	}
	facts.PartialFailure = facts.PartialFailure || terminal.Failure
	if terminal.Failure && terminal.Valid {
		if hint, ok := resilience.ParseUpstreamRecoveryHint(resp.Header, data, time.Now()); ok {
			state := resilience.StateFrom(c.Request.Context())
			state.SetUpstreamRecoveryHint(&hint)
			facts.UpstreamRecoveryHint = &hint
		}
	}
	return terminal
}

func finishOpenAICompatResponse(c *gin.Context, account *Account, out *OpenAIForwardResult, err error, facts *OpenAIForwardResult) {
	facts.PartialFailure = facts.PartialFailure || err != nil
	if out != nil {
		out.ResponseFactsKnown = facts.ResponseFactsKnown
		out.UsagePresent, out.UsageSource = facts.UsagePresent, facts.UsageSource
		out.TerminalSeen, out.TerminalValid = facts.TerminalSeen, facts.TerminalValid
		out.ResponseBodyKind, out.PartialFailure = facts.ResponseBodyKind, facts.PartialFailure
		if facts.PartialFailure {
			out.UpstreamRecoveryHint = facts.UpstreamRecoveryHint
		}
	}
	var failover *UpstreamFailoverError
	if errors.As(err, &failover) && facts.UpstreamRecoveryHint != nil {
		failover.UpstreamRecoveryHint = facts.UpstreamRecoveryHint
	}
	if c != nil && c.Request != nil && account != nil {
		resilience.StateFrom(c.Request.Context()).SetGatewayResponseFacts(account.ID, facts.ResponseFactsKnown, facts.UsagePresent, facts.UsageSource, facts.TerminalSeen, facts.TerminalValid, facts.ResponseBodyKind)
	}
	var repair *openAIRequestRepairSignal
	if errors.As(err, &repair) {
		return
	}
	if err != nil && failover == nil && c != nil && c.Writer != nil && c.Writer.Written() && strings.Contains(c.Writer.Header().Get("Content-Type"), "text/event-stream") && (out == nil || !out.ClientDisconnect) {
		protocol := PreOutputProtocolChat
		if c.Request != nil && c.Request.URL != nil && strings.Contains(c.Request.URL.Path, "/messages") {
			protocol = PreOutputProtocolAnthropic
		}
		writeCCStreamReadError(c, protocol, err)
	}
	if out != nil && !facts.PartialFailure && !out.ClientDisconnect {
		ClaimProtocolTerminal(c)
	}
}

func openAICompatFailedTerminalResponse(terminal OpenAIResponsesTerminal, payload []byte) *apicompat.ResponsesResponse {
	root := gjson.GetBytes(payload, "response")
	message := extractOpenAISSEErrorMessage(payload)
	if message == "" {
		message = "Upstream response failed"
	}
	code := ""
	for _, path := range []string{"response.error.code", "error.code"} {
		if value := gjson.GetBytes(payload, path); value.Type == gjson.String || value.Type == gjson.Number {
			code = value.String()
			break
		}
	}
	return &apicompat.ResponsesResponse{ID: root.Get("id").String(), Model: root.Get("model").String(), Status: "failed", Error: &apicompat.ResponsesError{Code: code, Message: message}}
}
