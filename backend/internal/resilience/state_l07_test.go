package resilience

import "testing"

func TestKeepStickyForAttempt(t *testing.T) {
	s := NewRequestState()
	if !s.KeepStickyForAttempt(5) || s.KeptSticky() != 5 {
		t.Fatal("attempt keep")
	}
	s.ReleaseAttemptSticky()
	if s.KeptSticky() != 0 {
		t.Fatal("released")
	}
	// A request-scoped (soft) keep is never released by the attempt release.
	s.KeepSticky(6)
	s.ReleaseAttemptSticky()
	if s.KeptSticky() != 6 || s.KeepStickyForAttempt(7) || !s.KeepStickyForAttempt(6) {
		t.Fatal("soft keep wins")
	}
	s.ReleaseAttemptSticky()
	if s.KeptSticky() != 6 {
		t.Fatal("soft keep survives")
	}
	// A soft keep of the same owner upgrades an attempt keep to the request.
	u := NewRequestState()
	u.KeepStickyForAttempt(8)
	u.KeepSticky(8)
	u.ReleaseAttemptSticky()
	if u.KeptSticky() != 8 {
		t.Fatal("upgraded")
	}
	var nilState *RequestState
	if nilState.KeepStickyForAttempt(1) || nilState.SelectionNoted() || nilState.L07() != nil {
		t.Fatal("nil-safe")
	}
	nilState.ReleaseAttemptSticky()
	nilState.SetL07(1)
}

func TestSelectionNotedAndL07Record(t *testing.T) {
	s := NewRequestState()
	if s.SelectionNoted() {
		t.Fatal("before selection")
	}
	s.NoteTurn(1, "m", "h")
	if !s.SelectionNoted() {
		t.Fatal("after selection")
	}
	type rec struct{ n int }
	r := &rec{1}
	s.SetL07(r)
	if s.L07().(*rec) != r {
		t.Fatal("record")
	}
	s.SetL07(nil)
	if s.L07().(*rec) != r {
		t.Fatal("nil ignored")
	}
}
