//go:build unit

package repository

import (
	"fmt"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestSchedPerfLedgerPagingAndAdjacency(t *testing.T) {
	db := newTrialPostgres(t)
	ctx := t.Context()
	for _, name := range []string{"294_sched_quality.sql", "305_sv2_attempt_tokens.sql", "313_sched_attempt_v2.sql", "319_sched_attempt_full_tier_timeout.sql", "054_ops_system_logs.sql"} {
		b, err := migrations.FS.ReadFile(name)
		require.NoError(t, err)
		_, err = db.Exec(string(b))
		require.NoError(t, err)
	}
	r := &schedAttemptRepository{db: db}
	_, err := db.Exec(`CREATE TABLE settings(key text,value text);
 CREATE TABLE accounts(id bigint, extra jsonb);
 CREATE TABLE groups(id bigint, platform text, deleted_at timestamptz);
 INSERT INTO groups VALUES(1,'openai',NULL),(2,'other',NULL);
 INSERT INTO accounts VALUES(1,'{"group_quality_reset_at":"2026-01-01T00:00:00Z"}');`)
	require.NoError(t, err)
	policies, resets, err := r.LoadPerfConfig(ctx)
	require.NoError(t, err)
	require.Len(t, policies, 1)
	require.Contains(t, policies, int64(1))
	require.Equal(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), resets[1])

	at := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	cut := at.Add(-12 * time.Hour)
	id := func(n int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012d", n) }
	add := func(n, account int, req, session string, start, finish, ins time.Time, out q.Outcome) {
		_, err := db.Exec(`INSERT INTO account_attempt_samples(attempt_id,started_at,finished_at,inserted_at,account_id,group_id,model,session_key,request_id,
  request_class,stream,warm,outcome,capture_version,cache_read_tokens,input_tokens,cache_creation_tokens)
  VALUES($1,$2,$3,$4,$5,1,'text',$6,$7,1,true,false,$8,3,30,60,10)`, id(n), start, finish, ins, account, session, req, out)
		require.NoError(t, err)
	}
	add(1, 1, "boundary", "left", cut.Add(-time.Minute), cut.Add(-time.Second), cut.Add(-time.Second), q.OutcomeTimeout)
	add(2, 2, "boundary", "left", cut, cut.Add(time.Second), cut.Add(time.Second), q.OutcomeOK)
	start := at.Add(-time.Hour)
	add(3, 1, "retry", "s", start, start.Add(time.Second), start.Add(time.Second), q.OutcomeTimeout)
	add(4, 2, "retry", "s", start.Add(2*time.Second), start.Add(3*time.Second), start.Add(3*time.Second), q.OutcomeOK)
	add(5, 2, "continued", "s", start.Add(6*time.Second), start.Add(8*time.Second), start.Add(8*time.Second), q.OutcomeOK)
	add(6, 2, "again", "z", start, start.Add(7*time.Second), start.Add(9*time.Second), q.OutcomeOK)
	add(7, 2, "again", "z", start, start.Add(8*time.Second), start.Add(9*time.Second), q.OutcomeOK)
	load := func(inserted bool, from, through time.Time) map[string]service.SchedPerfFact {
		out := map[string]service.SchedPerfFact{}
		cursor := service.SchedAttemptCursor{}
		for {
			facts, next, done, err := r.LoadPerfPage(ctx, from, through, cursor, inserted, 2)
			require.NoError(t, err)
			for _, f := range facts {
				out[f.ID] = f
			}
			if done {
				return out
			}
			require.NotEqual(t, cursor, next)
			cursor = next
		}
	}
	// The earlier dispatch is already in the database at T but its application
	// completion clock is ahead. At T only the later dispatch is visible.
	add(10, 1, "skew", "skew-session", start, at.Add(time.Second), at.Add(-11*time.Second), q.OutcomeTimeout)
	add(11, 2, "skew", "skew-session", start.Add(time.Second), start.Add(2*time.Second), start.Add(3*time.Second), q.OutcomeOK)
	before := load(false, cut, at)
	require.NotContains(t, before, id(10))
	require.True(t, before[id(11)].First)
	require.Len(t, before, 7)
	require.False(t, before[id(2)].First)
	require.True(t, before[id(2)].Delivered)
	require.True(t, before[id(3)].First)
	require.False(t, before[id(3)].Delivered)
	require.False(t, before[id(4)].First)
	require.True(t, before[id(4)].Delivered)
	require.True(t, before[id(6)].First)
	require.False(t, before[id(6)].Delivered)
	require.False(t, before[id(7)].First)
	require.True(t, before[id(7)].Delivered)
	a := q.AttemptSample{AccountID: 2, GroupID: 1, SessionKey: "s", Model: "text", Outcome: q.OutcomeOK, AttemptID: id(5), StartedAt: start.Add(6 * time.Second), FinishedAt: start.Add(8 * time.Second), InputTokens: new(60), CacheReadTokens: new(30), CacheCreationTokens: new(10)}
	prev := a
	prev.StartedAt = start.Add(2 * time.Second)
	prev.FinishedAt = start.Add(3 * time.Second)
	a.Continuation = q.IsContinuation(a, prev)
	evidence := q.AssessCache([]q.AttemptSample{a}, .5, time.Time{}, a.FinishedAt)
	require.Equal(t, 1, evidence.Turns)
	require.Equal(t, evidence.ReadTokens, before[id(5)].Read)
	require.Equal(t, evidence.InputTokens, before[id(5)].Input)
	// An intervening failed turn and an earlier first dispatch become visible late.
	late := at.Add(time.Minute)
	add(8, 1, "delayed", "s", start.Add(4*time.Second), start.Add(5*time.Second), late, q.OutcomeOther)
	add(9, 1, "again", "other", start.Add(-time.Second), start, late, q.OutcomeTimeout)
	end := at.Add(5 * time.Minute)
	inc := load(true, at.Add(-10*time.Second), end)
	require.Contains(t, inc, id(10))
	require.True(t, inc[id(10)].First)
	require.Contains(t, inc, id(11))
	require.False(t, inc[id(11)].First)
	require.Zero(t, inc[id(5)].Input)
	require.False(t, inc[id(6)].First)
	for key, f := range inc {
		before[key] = f
	}
	delete(before, id(2)) // this successful retry has expired at the next publication
	require.Equal(t, load(false, end.Add(-12*time.Hour), end), before)
	for _, record := range []string{
		`{"gateway_request_id":"retry","group_id":1,"from":1,"to":2,"kind":"first_output_follow","reason":"faster_delivery","committed":true}`,
		`{"gateway_request_id":"unmapped","group_id":1,"from":2,"to":1,"kind":"owner_released","reason":"profit_floor","committed":true}`,
		`{"gateway_request_id":"ignored","group_id":1,"from":2,"to":1,"kind":"migrate","committed":false}`,
	} {
		_, err := db.Exec(`INSERT INTO ops_system_logs(created_at,level,component,message,extra) VALUES ($1,'INFO','sched_quality','sched_quality.session_switch',$2::jsonb)`, at, record)
		require.NoError(t, err)
	}
	page, err := r.LoadPerfSwitches(ctx, at, end, 0, 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, "text", page[0].Model)
	page, err = r.LoadPerfSwitches(ctx, page[0].At, end, page[0].ID, 10)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Empty(t, page[0].Model)
	require.Equal(t, "profit_floor", page[0].Reason)
}

func TestSchedPerfLateSwitchAndSourceModel(t *testing.T) {
	db := newTrialPostgres(t)
	for _, name := range []string{"294_sched_quality.sql", "054_ops_system_logs.sql"} {
		migration, err := migrations.FS.ReadFile(name)
		require.NoError(t, err)
		_, err = db.Exec(string(migration))
		require.NoError(t, err)
	}
	r := &schedAttemptRepository{db: db}
	at := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	eventTime := at.Add(-30 * time.Second)
	addSwitch := func(req, kind, reason, source string) {
		_, err := db.Exec(`INSERT INTO ops_system_logs(created_at,level,component,message,extra)
  VALUES($1,'INFO','sched_quality','sched_quality.session_switch',
  jsonb_build_object('gateway_request_id',$2::text,'group_id',1,'from',1,'to',2,'kind',$3::text,'reason',$4::text,'committed',true) || jsonb_strip_nulls(jsonb_build_object('from_model',NULLIF($5::text,''))))`, eventTime, req, kind, reason, source)
		require.NoError(t, err)
	}
	read := func(tick time.Time) []service.SchedPerfSwitch {
		events, err := r.LoadPerfSwitches(t.Context(), tick.Add(-12*time.Hour), tick, 0, 10)
		require.NoError(t, err)
		return events
	}
	addSwitch("first", "first_output_follow", "", "")
	original := read(at)
	require.Len(t, original, 1)
	require.Empty(t, original[0].Model)
	addSwitch("late", "first_output_follow", "", "")
	_, err := db.Exec(`INSERT INTO account_attempt_samples(attempt_id,started_at,finished_at,inserted_at,request_id,account_id,group_id,model,request_class,stream,warm,outcome)
 VALUES('00000000-0000-0000-0000-000000000001',$1,$2,$3,'first',1,1,'text',1,true,false,2)`, at.Add(-time.Minute), eventTime, at.Add(time.Second))
	require.NoError(t, err)
	next := read(at.Add(5 * time.Minute))
	require.Len(t, next, 2)
	require.Equal(t, original[0].ID, next[0].ID)
	require.Equal(t, "text", next[0].Model)
	require.Equal(t, "late", next[1].Request)
	require.Empty(t, next[1].Model)

	// Only the target actually sends on these three old-format switches.
	insertTarget := func(req, model string) {
		_, err := db.Exec(`INSERT INTO account_attempt_samples(attempt_id,started_at,finished_at,inserted_at,request_id,account_id,group_id,model,request_class,stream,warm,outcome)
  VALUES(md5($1::text || $2::text)::uuid,$3,$4,$4,$1,2,1,$2,1,true,false,1)`, req, model, eventTime, eventTime.Add(time.Second))
		require.NoError(t, err)
	}
	for _, tc := range []struct{ kind, reason string }{{"takeover", "priority_takeover"}, {"migrate", "quality_migration_ttfo"}, {"owner_released", "profit_floor"}} {
		addSwitch(tc.kind, tc.kind, tc.reason, "")
		insertTarget(tc.kind, "target-text")
	}
	addSwitch("new-format", "takeover", "", "source-text")
	insertTarget("new-format", "target-text")
	addSwitch("ambiguous", "migrate", "", "")
	insertTarget("ambiguous", "one")
	insertTarget("ambiguous", "two")
	models := map[string]string{}
	for _, event := range read(at.Add(5 * time.Minute)) {
		models[event.Request] = event.Model
	}
	require.Equal(t, map[string]string{"first": "text", "late": "", "takeover": "target-text", "migrate": "target-text", "owner_released": "target-text", "new-format": "source-text", "ambiguous": ""}, models)
}
