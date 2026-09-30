package responseturn

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestReviewCancellationCannotWaitForCallback(t *testing.T) {
	m, _ := testManager(t, Config{})
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	o := testOptions()
	o.OnCancel = func(Reason) { close(entered); <-release }
	turn := mustCreate(t, m, o)
	turn.Cancel(ReasonUserStop)
	<-entered
	select {
	case <-turn.Context().Done():
		if CancellationReason(context.Cause(turn.Context())) != ReasonUserStop {
			t.Fatal(context.Cause(turn.Context()))
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("explicit cancellation remains live while the notification callback blocks")
	}
}

func TestReviewCompletedEventWinsBlockedDeliveryCancellation(t *testing.T) {
	m, _ := testManager(t, Config{})
	turn := mustCreate(t, m, testOptions())
	a := mustAttach(t, turn, nil, false)
	mustPublish(t, turn, event(0, "response.created"))
	terminal := event(1, "response.completed")
	terminal.Terminal = StateCompleted
	terminal.Result = []byte(`{"id":"resp-original","status":"completed","output":[]}`)
	done := make(chan error, 1)
	go func() { _, err := turn.Publish(context.Background(), terminal); done <- err }()
	deadline := time.Now().Add(time.Second)
	for len(turn.Snapshot().Result) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	snapshot := turn.Snapshot()
	cancelled := turn.Cancel(ReasonUserStop)
	_ = next(t, a)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher did not stop")
	}
	t.Logf("before_cancel_state=%s result=%s cancellation_accepted=%v final_state=%s", snapshot.State, snapshot.Result, cancelled, turn.Snapshot().State)
	if snapshot.State != StateCompleted || cancelled || turn.Snapshot().State != StateCompleted {
		t.Fatal("verified completed result became cancelled while downstream delivery was pending")
	}
	if got := next(t, a); !bytes.Equal(got.Data, terminal.Data) {
		t.Fatal("terminal event lost")
	}
}

func TestReviewTerminalRetentionOutlivesCreationKeyTTL(t *testing.T) {
	m, clock := testManager(t, Config{IdempotencyTTL: time.Second, TerminalTTL: time.Minute})
	turn := mustCreate(t, m, testOptions())
	clock.Add(2 * time.Second)
	result := []byte(`{"status":"completed"}`)
	if err := turn.Finish(StateCompleted, "", result); err != nil {
		t.Fatal(err)
	}
	m.Sweep()
	got, err := m.Lookup(turn.scope, "resp-original")
	if err != nil || got != turn || !bytes.Equal(turn.Snapshot().Result, result) {
		t.Fatalf("fresh terminal body expired with creation key TTL: lookup=%v result=%s", err, turn.Snapshot().Result)
	}
}

func TestReviewQuotaExpiryNeverMakesAccountingNegative(t *testing.T) {
	m, clock := testManager(t, Config{MaxEventBytes: 512, IdempotencyTTL: time.Second, TerminalTTL: time.Minute})
	turn := mustCreate(t, m, testOptions())
	first := mustAttach(t, turn, nil, false)
	mustPublish(t, turn, event(0, "response.created"))
	_ = next(t, first)
	first.Close()
	mustPublish(t, turn, event(1, "response.output_text.delta"))
	_ = mustAttach(t, turn, nil, true)
	overflow := event(2, "response.output_text.delta")
	overflow.Data = bytes.Repeat([]byte("x"), 1024)
	mustPublish(t, turn, overflow)
	clock.Add(2 * time.Second)
	if err := turn.Finish(StateCompleted, "", nil); err != nil {
		t.Fatal(err)
	}
	m.Sweep()
	usage := m.ResourceUsage()
	if usage.ProcessBytes < 0 || usage.TenantBytes[turn.scope.UserID] < 0 {
		t.Fatalf("quota accounting underflow: %+v", usage)
	}
	clock.Add(time.Minute)
	m.Sweep()
	if usage := m.ResourceUsage(); usage.ProcessBytes != 0 || len(usage.TenantBytes) != 0 {
		t.Fatalf("retained quota leak: %+v", usage)
	}
}

func TestReviewAmbiguousDuplicateKeysRejected(t *testing.T) {
	for _, body := range []string{
		`{"input":"first","input":"second"}`,
		`{"metadata":{"nested":1,"nested":2}}`,
		`{"input":[{"text":"first","text":"second"}]}`,
	} {
		m, _ := testManager(t, Config{})
		o := testOptions()
		o.Body = []byte(body)
		_, created, err := m.Create(o)
		var native *Error
		if created || !errors.As(err, &native) || native.Status != 400 {
			t.Errorf("ambiguous object accepted: body=%s created=%v error=%v", body, created, err)
		}
	}
}

func TestReviewEveryVerifiedTerminalReachesWriterBeforeEOF(t *testing.T) {
	for _, state := range []State{StateCompleted, StateFailed, StatePartial, StateCancelled} {
		t.Run(string(state), func(t *testing.T) {
			m, _ := testManager(t, Config{})
			turn := mustCreate(t, m, testOptions())
			a := mustAttach(t, turn, nil, false)
			mustPublish(t, turn, event(0, "response.created"))
			terminal := event(1, "response."+string(state))
			terminal.Terminal = state
			done := make(chan error, 1)
			go func() { _, err := turn.Publish(context.Background(), terminal); done <- err }()
			deadline := time.Now().Add(time.Second)
			for turn.Snapshot().State != state && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if got := turn.Snapshot().State; got != state {
				t.Fatalf("upstream terminal not committed: %s", got)
			}
			_ = next(t, a)
			if got := next(t, a); !bytes.Equal(got.Data, terminal.Data) {
				t.Fatal("terminal delivery lost behind earlier event")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if _, err := a.Next(context.Background()); err != io.EOF {
				t.Fatalf("expected terminal EOF: %v", err)
			}
		})
	}
}

func TestReviewExpiredHandleCannotRemoveNewIdempotentTurn(t *testing.T) {
	m, clock := testManager(t, Config{IdempotencyTTL: time.Second, TerminalTTL: time.Second})
	o := testOptions()
	original := mustCreate(t, m, o)
	if err := original.Finish(StateCompleted, "", nil); err != nil {
		t.Fatal(err)
	}
	clock.Add(2 * time.Second)
	replacement := mustCreate(t, m, o)
	_ = original.Snapshot()
	got, created, err := m.Create(o)
	if err != nil || created || got != replacement {
		t.Fatalf("expired handle removed replacement identity: created=%v err=%v", created, err)
	}
}
