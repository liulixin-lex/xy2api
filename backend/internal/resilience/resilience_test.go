package resilience

import (
	"context"
	"encoding/json"
	"strings"
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
		b.RecordAttempt()
	}
	require.False(t, b.CanAttempt(t0))
	require.Equal(t, ExhaustedAttempts, b.Exhausted())

	b = NewBudget(context.Background(), &s, KindText, t0)
	require.True(t, b.CanAttempt(t0.Add(237*time.Second)))
	require.False(t, b.CanAttempt(t0.Add(239*time.Second)))
	require.Equal(t, ExhaustedDeadline, b.Exhausted())

	// Output started: the deadline no longer applies.
	b.MarkOutputStarted()
	require.True(t, b.CanAttempt(t0.Add(500*time.Second)))
	require.Equal(t, time.Duration(0), b.Remaining(t0))
}

func TestBudgetImagesHaveNoDeadline(t *testing.T) {
	s := Defaults()
	b := NewBudget(context.Background(), &s, KindImage, t0)
	require.False(t, b.HasDeadline())
	require.True(t, b.CanAttempt(t0.Add(time.Hour)))
	require.Equal(t, time.Duration(0), b.AttemptTimeout(0, false, t0))
}

func TestBudgetCanceled(t *testing.T) {
	s := Defaults()
	ctx, cancel := context.WithCancel(context.Background())
	b := NewBudget(ctx, &s, KindText, t0)
	cancel()
	require.True(t, b.Canceled())
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
}

func TestFirstOutputTier(t *testing.T) {
	s := Defaults()
	require.Equal(t, 180*time.Second, FirstOutputTier(&s, "gpt-5.5", ""))
	require.Equal(t, 180*time.Second, FirstOutputTier(&s, "gpt-5.5", "medium"))
	require.Equal(t, 180*time.Second, FirstOutputTier(&s, "gpt-5.5", "high"))
	require.Equal(t, 200*time.Second, FirstOutputTier(&s, "gpt-5.5", "XHIGH"))
	require.Equal(t, 220*time.Second, FirstOutputTier(&s, "gpt-5.5", "max"))
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

func TestHeartbeatFrames(t *testing.T) {
	require.Equal(t, ":\n\n", string(HeartbeatFrame(ProtocolResponses)))
	require.Equal(t, ":\n\n", string(HeartbeatFrame(ProtocolChat)))
	require.Equal(t, "event: ping\ndata: {\"type\":\"ping\"}\n\n", string(HeartbeatFrame(ProtocolAnthropic)))
	require.Nil(t, HeartbeatFrame(ProtocolGemini))
	require.Nil(t, HeartbeatFrame(ProtocolUnknown))
}

func dataJSON(t *testing.T, frame []byte) map[string]any {
	t.Helper()
	var line string
	for _, l := range strings.Split(string(frame), "\n") {
		if strings.HasPrefix(l, "data: ") && l != "data: [DONE]" {
			line = strings.TrimPrefix(l, "data: ")
			break
		}
	}
	require.NotEmpty(t, line)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &m))
	return m
}

func TestTerminalFrameResponses(t *testing.T) {
	f := TerminalFrame(ProtocolResponses, Terminal{Category: CategoryRateLimit, RetryAfter: 45 * time.Second, SequenceNumber: 7, ResponseID: "resp_x", Model: "gpt-5.5"})
	require.True(t, strings.HasPrefix(string(f), "event: response.failed\ndata: {"))
	require.True(t, strings.HasSuffix(string(f), "}\n\n"))
	require.Equal(t, 1, strings.Count(string(f), "\ndata: "), "payload must be one line")
	m := dataJSON(t, f)
	require.Equal(t, "response.failed", m["type"])
	require.EqualValues(t, 7, m["sequence_number"])
	resp := m["response"].(map[string]any)
	require.Equal(t, "resp_x", resp["id"])
	require.Equal(t, "failed", resp["status"])
	e := resp["error"].(map[string]any)
	require.Equal(t, "rate_limit_exceeded", e["code"])
	require.EqualValues(t, 429, e["status"])
	require.Contains(t, e["message"], "Please try again in 30s.")
}

func TestTerminalFrameChat(t *testing.T) {
	f := TerminalFrame(ProtocolChat, Terminal{Category: CategoryStall})
	require.True(t, strings.HasSuffix(string(f), "data: [DONE]\n\n"))
	m := dataJSON(t, f)
	e := m["error"].(map[string]any)
	require.Equal(t, "server_error", e["code"])
	require.EqualValues(t, 504, e["status"])
	require.Contains(t, e["message"], "Please try again in 2s.")
}

func TestTerminalFrameAnthropicHasEventLine(t *testing.T) {
	// G3: Anthropic SDKs drop data frames without an event line.
	f := TerminalFrame(ProtocolAnthropic, Terminal{Category: CategoryTransient, Status: 502, Message: "Upstream request failed", RetryAfter: 10 * time.Second})
	require.True(t, strings.HasPrefix(string(f), "event: error\ndata: "))
	m := dataJSON(t, f)
	require.Equal(t, "error", m["type"])
	e := m["error"].(map[string]any)
	require.Equal(t, "api_error", e["type"])
	require.EqualValues(t, 502, e["status"])
	require.Equal(t, "Upstream request failed. Please try again in 10s.", e["message"])

	busy := dataJSON(t, TerminalFrame(ProtocolAnthropic, Terminal{Category: CategoryRateLimit}))
	require.Equal(t, "overloaded_error", busy["error"].(map[string]any)["type"])
	require.Nil(t, TerminalFrame(ProtocolGemini, Terminal{}))
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

func TestClampRetryAfter(t *testing.T) {
	require.Equal(t, 2, ClampRetryAfter(0, 2, 30))
	require.Equal(t, 30, ClampRetryAfter(time.Hour, 2, 30))
	require.Equal(t, 11, ClampRetryAfter(10500*time.Millisecond, 2, 30))
	require.Equal(t, 2, TerminalRetrySeconds(time.Second))
	require.Equal(t, 30, TerminalRetrySeconds(time.Minute))
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
