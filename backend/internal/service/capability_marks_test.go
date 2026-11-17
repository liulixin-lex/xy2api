//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func syntheticCapabilityMark() CapabilityMark {
	return CapabilityMark{Model: "synthetic-model", Protocol: "responses", Shape: "non_stream", Reason: "request_unsupported"}
}

func TestCapabilityMarksExpiryRenewalAndRestart(t *testing.T) {
	ctx := context.Background()
	account := capabilityTestAccount(1)
	repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	before, err := repo.GetByID(ctx, 1)
	require.NoError(t, err)
	start := time.Now()
	require.NoError(t, svc.LearnCapabilityMark(ctx, before, syntheticCapabilityMark()))
	require.Empty(t, before.Extra[capabilityMarkExtraKey("synthetic-model", "responses", "non_stream")], "do not mutate scheduler snapshots")
	fresh, err := repo.GetByID(ctx, 1)
	require.NoError(t, err)
	mark := fresh.CapabilityMarked("synthetic-model", "responses", "non_stream", start)
	require.NotNil(t, mark)
	require.WithinDuration(t, start.Add(24*time.Hour), mark.ExpiresAt, time.Second)
	require.NotNil(t, fresh.CapabilityMarked(mark.Model, mark.Protocol, mark.Shape, mark.ExpiresAt.Add(-time.Nanosecond)))
	require.Nil(t, fresh.CapabilityMarked(mark.Model, mark.Protocol, mark.Shape, mark.ExpiresAt))
	require.Nil(t, fresh.CapabilityMarked("another-model", mark.Protocol, mark.Shape, start))
	require.Nil(t, fresh.CapabilityMarked(mark.Model, "chat", mark.Shape, start))
	require.Nil(t, fresh.CapabilityMarked(mark.Model, mark.Protocol, "stream", start))
	// Serialize the complete persisted account as a restart/snapshot round trip.
	raw, err := json.Marshal(fresh)
	require.NoError(t, err)
	var restarted Account
	require.NoError(t, json.Unmarshal(raw, &restarted))
	require.Equal(t, mark, restarted.CapabilityMarked(mark.Model, mark.Protocol, mark.Shape, start))
	old := *mark
	old.ExpiresAt = start.Add(-time.Hour)
	fresh.Extra[capabilityMarkExtraKey(old.Model, old.Protocol, old.Shape)] = old
	require.Nil(t, fresh.CapabilityMarked(old.Model, old.Protocol, old.Shape, start))
	require.NoError(t, svc.LearnCapabilityMark(ctx, fresh, old))
	renewed, err := repo.GetByID(ctx, 1)
	require.NoError(t, err)
	require.True(t, renewed.CapabilityMarked(old.Model, old.Protocol, old.Shape, start).ExpiresAt.After(mark.ExpiresAt))
}

func TestCapabilityMarksFingerprintAndManagedExtra(t *testing.T) {
	account := capabilityTestAccount(1)
	repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	require.NoError(t, svc.LearnCapabilityMark(context.Background(), account, syntheticCapabilityMark()))
	key := capabilityMarkExtraKey("synthetic-model", "responses", "non_stream")
	for _, tc := range []struct {
		name   string
		edit   func(*Account)
		active bool
	}{
		{"priority", func(a *Account) { a.Priority++ }, true},
		{"concurrency", func(a *Account) { a.Concurrency++ }, true},
		{"rate multiplier", func(a *Account) { rate := 1.5; a.RateMultiplier = &rate }, true},
		{"platform", func(a *Account) { a.Platform = PlatformGrok }, false},
		{"type", func(a *Account) { a.Type = AccountTypeOAuth }, false},
		{"credentials", func(a *Account) { a.Credentials["api_key"] = "rotated-test-key" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := *account
			copy.Credentials = map[string]any{}
			for k, v := range account.Credentials {
				copy.Credentials[k] = v
			}
			tc.edit(&copy)
			require.Equal(t, tc.active, copy.CapabilityMarked("synthetic-model", "responses", "non_stream", time.Now()) != nil)
		})
	}
	injected := capabilityMarkExtraKey("injected", "responses", "stream")
	next := MergeCapabilityLearningExtra(map[string]any{key: nil, injected: account.Extra[key], "setting": true}, account.Extra)
	require.Equal(t, account.Extra[key], next[key])
	require.NotContains(t, next, injected)
	require.Equal(t, true, next["setting"])
	require.Nil(t, (*Account)(nil).CapabilityMarked("synthetic-model", "responses", "non_stream", time.Now()))
}

func TestCapabilityMarksConcurrentTuplesAndMappedAdmission(t *testing.T) {
	withResilienceSettings(t, func(cfg *resilience.Settings) { cfg.Capability.StreamOnlyLearning = false })
	account := capabilityTestAccount(1)
	account.Credentials["api_protocol"] = APIProtocolResponses
	account.Credentials["model_mapping"] = map[string]any{"public-model": "synthetic-model", "public-other": "synthetic-other"}
	repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	snapshot, err := repo.GetByID(context.Background(), 1)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		mark := syntheticCapabilityMark()
		if i == 1 {
			mark.Model, mark.Protocol, mark.Shape = "synthetic-second", "chat", "stream"
		}
		wg.Add(1)
		go func(mark CapabilityMark) {
			defer wg.Done()
			errors <- svc.LearnCapabilityMark(context.Background(), snapshot, mark)
		}(mark)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	fresh, err := repo.GetByID(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, fresh.CapabilityMarked("synthetic-second", "chat", "stream", time.Now()))
	for _, tc := range []struct {
		model              string
		stream, compatible bool
	}{{"public-model", false, false}, {"public-model", true, true}, {"public-other", false, true}} {
		ctx := resilience.WithCapabilityRequest(context.Background(), "responses", tc.stream)
		compatible, reason := svc.isAccountRequestCompatibleReason(ctx, fresh, OpenAIAccountScheduleRequest{RequestedModel: tc.model})
		require.Equal(t, tc.compatible, compatible, "model=%s stream=%t reason=%s", tc.model, tc.stream, reason)
	}
	require.Equal(t, true, fresh.Extra["unrelated_setting"])
}

func TestCapabilityMarksLearnOnlyExplicitUpstreamRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		reason string
	}{
		{"model", 404, `{"error":{"code":"MODEL_UNSUPPORTED"}}`, "model_unsupported"},
		{"request", 400, `{"error":{"message":"this site does not support this request"}}`, "request_unsupported"},
		{"protocol", 503, `{"error":{"code":"unsupported_protocol"}}`, "request_unsupported"},
		{"temporary missing channel", 503, `{"error":{"code":"model_not_found","message":"synthetic channel temporarily unavailable"}}`, ""},
		{"429 missing channel", 429, `{"error":{"code":"model_not_found"}}`, ""},
		{"input modality", 400, `{"error":{"code":"unsupported_input_capability"}}`, ""},
		{"frontend", 400, `{"error":{"message":"unsupported frontend"}}`, ""},
		{"detail frontend", 400, `{"detail":"unsupported frontend"}`, ""},
		{"ambiguous 404", 404, `{"error":{"message":"not found"}}`, ""},
		{"parameter", 400, `{"error":{"message":"invalid parameter"}}`, ""},
		{"success echo", 200, `{"error":{"code":"MODEL_UNSUPPORTED"}}`, ""},
		{"stream only remains separate", 400, `{"error":{"code":"streaming_required"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := capabilityTestAccount(1)
			repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
			svc := &OpenAIGatewayService{accountRepo: repo}
			learn := svc.capabilityFailureLearner(context.Background(), account, []byte(`{"model":"synthetic-model","stream":true}`), "responses")
			require.NotNil(t, learn)
			learn(tc.status, []byte(tc.body))
			mark := account.CapabilityMarked("synthetic-model", "responses", "stream", time.Now())
			if tc.reason == "" {
				if tc.name == "input modality" || tc.name == "frontend" || tc.name == "detail frontend" {
					classified := classifySchedAttempt(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault}, &resilience.Outcome{Source: resilience.SourceHTTP, StatusCode: tc.status, Body: []byte(tc.body)}, false)
					require.Equal(t, schedquality.ClassCapability, classified.Class)
				}
				require.Nil(t, mark)
				require.Zero(t, repo.writes)
				return
			}
			require.NotNil(t, mark)
			require.Equal(t, tc.reason, mark.Reason)
		})
	}
}

func TestCapabilityMarksSSEAndWebSocket(t *testing.T) {
	for _, ws := range []bool{false, true} {
		t.Run(fmt.Sprint("websocket=", ws), func(t *testing.T) {
			account := capabilityTestAccount(1)
			repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
			svc := &OpenAIGatewayService{accountRepo: repo}
			state := resilience.NewRequestState()
			state.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "synthetic-model"})
			ctx := resilience.WithRequestState(context.Background(), state)
			recorder := &errclassRecorder{}
			svc.schedAttemptRecorder = recorder
			capture := svc.beginSchedAttempt(ctx, account, nil, 0, true)
			if ws {
				svc.schedAttemptRecorder = recorder
				turn := &schedWSTurnCapture{}
				turn.begin(svc, capture.ctx, account, []byte(`{"model":"synthetic-model","type":"response.create"}`))
				capture = turn.active
			} else {
				capture.learnCapability = svc.capabilityFailureLearner(capture.ctx, account, []byte(`{"model":"synthetic-model","stream":true}`), "responses")
			}
			capture.observe([]byte(`{"type":"response.failed","response":{"error":{"code":"MODEL_UNSUPPORTED"}}}`))
			capture.finish(nil, nil)
			require.NotNil(t, account.CapabilityMarked("synthetic-model", "responses", "stream", time.Now()))
			require.Len(t, recorder.finishes, 1)
			require.Equal(t, schedquality.ClassCapability, recorder.finishes[0].Class)
		})
	}
}

func TestCapabilityMarksAdminClearAndReset(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(fmt.Sprint("reset=", reset), func(t *testing.T) {
			svc, engine, adminRepo := schedAdminFixture()
			account := adminRepo.rows[1]
			account.Type = AccountTypeAPIKey
			account.Credentials = map[string]any{"api_key": "test"}
			repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
			gateway := &OpenAIGatewayService{accountRepo: repo}
			require.NoError(t, gateway.LearnCapabilityMark(context.Background(), account, syntheticCapabilityMark()))
			svc.d.Now = time.Now
			engine.resetAt = time.Now()
			svc.d.Capability = &adminServiceImpl{accountRepo: repo}
			states, err := svc.AutoStates(context.Background(), 1)
			require.NoError(t, err)
			found := false
			for _, state := range states {
				if state.Kind == "capability_marked" {
					found = true
					require.True(t, state.Clearable)
				}
			}
			require.True(t, found)
			if reset {
				result, err := svc.Reset(context.Background(), 1, 7)
				require.NoError(t, err)
				require.Contains(t, result.Cleared, "capability_marked")
				require.Empty(t, result.Failed)
			} else {
				cleared, err := svc.ClearAutoState(context.Background(), 1, "capability_marked", 7)
				require.NoError(t, err)
				require.True(t, cleared)
			}
			require.Nil(t, account.CapabilityMarked("synthetic-model", "responses", "non_stream", time.Now()))
		})
	}
}

func TestCapabilityMarksConvertedProtocolAndWildcard(t *testing.T) {
	account := capabilityTestAccount(1)
	account.Credentials["api_protocol"] = APIProtocolResponses
	repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	mark := syntheticCapabilityMark()
	mark.Shape = "stream"
	require.NoError(t, svc.LearnCapabilityMark(context.Background(), account, mark))
	chat := resilience.WithCapabilityRequest(context.Background(), "chat", false)
	require.Equal(t, resilience.ReasonCapabilityMarked, capabilityEligibilityReason(chat, account, mark.Model, time.Now()), "Chat-to-Responses sends stream=true upstream")
	native := resilience.WithCapabilityRequest(context.Background(), "responses", false)
	require.Empty(t, capabilityEligibilityReason(native, account, mark.Model, time.Now()))
	mark.Model, mark.Shape = "wildcard-fixture", "*"
	require.NoError(t, svc.LearnCapabilityMark(context.Background(), account, mark))
	require.NotNil(t, account.CapabilityMarked(mark.Model, mark.Protocol, "non_stream", time.Now()))
	require.NotNil(t, account.CapabilityMarked(mark.Model, mark.Protocol, "stream", time.Now()))
	require.Nil(t, account.CapabilityMarked(mark.Model, "chat", "stream", time.Now()))
}

func TestCapabilityMarksGrokHookAndUnrepresentedShapes(t *testing.T) {
	account := capabilityTestAccount(1)
	account.Platform = PlatformGrok
	repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	ctx := resilience.WithCapabilityRequest(context.Background(), "responses", true)
	failure := svc.LearnStreamOnlyFailure(ctx, account, "responses", true, 400, []byte(`{"error":{"code":"MODEL_UNSUPPORTED"}}`), []byte(`{"model":"synthetic-model","stream":true}`))
	require.Nil(t, failure)
	require.NotNil(t, account.CapabilityMarked("synthetic-model", "responses", "stream", time.Now()))
	before := repo.writes
	svc.LearnStreamOnlyFailure(ctx, account, "chat", true, 400, []byte(`{"error":{"code":"MODEL_UNSUPPORTED"}}`), []byte(`{"model":"chat-fixture","stream":true}`))
	require.Equal(t, before, repo.writes, "Grok Chat remains outside new marking")
	require.Nil(t, svc.capabilityFailureLearner(ctx, account, []byte(`{"model":"gpt-image-1","stream":true}`), "responses"))
	require.Nil(t, svc.capabilityFailureLearner(ctx, account, nil, "responses"))
	require.Nil(t, svc.capabilityFailureLearner(ctx, account, []byte(`{"model":"fixture"}`), "anthropic"))
}

func TestCapabilityMarksPassiveNamespaceAndMappedForward(t *testing.T) {
	withResilienceSettings(t, nil)
	account := rawChatCompletionsTestAccount()
	account.Credentials["api_protocol"] = APIProtocolResponses
	account.Credentials["model_mapping"] = map[string]any{"public-fixture": "synthetic-upstream"}
	repo := &capabilityRepo{accounts: map[int64]*Account{account.ID: account}}
	upstream := &capabilityForwardUpstream{&httpUpstreamRecorder{resp: &http.Response{
		StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"MODEL_UNSUPPORTED"}}`)),
	}}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream, accountRepo: repo, schedAttemptRecorder: &errclassRecorder{}}
	body := `{"model":"public-fixture","stream":false,"instructions":"test","input":"fixture","tools":[{"type":"namespace","name":"image_gen","tools":[]}]}`
	c, _ := compatStreamContext("/v1/responses", body)
	state := resilience.NewRequestState()
	state.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: "synthetic-upstream"})
	ctx := resilience.WithCapabilityRequest(resilience.WithRequestState(context.Background(), state), "responses", false)
	c.Request = c.Request.WithContext(ctx)
	_, err := svc.Forward(ctx, c, account, []byte(body))
	require.Error(t, err)
	fresh, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	mark := fresh.CapabilityMarked("synthetic-upstream", "responses", "non_stream", time.Now())
	require.NotNil(t, mark, "passive image_gen namespace must permit text capability learning")
	require.Equal(t, "*", mark.Shape, "model refusals cover both stream modes")
	require.Nil(t, fresh.CapabilityMarked("public-fixture", "responses", "non_stream", time.Now()))
	for _, stream := range []bool{false, true} {
		ctx := resilience.WithCapabilityRequest(context.Background(), "responses", stream)
		compatible, reason := svc.isAccountRequestCompatibleReason(ctx, fresh, OpenAIAccountScheduleRequest{RequestedModel: "public-fixture"})
		require.False(t, compatible)
		require.Equal(t, resilience.ReasonCapabilityMarked, reason)
	}
}

func TestCapabilityMarksAfterOutputAreInterrupted(t *testing.T) {
	account := capabilityTestAccount(1)
	repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
	recorder := &errclassRecorder{}
	svc := &OpenAIGatewayService{accountRepo: repo, schedAttemptRecorder: recorder}
	state := resilience.NewRequestState()
	state.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "synthetic-model"})
	ctx := resilience.WithRequestState(context.Background(), state)
	capture := svc.beginSchedAttempt(ctx, account, nil, 0, true)
	capture.learnCapability = svc.capabilityFailureLearner(ctx, account, []byte(`{"model":"synthetic-model","stream":true}`), "responses")
	at := time.Now()
	capture.MarkOutput(OutputValid, at)
	state.NoteSemanticOutput(at)
	capture.observe([]byte(`{"type":"response.failed","response":{"error":{"code":"MODEL_UNSUPPORTED"}}}`))
	capture.finish(nil, nil)
	require.Zero(t, repo.writes)
	require.Len(t, recorder.finishes, 1)
	require.Equal(t, schedquality.ClassInterrupted, recorder.finishes[0].Class)
}

func TestCapabilityMarksLimitRenewalExpiryAndIdentity(t *testing.T) {
	account := capabilityTestAccount(1)
	repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	ctx := context.Background()
	for i := 0; i < CapabilityMarkLimit; i++ {
		mark := syntheticCapabilityMark()
		mark.Model = fmt.Sprintf("fixture-%d", i)
		require.NoError(t, svc.LearnCapabilityMark(ctx, account, mark))
	}
	mark := syntheticCapabilityMark()
	mark.Model = "overflow-fixture"
	require.NoError(t, svc.LearnCapabilityMark(ctx, account, mark))
	require.Nil(t, account.CapabilityMarked(mark.Model, mark.Protocol, mark.Shape, time.Now()))
	// Both callers hold the same stale 31-record snapshot. The repository's
	// lock must serialize the final capacity check against the actual row.
	delete(account.Extra, capabilityMarkExtraKey("fixture-31", mark.Protocol, mark.Shape))
	snapshot, err := repo.GetByID(ctx, 1)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		m := mark
		m.Model = fmt.Sprintf("race-fixture-%d", i)
		wg.Add(1)
		go func(m CapabilityMark) { defer wg.Done(); errors <- svc.LearnCapabilityMark(ctx, snapshot, m) }(m)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	count := 0
	for key := range account.Extra {
		if strings.HasPrefix(key, CapabilityMarksExtraKey+"_") {
			count++
		}
	}
	require.Equal(t, CapabilityMarkLimit, count)
	mark.Model = "fixture-0"
	old := account.CapabilityMarked(mark.Model, mark.Protocol, mark.Shape, time.Now())
	require.NotNil(t, old)
	require.NoError(t, svc.LearnCapabilityMark(ctx, account, mark))
	require.True(t, account.CapabilityMarked(mark.Model, mark.Protocol, mark.Shape, time.Now()).ExpiresAt.After(old.ExpiresAt))
	expired := *old
	expired.ExpiresAt = time.Now().Add(-time.Second)
	account.Extra[capabilityMarkExtraKey(mark.Model, mark.Protocol, mark.Shape)] = expired
	mark.Model = "expiry-room-fixture"
	require.NoError(t, svc.LearnCapabilityMark(ctx, account, mark))
	require.NotNil(t, account.CapabilityMarked(mark.Model, mark.Protocol, mark.Shape, time.Now()))
	account.Credentials["api_key"] = "rotated-fixture"
	mark.Model = "new-identity-fixture"
	require.NoError(t, svc.LearnCapabilityMark(ctx, account, mark))
	require.NotNil(t, account.CapabilityMarked(mark.Model, mark.Protocol, mark.Shape, time.Now()))
}

func TestCapabilityMarksClearScopesAndResetBoth(t *testing.T) {
	for _, action := range []string{"marks", "stream_only", "reset"} {
		t.Run(action, func(t *testing.T) {
			svc, engine, adminRepo := schedAdminFixture()
			account := adminRepo.rows[1]
			account.Type, account.Credentials = AccountTypeAPIKey, map[string]any{"api_key": "test"}
			repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
			gateway := &OpenAIGatewayService{accountRepo: repo}
			require.NoError(t, gateway.LearnCapabilityMark(context.Background(), account, syntheticCapabilityMark()))
			account.Extra[capabilityLearnedResponsesKey] = map[string]any{"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339Nano), "fingerprint": capabilityFingerprint(account)}
			svc.d.Now, engine.resetAt = time.Now, time.Now()
			svc.d.Capability = &adminServiceImpl{accountRepo: repo}
			if action == "reset" {
				result, err := svc.Reset(context.Background(), 1, 7)
				require.NoError(t, err)
				require.Contains(t, result.Cleared, "capability_marked")
				require.Contains(t, result.Cleared, "capability_learned")
				require.Empty(t, result.Failed)
			} else {
				key := "capability_marked"
				if action == "stream_only" {
					key = "capability_learned"
				}
				ok, err := svc.ClearAutoState(context.Background(), 1, key, 7)
				require.NoError(t, err)
				require.True(t, ok)
			}
			require.Equal(t, action == "stream_only", account.CapabilityMarked("synthetic-model", "responses", "non_stream", time.Now()) != nil)
			require.Equal(t, action == "marks", len(account.CapabilityLearned(time.Now())) > 0)
		})
	}
}

func TestCapabilityMarksModelRoutedAccountsStayEligible(t *testing.T) {
	account := capabilityTestAccount(1)
	account.Platform = PlatformOpenCodeGo
	account.Credentials["base_url"] = "https://routing.example/v1"
	require.True(t, account.routesByModel())
	repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	require.Nil(t, svc.capabilityFailureLearner(context.Background(), account, []byte(`{"model":"synthetic-model"}`), "responses"))
	require.NoError(t, svc.LearnCapabilityMark(context.Background(), account, syntheticCapabilityMark()))
	require.Zero(t, repo.writes)
	mark := syntheticCapabilityMark()
	mark.Fingerprint, mark.ExpiresAt = capabilityFingerprint(account), time.Now().Add(time.Hour)
	account.Extra[capabilityMarkExtraKey(mark.Model, mark.Protocol, mark.Shape)] = mark
	require.Empty(t, capabilityEligibilityReason(resilience.WithCapabilityRequest(context.Background(), "responses", false), account, mark.Model, time.Now()))
}

func TestCapabilityMarksExcludeExplicitImagesAndNativeCompact(t *testing.T) {
	account := capabilityTestAccount(1)
	repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	for _, body := range []string{
		`{"model":"gpt-image-1","prompt":"fixture"}`,
		`{"model":"synthetic-model","input":"fixture","tools":[{"type":"image_generation"}]}`,
		`{"model":"synthetic-model","stream":true,"input":[{"type":"compaction_trigger"}]}`,
	} {
		learn := svc.capabilityFailureLearner(context.Background(), account, []byte(body), "responses")
		require.Nil(t, learn, "excluded request body: %s", body)
	}
	require.Zero(t, repo.writes)
}

func TestCapabilityMarksStreamRespectsExplicitErrorStatus(t *testing.T) {
	for _, status := range []int{400, 401, 404, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			account := capabilityTestAccount(1)
			repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
			svc := &OpenAIGatewayService{accountRepo: repo}
			learn := svc.capabilityFailureLearner(context.Background(), account, []byte(`{"model":"synthetic-model","stream":true}`), "responses")
			observeCapabilityFailure(learn, []byte(fmt.Sprintf(`{"type":"response.failed","response":{"error":{"code":"model_not_found","status_code":%d}}}`, status)))
			require.Equal(t, status == 400 || status == 404, account.CapabilityMarked("synthetic-model", "responses", "stream", time.Now()) != nil)
		})
	}
}

func TestCapabilityMarksWebSocketPreludeAndSemanticOutput(t *testing.T) {
	for _, effective := range []bool{false, true} {
		t.Run(fmt.Sprint("semantic_output=", effective), func(t *testing.T) {
			account := capabilityTestAccount(1)
			repo := &capabilityRepo{accounts: map[int64]*Account{1: account}}
			recorder := &errclassRecorder{}
			svc := &OpenAIGatewayService{accountRepo: repo, schedAttemptRecorder: recorder}
			state := resilience.NewRequestState()
			state.SetSchedSelection(resilience.SchedSelection{AccountID: 1, Model: "synthetic-model"})
			ctx := resilience.WithRequestState(context.Background(), state)
			capture := svc.beginSchedAttempt(ctx, account, nil, 0, true)
			capture.learnCapability = svc.capabilityWSFailureLearner(ctx, account, []byte(`{"type":"response.create","model":"synthetic-model"}`))
			created := []byte(`{"type":"response.created","response":{"id":"fixture-response"}}`)
			capture.observe(created)
			noteGatewayWSOutput(ctx, created)
			require.Positive(t, state.ClientOutputBytes())
			require.True(t, state.SemanticOutputAt().IsZero(), "response.created carries no effective output")
			if effective {
				content := []byte(`{"type":"response.output_text.delta","delta":"fixture"}`)
				markSchedWSOutput(capture, content)
				noteGatewayWSOutput(ctx, content)
				require.False(t, state.SemanticOutputAt().IsZero())
			}
			capture.observe([]byte(`{"type":"response.failed","response":{"error":{"code":"MODEL_UNSUPPORTED"}}}`))
			capture.finish(nil, nil)
			require.Equal(t, !effective, account.CapabilityMarked("synthetic-model", "responses", "stream", time.Now()) != nil)
			require.Len(t, recorder.finishes, 1)
			want := schedquality.ClassCapability
			if effective {
				want = schedquality.ClassInterrupted
			}
			require.Equal(t, want, recorder.finishes[0].Class)
		})
	}
}
