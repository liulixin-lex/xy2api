//go:build unit || integration

package repository

import (
	"context"
	"database/sql"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Shared by the isolated local-PG test and CI's migrated integration database.
func assertSchedAttemptV2Evidence(t *testing.T, db *sql.DB) {
	t.Helper()
	requestID := uuid.NewString()
	t.Cleanup(func() { _, _ = db.Exec("DELETE FROM account_attempt_samples WHERE request_id=$1", requestID) })
	// Keep the global ledger's other fixtures outside our page and finish ranges.
	start := time.Now().UTC().Truncate(time.Microsecond).Add(24 * time.Hour)
	legacyID := uuid.NewString()
	_, err := db.Exec(`INSERT INTO account_attempt_samples
        (attempt_id, started_at, finished_at, account_id, group_id, model, request_class, stream, warm, outcome, session_key, request_id, inserted_at)
        VALUES ($1,$2,$3,1,1,'model',1,true,false,1,$4,$4,$5)`,
		legacyID, start, start.Add(time.Second), requestID, start.Add(2*time.Second))
	require.NoError(t, err)
	var version int
	require.NoError(t, db.QueryRow("SELECT capture_version FROM account_attempt_samples WHERE attempt_id=$1", legacyID).Scan(&version))
	require.Equal(t, 1, version)
	ctx := context.Background()
	repo := &schedAttemptRepository{db: db}
	// A page containing only v1 data must not emit evidence or advance the cursor.
	rows, cursor, err := repo.LoadInsertedAfter(ctx, start.Add(-time.Second), 1)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.True(t, cursor.IsZero())
	finished, err := repo.LoadFinishedSince(ctx, start)
	require.NoError(t, err)
	require.Empty(t, finished)
	emptyPage, err := repo.LoadFinishedPage(ctx, start, start.Add(time.Hour), service.SchedAttemptCursor{}, 1)
	require.NoError(t, err)
	require.Empty(t, emptyPage, "v1 evidence must not enter paged boot reads")
	current := schedquality.AttemptSample{AttemptID: uuid.NewString(), StartedAt: start.Add(20 * time.Second), GroupID: 1, Model: "model", SessionKey: requestID}
	previous, err := repo.LoadPreviousAttempts(ctx, []schedquality.AttemptSample{current})
	require.NoError(t, err)
	require.Nil(t, previous[current.AttemptID])

	event, valid, answer, status, output := 10, 20, 30, 503, 42
	sample := schedquality.AttemptSample{
		AttemptID: uuid.NewString(), StartedAt: start.Add(3 * time.Second), FinishedAt: start.Add(4 * time.Second),
		AccountID: 1, GroupID: 1, Model: "model", Stream: true, SessionKey: requestID, RequestID: requestID,
		TTFOMs: &valid, FirstEventMs: &event, FirstAnswerMs: &answer, Outcome: schedquality.OutcomeAccountFault,
		Class: schedquality.ClassUpstream, HTTPStatus: &status, StatusOrigin: schedquality.OriginUpstream, OutputBytes: &output,
	}
	nullable := sample
	nullable.AttemptID, nullable.StartedAt, nullable.FinishedAt = uuid.NewString(), start.Add(5*time.Second), start.Add(6*time.Second)
	nullable.TTFOMs, nullable.FirstEventMs, nullable.FirstAnswerMs = nil, nil, nil
	nullable.Class, nullable.HTTPStatus, nullable.StatusOrigin, nullable.OutputBytes = schedquality.ClassNone, nil, schedquality.OriginNone, nil
	require.NoError(t, repo.InsertBatch(ctx, []schedquality.AttemptSample{sample, nullable}))
	require.NoError(t, db.QueryRow("SELECT capture_version FROM account_attempt_samples WHERE attempt_id=$1", sample.AttemptID).Scan(&version))
	require.Equal(t, 3, version)
	_, err = db.Exec("UPDATE account_attempt_samples SET inserted_at=$1 WHERE attempt_id=$2", start.Add(7*time.Second), sample.AttemptID)
	require.NoError(t, err)
	_, err = db.Exec("UPDATE account_attempt_samples SET inserted_at=$1 WHERE attempt_id=$2", start.Add(8*time.Second), nullable.AttemptID)
	require.NoError(t, err)
	// Put v1 immediately before current: it must not mask the eligible predecessor.
	_, err = db.Exec("UPDATE account_attempt_samples SET started_at=$1 WHERE attempt_id=$2", start.Add(19*time.Second), legacyID)
	require.NoError(t, err)

	rows, cursor, err = repo.LoadInsertedAfter(ctx, start.Add(-time.Second), 1)
	require.NoError(t, err)
	require.Len(t, rows, 1, "v1 must not consume the page limit and stall ingestion")
	require.Equal(t, sample.AttemptID, rows[0].AttemptID)
	require.True(t, cursor.Equal(start.Add(7*time.Second)))
	require.Equal(t, sample.FirstEventMs, rows[0].FirstEventMs)
	require.Equal(t, sample.TTFOMs, rows[0].TTFOMs)
	require.Equal(t, sample.FirstAnswerMs, rows[0].FirstAnswerMs)
	require.Equal(t, sample.Class, rows[0].Class)
	require.Equal(t, sample.HTTPStatus, rows[0].HTTPStatus)
	require.Equal(t, sample.StatusOrigin, rows[0].StatusOrigin)
	require.Equal(t, sample.OutputBytes, rows[0].OutputBytes)
	finished, err = repo.LoadFinishedSince(ctx, start)
	require.NoError(t, err)
	require.Len(t, finished, 2)
	require.Equal(t, nullable.AttemptID, finished[1].AttemptID)
	// Split equal completion timestamps by UUID, including the lower endpoint.
	_, err = db.Exec("UPDATE account_attempt_samples SET finished_at=$1 WHERE attempt_id=$2", nullable.FinishedAt, sample.AttemptID)
	require.NoError(t, err)
	ids := []string{sample.AttemptID, nullable.AttemptID}
	sort.Strings(ids)
	var pageCursor service.SchedAttemptCursor
	for _, id := range ids {
		page, e := repo.LoadFinishedPage(ctx, nullable.FinishedAt, start.Add(time.Hour), pageCursor, 1)
		require.NoError(t, e)
		require.Len(t, page, 1)
		require.Equal(t, id, page[0].AttemptID)
		pageCursor = service.SchedAttemptCursor{At: page[0].FinishedAt, AttemptID: page[0].AttemptID}
	}
	emptyPage, err = repo.LoadFinishedPage(ctx, nullable.FinishedAt, start.Add(time.Hour), pageCursor, 1)
	require.NoError(t, err)
	require.Empty(t, emptyPage)
	require.Nil(t, finished[1].FirstEventMs)
	require.Nil(t, finished[1].FirstAnswerMs)
	require.Nil(t, finished[1].HTTPStatus)
	require.Nil(t, finished[1].OutputBytes)
	require.Equal(t, schedquality.ClassNone, finished[1].Class)
	require.Equal(t, schedquality.OriginNone, finished[1].StatusOrigin)
	previous, err = repo.LoadPreviousAttempts(ctx, []schedquality.AttemptSample{current})
	require.NoError(t, err)
	require.Equal(t, nullable.AttemptID, previous[current.AttemptID].AttemptID)
}

// Only membership and policy are synthetic. The follower engine loads all
// attempts, including their dispatch-order predecessors, from PostgreSQL.
type attemptEvidenceSettings struct {
	service.SchedQualityStore
	members []int64
}

func (s attemptEvidenceSettings) LoadMembers(context.Context) (map[int64][]int64, error) {
	return map[int64][]int64{7: s.members}, nil
}
func (attemptEvidenceSettings) LoadPolicies(context.Context) (map[int64]service.SchedPolicyRecord, error) {
	return map[int64]service.SchedPolicyRecord{7: {GroupID: 7, Policy: schedquality.DefaultPolicy()}}, nil
}
func (attemptEvidenceSettings) LoadTTFTRule(context.Context) (service.SchedTTFTRuleRecord, error) {
	return service.SchedTTFTRuleRecord{Params: schedquality.DefaultRuleBParams()}, nil
}
func (attemptEvidenceSettings) RunCycle(context.Context, func(service.SchedQualityTx) error) (bool, error) {
	return false, nil
}
func (attemptEvidenceSettings) LoadRows(context.Context) ([]schedquality.LineState, []service.SchedOverride, map[int64]time.Time, error) {
	return nil, nil, nil, nil
}

func assertSchedAttemptOwnerTimeoutEvidence(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	repo := NewSchedAttemptRepository(db)
	model := uuid.NewString()
	t.Cleanup(func() { _, _ = db.Exec("DELETE FROM account_attempt_samples WHERE model=$1", model) })
	now := time.Now().UTC().Truncate(time.Microsecond).Add(48 * time.Hour)
	start := now.Add(-20 * time.Second)
	cases := []struct {
		name                    string
		version                 int
		warm, full              bool
		slow, known, cacheTurns int
	}{
		{"old-switch", 2, false, false, 0, 0, 0},
		{"owner-full", 3, true, true, 1, 1, 0},
		{"owner-short", 3, true, false, 0, 0, 0},
		{"cold-full", 3, false, true, 0, 0, 0},
		{"old-continuation", 2, false, false, 0, 1, 1},
	}
	var batch, probes []schedquality.AttemptSample
	ids := make(map[string]string)
	for i, tc := range cases {
		account := int64(11 + i)
		sample := schedquality.AttemptSample{AttemptID: uuid.NewString(), AccountID: account, GroupID: 7,
			Model: model, SessionKey: tc.name, RequestID: uuid.NewString(), Stream: true,
			StartedAt: start, FinishedAt: now, Outcome: schedquality.OutcomeTimeout, Warm: tc.warm, FullTierFirstOutputTimeout: tc.full}
		censor := 20000
		if tc.name == "owner-short" {
			censor = 10000
		}
		sample.CensoredMs = &censor
		pred := schedquality.AttemptSample{AttemptID: uuid.NewString(), AccountID: 91, GroupID: 7, Model: model,
			SessionKey: sample.SessionKey, RequestID: uuid.NewString(), StartedAt: start.Add(-time.Minute), FinishedAt: start.Add(-time.Second), Outcome: schedquality.OutcomeOK}
		if tc.cacheTurns > 0 {
			pred.AccountID = account
			valid, input, read, create := 100, 20, 80, 0
			sample.Outcome, sample.TTFOMs, sample.CensoredMs = schedquality.OutcomeOK, &valid, nil
			sample.InputTokens, sample.CacheReadTokens, sample.CacheCreationTokens = &input, &read, &create
		}
		batch = append(batch, pred)
		if tc.version == 2 {
			// Reproduce the v2 writer, which had no full-tier column.
			_, err := db.ExecContext(ctx, `INSERT INTO account_attempt_samples
    (attempt_id, started_at, finished_at, account_id, group_id, model, session_key, request_id,
     request_class, stream, warm, capture_version, outcome, ttfo_ms, censored_ms, input_tokens, cache_read_tokens, cache_creation_tokens)
    VALUES ($1,$2,$3,$4,7,$5,$6,$7,1,true,false,2,$8,$9,$10,$11,$12,$13)`,
				sample.AttemptID, start, now, account, model, sample.SessionKey, sample.RequestID, sample.Outcome, sample.TTFOMs, sample.CensoredMs,
				sample.InputTokens, sample.CacheReadTokens, sample.CacheCreationTokens)
			require.NoError(t, err)
		} else {
			batch = append(batch, sample)
		}
		ids[tc.name] = sample.AttemptID
		probes = append(probes, schedquality.AttemptSample{AttemptID: uuid.NewString(), GroupID: 7, Model: model,
			SessionKey: tc.name, StartedAt: now.Add(time.Second)})
	}
	require.NoError(t, repo.InsertBatch(ctx, batch))
	_, err := db.ExecContext(ctx, "UPDATE account_attempt_samples SET inserted_at=finished_at WHERE model=$1", model)
	require.NoError(t, err)
	boot, err := repo.LoadFinishedPage(ctx, now, now, service.SchedAttemptCursor{}, len(cases))
	require.NoError(t, err)
	delta, cursor, err := repo.LoadInsertedAfter(ctx, now.Add(-time.Microsecond), len(cases))
	require.NoError(t, err)
	require.True(t, cursor.Equal(now))
	before, err := repo.LoadPreviousAttempts(ctx, probes)
	require.NoError(t, err)
	var prev []schedquality.AttemptSample
	for _, probe := range probes {
		require.NotNil(t, before[probe.AttemptID])
		prev = append(prev, *before[probe.AttemptID])
	}
	for path, rows := range map[string][]schedquality.AttemptSample{"boot": boot, "incremental": delta, "predecessor": prev} {
		t.Run(path, func(t *testing.T) {
			byID := make(map[string]schedquality.AttemptSample)
			for _, r := range rows {
				byID[r.AttemptID] = r
			}
			require.Len(t, byID, len(cases))
			for _, tc := range cases {
				r := byID[ids[tc.name]]
				require.Equal(t, tc.version, r.CaptureVersion, tc.name)
				require.Equal(t, tc.warm, r.Warm, tc.name)
				require.Equal(t, tc.full, r.FullTierFirstOutputTimeout, tc.name)
			}
		})
	}

	// Rule B exposes the peer bound U at 20 known attempts. Read 19 fast
	// samples from another member in a different source
	// group so they do not affect group 7's prior. Account 91 is the observer.
	fast := 100
	var background []schedquality.AttemptSample
	for i := 0; i < 19; i++ {
		background = append(background, schedquality.AttemptSample{
			AttemptID: uuid.NewString(), AccountID: 90, GroupID: 8, Model: model,
			StartedAt: start, FinishedAt: now, Outcome: schedquality.OutcomeOK, Stream: true, TTFOMs: &fast,
		})
	}
	require.NoError(t, repo.InsertBatch(ctx, background))
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := int64(11 + i)
			settings := attemptEvidenceSettings{members: []int64{account, 90, 91}}
			engine := service.NewSchedQualityEngineService(settings, repo, nil, func() time.Time { return now }, nil)
			engine.Start(ctx)
			defer engine.Stop()
			snap := engine.Snapshot()
			observer := snap.GroupRows[91]
			require.Len(t, observer, 1)
			require.Equal(t, tc.known == 1, observer[0].TTFT.U != nil, "candidate must contribute the 20th known peer sample exactly when eligible")
			groupRows := snap.GroupRows[account]
			require.Len(t, groupRows, 1)
			row := groupRows[0]
			require.Equal(t, float64(tc.known), row.TTFT.N)
			require.Equal(t, float64(tc.slow), row.TTFT.K)
			require.Zero(t, row.TTFT.X)
			require.Equal(t, tc.cacheTurns, row.Cache.Turns)
			require.Equal(t, int64(tc.cacheTurns*80), row.Cache.ReadTokens)
			if tc.known == 0 {
				require.Empty(t, row.TTFTSourceGroups)
			} else {
				require.Equal(t, []int64{7}, row.TTFTSourceGroups)
			}
			// All accounts see the same group prior. The account prior excludes its own evidence.
			require.Equal(t, 0.5, row.TTFT.PeerNu)
			prior := 0.5
			if tc.known > 0 {
				if tc.slow > 0 {
					prior = 0.001
				} else {
					prior = 0.999
				}
			}
			require.Equal(t, prior, row.TTFT.PriorMu)
		})
	}
}
