//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func TestSchedTestRepository(t *testing.T) {
	ctx := context.Background()
	repo := NewSchedTestRepository(integrationDB)
	now := time.Now().UTC()
	account := int64(81370001)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM account_quality_tests WHERE account_id=$1`, account)
	})
	newRecord := func(at time.Time, running bool) service.SchedTestRecord {
		return service.SchedTestRecord{AccountID: account, Model: "test-model", Running: running, TestResult: schedquality.TestResult{ID: uuid.NewString(), StartedAt: at, Done: !running}}
	}
	old := newRecord(now.Add(-6*time.Hour-time.Minute), false)
	fresh := newRecord(now.Add(-6*time.Hour+time.Minute), false)
	running := newRecord(now, true)
	for _, v := range []service.SchedTestRecord{old, fresh, running} {
		if err := repo.Insert(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Insert(ctx, newRecord(now, true)); !errors.Is(err, service.ErrSchedTestRunning) {
		t.Fatalf("expected running conflict: %v", err)
	}
	rows, err := repo.Since(ctx, now.Add(-6*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != fresh.ID || rows[1].ID != running.ID || !rows[1].Running {
		t.Fatalf("unexpected fresh rows: %+v", rows)
	}
}
