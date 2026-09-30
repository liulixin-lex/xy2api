package responseturn

import (
	"bytes"
	"testing"
)

func TestQuotaDegradationRetainsUnreadReplayAccounting(t *testing.T) {
	for _, closeReplay := range []bool{false, true} {
		name := "consume"
		if closeReplay {
			name = "close"
		}
		t.Run(name, func(t *testing.T) {
			m, _ := testManager(t, Config{MaxEventBytes: 512})
			turn := mustCreate(t, m, testOptions())
			initial := mustAttach(t, turn, nil, false)
			first := event(0, "response.created")
			second := event(1, "response.output_text.delta")
			mustPublish(t, turn, first)
			_ = next(t, initial)
			initial.Close()
			mustPublish(t, turn, second)
			replay := mustAttach(t, turn, nil, true)
			overflow := event(2, "response.future_event")
			overflow.Data = bytes.Repeat([]byte("x"), 1024)
			mustPublish(t, turn, overflow)
			assertBytes := func(want int64) {
				t.Helper()
				m.mu.Lock()
				defer m.mu.Unlock()
				if turn.journalBytes != want || m.processBytes != want || m.tenantBytes[turn.scope.UserID] != want {
					t.Fatalf("retained replay accounting: turn=%d process=%d tenant=%d want=%d", turn.journalBytes, m.processBytes, m.tenantBytes[turn.scope.UserID], want)
				}
			}
			assertBytes(first.cost() + second.cost())
			if turn.Snapshot().Recoverable {
				t.Fatal("quota overflow still advertises recovery")
			}
			if closeReplay {
				replay.Close()
				assertBytes(0)
				return
			}
			if got := next(t, replay); !bytes.Equal(got.Data, first.Data) || !got.Replay {
				t.Fatal("first historical event was lost")
			}
			assertBytes(second.cost())
			if got := next(t, replay); !bytes.Equal(got.Data, second.Data) || !got.Replay {
				t.Fatal("second historical event was lost")
			}
			assertBytes(0)
			if got := next(t, replay); !bytes.Equal(got.Data, overflow.Data) || got.Replay {
				t.Fatal("quota overflow interrupted ordinary live delivery")
			}
			replay.Close()
			assertBytes(0)
		})
	}
}
