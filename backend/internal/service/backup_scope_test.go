//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

type scopeTestDumper struct {
	mockDumper
	fullCalls   int
	schemaCalls []string
	started     chan struct{}
	resume      chan struct{}
}

func (d *scopeTestDumper) Dump(ctx context.Context) (io.ReadCloser, error) {
	d.fullCalls++
	return d.mockDumper.Dump(ctx)
}

func (d *scopeTestDumper) DumpSchema(ctx context.Context, schema string) (io.ReadCloser, error) {
	d.schemaCalls = append(d.schemaCalls, schema)
	if d.started != nil {
		close(d.started)
		<-d.resume
	}
	return d.mockDumper.Dump(ctx)
}

func TestBackupScopeDefaultsAndLegacyRecords(t *testing.T) {
	ctx := context.Background()
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	schedule, err := svc.GetSchedule(ctx)
	require.NoError(t, err)
	require.Equal(t, BackupScopeDatabase, schedule.BackupScope)
	require.NoError(t, repo.Set(ctx, settingKeyBackupSchedule, `{"enabled":false,"retain_days":14}`))
	schedule, err = svc.GetSchedule(ctx)
	require.NoError(t, err)
	require.Equal(t, BackupScopeDatabase, schedule.BackupScope)
	require.NoError(t, repo.Set(ctx, settingKeyBackupRecords, `[{"id":"old","status":"completed"}]`))
	record, err := svc.GetBackupRecord(ctx, "old")
	require.NoError(t, err)
	require.Equal(t, BackupScopeDatabase, record.BackupScope)
}

func TestBackupScopeValidationAndOmittedFieldPreservation(t *testing.T) {
	ctx := context.Background()
	repo := newMockSettingRepo()
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	_, err := svc.UpdateSchedule(ctx, BackupScheduleConfig{BackupScope: BackupScopePublic})
	require.NoError(t, err)
	saved, err := svc.UpdateSchedule(ctx, BackupScheduleConfig{RetainDays: 3})
	require.NoError(t, err)
	require.Equal(t, BackupScopePublic, saved.BackupScope)
	_, err = svc.UpdateSchedule(ctx, BackupScheduleConfig{BackupScope: "public,*"})
	require.ErrorContains(t, err, "backup scope must be database or public")
	saved, err = svc.GetSchedule(ctx)
	require.NoError(t, err)
	require.Equal(t, BackupScopePublic, saved.BackupScope)
	require.NoError(t, repo.Set(ctx, settingKeyBackupSchedule, `{"backup_scope":"unknown"}`))
	_, err = svc.GetSchedule(ctx)
	require.ErrorContains(t, err, "invalid backup scope")
}

func TestBackupScopeSharedByManualAndScheduledBackups(t *testing.T) {
	ctx := context.Background()
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	dumper := &scopeTestDumper{mockDumper: mockDumper{dumpData: []byte("-- schema data")}}
	svc := newTestBackupService(repo, dumper, newMockObjectStore())
	_, err := svc.UpdateSchedule(ctx, BackupScheduleConfig{BackupScope: BackupScopePublic})
	require.NoError(t, err)
	manual, err := svc.CreateBackup(ctx, "manual", 14)
	require.NoError(t, err)
	require.Equal(t, BackupScopePublic, manual.BackupScope)
	async, err := svc.StartBackup(ctx, "manual", 14)
	require.NoError(t, err)
	require.Equal(t, BackupScopePublic, async.BackupScope)
	svc.wg.Wait()
	svc.runScheduledBackup()
	records, err := svc.ListBackups(ctx)
	require.NoError(t, err)
	require.Len(t, records, 3)
	for _, record := range records {
		require.Equal(t, "completed", record.Status)
		require.Equal(t, BackupScopePublic, record.BackupScope)
	}
	require.Zero(t, dumper.fullCalls)
	require.Equal(t, []string{"public", "public", "public"}, dumper.schemaCalls)
}

func TestBackupScopeAsyncOperationKeepsSelectedScope(t *testing.T) {
	ctx := context.Background()
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	dumper := &scopeTestDumper{started: make(chan struct{}), resume: make(chan struct{})}
	svc := newTestBackupService(repo, dumper, newMockObjectStore())
	_, err := svc.UpdateSchedule(ctx, BackupScheduleConfig{BackupScope: BackupScopePublic})
	require.NoError(t, err)
	record, err := svc.StartBackup(ctx, "manual", 14)
	require.NoError(t, err)
	<-dumper.started
	_, err = svc.UpdateSchedule(ctx, BackupScheduleConfig{BackupScope: BackupScopeDatabase})
	close(dumper.resume)
	svc.wg.Wait()
	require.NoError(t, err)
	record, err = svc.GetBackupRecord(ctx, record.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", record.Status)
	require.Equal(t, BackupScopePublic, record.BackupScope)
	require.Zero(t, dumper.fullCalls)
}

func TestBackupScopeDoesNotFallbackOnFailure(t *testing.T) {
	for _, scope := range []string{BackupScopeDatabase, BackupScopePublic} {
		t.Run(scope, func(t *testing.T) {
			ctx := context.Background()
			repo := newMockSettingRepo()
			seedS3Config(t, repo)
			dumper := &scopeTestDumper{mockDumper: mockDumper{dumpErr: fmt.Errorf("permission denied")}}
			svc := newTestBackupService(repo, dumper, newMockObjectStore())
			_, err := svc.UpdateSchedule(ctx, BackupScheduleConfig{BackupScope: scope})
			require.NoError(t, err)
			record, err := svc.CreateBackup(ctx, "manual", 14)
			require.ErrorContains(t, err, "permission denied")
			require.Equal(t, "failed", record.Status)
			require.Equal(t, scope, record.BackupScope)
			if scope == BackupScopePublic {
				require.Zero(t, dumper.fullCalls)
				require.Equal(t, []string{"public"}, dumper.schemaCalls)
			} else {
				require.Equal(t, 1, dumper.fullCalls)
				require.Empty(t, dumper.schemaCalls)
			}
		})
	}
}

func TestBackupScopeRejectsUnsupportedDumper(t *testing.T) {
	ctx := context.Background()
	repo := newMockSettingRepo()
	seedS3Config(t, repo)
	svc := newTestBackupService(repo, &mockDumper{}, newMockObjectStore())
	_, err := svc.UpdateSchedule(ctx, BackupScheduleConfig{BackupScope: BackupScopePublic})
	require.NoError(t, err)
	record, err := svc.CreateBackup(ctx, "manual", 14)
	require.ErrorContains(t, err, "does not support the public backup scope")
	require.Equal(t, "failed", record.Status)
}
