//go:build unit

package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

type collabWatchdogPanicReader struct{}

func (collabWatchdogPanicReader) Read([]byte) (int, error) { panic("synthetic drain failure") }
func (collabWatchdogPanicReader) Close() error             { return nil }

func TestCollabWatchdogDrainRecovers(t *testing.T) {
	before := resilience.CollabSafetySnapshot().ConsumerPanics
	logs := make(collabWatchdogLog, 1)
	previous := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(previous) })
	b := &codexTicketWatchdogBody{ReadCloser: collabWatchdogPanicReader{}, ctx: context.Background()}
	require.NoError(t, b.Close())
	require.Equal(t, before+1, resilience.CollabSafetySnapshot().ConsumerPanics)
	select {
	case entry := <-logs:
		require.Equal(t, slog.LevelError, entry.Level)
	default:
		t.Fatal("watchdog panic must be logged")
	}
}

func TestCollabWorkerRecoveryRejectsIneffectiveDefer(t *testing.T) {
	for _, source := range []string{
		`package p;func worker(){panic("before defer");defer RecoverCollabWorker()}`,
		`package p;func worker(){defer func(){func(){recover()}();counter.Add(1)}();panic("indirect")}`,
		`package p;func worker(){if false {defer RecoverCollabWorker()};panic("unreachable")}`,
		`package p;func worker(){return;defer RecoverCollabWorker()}`,
		`package p;func worker(){defer func(){if r:=recover();false{NoteCollabWorkerPanic(r)}}()}`,
		`package p;func worker(){defer func(){if r:=recover();r!=nil{if false{NoteCollabWorkerPanic(r)}}}()}`,
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, 0)
		require.NoError(t, err)
		require.False(t, collabWorkerRecovered(file.Decls[0].(*ast.FuncDecl).Body), source)
	}
}

type collabWatchdogLog chan slog.Record

func (h collabWatchdogLog) Enabled(context.Context, slog.Level) bool { return true }
func (h collabWatchdogLog) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "resilience.worker_panic" {
		h <- r.Clone()
	}
	return nil
}
func (h collabWatchdogLog) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h collabWatchdogLog) WithGroup(string) slog.Handler      { return h }
