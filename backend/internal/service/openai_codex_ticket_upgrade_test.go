package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketUpgradeIQTargetProbeInjectsAndWaits(t *testing.T) {
	account := ticketTestAccount(41)
	ticket := verifiedTestTicket(account, 292)
	account.Extra[openAICodexTicketExtraKey(ticket.Model)] = ticket
	configState := codexAccountTicketConfigOf(account)
	configState.Enabled = true
	account.Extra[codexAccountTicketConfigKey] = configState
	challenge := iqcheck.NewChallenge()
	answer, err := json.Marshal(map[string]string{"answer": fmt.Sprint(challenge.Answer())})
	require.NoError(t, err)
	transport := &targetProbeTransport{status: http.StatusOK, body: fmt.Sprintf(`{"object":"response","id":"fixture","status":"completed","model":"%s","output":[{"id":"m1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":%q}]}]}`, ticket.Model, string(answer))}
	gateway := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, transport)
	repository := &codexTicketRefreshRepo{accounts: []Account{*account}}
	gateway.accountRepo = repository
	service := &IQCheckService{accounts: repository, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: transport, openaiGatewayService: gateway}}
	result := service.probe(context.Background(), account.ID, iqProbeRequest{
		TargetModel: domain.IQModelAstra, TargetEffort: "low", Challenge: challenge,
		Profile: iqProbeProfile{Model: domain.IQModelAstra, ReasoningEffort: "low"},
	})
	require.Equal(t, "smart", result.Status, result)
	require.Equal(t, ticket.State, transport.request.Header.Get(openAICodexTurnStateHeader))
	require.Equal(t, "protected", result.Diagnostic.StateProtection)
	require.NotEmpty(t, result.StateTicketID)
	firstRequest := transport.request

	require.NoError(t, repository.UpdateExtra(context.Background(), account.ID, map[string]any{openAICodexTicketExtraKey(ticket.Model): nil}))
	result = service.probe(context.Background(), account.ID, iqProbeRequest{
		TargetModel: domain.IQModelAstra, TargetEffort: "low", Challenge: challenge,
		Profile: iqProbeProfile{Model: domain.IQModelAstra, ReasoningEffort: "low"},
	})
	require.Equal(t, "waiting_state", result.Reason)
	require.Same(t, firstRequest, transport.request, "missing STATE must be rejected before another upstream request")
}
