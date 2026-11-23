//go:build unit

package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/service"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type replaySend struct {
	Optional     bool                    `json:"optional,omitempty"`
	Action       resilience.CollabAction `json:"action,omitempty"`
	RejectedItem string                  `json:"rejected_item,omitempty"`
	Account      string                  `json:"account"`
	Body         string                  `json:"body_digest"`
	Survived     []string                `json:"survived"`
	Learning     []string                `json:"learning"`
	Rule         string                  `json:"rule"`
	Items        map[string]string       `json:"item_digests"`
}

type replayBinding struct {
	Key     string `json:"key_digest"`
	Account string `json:"account"`
}

type replayOutcome struct {
	Status                int             `json:"status"`
	IntendedStatus        int             `json:"intended_status"`
	Protocol              string          `json:"protocol"`
	Code                  string          `json:"code"`
	ErrorType             string          `json:"error_type"`
	Success               bool            `json:"success"`
	Retryable             bool            `json:"retryable"`
	RetryAfter            string          `json:"retry_after"`
	RetryHeader           string          `json:"retry_header"`
	Sends                 []replaySend    `json:"sends"`
	SendsPerAccount       map[string]int  `json:"sends_per_account"`
	Events                []string        `json:"event_digests"`
	TerminalCount         int             `json:"terminal_count"`
	Survived              []string        `json:"survived"`
	Bindings              []replayBinding `json:"bindings"`
	Avoidance             []replayBinding `json:"avoidance"`
	RateLimited           []string        `json:"rate_limited"`
	UsageAccounts         []string        `json:"usage_accounts"`
	UnbilledCount         int             `json:"unbilled_count"`
	Learning              []string        `json:"learning"`
	MaxIdleMillis         int64           `json:"max_idle_millis"`
	IdleLimitMillis       int64           `json:"idle_limit_millis"`
	RecordCalls           int             `json:"record_calls"`
	TerminalMillis        int64           `json:"terminal_millis"`
	AcceptedMillis        int64           `json:"accepted_millis"`
	HeadersBeforeTerminal bool            `json:"headers_before_terminal"`
	UserWaiters           bool            `json:"user_waiters"`
	TerminalCounters      []string        `json:"terminal_counters"`
	TerminalEffects       []string        `json:"terminal_effects"`
	RetryAdvice           string          `json:"retry_advice_digest"`
	LocalEnding           bool            `json:"local_ending"`
	Delivery              string          `json:"delivery_digest"`
}

type replayGolden struct {
	Case  string          `json:"case"`
	Turns []replayOutcome `json:"turns"`
}

// A public synthetic key, never an operational digest key. Hash exact bytes:
// do not canonicalize JSON or erase IDs/timestamps to hide nondeterminism.
func replayDigest(body []byte) string {
	h := hmac.New(sha256.New, []byte("synthetic-replay-digest-key"))
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// The snapshot is supplied by the caller and published before ingress. Future
// switches can be set on it without changing the comparison contract.
func checkReplayOff(want, got []replayGolden) error {
	if len(want) != len(got) {
		return fmt.Errorf("case count differs: %d != %d", len(want), len(got))
	}
	for i := range want {
		if !reflect.DeepEqual(want[i], got[i]) {
			return fmt.Errorf("baseline differs: %s", want[i].Case)
		}
	}
	return nil
}

func checkReplayOn(snapshot resilience.Settings, mode string, want, got []replayGolden) error {
	// Include the snapshot's digest in diagnostics, never its body.
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	fail := func(name, reason string) error {
		return fmt.Errorf("%s: %s (settings %s)", name, reason, replayDigest(raw))
	}
	if mode != "ladder" && mode != "lossless_first" {
		return fail("policy", "unknown comparison mode")
	}
	if len(want) != len(got) {
		return fail("corpus", "case count differs")
	}
	for i, before := range want {
		after := got[i]
		if before.Case != after.Case || len(before.Turns) != len(after.Turns) {
			return fail(before.Case, "case or turn mismatch")
		}
		for j, old := range before.Turns {
			next := after.Turns[j]
			if mode == "ladder" && old.Success {
				// Includes learning at send boundaries, account order, body/event
				// bytes, bindings, avoidance, and usage attribution.
				oldBehavior, nextBehavior := old, next
				oldBehavior.RecordCalls, nextBehavior.RecordCalls = 0, 0
				if !reflect.DeepEqual(oldBehavior, nextBehavior) {
					return fail(before.Case, "successful baseline changed")
				}
				continue
			}
			if mode == "ladder" && (next.IdleLimitMillis != old.IdleLimitMillis || next.IdleLimitMillis <= 0 || next.MaxIdleMillis >= next.IdleLimitMillis) {
				return fail(before.Case, "client idle timeout reached")
			}
			if mode == "ladder" {
				if !reflect.DeepEqual(old.TerminalCounters, next.TerminalCounters) || !reflect.DeepEqual(old.TerminalEffects, next.TerminalEffects) || !reflect.DeepEqual(old.Avoidance, next.Avoidance) || !reflect.DeepEqual(old.RateLimited, next.RateLimited) {
					return fail(before.Case, "terminal side effects changed")
				}
				if !next.Success && old.RetryAdvice != next.RetryAdvice {
					return fail(before.Case, "terminal retry advice changed")
				}
				if old.TerminalMillis >= 0 {
					limit := int64(60000)
					if old.Retryable {
						limit = 15000
					}
					if old.UserWaiters {
						limit = 0
					}
					at := next.TerminalMillis
					if next.Success {
						at = next.AcceptedMillis
					}
					if at < 0 || at > old.TerminalMillis+limit {
						return fail(before.Case, "saved error takeover deadline exceeded")
					}
					if !old.HeadersBeforeTerminal && !next.Success && next.HeadersBeforeTerminal {
						return fail(before.Case, "headers committed before saved error")
					}
				}
				if old.UserWaiters && (old.Success != next.Success || old.TerminalMillis != next.TerminalMillis || !reflect.DeepEqual(old.Sends, next.Sends) || !reflect.DeepEqual(old.Events, next.Events)) {
					return fail(before.Case, "queued user did not receive immediate baseline result")
				}
			}
			if old.Retryable && next.LocalEnding {
				prior := 0
				for _, turn := range before.Turns[:j] {
					if turn.Delivery == old.Delivery && !turn.Success && turn.Code == old.Code && turn.ErrorType == old.ErrorType {
						prior++
					}
				}
				if prior < 2 {
					return fail(before.Case, "local ending preempted client retry")
				}
			}
			if old.Success && !next.Success {
				return fail(before.Case, "new client failure")
			}
			if !old.Success && !next.Success && (old.Status != next.Status || old.IntendedStatus != next.IntendedStatus || old.Protocol != next.Protocol || old.Code != next.Code || old.ErrorType != next.ErrorType || old.Retryable != next.Retryable || old.RetryHeader != next.RetryHeader || old.RetryAfter != next.RetryAfter) {
				return fail(before.Case, "failure form changed")
			}
			if next.TerminalCount > 1 && old.TerminalCount <= 1 {
				return fail(before.Case, "duplicate terminal")
			}
			if len(next.UsageAccounts) > 1 && len(old.UsageAccounts) <= 1 {
				return fail(before.Case, "duplicate usage attribution")
			}
			// Success takes priority over content. A previously failed request
			// may now succeed through a lossy fallback.
			if !old.Success && next.Success {
				continue
			}
			if mode == "ladder" {
				continue
			}
			if replayReasoningTradeoff(before.Case, old, next) {
				continue
			}
			for _, content := range old.Survived {
				found := false
				for _, retained := range next.Survived {
					if retained == content {
						found = true
						break
					}
				}
				if !found {
					return fail(before.Case, "additional content loss: "+content)
				}
			}
		}
	}
	return nil
}

func checkHandledReplay(got []replayGolden) error {
	for _, c := range got {
		target := ""
		if strings.HasPrefix(c.Case, "handled_task_named_again/") {
			target = "amsg_task"
		}
		if strings.HasPrefix(c.Case, "handled_reasoning_named_again/") {
			target = "rs_alpha"
		}
		if target == "" {
			continue
		}
		for _, turn := range c.Turns {
			first := map[string]replaySend{}
			renamed := ""
			replaced := map[string]bool{}
			protected := target == "amsg_task"
			for _, send := range turn.Sends {
				if !send.Optional {
					renamed = ""
				} // Legacy failover retains its own sequence.
				if send.Optional && renamed != "" && (!protected || renamed != send.Account || replaced[renamed] || send.Action != resilience.CollabActionReplaceAll) {
					return fmt.Errorf("%s: send after re-named handled item", c.Case)
				}
				original, seen := first[send.Account]
				if !seen {
					first[send.Account] = send
					continue
				}
				if send.Optional {
					if send.Action == resilience.CollabActionReplaceAll {
						if replaced[send.Account] {
							return fmt.Errorf("%s: second replace-all on account", c.Case)
						}
						replaced[send.Account] = true
					}
				}
				// The immutable input digest changes when this send handles
				// the named item. A refusal naming it again closes its path.
				namesTarget := send.RejectedItem == target || !send.Optional && send.Rule == "named_always"
				if namesTarget && original.Items[target] != send.Items[target] {
					renamed = send.Account
				}
				if !send.Optional {
					for id, digest := range original.Items {
						if id != target && send.Items[id] != digest {
							return fmt.Errorf("%s: repeated named item changed another item", c.Case)
						}
					}
				}
			}
		}
	}
	return nil
}

func TestCollaborationReplayCheckersRejectRegressions(t *testing.T) {
	good := []replayGolden{{Case: "synthetic", Turns: []replayOutcome{{Status: 200, Success: true, TerminalCount: 1, Survived: []string{"task", "reasoning"}, Events: []string{"digest"}, Sends: []replaySend{{Account: "first", Body: "digest"}}}}}}
	clone := func(in []replayGolden) []replayGolden {
		raw, err := json.Marshal(in)
		require.NoError(t, err)
		var out []replayGolden
		require.NoError(t, json.Unmarshal(raw, &out))
		return out
	}
	snapshot := resilience.Defaults()
	require.NoError(t, checkReplayOff(good, good))
	require.NoError(t, checkReplayOn(snapshot, "lossless_first", good, good))
	for _, mutate := range []func(*replayOutcome){
		func(o *replayOutcome) { o.Success = false },
		func(o *replayOutcome) { o.Survived = []string{"reasoning"} },
		func(o *replayOutcome) { o.TerminalCount++ },
		func(o *replayOutcome) { o.UsageAccounts = []string{"first", "second"} },
	} {
		bad := clone(good)
		mutate(&bad[0].Turns[0])
		require.Error(t, checkReplayOff(good, bad))
		require.Error(t, checkReplayOn(snapshot, "lossless_first", good, bad))
	}
	for _, mutate := range []func(*replayOutcome){func(o *replayOutcome) { o.Events[0] = "changed" }, func(o *replayOutcome) { o.Sends[0].Body = "changed" }, func(o *replayOutcome) { o.Sends[0].Account = "second" }} {
		bad := clone(good)
		mutate(&bad[0].Turns[0])
		require.Error(t, checkReplayOff(good, bad))
	}
	failed := clone(good)
	failed[0].Turns[0] = replayOutcome{Status: 502, Code: "server_error", ErrorType: "server_error", Retryable: true}
	for _, mutate := range []func(*replayOutcome){func(o *replayOutcome) { o.Status = 400 }, func(o *replayOutcome) { o.Code = "invalid_prompt" }, func(o *replayOutcome) { o.ErrorType = "invalid_request_error" }, func(o *replayOutcome) { o.Retryable = false }, func(o *replayOutcome) { o.RetryHeader = "false" }, func(o *replayOutcome) { o.RetryAfter = "90" }} {
		bad := clone(failed)
		mutate(&bad[0].Turns[0])
		require.Error(t, checkReplayOn(snapshot, "lossless_first", failed, bad))
	}
	require.Error(t, checkReplayOn(snapshot, "lossless_first", good, nil))
	require.Error(t, checkReplayOff(good, nil))
	improved := clone(good)
	improved[0].Turns[0].Survived = nil
	failed[0].Turns[0].Survived = []string{"task"}
	require.NoError(t, checkReplayOn(snapshot, "lossless_first", failed, improved), "recovering a failure takes priority over preserving unusable input")
	// Conservative mode freezes successful behavior, including harmless-looking
	// changes which are permitted by the lossless comparator.
	for _, mutate := range []func(*replayOutcome){
		func(o *replayOutcome) { o.Events[0] = "changed" },
		func(o *replayOutcome) { o.Sends[0].Body = "changed" },
		func(o *replayOutcome) { o.Sends[0].Account = "second" },
		func(o *replayOutcome) { o.Learning = []string{"new"} },
		func(o *replayOutcome) { o.Bindings = []replayBinding{{"digest", "second"}} },
		func(o *replayOutcome) { o.Avoidance = []replayBinding{{"digest", "first"}} },
		func(o *replayOutcome) { o.Sends = append(o.Sends, o.Sends[0]) },
	} {
		bad := clone(good)
		mutate(&bad[0].Turns[0])
		require.Error(t, checkReplayOn(snapshot, "ladder", good, bad))
	}
	failed[0].Turns[0].IdleLimitMillis = 60000
	require.NoError(t, checkReplayOn(snapshot, "ladder", failed, failed))
	late := clone(failed)
	late[0].Turns[0].MaxIdleMillis = 60000
	require.Error(t, checkReplayOn(snapshot, "ladder", failed, late))
	improved[0].Turns[0].IdleLimitMillis = 60000
	require.NoError(t, checkReplayOn(snapshot, "ladder", failed, improved))
	wrongError := clone(failed)
	wrongError[0].Turns[0].Retryable = false
	require.Error(t, checkReplayOn(snapshot, "ladder", failed, wrongError))
	require.Error(t, checkReplayOn(snapshot, "unknown", good, good))
	failed[0].Turns[0].TerminalMillis = 0
	for _, retryable := range []bool{true, false} {
		base := clone(failed)
		base[0].Turns[0].Retryable = retryable
		base[0].Turns[0].IdleLimitMillis = 120000
		limit := int64(60000)
		if retryable {
			limit = 15000
		}
		late := clone(base)
		late[0].Turns[0].TerminalMillis = limit + 1
		require.Error(t, checkReplayOn(snapshot, "ladder", base, late))
	}
	for _, mutate := range []func(*replayOutcome){
		func(o *replayOutcome) { o.HeadersBeforeTerminal = true },
		func(o *replayOutcome) { o.TerminalCounters = []string{"changed"} },
		func(o *replayOutcome) { o.TerminalEffects = []string{"avoid", "avoid"} },
		func(o *replayOutcome) { o.RetryAdvice = "changed" },
		func(o *replayOutcome) { o.LocalEnding = true },
	} {
		bad := clone(failed)
		mutate(&bad[0].Turns[0])
		require.Error(t, checkReplayOn(snapshot, "ladder", failed, bad))
	}
	queued := clone(failed)
	queued[0].Turns[0].UserWaiters = true
	delayed := clone(queued)
	delayed[0].Turns[0].TerminalMillis = 1
	require.Error(t, checkReplayOn(snapshot, "ladder", queued, delayed))
	named := []replayGolden{{Case: "handled_task_named_again/synthetic", Turns: []replayOutcome{{Sends: []replaySend{{Account: "first", Items: map[string]string{"amsg_task": "cipher", "rs_alpha": "keep"}}, {Account: "first", Items: map[string]string{"amsg_task": "placeholder", "rs_alpha": "keep"}}}}}}}
	require.NoError(t, checkHandledReplay(named))
	named[0].Turns[0].Sends[1].Items["rs_alpha"] = "changed"
	require.Error(t, checkHandledReplay(named))
	three := clone(failed)
	three[0].Turns = append(three[0].Turns, three[0].Turns[0], three[0].Turns[0])
	ending := clone(three)
	last := &ending[0].Turns[2]
	last.Success = true
	last.LocalEnding = true
	last.Status = 200
	last.Code, last.ErrorType, last.Retryable, last.TerminalCount = "", "", false, 1
	last.AcceptedMillis = 0
	require.NoError(t, checkReplayOn(snapshot, "ladder", three, ending), "two prior matching delivery failures permit a local ending")
	three[0].Turns[0].Delivery = "different"
	require.Error(t, checkReplayOn(snapshot, "ladder", three, ending), "other deliveries do not satisfy the retry allowance")
}

var replayForbidden = []*regexp.Regexp{
	regexp.MustCompile(`(?i)[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}`),
	regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`),
	regexp.MustCompile(`(?i)(?:^|["\s])(?:[a-z]:[\\/]|/|\\\\)`),
	regexp.MustCompile(`(?i)"[^"]*(?:installation|workspace)[^"]*"\s*:`),
}

func validateReplayFixture(raw []byte) error {
	for i, pattern := range replayForbidden {
		if pattern.Match(raw) {
			return fmt.Errorf("forbidden fixture class %d", i)
		}
	}
	return nil
}

func TestCollaborationReplayFixtureHygiene(t *testing.T) {
	raw, err := json.Marshal(collaborationReplayCorpus())
	require.NoError(t, err)
	require.NoError(t, validateReplayFixture(raw))
	for _, c := range collaborationReplayCorpus() {
		require.NoError(t, validateReplayFixture(replayRequestBody(t, c)), c.Name)
	}
	golden, err := readReplayGolden()
	require.NoError(t, err)
	require.NoError(t, validateReplayFixture(golden))
	// Construct forbidden examples so even this source contains no captured IDs.
	for _, value := range []string{"/" + "private/fixture/file", "/" + "fixture", "/", "C:" + `\fixture\file`, "person" + "@" + "example.test", strings.Repeat("a", 8) + "-" + strings.Repeat("b", 4) + "-" + strings.Repeat("c", 4) + "-" + strings.Repeat("d", 4) + "-" + strings.Repeat("e", 12)} {
		raw, err := json.Marshal(map[string]string{"value": value})
		require.NoError(t, err)
		require.Error(t, validateReplayFixture(raw))
	}
	for _, key := range []string{"installation" + "_id", "workspace" + "_name"} {
		raw, err := json.Marshal(map[string]string{key: "synthetic"})
		require.NoError(t, err)
		require.Error(t, validateReplayFixture(raw))
	}
}

func TestCollaborationReplayReasoningTradeoff(t *testing.T) {
	name := "task_then_reasoning_elsewhere/pass_false/stream_false/attempts_2"
	old := replayOutcome{Status: 200, Success: true, Survived: []string{"reasoning", "reasoning_summary", "question"}}
	next := replayOutcome{Status: 200, Success: true, Survived: []string{"task", "reasoning_summary", "question"}}
	check := func(before, after replayOutcome) error {
		return checkReplayOn(resilience.Defaults(), "lossless_first", []replayGolden{{Case: name, Turns: []replayOutcome{before}}}, []replayGolden{{Case: name, Turns: []replayOutcome{after}}})
	}
	require.NoError(t, check(old, next))
	t.Run("no protected gain", func(t *testing.T) {
		bad := next
		bad.Survived = []string{"reasoning_summary", "question"}
		require.Error(t, check(old, bad))
	})
	t.Run("protected loss", func(t *testing.T) {
		before := old
		before.Survived = append([]string{"task"}, old.Survived...)
		bad := next
		bad.Survived = []string{"reasoning_summary", "question"}
		require.Error(t, check(before, bad))
	})
	t.Run("summary loss", func(t *testing.T) {
		bad := next
		bad.Survived = []string{"task", "question"}
		require.Error(t, check(old, bad))
	})
	t.Run("new failure", func(t *testing.T) { bad := next; bad.Success = false; require.Error(t, check(old, bad)) })
	t.Run("ordinary content loss", func(t *testing.T) {
		before := old
		before.Survived = append([]string{"ordinary"}, old.Survived...)
		require.Error(t, check(before, next))
	})
	t.Run("unidentified task", func(t *testing.T) { name = "synthetic unknown"; require.Error(t, check(old, next)) })
}

// The only permitted exchange loses reasoning ciphertext, retains its readable
// summary and gains protected input that the successful legacy path replaced.
func replayReasoningTradeoff(name string, old, next replayOutcome) bool {
	if !old.Success || !next.Success {
		return false
	}
	summaries := map[string]string{"reasoning": "reasoning_summary", "reasoning_second": "reasoning_summary_second", "reasoning_third": "reasoning_summary_third"}
	lost := false
	for _, item := range old.Survived {
		if slices.Contains(next.Survived, item) {
			continue
		}
		summary, ok := summaries[item]
		if !ok || !slices.Contains(next.Survived, summary) {
			return false
		}
		lost = true
	}
	if !lost {
		return false
	}
	for _, fixture := range collaborationReplayCorpus() {
		if fixture.Name != name {
			continue
		}
		raw, err := json.Marshal(map[string]any{"input": fixture.Items})
		if err != nil {
			return false
		}
		parsed := service.ParseCollabBody(raw)
		if parsed.Shape.Ambiguous {
			return false
		}
		for _, item := range parsed.Items {
			if item.Class != resilience.CollabItemClassProtected || item.Family != resilience.CollabFamilyAgentMessage && item.Family != resilience.CollabFamilyToolOutput {
				continue
			}
			for _, content := range replaySurvived([]byte("{\"input\":[" + string(fixture.Items[item.Index]) + "]}")) {
				if content == "task" && !slices.Contains(old.Survived, content) && slices.Contains(next.Survived, content) {
					return true
				}
			}
		}
	}
	return false
}

func replayDisclosedExecutableCase(name string) bool {
	switch name {
	case "target_executable_then_failure/pass_false/stream_true/attempts_2",
		"target_executable_then_failure/pass_false/stream_true/attempts_4",
		"target_executable_then_failure/pass_false/stream_true/attempts_0":
		return true
	}
	return false
}

// Match the entire disclosed outcome, including actual output-frame digests.
// A successful residual, missing executable output, or any other difference
// requires an explicit revision of this three-case contract.
func checkReplayExecutableResidual(old, next replayOutcome) error {
	if !old.Success || len(old.Sends) != 2 || old.Sends[0].Account != "first" || old.Sends[0].Rule != "named" || old.Sends[1].Account != "first" || old.Sends[1].Rule != "success" || len(old.Bindings) == 0 || old.Bindings[0].Account != "first" {
		return fmt.Errorf("disclosed residual baseline changed")
	}
	call := replayFrame("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_fixture","call_id":"call_fixture","name":"synthetic_action","arguments":"{}"}}`)
	failure := replayFrame("response.failed", `{"type":"response.failed","response":{"id":"resp_fixture","status":"failed","error":{"type":"server_error","code":"server_error","message":"synthetic failure after call"}}}`)
	expected := old
	expected.Success = false
	expected.Code = "server_error"
	expected.ErrorType = "server_error"
	expected.Retryable = true
	original := old.Sends[0]
	target := original
	target.Account = "second"
	target.Rule = "executable_failure"
	target.Optional = true
	target.Action = resilience.CollabActionSwitchOriginal
	expected.Sends = []replaySend{original, target}
	expected.SendsPerAccount = map[string]int{"first": 1, "second": 1}
	expected.Events = []string{replayDigest([]byte(call)), replayDigest([]byte(failure))}
	expected.Survived = original.Survived
	// In this fixed corpus the sorted first binding is the ingress session;
	// the successful legacy response's later response-ID bindings are absent.
	expected.Bindings = old.Bindings[:1]
	expected.UsageAccounts = []string{"second"}
	expected.Learning = []string{}
	if !reflect.DeepEqual(expected, next) {
		return fmt.Errorf("disclosed failure after executable output changed")
	}
	return nil
}

func checkReplayDelivery(turn replayOutcome) error {
	hasOptional := false
	for _, send := range turn.Sends {
		hasOptional = hasOptional || send.Optional
	}
	if !hasOptional {
		return nil
	}
	delivered := -1
	if turn.Success || turn.AcceptedMillis >= 0 {
		delivered = len(turn.Sends) - 1
	}
	if !slices.Equal(turn.Survived, replayDeliveredContent(turn.Sends, delivered)) {
		return fmt.Errorf("surviving content does not belong to the delivered response")
	}
	return nil
}
