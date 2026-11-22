//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/config"
	"io"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

func collabRegressionVerified(id string) resilience.CollabEvidence {
	e := resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeVerified}
	if id != "" {
		e.Named = &resilience.CollabNamedItem{Identity: id}
	}
	return e
}
func collabRegressionSelect(_ context.Context, preferred int64, exclude map[int64]bool) (int64, func()) {
	if preferred > 0 && !exclude[preferred] {
		return preferred, func() {}
	}
	for _, id := range []int64{1, 2, 3} {
		if !exclude[id] {
			return id, func() {}
		}
	}
	return 0, nil
}
func TestCollabRegressionController(t *testing.T) {
	body := ParseCollabBody([]byte(collabBodyFixture))
	t.Run("failed_probe_is_discarded", func(t *testing.T) {
		l := CollabLadder{Body: body, First: 1, Evidence: collabRegressionVerified(""), MaxAttempts: 4, Select: collabRegressionSelect}
		sends := 0
		l.Send = func(_ context.Context, id int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
			sends++
			t.Logf("send=%d account=%d action=%s reasoning=%t", sends, id, a, gjson.GetBytes(b, "input.0.encrypted_content").Exists())
			if a == resilience.CollabActionNamedRepair {
				if !gjson.GetBytes(b, "input.0.encrypted_content").Exists() {
					t.Error("failed probe was retained in later named task repair; reasoning must be restored")
				}
				return resilience.CollabEvidence{}, true
			}
			return collabRegressionVerified("amsg_a"), false
		}
		if !l.Run(context.Background()) {
			t.Error("expected final named repair")
		}
	})
	t.Run("unnamed_after_switch_returns_to_unnamed_order", func(t *testing.T) {
		l := CollabLadder{Body: body, First: 1, Evidence: collabRegressionVerified("amsg_a"), MaxAttempts: 4, Select: collabRegressionSelect}
		sends := 0
		l.Send = func(_ context.Context, id int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
			sends++
			t.Logf("send=%d account=%d action=%s", sends, id, a)
			if sends == 1 {
				return collabRegressionVerified(""), false
			}
			if a != resilience.CollabActionProbeReasoning || id != 2 {
				t.Errorf("got %s on %d, want reasoning probe on current account 2", a, id)
			}
			if !gjson.GetBytes(b, "input.2.content.1.encrypted_content").Exists() {
				t.Error("unnamed error caused premature protected task replacement")
			}
			return resilience.CollabEvidence{}, true
		}
		l.Run(context.Background())
	})
	t.Run("source_conflict_only_replaces_replaceable_agent", func(t *testing.T) {
		raw := `{"input":[{"type":"agent_message","id":"amsg_old","content":[{"type":"input_text","text":"Message Type: MESSAGE\nTask name: child\nSender: parent\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"old"}]},{"type":"message","role":"assistant","content":"done"},{"type":"reasoning","id":"rs_a","encrypted_content":"r","summary":[]},{"type":"compaction","id":"cmp_a","encrypted_content":"c"},{"type":"message","role":"user","id":"msg_a","content":[{"type":"encrypted_content","encrypted_content":"unread"}]}]}`
		p := ParseCollabBody([]byte(raw))
		e := collabRegressionVerified("")
		e.Family = resilience.CollabFamilyOwnership
		e.Ownership = resilience.CollabOwnershipClassSourceConflict
		l := CollabLadder{Body: p, First: 1, Evidence: e, MaxAttempts: 4, Select: collabRegressionSelect}
		l.Send = func(_ context.Context, id int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
			t.Logf("account=%d action=%s body=%s", id, a, b)
			for _, i := range []int{1, 2, 3, 4} {
				path := fmt.Sprintf("input.%d", i)
				if gjson.Get(raw, path).Raw != gjson.GetBytes(b, path).Raw {
					t.Errorf("source-conflict first step changed independent item %d", i)
				}
			}
			return resilience.CollabEvidence{}, true
		}
		l.Run(context.Background())
	})
	t.Run("named_repairs_do_not_consume_optional_quota", func(t *testing.T) {
		raw := `{"input":[{"type":"reasoning","id":"rs_a","encrypted_content":"r"},{"type":"agent_message","id":"amsg_a","content":[{"type":"encrypted_content","encrypted_content":"a"}]},{"type":"message","role":"assistant","content":"done"},{"type":"compaction","id":"cmp_a","encrypted_content":"c"},{"type":"message","id":"msg_a","role":"user","content":[{"type":"encrypted_content","encrypted_content":"m"}]},{"type":"agent_message","id":"amsg_protected","content":[{"type":"encrypted_content","encrypted_content":"task"}]}]}`
		l := CollabLadder{Body: ParseCollabBody([]byte(raw)), First: 1, Evidence: collabRegressionVerified("rs_a"), MaxAttempts: 4, Select: collabRegressionSelect}
		seq := []string{"amsg_a", "cmp_a", "cmp_a", "msg_a"}
		n := 0
		l.Send = func(_ context.Context, id int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
			t.Logf("send=%d account=%d action=%s", n+1, id, a)
			if n == 3 {
				if a != resilience.CollabActionNamedRepair {
					t.Errorf("fourth send is %s; only one optional switch used, want named compaction repair", a)
				}
				if !gjson.GetBytes(b, "input.4.content.0.encrypted_content").Exists() {
					t.Error("named repair prematurely replaced protected task")
				}
				return resilience.CollabEvidence{}, true
			}
			e := collabRegressionVerified(seq[n])
			n++
			return e, false
		}
		l.Run(context.Background())
	})
	t.Run("third_account_all_placeholder_quota", func(t *testing.T) {
		l := CollabLadder{Body: body, First: 1, Evidence: collabRegressionVerified(""), MaxAttempts: 1, Select: collabRegressionSelect}
		n := 0
		l.Send = func(_ context.Context, id int64, _ []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
			n++
			t.Logf("account=%d action=%s", id, a)
			return collabRegressionVerified(""), id == 3
		}
		if !l.Run(context.Background()) {
			t.Errorf("third eligible account can accept, stopped after %d account-specific placeholder attempts", n)
		}
	})
}

func TestCollabRegressionBody(t *testing.T) {
	t.Run("anonymous_index_is_locatable", func(t *testing.T) {
		raw := `{"input":[{"type":"message","role":"user","content":[{"type":"encrypted_content","encrypted_content":"unread"}]}]}`
		p := ParseCollabBody([]byte(raw))
		o := NewCollabSendObserver(p)
		o.record(CollabRejection{Status: 400, Payload: []byte(`{"error":{"code":"invalid_encrypted_content","param":"input[0].content[0]"}}`)}, 0)
		if !o.Rescuable() {
			t.Error("unambiguous numeric locator with no id is rejected")
		}
	})
	t.Run("malformed_envelope_protected", func(t *testing.T) {
		raw := `{"input":[{"type":"agent_message","id":"amsg_a","content":[{"type":"input_text","text":"Message Type: MESSAGE\nTask name: child\nSender: first\nSender: second\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"cipher"}]}]}`
		p := ParseCollabBody([]byte(raw))
		t.Logf("class=%s", p.Items[0].Class)
		if p.Items[0].Class != resilience.CollabItemClassProtected {
			t.Error("duplicate Sender must make envelope unknown/protected")
		}
	})
	t.Run("function_call_is_model_output_boundary", func(t *testing.T) {
		raw := `{"input":[{"type":"function_call_output","id":"fco_a","call_id":"call_a","output":[{"type":"encrypted_content","encrypted_content":"tool-result"}]},{"type":"function_call","id":"fc_next","call_id":"call_next","name":"read","arguments":"{}"}]}`
		p := ParseCollabBody([]byte(raw))
		if p.Items[0].Class != resilience.CollabItemClassReplaceable {
			t.Errorf("historical output class=%s, want replaceable", p.Items[0].Class)
		}
	})
	t.Run("old_client_pre_summary_task_protected", func(t *testing.T) {
		raw := `{"input":[{"type":"agent_message","id":"amsg_a","recipient":"child","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nTask name: child\nSender: parent\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"unread-task"}]},{"type":"compaction","id":"cmp_a","encrypted_content":"summary"}]}`
		p := ParseCollabBody([]byte(raw))
		if p.Items[0].Class != resilience.CollabItemClassProtected {
			t.Error("pre-summary latest task classified processed without version, thread identity or consumption evidence")
		}
	})
	t.Run("byte_preservation", func(t *testing.T) {
		raw := []byte("{ \"model\" : \"test\", \"input\" : [ {\"type\":\"reasoning\",\"id\":\"rs_a\",\"encrypted_content\":\"r\", \"summary\" : [ {\"text\":\"keep\\u0020this\"} ]}, { \"type\":\"message\", \"role\":\"user\", \"content\":\"a\\/b\" } ], \"metadata\" : { \"x\" : 1e2 } }")
		p := ParseCollabBody(raw)
		before := append([]byte(nil), raw...)
		next, changed := p.Derive(raw, resilience.CollabActionNamedRepair, "rs_a", false)
		if !changed || !bytes.Equal(raw, before) {
			t.Fatal("invalid derivation or original mutation")
		}
		for _, path := range []string{"input.0.summary", "input.1", "metadata"} {
			if gjson.GetBytes(next, path).Raw != gjson.GetBytes(raw, path).Raw {
				t.Errorf("changed %s", path)
			}
		}
		want := bytes.Replace(raw, []byte(`"encrypted_content":"r",`), nil, 1)
		if !bytes.Equal(next, want) {
			t.Errorf("unrelated bytes changed: %s", next)
		}
	})
}

func TestCollabRegressionCarriers(t *testing.T) {
	payload := `{"error":{"code":"invalid_encrypted_content"}}`
	failed := `{"type":"response.failed","response":{"status":"failed","error":{"code":"invalid_encrypted_content"}}}`
	for _, tc := range []struct {
		name, wire string
		status     int
		stream     bool
	}{
		{"http", payload, 400, false}, {"http_outer_502", payload, 502, false}, {"failed_json", `{"status":"failed","error":{"code":"invalid_encrypted_content"}}`, 200, false},
		{"failed_json_envelope", failed, 200, false}, {"stream", "event: response.failed\ndata: " + failed + "\n\n", 200, true},
		{"stream_final_frame_eof", "event: response.failed\ndata: " + failed + "\n", 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := NewCollabSendObserver(ParseCollabBody([]byte(collabBodyFixture)))
			b := &collabEvidenceBody{ReadCloser: io.NopCloser(strings.NewReader(tc.wire)), observer: o, stream: tc.stream, rejection: CollabRejection{Status: tc.status, Source: resilience.CollabEvidenceSourceAggregatedStream}}
			_, err := io.ReadAll(b)
			if err != nil {
				t.Fatal(err)
			}
			b.Close()
			if !o.Rescuable() {
				t.Errorf("ciphertext rejection missed: count=%d reason=%s", len(o.Rejections()), o.IneligibleReason())
			}
		})
	}
}

func TestCollabRegressionGradeTransition(t *testing.T) {
	for _, startClaimed := range []bool{false, true} {
		t.Run(fmt.Sprintf("start_claimed_%t", startClaimed), func(t *testing.T) {
			p := ParseCollabBody([]byte(collabBodyFixture))
			e := collabRegressionVerified("amsg_a")
			if startClaimed {
				e.Grade = resilience.CollabEvidenceGradeClaimed
				e.Named = nil
			}
			l := CollabLadder{Body: p, First: 1, Evidence: e, MaxAttempts: 4, Select: collabRegressionSelect}
			n := 0
			l.Send = func(_ context.Context, id int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
				n++
				t.Logf("send=%d account=%d action=%s", n, id, a)
				if n == 1 {
					next := collabRegressionVerified("")
					if !startClaimed {
						next.Grade = resilience.CollabEvidenceGradeClaimed
					}
					return next, false
				}
				if !startClaimed && !gjson.GetBytes(b, "input.0.encrypted_content").Exists() {
					t.Error("claimed latest error removed reasoning under stale verified grade")
				}
				if startClaimed && a != resilience.CollabActionProbeReasoning {
					t.Error("verified latest error skipped reasoning probe under stale claimed grade")
				}
				return resilience.CollabEvidence{}, true
			}
			l.Run(context.Background())
		})
	}
}

func TestCollabObserverNilFlag(t *testing.T) {
	var observer *CollabSendObserver
	if observer.RescuableFlag() != nil {
		t.Fatal("nil observer must disarm")
	}
}

func TestCollabRegressionFallbackAfterOrdinaryFailure(t *testing.T) {
	l := CollabLadder{Body: ParseCollabBody([]byte(collabBodyFixture)), First: 1, Evidence: collabRegressionVerified("amsg_a"), Select: collabRegressionSelect}
	var actions []resilience.CollabAction
	l.Send = func(_ context.Context, id int64, body []byte, action resilience.CollabAction) (resilience.CollabEvidence, bool) {
		actions = append(actions, action)
		if len(actions) == 1 {
			return resilience.CollabEvidence{}, false
		}
		if id != 1 || action != resilience.CollabActionReplaceAll {
			t.Fatalf("final disposal: %d %s", id, action)
		}
		return resilience.CollabEvidence{}, true
	}
	if !l.Run(context.Background()) || len(actions) != 2 {
		t.Fatalf("no final placeholder: %v", actions)
	}
}
func TestCollabRegressionNamedCurrentAndAdmission(t *testing.T) {
	for _, available := range []bool{true, false} {
		l := CollabLadder{Body: ParseCollabBody([]byte(collabBodyFixture)), First: 1, Current: 2, Evidence: collabRegressionVerified("rs_a")}
		l.Select = func(ctx context.Context, preferred int64, excluded map[int64]bool) (int64, func()) {
			if preferred == 2 && !available {
				return 0, nil
			}
			return collabRegressionSelect(ctx, preferred, excluded)
		}
		l.Send = func(_ context.Context, id int64, body []byte, action resilience.CollabAction) (resilience.CollabEvidence, bool) {
			want := int64(1)
			if available {
				want = 2
			}
			if id != want {
				t.Fatalf("account=%d want=%d", id, want)
			}
			if !gjson.GetBytes(body, "input.2.content.1.encrypted_content").Exists() {
				t.Fatal("independent task lost")
			}
			return resilience.CollabEvidence{}, true
		}
		if !l.Run(context.Background()) {
			t.Fatal("named repair lost on admission")
		}
	}
}
func TestCollabRegressionFinalDecisionPrecedesTaskReplacement(t *testing.T) {
	l := CollabLadder{Body: ParseCollabBody([]byte(collabBodyFixture)), First: 1, MaxAttempts: 1, Evidence: collabRegressionVerified("amsg_a"), Select: collabRegressionSelect}
	called := false
	l.BeforeDisposal = func(context.Context, resilience.CollabEvidence) bool { called = true; return true }
	l.Send = func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
		t.Fatal("task replaced before local decision")
		return resilience.CollabEvidence{}, false
	}
	if !l.Run(context.Background()) || !called {
		t.Fatal("decision not offered")
	}
}
func TestCollabRegressionWrongLocatorFamily(t *testing.T) {
	p := ParseCollabBody([]byte(collabBodyFixture))
	e := resilience.ClassifyCollabEvidence(resilience.CollabEvidenceInput{Payload: []byte(`{"error":{"code":"invalid_encrypted_content","param":"input[2].encrypted_function_args"}}`)})
	if !p.ResolveEvidence(e).Named.Ambiguous {
		t.Fatal("argument locator guessed an agent content field")
	}
}

func TestCollabRegressionNoSendPreservesSharedQuota(t *testing.T) {
	progress := &CollabLadderProgress{}
	l := CollabLadder{Body: ParseCollabBody([]byte(collabBodyFixture)), First: 1, Evidence: collabRegressionVerified("amsg_a"), Progress: progress, Select: collabRegressionSelect, Sent: func() bool { return false }}
	l.Send = func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
		return resilience.CollabEvidence{}, false
	}
	l.Run(context.Background())
	if progress.optional != 0 || progress.switched || len(progress.replaced) != 0 {
		t.Fatalf("no-send spent quota: %+v", progress)
	}
	l.Sent = nil
	l.Send = func(_ context.Context, id int64, _ []byte, action resilience.CollabAction) (resilience.CollabEvidence, bool) {
		if action != resilience.CollabActionSwitchOriginal || id != 2 {
			t.Fatalf("terminal entry lost switch: %d %s", id, action)
		}
		return resilience.CollabEvidence{}, true
	}
	if !l.Run(context.Background()) {
		t.Fatal("terminal entry did not run")
	}
}
func TestCollabRegressionTaskContext(t *testing.T) {
	raw := []byte(`{"input":[{"type":"agent_message","id":"amsg_task","recipient":"child","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nTask name: child\nSender: parent\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"task"}]},{"type":"compaction","encrypted_content":"summary"}]}`)
	for _, test := range []struct {
		facts CollabBodyContext
		want  resilience.CollabItemClass
	}{
		{CollabBodyContext{Thread: "child", ClientVersion: "0.167.0"}, resilience.CollabItemClassProtected},
		{CollabBodyContext{Thread: "child", ClientVersion: "0.168.0"}, resilience.CollabItemClassReplaceable},
		{CollabBodyContext{Thread: "different", ClientVersion: "0.167.0"}, resilience.CollabItemClassReplaceable},
		{CollabBodyContext{Thread: "child", Consumed: map[string]bool{"amsg_task": true}}, resilience.CollabItemClassReplaceable},
	} {
		if got := ParseCollabBodyWithContext(raw, test.facts).Items[0].Class; got != test.want {
			t.Errorf("%+v: %s want %s", test.facts, got, test.want)
		}
	}
}

func TestCollabRegressionBodyFacts(t *testing.T) {
	body := []byte(`{"input":[{"type":"agent_message","id":"amsg_current","recipient":"child","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nTask name: child\nSender: parent\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"opaque"}]},{"type":"compaction","encrypted_content":"summary"}]}`)
	for _, tc := range []struct {
		facts     CollabBodyContext
		protected bool
	}{
		{CollabBodyContext{Thread: "child", ClientVersion: "0.167.0"}, true},
		{CollabBodyContext{Thread: "child", ClientVersion: "0.168.0"}, false},
		{CollabBodyContext{Thread: "parent", ClientVersion: "0.167.0"}, false},
		{CollabBodyContext{Thread: "child", Consumed: map[string]bool{"amsg_current": true}}, false},
	} {
		p := ParseCollabBodyWithContext(body, tc.facts)
		if got := p.Items[0].Class == resilience.CollabItemClassProtected; got != tc.protected {
			t.Fatalf("facts=%+v protected=%v", tc.facts, got)
		}
	}
}
func TestCollabRegressionDigestFraming(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "synthetic-secret"}}}
	for _, body := range [][]byte{nil, []byte("a\x00b"), bytes.Repeat([]byte("body"), 1<<18)} {
		if svc.collabBodyDigest(body) != svc.CollabDigest(resilience.CollabDigestDomainBody, string(body)) {
			t.Fatal("digest framing changed")
		}
	}
}
