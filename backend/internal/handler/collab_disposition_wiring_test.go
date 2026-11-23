//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCollabWiringDispositionBeforeFirstSend(t *testing.T) {
	quietReplayLogs(t)
	settings := resilience.Defaults()
	settings.Collab.Ladder = true
	settings.Collab.Records = false
	var sent []byte
	fixture := newCollabWiringFixture(t, settings, func(req *http.Request, _ int64) (*http.Response, error) {
		sent, _ = io.ReadAll(req.Body)
		return replayResponse(200, "application/json", `{"id":"resp_sample","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`), nil
	})
	body := []byte(`{"model":"gpt-5.5","input":[` + replayTask + `,` + replayQuestion + `]}`)
	_, state := fixture.request(body)
	key := state.CollabState().Items[0].Key
	account, _ := fixture.gateway.CollabRecordAccount(71, "https://fixture.invalid")
	require.True(t, fixture.gateway.CollabRecords().Replaced(context.Background(), resilience.CollabReplacedRecord{Key: key, Account: account, Placeholder: resilience.CollabPlaceholderProcessedTask, ExpiresAt: time.Now().Add(time.Hour)}))
	rec, _ := fixture.request(body)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 2, fixture.upstream.sends)
	require.Equal(t, "input_text", gjson.GetBytes(sent, "input.0.content.0.type").String())
	require.Contains(t, gjson.GetBytes(sent, "input.0.content.0.text").String(), "already processed")
}

func TestCollabWiringHistoryPreservesLegacyBytes(t *testing.T) {
	quietReplayLogs(t)
	replayGroupLookup = true
	defer func() { replayGroupLookup = false }()
	for _, c := range collaborationReplayCorpus() {
		if c.Name != "next_tool_loop/pass_false/stream_false/attempts_2" {
			continue
		}
		settings := resilience.Defaults()
		before := runCollaborationReplay(t, c, settings)
		settings.Collab.Ladder = true
		after := runCollaborationReplay(t, c, settings)
		require.NoError(t, checkReplayOn(settings, "ladder", []replayGolden{before}, []replayGolden{after}))
	}
}

func TestCollabWiringRecordOrderingAfterFailure(t *testing.T) {
	quietReplayLogs(t)
	settings := resilience.Defaults()
	settings.Collab.Ladder, settings.Collab.RecordRouting = true, true
	failed := false
	var accounts []int64
	fixture := newCollabWiringFixture(t, settings, func(_ *http.Request, account int64) (*http.Response, error) {
		accounts = append(accounts, account)
		if failed && account == 71 {
			return replayResponse(503, "application/json", `{"error":{"type":"server_error","message":"temporarily unavailable"}}`), nil
		}
		return replayResponse(200, "application/json", `{"id":"resp_sample","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`), nil
	})
	fixture.handler.maxAccountSwitches = 2
	for _, id := range []int64{72, 73} {
		account := fixture.repo.accounts[0]
		account.ID, account.Priority = id, int(id-71)
		fixture.repo.accounts = append(fixture.repo.accounts, account)
	}
	body := []byte(`{"model":"gpt-5.5","input":[` + replayTask + `,` + replayQuestion + `]}`)
	_, state := fixture.request(body)
	key := state.CollabState().Items[0].Key
	digest, _ := fixture.gateway.CollabRecordAccount(73, "https://fixture.invalid")
	token, ok := fixture.gateway.CollabRecords().PendingIssued(context.Background(), resilience.CollabIssuedRecord{Scope: key.Scope, Call: "synthetic-call", Digest: key.Digest, Account: digest, Site: "synthetic-site", Generation: 1, ExpiresAt: time.Now().Add(time.Hour)})
	require.True(t, ok)
	require.True(t, fixture.gateway.CollabRecords().ConfirmIssued(context.Background(), token))
	failed = true
	rec, _ := fixture.request(body)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(73), accounts[len(accounts)-1])
	require.NotContains(t, accounts, int64(72), "issuer preference belongs inside the normal admitted round")
}
