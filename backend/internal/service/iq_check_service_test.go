package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/stretchr/testify/require"
)

type iqRunAccounts struct {
	AccountRepository
	accounts map[int64]*Account
	errors   map[int64]error
}

func (r *iqRunAccounts) GetByID(_ context.Context, id int64) (*Account, error) {
	if err := r.errors[id]; err != nil {
		return nil, err
	}
	return r.accounts[id], nil
}

type iqRunTargetRepo struct {
	IQTargetRepository
	saved  []domain.IQTargetSettings
	queued []string
	result []IQRunTarget
}

func (r *iqRunTargetRepo) SaveIQTargets(_ context.Context, _ int64, patch domain.IQTargetsPatch) error {
	r.saved = append(r.saved, patch.Targets...)
	return nil
}
func (r *iqRunTargetRepo) QueueIQTargets(_ context.Context, _ int64, models []string) ([]IQRunTarget, error) {
	r.queued = models
	return r.result, nil
}

func TestIQRequestRunCreatesDefaultTargetsAndAllowsStoppedAccounts(t *testing.T) {
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: false, Credentials: map[string]any{"api_key": "fixture"}}
	repo := &iqRunTargetRepo{result: []IQRunTarget{{Model: "gpt-6-astra", State: "queued"}, {Model: "gpt-6.1-sol", State: "queued"}, {Model: "custom-model", State: "queued"}}}
	svc := &IQCheckService{accounts: &iqRunAccounts{accounts: map[int64]*Account{7: account}}, targetRepo: repo}
	targets, err := svc.RequestRun(context.Background(), account.ID, nil)
	require.NoError(t, err)
	require.Len(t, targets, 3)
	require.Len(t, repo.saved, 2)
	require.Equal(t, []domain.IQTargetSettings{{Model: domain.IQModelAstra}, {Model: domain.IQModelSol}}, repo.saved)
	require.Nil(t, repo.queued, "omitting models queues all configured targets")
}

func TestIQRequestRunRejectsUnavailableCredentialsAndBatchMapsReasons(t *testing.T) {
	accounts := &iqRunAccounts{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{}},
		2: {ID: 2, Platform: PlatformGemini, Type: AccountTypeOAuth},
	}}
	service := &IQCheckService{accounts: accounts, targetRepo: &iqRunTargetRepo{}}
	result, err := service.RequestRun(context.Background(), 1, []string{domain.IQModelSol})
	require.NoError(t, err)
	require.Equal(t, []IQRunTarget{{Model: domain.IQModelSol, State: "rejected", Reason: "credentials_unavailable"}}, result)
	result, err = service.RequestRun(context.Background(), 2, []string{domain.IQModelAstra})
	require.NoError(t, err)
	require.Equal(t, []IQRunTarget{{Model: domain.IQModelAstra, State: "rejected", Reason: "not_openai"}}, result)

	batch := service.RequestRunBatch(context.Background(), []int64{1, 2}, nil)
	require.Equal(t, "credentials_unavailable", batch[0].Reason)
	require.Empty(t, batch[0].Targets)
	require.Equal(t, "not_openai", batch[1].Reason)
	require.Empty(t, batch[1].Targets)
}

func TestIQTargetProbeUsesConfiguredResponsesAndChatRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, accountType, mode, protocol, targetModel string
	}{
		{name: "oauth responses", accountType: AccountTypeOAuth, protocol: "responses", targetModel: domain.IQModelAstra},
		{name: "api responses", accountType: AccountTypeAPIKey, mode: "force_responses", protocol: "responses", targetModel: "custom/model"},
		{name: "api chat", accountType: AccountTypeAPIKey, mode: "force_chat_completions", protocol: "chat_completions", targetModel: "custom/model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credentials := map[string]any{"api_key": "fixture", "access_token": "fixture", "base_url": "https://example.test"}
			if tc.accountType == AccountTypeAPIKey {
				credentials["model_mapping"] = map[string]any{"custom/model": domain.IQModelAstra}
			}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: tc.accountType,
				Credentials: credentials,
				Extra:       map[string]any{"openai_responses_mode": tc.mode}}
			challenge := iqcheck.NewChallenge()
			answer := fmt.Sprintf(`{"answer":%d}`, challenge.Answer())
			body := fmt.Sprintf(`{"status":"completed","model":"gpt-6-astra","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}]}`, answer)
			if tc.protocol == "chat_completions" {
				body = fmt.Sprintf(`{"model":"gpt-6-astra","choices":[{"index":0,"message":{"content":%q},"finish_reason":"stop"}]}`, answer)
			}
			transport := &targetProbeTransport{status: http.StatusOK, body: body}
			probe := &IQCheckService{accounts: &targetProbeAccounts{account: account}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: transport}}
			result := probe.probe(context.Background(), account.ID, iqProbeRequest{
				TargetModel: tc.targetModel, TargetEffort: "low", Challenge: challenge,
				Profile: iqProbeProfile{Model: tc.targetModel, ReasoningEffort: "low"},
			})
			require.Equal(t, "smart", result.Status, result)
			require.Equal(t, "gpt-6-astra", transport.payload["model"], "the probe uses the canonical account model")
			require.Equal(t, false, transport.payload["store"])
			if tc.protocol == "responses" {
				require.Contains(t, transport.payload, "reasoning")
				require.NotContains(t, transport.payload, "reasoning_effort")
			} else {
				require.Contains(t, transport.payload, "reasoning_effort")
				require.NotContains(t, transport.payload, "reasoning")
			}
		})
	}
}

func TestIQTargetProbeParsesOAuthResponsesStreamTerminal(t *testing.T) {
	account := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "fixture"}, Extra: map[string]any{"openai_responses_mode": "force_responses"}}
	challenge := iqcheck.NewChallenge()
	answer := fmt.Sprintf(`{"answer":%d}`, challenge.Answer())
	transport := &targetProbeTransport{status: http.StatusOK, headers: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"fixture-request"}}, body: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ignored partial\"}\n\n" +
		fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}]}}\n\n", answer)}
	probe := &IQCheckService{accounts: &targetProbeAccounts{account: account}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: transport}}
	result := probe.probe(context.Background(), account.ID, iqProbeRequest{
		TargetModel: domain.IQModelAstra, TargetEffort: "low", Challenge: challenge,
		Profile: iqProbeProfile{Model: domain.IQModelAstra, ReasoningEffort: "low"},
	})
	require.Equal(t, "smart", result.Status, result)
	require.Equal(t, answer, result.Answer)
	require.Equal(t, "fixture-request", result.Diagnostic.RequestID)
	require.Equal(t, "single_attempt", result.Diagnostic.RetryVisibility)
}
