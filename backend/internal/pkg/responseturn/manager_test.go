package responseturn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct{ n atomic.Int64 }

func (f *fakeClock) Now() time.Time      { return time.Unix(0, f.n.Load()) }
func (f *fakeClock) Add(d time.Duration) { f.n.Add(int64(d)) }
func testManager(t *testing.T, cfg Config) (*Manager, *fakeClock) {
	t.Helper()
	f := &fakeClock{}
	f.n.Store(time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC).UnixNano())
	cfg.Now = f.Now
	cfg.DisableBackground = true
	m := NewManager(cfg)
	t.Cleanup(func() { _ = m.Close() })
	return m, f
}
func testOptions() CreateOptions {
	return CreateOptions{Scope: Scope{1, 2, 3, "responses"}, IdempotencyKey: "request-1",
		Body:           []byte(`{"background":true,"stream":true,"model":"fixture","input":"same"}`),
		Capabilities:   Capabilities{Protocol: "responses_http_sse", Verified: true, Retrieve: true, NativeCursor: true},
		ControlContext: context.Background(), PolicyVersion: "policy-7"}
}
func mustCreate(t *testing.T, m *Manager, o CreateOptions) *Turn {
	t.Helper()
	turn, created, err := m.Create(o)
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	if err := turn.BindOwner(42, "resp-original"); err != nil {
		t.Fatal(err)
	}
	return turn
}
func mustAttach(t *testing.T, turn *Turn, cursor *int64, replace bool) *Attachment {
	t.Helper()
	a, err := turn.Attach(turn.scope, cursor, replace)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func seq(n int64) *int64 { return &n }
func event(n int64, typ string) Event {
	return Event{Data: []byte(fmt.Sprintf("event: %s\ndata: {\"sequence_number\":%d,\"type\":%q}\n\n", typ, n, typ)), NativeSequence: seq(n), ResponseID: "resp-original"}
}
func mustPublish(t *testing.T, turn *Turn, e Event) PublishResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := turn.Publish(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func next(t *testing.T, a *Attachment) Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e, err := a.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestRecoveryEligibilityClosed(t *testing.T) {
	cases := []struct {
		name, body string
		caps       Capabilities
		eligible   bool
	}{
		{"verified", `{"background":true,"stream":true}`, testOptions().Capabilities, true},
		{"stream alone", `{"stream":true}`, testOptions().Capabilities, false},
		{"previous alone", `{"stream":true,"previous_response_id":"resp-old"}`, testOptions().Capabilities, false},
		{"store false", `{"background":true,"stream":true,"store":false}`, testOptions().Capabilities, false},
		{"unverified", `{"background":true,"stream":true}`, Capabilities{Protocol: "responses_http_sse", Retrieve: true, NativeCursor: true}, false},
		{"no cursor", `{"background":true,"stream":true}`, Capabilities{Protocol: "responses_http_sse", Verified: true, Retrieve: true}, false},
		{"nonbool intent", `{"background":"true","stream":true}`, testOptions().Capabilities, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := testManager(t, Config{})
			o := testOptions()
			o.Body = []byte(tc.body)
			o.Capabilities = tc.caps
			turn := mustCreate(t, m, o)
			if got := turn.Snapshot().Recoverable; got != tc.eligible {
				t.Fatalf("recoverable=%v", got)
			}
			a := mustAttach(t, turn, nil, false)
			mustPublish(t, turn, event(0, "response.created"))
			_ = next(t, a)
			a.Close()
			if !tc.eligible && turn.Snapshot().State != StateCancelled {
				t.Fatal("ordinary stream survives detach")
			}
		})
	}
}

func TestIdempotencyNormalizedConcurrentScopeAndTombstone(t *testing.T) {
	m, f := testManager(t, Config{})
	o := testOptions()
	first := mustCreate(t, m, o)
	equivalent := o
	equivalent.Body = []byte(`{ "input": "same", "model": "fixture", "stream": true, "background": true }`)
	var created atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			turn, newTurn, err := m.Create(equivalent)
			if err != nil || turn != first {
				t.Errorf("duplicate turn=%p err=%v", turn, err)
			}
			if newTurn {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 0 {
		t.Fatal("duplicate admission")
	}
	different := o
	different.Body = []byte(`{"background":true,"stream":true,"input":"different"}`)
	if _, _, err := m.Create(different); !errors.Is(err, ErrConflict) {
		t.Fatalf("different body: %v", err)
	}
	other := o
	other.Scope.UserID = 99
	if _, fresh, err := m.Create(other); err != nil || !fresh {
		t.Fatalf("other scope: %v %v", fresh, err)
	}
	if err := first.Finish(StateCompleted, "", []byte(`{"id":"resp-original","status":"completed"}`)); err != nil {
		t.Fatal(err)
	}
	f.Add(5 * time.Minute)
	m.Sweep()
	if _, _, err := m.Create(o); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired key recreated: %v", err)
	}
	if _, _, err := m.Create(different); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired mismatch: %v", err)
	}
	if _, err := m.Lookup(o.Scope, "resp-original"); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired lookup: %v", err)
	}
	f.Add(24 * time.Hour)
	m.Sweep()
	if _, fresh, err := m.Create(o); err != nil || !fresh {
		t.Fatalf("key after retention: %v %v", fresh, err)
	}
}

func TestLookupScopeAndOwnerImmutable(t *testing.T) {
	m, _ := testManager(t, Config{})
	o := testOptions()
	turn := mustCreate(t, m, o)
	for _, bad := range []Scope{{9, 2, 3, "responses"}, {1, 9, 3, "responses"}, {1, 2, 9, "responses"}, {1, 2, 3, "chat"}} {
		if _, err := m.Lookup(bad, "resp-original"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("scope leakage: %v", err)
		}
		if _, err := turn.Attach(bad, nil, false); !errors.Is(err, ErrNotFound) {
			t.Fatalf("attachment scope: %v", err)
		}
	}
	if got, err := m.Lookup(o.Scope, "resp-original"); err != nil || got != turn {
		t.Fatal("upstream identity lookup")
	}
	if err := turn.BindOwner(43, ""); !errors.Is(err, ErrOwner) {
		t.Fatalf("account rebound: %v", err)
	}
	if err := turn.BindOwner(42, "resp-new"); !errors.Is(err, ErrOwner) {
		t.Fatalf("response rebound: %v", err)
	}
	if err := turn.BindOwner(0, "resp-original"); err != nil {
		t.Fatal(err)
	}
}

func TestReplayLiveOrderUnknownEventsAndNativeCursor(t *testing.T) {
	m, _ := testManager(t, Config{})
	turn := mustCreate(t, m, testOptions())
	a := mustAttach(t, turn, nil, false)
	for i, kind := range []string{"response.created", "response.in_progress", "response.unrecognized", "response.custom_tool_call_input.delta", "response.output_text.delta"} {
		original := event(int64(i), kind)
		mustPublish(t, turn, original)
		got := next(t, a)
		if !bytes.Equal(original.Data, got.Data) || got.InternalSequence != uint64(i+1) {
			t.Fatalf("rewritten/reordered event %d", i)
		}
	}
	a.Close()
	mustPublish(t, turn, event(5, "response.reasoning_text.delta"))
	b := mustAttach(t, turn, seq(2), false)
	for _, n := range []int64{3, 4, 5} {
		if got := next(t, b); *got.NativeSequence != n {
			t.Fatalf("replay sequence: %+v", got)
		}
	}
	mustPublish(t, turn, event(6, "response.output_text.delta"))
	if *next(t, b).NativeSequence != 6 {
		t.Fatal("live boundary")
	}
	b.Close()
	for _, n := range []int64{-1, 500} {
		if _, err := turn.Attach(turn.scope, seq(n), false); !errors.Is(err, ErrCursor) {
			t.Fatal(err)
		}
	}
}

func TestAtomicReplayLiveBoundaryConcurrent(t *testing.T) {
	m, _ := testManager(t, Config{})
	turn := mustCreate(t, m, testOptions())
	a := mustAttach(t, turn, nil, false)
	mustPublish(t, turn, event(0, "response.created"))
	_ = next(t, a)
	a.Close()
	start := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		<-start
		for i := int64(1); i <= 200; i++ {
			if _, err := turn.Publish(context.Background(), event(i, "response.output_text.delta")); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	close(start)
	b := mustAttach(t, turn, seq(0), false)
	for i := int64(1); i <= 200; i++ {
		got := next(t, b)
		if *got.NativeSequence != i {
			t.Fatalf("gap/duplicate at %d: %d", i, *got.NativeSequence)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestEpochReplacementAndSingleWriterFence(t *testing.T) {
	m, _ := testManager(t, Config{})
	turn := mustCreate(t, m, testOptions())
	a := mustAttach(t, turn, nil, false)
	mustPublish(t, turn, event(0, "response.created"))
	_ = next(t, a)
	entered := make(chan struct{})
	release := make(chan struct{})
	oldDone := make(chan error, 1)
	go func() { oldDone <- a.Write(func() error { close(entered); <-release; return nil }) }()
	<-entered
	attached := make(chan *Attachment, 1)
	go func() {
		b, err := turn.Attach(turn.scope, seq(0), true)
		if err != nil {
			t.Error(err)
		}
		attached <- b
	}()
	select {
	case <-attached:
		t.Fatal("new epoch crossed writer")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}
	b := <-attached
	a.Close()
	if !turn.Snapshot().Attached || turn.Snapshot().State != StateRunning {
		t.Fatal("stale detach cancelled new writer")
	}
	called := false
	if err := a.Write(func() error { called = true; return nil }); !errors.Is(err, ErrAttachmentReplaced) || called {
		t.Fatalf("stale write: %v", err)
	}
	if _, err := turn.Attach(turn.scope, nil, false); !errors.Is(err, ErrActive) {
		t.Fatalf("duplicate stole writer: %v", err)
	}
	b.Close()
}

func TestCumulativeOfflineBudgetNotResetByReconnect(t *testing.T) {
	m, f := testManager(t, Config{})
	turn := mustCreate(t, m, testOptions())
	a := mustAttach(t, turn, nil, false)
	mustPublish(t, turn, event(0, "response.created"))
	_ = next(t, a)
	a.Close()
	f.Add(70 * time.Second)
	m.Sweep()
	b := mustAttach(t, turn, seq(0), false)
	if got := turn.Snapshot().OfflineRemaining; got != 50*time.Second {
		t.Fatalf("remaining=%v", got)
	}
	f.Add(time.Hour)
	m.Sweep()
	if turn.Snapshot().State != StateRunning {
		t.Fatal("online time charged")
	}
	b.Close()
	f.Add(49 * time.Second)
	m.Sweep()
	if turn.Snapshot().State != StateDetached {
		t.Fatal("early timeout")
	}
	f.Add(time.Second)
	m.Sweep()
	s := turn.Snapshot()
	if s.State != StateCancelled || s.Reason != ReasonOfflineBudget || s.OfflineUsed != 120*time.Second {
		t.Fatalf("budget: %+v", s)
	}
	if turn.Context().Err() == nil {
		t.Fatal("upstream not cancelled")
	}
	if _, err := turn.Attach(turn.scope, seq(0), false); err != nil {
		t.Fatal(err)
	}
	if turn.Snapshot().State != StateCancelled {
		t.Fatal("revived")
	}
}

func TestExplicitCancellationAndControlDeadlineNeverRevive(t *testing.T) {
	for _, reason := range []Reason{ReasonUserStop, ReasonAdminCancel, ReasonLeaseLost, ReasonSlowConsumer} {
		t.Run(string(reason), func(t *testing.T) {
			m, _ := testManager(t, Config{})
			turn := mustCreate(t, m, testOptions())
			a := mustAttach(t, turn, nil, false)
			mustPublish(t, turn, event(0, "response.created"))
			_ = next(t, a)
			if !turn.Cancel(reason) || turn.Cancel(reason) {
				t.Fatal("cancel not idempotent")
			}
			a.Close()
			if _, err := turn.Attach(turn.scope, seq(0), false); err != nil {
				t.Fatal(err)
			}
			if _, err := turn.Publish(context.Background(), event(1, "response.output_text.delta")); !errors.Is(err, ErrTerminal) {
				t.Fatalf("revived: %v", err)
			}
			if s := turn.Snapshot(); s.State != StateCancelled || s.Reason != reason {
				t.Fatalf("reason: %+v", s)
			}
		})
	}
	t.Run("control", func(t *testing.T) {
		m, _ := testManager(t, Config{})
		control, cancel := context.WithCancelCause(context.Background())
		o := testOptions()
		o.ControlContext = control
		turn := mustCreate(t, m, o)
		cancel(&Cancellation{ReasonLeaseLost})
		m.Sweep()
		if s := turn.Snapshot(); s.Reason != ReasonLeaseLost || s.State != StateCancelled {
			t.Fatalf("control lost: %+v", s)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		m, _ := testManager(t, Config{})
		o := testOptions()
		o.Deadline = time.Now().Add(-time.Second)
		turn := mustCreate(t, m, o)
		m.Sweep()
		if turn.Context().Err() != context.DeadlineExceeded || turn.Snapshot().Reason != ReasonDeadline {
			t.Fatal("deadline stripped")
		}
	})
}

func TestNativeIdentityDeduplicationAndConflicts(t *testing.T) {
	m, _ := testManager(t, Config{})
	turn := mustCreate(t, m, testOptions())
	a := mustAttach(t, turn, nil, false)
	e := event(0, "response.created")
	e.Identity = "native-event-0"
	mustPublish(t, turn, e)
	_ = next(t, a)
	if r := mustPublish(t, turn, e); !r.Duplicate {
		t.Fatal("duplicate retained")
	}
	conflict := e
	conflict.Data = []byte("changed")
	if _, err := turn.Publish(context.Background(), conflict); !errors.Is(err, ErrConsistency) {
		t.Fatalf("identity conflict: %v", err)
	}
	if s := turn.Snapshot(); s.State != StateFailed || s.Reason != ReasonConsistency {
		t.Fatal("conflict not terminal")
	}
}

func TestQuotaOnlineContinuesOfflineCancelsAndSharedLimits(t *testing.T) {
	t.Run("online", func(t *testing.T) {
		m, _ := testManager(t, Config{MaxEventBytes: 16})
		turn := mustCreate(t, m, testOptions())
		a := mustAttach(t, turn, nil, false)
		e := event(0, "response.created")
		r := mustPublish(t, turn, e)
		if r.Recoverable || r.RecoveryUnavailableReason != "recovery_quota_exceeded" {
			t.Fatalf("quota: %+v", r)
		}
		if got := next(t, a); !bytes.Equal(got.Data, e.Data) {
			t.Fatal("live oversized lost")
		}
		mustPublish(t, turn, event(1, "response.output_text.delta"))
		_ = next(t, a)
		if m.ResourceUsage().ProcessBytes != 0 {
			t.Fatal("overlimit journal retained")
		}
		a.Close()
		if _, err := turn.Attach(turn.scope, seq(0), false); !errors.Is(err, ErrCursorExpired) {
			t.Fatalf("false recovery: %v", err)
		}
	})
	t.Run("offline", func(t *testing.T) {
		m, _ := testManager(t, Config{MaxEventBytes: 16})
		turn := mustCreate(t, m, testOptions())
		a := mustAttach(t, turn, nil, false)
		a.Close()
		if _, err := turn.Publish(context.Background(), event(0, "response.created")); CancellationReason(err) != ReasonQuota {
			t.Fatalf("quota: %v", err)
		}
		if turn.Snapshot().Reason != ReasonQuota || turn.Context().Err() == nil {
			t.Fatal("offline survived")
		}
	})
	for _, global := range []bool{false, true} {
		t.Run(fmt.Sprintf("global=%v", global), func(t *testing.T) {
			cfg := Config{MaxTenantBytes: 350, MaxProcessBytes: 1 << 20}
			if global {
				cfg.MaxTenantBytes = 1 << 20
				cfg.MaxProcessBytes = 350
			}
			m, _ := testManager(t, cfg)
			one := mustCreate(t, m, testOptions())
			a := mustAttach(t, one, nil, false)
			mustPublish(t, one, event(0, "response.created"))
			_ = next(t, a)
			o := testOptions()
			o.IdempotencyKey = "second"
			if global {
				o.Scope.UserID = 7
			}
			two, fresh, err := m.Create(o)
			if err != nil || !fresh {
				t.Fatal(err)
			}
			if err = two.BindOwner(42, "resp-second"); err != nil {
				t.Fatal(err)
			}
			b := mustAttach(t, two, nil, false)
			e := event(0, "response.created")
			e.ResponseID = "resp-second"
			mustPublish(t, two, e)
			_ = next(t, b)
			if two.Snapshot().Recoverable {
				t.Fatal("shared quota not enforced")
			}
			if m.ResourceUsage().ProcessBytes > 350 {
				t.Fatal("accounting")
			}
		})
	}
}

func TestTerminalReplayRetentionSettlementAndStoreFalse(t *testing.T) {
	m, f := testManager(t, Config{})
	turn := mustCreate(t, m, testOptions())
	a := mustAttach(t, turn, nil, false)
	mustPublish(t, turn, event(0, "response.created"))
	_ = next(t, a)
	terminal := event(1, "response.completed")
	terminal.Terminal = StateCompleted
	terminal.Result = []byte(`{"id":"resp-original","status":"completed","output":[]}`)
	mustPublish(t, turn, terminal)
	a.Close()
	b := mustAttach(t, turn, seq(0), false)
	got := next(t, b)
	if !bytes.Equal(got.Data, terminal.Data) || !bytes.Equal(turn.Snapshot().Result, terminal.Result) {
		t.Fatal("terminal lost")
	}
	if _, err := b.Next(context.Background()); err != io.EOF {
		t.Fatalf("EOF=%v", err)
	}
	var bills atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); turn.SettleOnce(func() { bills.Add(1) }) }()
	}
	wg.Wait()
	if bills.Load() != 1 {
		t.Fatal("duplicate bill")
	}
	f.Add(5 * time.Minute)
	m.Sweep()
	if m.ResourceUsage().ProcessBytes != 0 {
		t.Fatal("TTL body leak")
	}
	if _, err := m.Lookup(turn.scope, "resp-original"); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	o := testOptions()
	o.IdempotencyKey = "store-false"
	o.Body = []byte(`{"store":false,"background":true,"stream":false}`)
	other, fresh, err := m.Create(o)
	if err != nil || !fresh {
		t.Fatal(err)
	}
	if err := other.Finish(StateCompleted, "", []byte("sensitive body")); err != nil {
		t.Fatal(err)
	}
	if len(other.Snapshot().Result) != 0 {
		t.Fatal("store false retained body")
	}
	if _, _, err := m.Create(o); !errors.Is(err, ErrExpired) {
		t.Fatalf("store false recreated: %v", err)
	}
}

func TestNonStreamingBackgroundIgnoresOfflineBudget(t *testing.T) {
	m, f := testManager(t, Config{})
	o := testOptions()
	o.Body = []byte(`{"background":true,"stream":false}`)
	turn := mustCreate(t, m, o)
	a := mustAttach(t, turn, nil, false)
	a.Close()
	f.Add(10 * time.Minute)
	m.Sweep()
	if s := turn.Snapshot(); s.State != StateRunning || s.OfflineUsed != 0 {
		t.Fatalf("nonstream timeout: %+v", s)
	}
}

func TestResourceCapacityAndClose(t *testing.T) {
	m, _ := testManager(t, Config{MaxTurns: 1})
	turn := mustCreate(t, m, testOptions())
	o := testOptions()
	o.IdempotencyKey = "next"
	if _, _, err := m.Create(o); !errors.Is(err, ErrCapacity) {
		t.Fatalf("metadata limit: %v", err)
	}
	a := mustAttach(t, turn, nil, false)
	mustPublish(t, turn, event(0, "response.created"))
	_ = next(t, a)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if turn.Context().Err() == nil || m.ResourceUsage().ProcessBytes != 0 {
		t.Fatal("close leak")
	}
	if _, _, err := m.Create(o); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
