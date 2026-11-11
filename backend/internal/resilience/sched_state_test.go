//go:build unit

package resilience

import (
	"testing"
	"time"
)

type schedTestMarker struct{ id int }

func (*schedTestMarker) MarkFirstOutput(time.Time) {}
func TestSchedState(t *testing.T) {
	s := NewRequestState()
	if _, ok := s.SchedSelection(); ok {
		t.Fatal("unexpected selection")
	}
	want := SchedSelection{AccountID: 42, Model: "model", StickyWarm: true}
	s.SetSchedSelection(want)
	if got, ok := s.SchedSelection(); !ok || got != want {
		t.Fatalf("selection: %v %v", got, ok)
	}
	v := &struct{ X int }{1}
	s.SetSchedTest(v)
	if s.SchedTest() != v {
		t.Fatal("test marker")
	}
	a, b := &schedTestMarker{1}, &schedTestMarker{2}
	s.SetCurrentAttempt(a)
	s.SetCurrentAttempt(b)
	s.ClearCurrentAttemptIf(a)
	if s.CurrentAttempt() != b {
		t.Fatal("cleared another attempt")
	}
	s.ClearCurrentAttemptIf(b)
	if s.CurrentAttempt() != nil {
		t.Fatal("not cleared")
	}
}
