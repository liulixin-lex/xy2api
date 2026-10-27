package resilience

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func TestDefaultsAreValidAndRoundTrip(t *testing.T) {
	d := Defaults()
	require.NoError(t, d.Validate())
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	parsed, err := Parse(string(raw))
	require.NoError(t, err)
	require.Equal(t, d, parsed)

	empty, err := Parse("")
	require.NoError(t, err)
	require.Equal(t, d, empty)
}

func TestParseKeepsDefaultsForMissingFields(t *testing.T) {
	s, err := Parse(`{"enabled":true,"keepalive":{"enabled":false}}`)
	require.NoError(t, err)
	require.False(t, s.KeepaliveOn())
	// Fields of the same object that were omitted keep their default.
	require.Equal(t, 15, s.Keepalive.DelaySeconds)
	require.True(t, s.BudgetOn())
}

func TestParseRejectsInvalid(t *testing.T) {
	_, err := Parse(`{"enabled":true,"budget":{"enabled":true,"max_attempts":0}}`)
	require.Error(t, err)
	_, err = Parse(`{not json`)
	require.Error(t, err)
}

func TestValidateRanges(t *testing.T) {
	s := Defaults()
	s.Keepalive.DelaySeconds = 61
	s.Budget.PreOutputBudgetSeconds = 300
	s.Stall.TextStallSeconds = 10
	s.Budget.FirstOutputTimeoutSeconds.Max = 250
	err := s.Validate()
	require.Error(t, err)
	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	require.GreaterOrEqual(t, len(ve.Problems), 3)

	s = Defaults()
	s.Budget.FirstOutputTimeoutSeconds.Max = 241 // above the 240 s budget
	require.Error(t, s.Validate())
	s.Budget.PreOutputBudgetSeconds = 290
	require.NoError(t, s.Validate())
}

func TestMasterSwitchDisablesEverything(t *testing.T) {
	s := Defaults()
	s.Enabled = false
	require.False(t, s.BudgetOn())
	require.False(t, s.KeepaliveOn())
	require.False(t, s.SessionOn())
	require.False(t, s.ErrorsOn())
	require.False(t, s.FailureOn())
	var nilS *Settings
	require.False(t, nilS.BudgetOn())
}

func TestPublishCurrent(t *testing.T) {
	orig := *Current()
	defer Publish(orig)
	s := Defaults()
	s.Budget.MaxAttempts = 7
	Publish(s)
	require.Equal(t, 7, Current().Budget.MaxAttempts)
}

func TestBudgetAttemptsAndDeadline(t *testing.T) {
	s := Defaults()
	b := NewBudget(context.Background(), &s, KindText, t0)
	require.NotNil(t, b)
	for i := 0; i < 4; i++ {
		require.True(t, b.CanAttempt(t0))
		b.RecordAttemptAt(t0)
	}
	require.False(t, b.CanAttempt(t0))
	require.Equal(t, ExhaustedAttempts, b.Exhausted())

	b = NewBudget(context.Background(), &s, KindText, t0)
	require.True(t, b.CanAttempt(t0.Add(237*time.Second)))
	require.False(t, b.CanAttempt(t0.Add(239*time.Second)))
	require.Equal(t, ExhaustedDeadline, b.Exhausted())
}

func TestBudgetImagesHaveNoDeadline(t *testing.T) {
	s := Defaults()
	b := NewBudget(context.Background(), &s, KindImage, t0)
	require.True(t, b.Deadline().IsZero())
	require.True(t, b.CanAttempt(t0.Add(time.Hour)))
	require.Equal(t, time.Duration(0), b.AttemptTimeout(0, false, t0))
}

func TestBudgetCanceled(t *testing.T) {
	s := Defaults()
	ctx, cancel := context.WithCancel(context.Background())
	b := NewBudget(ctx, &s, KindText, t0)
	cancel()
	require.False(t, b.CanAttempt(t0))
	require.Equal(t, ExhaustedCanceled, b.Exhausted())
}

func TestBudgetDisabledIsNil(t *testing.T) {
	s := Defaults()
	s.Budget.Enabled = false
	b := NewBudget(context.Background(), &s, KindText, t0)
	require.Nil(t, b)
	require.True(t, b.CanAttempt(t0))
	require.True(t, b.AllowSameAccountRetry(1))
	require.Equal(t, 5*time.Second, b.ClampWait(5*time.Second, t0))
	require.Equal(t, 60*time.Second, b.AttemptTimeout(60*time.Second, false, t0))
}

func TestBudgetSameAccountAndCapacity(t *testing.T) {
	s := Defaults()
	b := NewBudget(context.Background(), &s, KindText, t0)
	require.True(t, b.AllowSameAccountRetry(1))
	require.False(t, b.AllowSameAccountRetry(1))
	require.True(t, b.AllowSameAccountRetry(2))
	require.True(t, b.AllowCapacitySwitch())
	require.False(t, b.AllowCapacitySwitch())
}

func TestBudgetClampWaitAndAttemptTimeout(t *testing.T) {
	s := Defaults()
	b := NewBudget(context.Background(), &s, KindText, t0)
	// Capped at 60 s.
	require.Equal(t, 60*time.Second, b.ClampWait(120*time.Second, t0))
	// Bounded by the remaining time minus the attempt window.
	require.Equal(t, 8*time.Second, b.ClampWait(30*time.Second, t0.Add(230*time.Second)))

	// Spec: medium-effort tier is 60 s; last candidate only answers to the deadline.
	require.Equal(t, 60*time.Second, b.AttemptTimeout(60*time.Second, false, t0))
	require.Equal(t, 240*time.Second, b.AttemptTimeout(60*time.Second, true, t0))
	require.Equal(t, 40*time.Second, b.AttemptTimeout(60*time.Second, false, t0.Add(200*time.Second)))
	// A later attempt gets at least the time already spent (geometric back-off).
	require.Equal(t, 90*time.Second, b.AttemptTimeout(60*time.Second, false, t0.Add(90*time.Second)))
}

func TestFirstOutputTier(t *testing.T) {
	s := Defaults()
	require.Equal(t, 20*time.Second, FirstOutputTier(&s, "gpt-5.5", ""))
	require.Equal(t, 20*time.Second, FirstOutputTier(&s, "gpt-5.5", "medium"))
	require.Equal(t, 25*time.Second, FirstOutputTier(&s, "gpt-5.5", "high"))
	require.Equal(t, 30*time.Second, FirstOutputTier(&s, "gpt-5.5", "XHIGH"))
	require.Equal(t, 30*time.Second, FirstOutputTier(&s, "gpt-5.5", "max"))
	s.Budget.ModelOverrides = map[string]int{"gpt-6.1-sol": 200, "gpt-6*": 150}
	require.Equal(t, 200*time.Second, FirstOutputTier(&s, "GPT-6.1-sol", "low"))
	require.Equal(t, 150*time.Second, FirstOutputTier(&s, "gpt-6.2", "low"))
}

func TestJitterBounds(t *testing.T) {
	for i := 0; i < 1000; i++ {
		d := Jitter(100*time.Second, 0.15)
		require.GreaterOrEqual(t, d, 85*time.Second)
		require.LessOrEqual(t, d, 115*time.Second)
	}
	require.Equal(t, time.Second, Jitter(time.Second, 0))
}

func TestSleepCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, Sleep(ctx, time.Hour))
	require.True(t, Sleep(context.Background(), time.Millisecond))
}

func TestEdgeOf(t *testing.T) {
	edges := []string{"cf", "esa"}
	require.Equal(t, "cf", EdgeOf(" CF ", true, edges))
	require.Equal(t, "", EdgeOf("cf", false, edges), "untrusted peers cannot set the marker")
	require.Equal(t, "", EdgeOf("akamai", true, edges))
	require.Equal(t, "", EdgeOf("", true, edges))
}

func TestNegativeCache(t *testing.T) {
	c := NewNegativeCache()
	key := NegativeKey(28, "grok", "grok-4")
	resp := CachedResponse{Status: 503, Body: []byte("x"), RetryAt: t0.Add(10 * time.Second)}
	c.Put(key, 28, resp, 2*time.Second, t0)
	got, ok := c.Get(key, t0.Add(time.Second))
	require.True(t, ok)
	require.Equal(t, 9, got.RetryAfterSeconds(t0.Add(time.Second)))
	_, ok = c.Get(key, t0.Add(2*time.Second))
	require.False(t, ok)

	c.Put(key, 28, resp, 2*time.Second, t0)
	c.InvalidateGroup(28)
	_, ok = c.Get(key, t0)
	require.False(t, ok)
}

func TestFastFailTripsAfterThreshold(t *testing.T) {
	f := NewFastFail()
	sig := Signature(503, "api_error", "Service temporarily unavailable")
	resp := CachedResponse{Status: 503, RetryAt: t0.Add(25 * time.Second)}
	for i := 0; i < 9; i++ {
		require.False(t, f.Observe(42, sig, resp, 30*time.Second, 10, t0.Add(time.Duration(i)*time.Second)))
		_, tripped := f.Check(42, t0.Add(time.Duration(i)*time.Second))
		require.False(t, tripped)
	}
	require.True(t, f.Observe(42, sig, resp, 30*time.Second, 10, t0.Add(9*time.Second)))
	got, tripped := f.Check(42, t0.Add(10*time.Second))
	require.True(t, tripped)
	require.Equal(t, 15, got.RetryAfterSeconds(t0.Add(10*time.Second)))

	// Different keys are independent.
	_, tripped = f.Check(43, t0.Add(10*time.Second))
	require.False(t, tripped)

	// Once RetryAt passes, one request goes through again.
	_, tripped = f.Check(42, t0.Add(26*time.Second))
	require.False(t, tripped)

	// A different response resets the streak.
	f2 := NewFastFail()
	for i := 0; i < 9; i++ {
		f2.Observe(1, sig, resp, 30*time.Second, 10, t0)
	}
	require.False(t, f2.Observe(1, Signature(503, "api_error", "other"), resp, 30*time.Second, 10, t0))

	// Old observations fall out of the window.
	f3 := NewFastFail()
	for i := 0; i < 9; i++ {
		f3.Observe(1, sig, resp, 30*time.Second, 10, t0)
	}
	require.False(t, f3.Observe(1, sig, resp, 30*time.Second, 10, t0.Add(31*time.Second)))
}

func TestTurnCapCountsConsecutiveFailures(t *testing.T) {
	// Spec: a conversation's exhausted failures are counted in a row; a gap
	// longer than the window or a success starts over.
	tc := NewTurnCap()
	key := TurnKey(7, 28, "gpt-6.1-sol", "sess")
	for i := 1; i <= 5; i++ {
		require.Equal(t, i, tc.Fail(key, 120*time.Second, t0.Add(time.Duration(i)*30*time.Second)))
	}
	require.Equal(t, 1, tc.Fail(TurnKey(7, 28, "gpt-6.1-sol", "other"), 120*time.Second, t0), "other conversations are independent")
	require.Equal(t, 1, tc.Fail(key, 120*time.Second, t0.Add(150*time.Second+121*time.Second)), "gap above the window")
	tc.Reset(key)
	require.Equal(t, 1, tc.Fail(key, 120*time.Second, t0.Add(300*time.Second)), "reset after success")
	require.Equal(t, 0, tc.Fail("", 120*time.Second, t0))

	s := Defaults()
	require.Equal(t, TurnCapSettings{Enabled: true, Failures: 5, WindowSeconds: 120}, s.Errors.TurnCap)
	s.Errors.TurnCap.Failures = 1
	require.Error(t, s.Validate())
	s.Errors.TurnCap.Failures = 5
	s.Errors.TurnCap.WindowSeconds = 5
	require.Error(t, s.Validate())
}

func TestRequestStateTurnKey(t *testing.T) {
	st := NewRequestState()
	require.Empty(t, st.TurnKey(), "no selection yet")
	st.NoteTurn(28, "gpt-6.1-sol", "sess")
	require.Empty(t, st.TurnKey(), "no API key")
	st.SetAPIKey(7)
	require.Equal(t, TurnKey(7, 28, "gpt-6.1-sol", "sess"), st.TurnKey())
	noSession := NewRequestState()
	noSession.SetAPIKey(7)
	noSession.NoteTurn(28, "gpt-6.1-sol", "")
	require.Empty(t, noSession.TurnKey(), "requests without a session are not one turn")
	require.False(t, st.TurnFailed())
	st.MarkTurnFailed()
	require.True(t, st.TurnFailed())
	var nilState *RequestState
	nilState.NoteTurn(1, "m", "s")
	nilState.MarkTurnFailed()
	require.Empty(t, nilState.TurnKey())
}

func TestClampRetryAfter(t *testing.T) {
	require.Equal(t, 2, ClampRetryAfter(0, 2, 30))
	require.Equal(t, 30, ClampRetryAfter(time.Hour, 2, 30))
	require.Equal(t, 11, ClampRetryAfter(10500*time.Millisecond, 2, 30))
}

func TestJitterSeconds(t *testing.T) {
	seen := map[int]bool{}
	for i := 0; i < 200; i++ {
		v := JitterSeconds(5, 1)
		if v < 4 || v > 6 {
			t.Fatalf("out of range: %d", v)
		}
		seen[v] = true
	}
	if len(seen) < 2 {
		t.Fatalf("no spread: %v", seen)
	}
	if JitterSeconds(1, 3) < 1 || JitterSeconds(0, 0) != 1 {
		t.Fatal("floor of 1 violated")
	}
}

func TestBudgetInPlaceRetryAndAttemptDuration(t *testing.T) {
	s := Defaults()
	b := NewBudget(context.Background(), &s, KindText, t0)
	require.Equal(t, 5*time.Second, b.RecordAttemptAt(t0.Add(5*time.Second)))
	require.Equal(t, 5*time.Second, b.LastAttemptDuration())

	// A same-account in-place retry is allowed once per account.
	require.True(t, b.ConsumeInPlaceRetry(1, true, t0.Add(6*time.Second)))
	require.False(t, b.ConsumeInPlaceRetry(1, true, t0.Add(7*time.Second)))
	require.Equal(t, 2, b.Attempts(), "a refused retry records nothing")

	// The last attempt is never consumed in place: the handler records it.
	require.True(t, b.ConsumeInPlaceRetry(2, false, t0.Add(8*time.Second)))
	require.False(t, b.ConsumeInPlaceRetry(3, false, t0.Add(9*time.Second)))
	require.Equal(t, 3, b.Attempts())

	// Capacity switch bookkeeping.
	require.False(t, b.CapacitySwitchUsed())
	require.True(t, b.AllowCapacitySwitch())
	require.True(t, b.CapacitySwitchUsed())

	var nb *Budget
	require.True(t, nb.ConsumeInPlaceRetry(1, true, t0))
	require.Zero(t, nb.LastAttemptDuration())
}

func TestReportStreamFailureOncePerAttempt(t *testing.T) {
	st := NewRequestState()
	resp := &struct{ n int }{}
	require.False(t, st.StreamFailureReported(7))
	require.True(t, st.ReportStreamFailure(7, resp))
	require.False(t, st.ReportStreamFailure(7, resp), "the same attempt counts once")
	require.True(t, st.StreamFailureReported(7))
	require.False(t, st.StreamFailureReported(8))
	require.True(t, st.ReportStreamFailure(7, 2), "another attempt (a later WebSocket turn) counts")
	require.True(t, st.ReportStreamFailure(8, resp), "another account counts")

	var none *RequestState
	require.True(t, none.ReportStreamFailure(7, resp), "without a state every report is new")
	require.False(t, none.StreamFailureReported(7))
}
