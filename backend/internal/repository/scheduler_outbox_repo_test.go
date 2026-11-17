package repository

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestSchedulerOutboxRepositoryOldestPendingCreatedAt(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	repo := &schedulerOutboxRepository{db: db}
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	const expectedSQL = `
		SELECT created_at
		FROM scheduler_outbox
		ORDER BY created_at ASC, id ASC
		LIMIT 1
	`
	mock.ExpectQuery(regexp.QuoteMeta(expectedSQL)).
		WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(createdAt))

	got, ok, err := repo.OldestPendingCreatedAt(context.Background())

	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, createdAt, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSchedulerOutboxRepositoryOldestPendingCreatedAtReturnsNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	repo := &schedulerOutboxRepository{db: db}
	const expectedSQL = `
		SELECT created_at
		FROM scheduler_outbox
		ORDER BY created_at ASC, id ASC
		LIMIT 1
	`
	mock.ExpectQuery(regexp.QuoteMeta(expectedSQL)).
		WillReturnRows(sqlmock.NewRows([]string{"created_at"}))

	got, ok, err := repo.OldestPendingCreatedAt(context.Background())

	require.NoError(t, err)
	require.False(t, ok)
	require.True(t, got.IsZero())
	require.NoError(t, mock.ExpectationsWereMet())
}

func expectOutboxTransaction(mock sqlmock.Sqlmock, acquired bool) {
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL idle_in_transaction_session_timeout = '60s'; SET LOCAL statement_timeout = '60s'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_xact_lock(hashtext('scheduler_outbox_cleanup'))")).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(acquired))
}
func TestSchedulerOutboxRepositoryAcknowledgesExactIDsOutsideLease(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	expectOutboxTransaction(mock, true)
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM scheduler_outbox WHERE id = ANY($1::bigint[])")).WithArgs("{2,9}").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"alive"}).AddRow(1))
	mock.ExpectRollback()
	repo := &schedulerOutboxRepository{db: db}
	lease, acquired, err := repo.TryAcquireConsumerLock(context.Background())
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, repo.DeleteProcessed(context.Background(), []int64{2, 9}))
	require.NoError(t, lease.KeepAlive(context.Background()))
	lease.Release() // only the advisory lock lives in this transaction
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestSchedulerOutboxRepositoryTransactionRollsBack(t *testing.T) {
	for _, acquired := range []bool{true, false} {
		t.Run(fmt.Sprint(acquired), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			expectOutboxTransaction(mock, acquired)
			mock.ExpectRollback()
			lease, got, err := (&schedulerOutboxRepository{db: db}).TryAcquireConsumerLock(context.Background())
			require.NoError(t, err)
			require.Equal(t, acquired, got)
			if acquired {
				lease.Release()
			} else {
				require.Nil(t, lease)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// buildSchedulerGroupPayload 在 groupIDs 为空时必须返回 untyped nil（any），
// 否则 enqueueSchedulerOutbox 的 "payload != nil" 接口判空会被 typed-nil 欺骗，
// 把 payload marshal 成 "null" 写入 dedup_key 哈希，破坏与其他 nil-payload
// 调用的去重一致性。本测试用 ungrouped 账号场景验证两条路径的 dedup_key 一致。
func TestEnqueueSchedulerOutbox_UngroupedAccountDedupesWithLiteralNilPayload(t *testing.T) {
	accountID := int64(42)

	// Path A: 显式 nil payload（如 SetError、SetStatus 等调用模式）
	keyLiteralNil := schedulerOutboxDedupKey("account_changed", &accountID, nil, nil)

	// Path B: buildSchedulerGroupPayload(account.GroupIDs) 当账号没有任何分组
	emptyGroupsPayload := buildSchedulerGroupPayload(nil)
	require.Nil(t, emptyGroupsPayload,
		"buildSchedulerGroupPayload(empty) must return untyped-nil any to avoid typed-nil marshal")

	// 模拟 enqueueSchedulerOutbox 内部的判空逻辑
	var payloadJSON []byte
	if emptyGroupsPayload != nil {
		t.Fatalf("typed-nil regression: buildSchedulerGroupPayload(empty) interface should be nil")
	}
	keyEmptyGroups := schedulerOutboxDedupKey("account_changed", &accountID, nil, payloadJSON)

	require.Equal(t, keyLiteralNil, keyEmptyGroups,
		"ungrouped-account account_changed must share dedup_key with other nil-payload variants")
}
