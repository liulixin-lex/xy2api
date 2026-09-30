package responseturn

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIndependentReviewConcurrentFirstCreateAndScope(t *testing.T) {
	m, _ := testManager(t, Config{})
	start := make(chan struct{})
	var wg sync.WaitGroup
	var created atomic.Int64
	ids := sync.Map{}
	for i := 0; i < 96; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			turn, fresh, err := m.Create(testOptions())
			if err != nil {
				t.Error(err)
				return
			}
			ids.Store(turn.ID(), true)
			if fresh {
				created.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	count := 0
	ids.Range(func(_, _ any) bool { count++; return true })
	if created.Load() != 1 || count != 1 {
		t.Fatalf("created=%d identities=%d", created.Load(), count)
	}
	original, _, err := m.Create(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []Scope{{1, 8, 3, "responses"}, {1, 2, 9, "responses"}, {1, 2, 3, "chat"}, {8, 2, 3, "responses"}} {
		o := testOptions()
		o.Scope = scope
		turn, fresh, err := m.Create(o)
		if err != nil || !fresh || turn == original {
			t.Fatalf("scope collision: %+v %v", scope, err)
		}
	}
}

func TestIndependentReviewTerminalTTLReleasesAttachmentReplay(t *testing.T) {
	m, f := testManager(t, Config{TerminalTTL: time.Second})
	turn := mustCreate(t, m, testOptions())
	a := mustAttach(t, turn, nil, false)
	mustPublish(t, turn, event(0, "response.created"))
	_ = next(t, a)
	a.Close()
	for i := int64(1); i < 5; i++ {
		mustPublish(t, turn, event(i, "response.output_text.delta"))
	}
	if err := turn.Finish(StateCompleted, "", []byte("{\"id\":\"resp-original\",\"status\":\"completed\"}")); err != nil {
		t.Fatal(err)
	}
	replay := mustAttach(t, turn, seq(0), false)
	f.Add(2 * time.Second)
	m.Sweep()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	leaked, err := replay.Next(ctx)
	t.Logf("expired=true accounted_bytes=%d retained_replay_slots=%d replay_bytes=%d next_error=%v", m.ResourceUsage().ProcessBytes, len(replay.replay), len(leaked.Data), err)
	if err == nil || len(leaked.Data) > 0 {
		t.Fatal("terminal TTL erased accounting but existing attachment still retained and delivered expired body")
	}
}

func TestIndependentReviewStoreFalseTerminalReleasesQueuedBody(t *testing.T) {
	m, _ := testManager(t, Config{})
	o := testOptions()
	o.Body = []byte("{\"background\":true,\"stream\":true,\"store\":false}")
	turn := mustCreate(t, m, o)
	a := mustAttach(t, turn, nil, false)
	terminal := event(0, "response.completed")
	terminal.Terminal = StateCompleted
	mustPublish(t, turn, terminal)
	a.Close()
	m.mu.Lock()
	retained := len(a.live)
	m.mu.Unlock()
	t.Logf("store=false state=%s accounted_bytes=%d closed_attachment_retained_events=%d", turn.Snapshot().State, m.ResourceUsage().ProcessBytes, retained)
	if retained != 0 {
		t.Fatal("closed attachment retains terminal body after store:false cleanup")
	}
}

func TestIndependentReviewCancelCallbackCanObserveState(t *testing.T) {
	// Do not register Close cleanup until the potential deadlock is disproven.
	m := NewManager(Config{DisableBackground: true})
	o := testOptions()
	var turn *Turn
	seen := make(chan State, 1)
	o.OnCancel = func(Reason) { seen <- turn.Snapshot().State }
	var err error
	turn, _, err = m.Create(o)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() { done <- turn.Cancel(ReasonUserStop) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Cancel invoked host callback under manager lock; Snapshot deadlocked")
	}
	select {
	case state := <-seen:
		if state != StateCancelled {
			t.Fatalf("callback observed nonterminal state %s", state)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel callback not delivered")
	}
	_ = m.Close()
}

func TestIndependentReviewClientDetachReasonIsNotUserStop(t *testing.T) {
	m, _ := testManager(t, Config{})
	turn := mustCreate(t, m, testOptions())
	turn.Cancel(ReasonClientDetached)
	got := turn.Snapshot().Reason
	t.Logf("requested_reason=%s observed_reason=%s", ReasonClientDetached, got)
	if got != ReasonClientDetached {
		t.Fatal("network detach was mislabeled as an explicit user stop")
	}
}

func TestIndependentReviewJitterDoesNotResetOfflineBudget(t *testing.T) {
	m, f := testManager(t, Config{})
	turn := mustCreate(t, m, testOptions())
	a := mustAttach(t, turn, nil, false)
	mustPublish(t, turn, event(0, "response.created"))
	_ = next(t, a)
	for i := 0; i < 60; i++ {
		a.Close()
		f.Add(2 * time.Second)
		m.Sweep()
		if i < 59 {
			var err error
			a, err = turn.Attach(turn.scope, seq(0), false)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	s := turn.Snapshot()
	if s.State != StateCancelled || s.Reason != ReasonOfflineBudget || s.OfflineUsed != 120*time.Second {
		t.Fatalf("jitter reset budget: %+v", s)
	}
}

func TestIndependentReviewQuotaOnlinePassesOfflineCancels(t *testing.T) {
	for _, offline := range []bool{false, true} {
		t.Run(map[bool]string{false: "online", true: "offline"}[offline], func(t *testing.T) {
			m, _ := testManager(t, Config{MaxEventBytes: 32, MaxTurnBytes: 1024})
			turn := mustCreate(t, m, testOptions())
			a := mustAttach(t, turn, nil, false)
			if offline {
				a.Close()
			}
			frame := event(0, "response.created")
			got, err := turn.Publish(context.Background(), frame)
			if offline {
				if err == nil || turn.Snapshot().Reason != ReasonQuota || turn.Context().Err() == nil {
					t.Fatalf("offline overquota execution continued: %+v %v", got, err)
				}
			} else {
				if err != nil || got.Recoverable {
					t.Fatalf("online recovery bound blocked live data: %+v %v", got, err)
				}
				delivered := next(t, a)
				if string(delivered.Data) != string(frame.Data) {
					t.Fatal("ordinary live event was lost")
				}
				if _, err := turn.Attach(turn.scope, nil, true); !errors.Is(err, ErrCursorExpired) {
					t.Fatalf("lossless recovery misrepresented: %v", err)
				}
			}
		})
	}
}
