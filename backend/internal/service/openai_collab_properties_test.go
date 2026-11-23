//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func collabPropertyRuns() int {
	n, _ := strconv.Atoi(os.Getenv("COLLAB_PROPERTY_RUNS"))
	if n < 1 {
		return 500
	}
	return n
}

func TestCollabPropertyReferences(t *testing.T) {
	t.Run("identity_dimensions", collabPropertyIdentityChannels)
	t.Run("all_channels", collabPropertyReferenceChannels)
	t.Run("exact_table", collabPropertyExactReferences)
	raw := strings.TrimSuffix(collabBatchBody, "]}") + `,{"type":"compaction","id":"cmp_a","encrypted_content":"c"},{"type":"function_call_output","id":"fco_a","call_id":"call_a","output":[{"type":"encrypted_content","encrypted_content":"o"}]},{"type":"function_call","id":"fc_a","encrypted_function_args":["a"],"arguments":"{}"}]}`
	p := ParseCollabBody([]byte(raw))
	valid := map[string]string{}
	var seeds []string
	for _, item := range p.Items {
		for _, quote := range []string{"", "'", "\"", "`"} {
			token := quote + item.Identity + quote
			valid["id:"+token] = item.Identity
			seeds = append(seeds, "id:"+token)
		}
		for _, path := range append([]string{"input." + strconv.Itoa(item.Index)}, item.Paths...) {
			valid["path:"+path] = item.Identity
			seeds = append(seeds, "path:"+path)
			bracket := strings.Replace(path, "input."+strconv.Itoa(item.Index), fmt.Sprintf("input[%d]", item.Index), 1)
			bracket = strings.ReplaceAll(bracket, "content.1", "content[1]")
			valid["path:"+bracket] = item.Identity
			seeds = append(seeds, "path:"+bracket)
		}
	}
	// The original adversarial probes are fixed seeds, alongside generated edits.
	seeds = append(seeds, "id:'amsg_task.missing'", "id:'amsg_task/missing'", "id:'amsg_task:missing'", "id:'rs_a.missing'",
		"path:input[-1].arguments", "path:input[bad].arguments", "path:input.foo.arguments", "path:input[2].arguments",
		"path:input[2]].content[1]", "path:input.2.content[1", "path:input[2][bad]", "path:input[2][-1]", "path:input[2.5]", "path:input [bad].content",
		"path:input[2].content[1] trailing", "path:input[2].content[1].missing", "id:'fc_task'", "id:'amsg_task'_missing")
	alphabet := []byte("abc012_./:'\"`[] -")
	for _, seed := range []int64{3, 17, 101} {
		rng := rand.New(rand.NewSource(seed))
		candidates := append([]string(nil), seeds...)
		for i := 0; i < collabPropertyRuns(); i++ {
			base := seeds[rng.Intn(len(seeds))]
			parts := strings.SplitN(base, ":", 2)
			token := parts[1]
			at := rng.Intn(len(token) + 1)
			ch := string(alphabet[rng.Intn(len(alphabet))])
			switch rng.Intn(4) {
			case 0:
				token = token[:at] + ch + token[at:]
			case 1:
				if at < len(token) {
					token = token[:at] + token[at+1:]
				}
			case 2:
				if at < len(token) {
					token = token[:at] + token[at:at+1] + token[at:]
				}
			case 3:
				if at < len(token) {
					token = token[:at] + ch + token[at+1:]
				}
			}
			candidates = append(candidates, parts[0]+":"+token)
		}
		for _, candidate := range candidates {
			parts := strings.SplitN(candidate, ":", 2)
			for _, channel := range []string{"param", "message"} {
				fields := map[string]string{"code": "invalid_encrypted_content", "message": "Encrypted content could not be decrypted."}
				if parts[0] == "id" {
					fields["message"] = "Encrypted content hydration failed for item " + parts[1] + "."
				} else if channel == "param" {
					fields["param"] = parts[1]
				} else {
					fields["message"] = "Encrypted content could not be decrypted at " + parts[1] + "."
				}
				payload, _ := json.Marshal(map[string]any{"error": fields})
				e := p.ResolveEvidence((CollabRejection{Payload: payload, Status: 400}).Evidence(p.Shape))
				want := valid[parts[0]+":"+strings.TrimSpace(parts[1])]
				// Prose ends an unquoted reference at whitespace; param is a whole field.
				if channel == "message" || parts[0] == "id" {
					if fields := strings.Fields(parts[1]); len(fields) > 0 {
						want = valid[parts[0]+":"+fields[0]]
					}
				}
				if e.Named != nil && e.Named.Located {
					require.Equal(t, want, e.Named.Identity, "seed=%d candidate=%q channel=%s", seed, candidate, channel)
					require.NotEmpty(t, want, "mutation resolved by prefix: %q", candidate)
				}
				// Invalid references cannot turn into an unnamed broad edit in any branch.
				if want == "" {
					sends := 0
					l := CollabLadder{Body: p, First: 1, Evidence: e, MaxAttempts: 1, Select: collabBatchSelect, Send: func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
						sends++
						return resilience.CollabEvidence{}, true
					}}
					l.Run(context.Background())
					require.Zero(t, sends, "seed=%d candidate=%q channel=%s named=%+v", seed, candidate, channel, e.Named)
				}
			}
		}
	}
	// Whole identity and expected family, not just a matching index or prefix.
	duplicate := p
	duplicate.Items = append(append([]CollabBodyItem(nil), p.Items...), p.Items[0])
	require.True(t, duplicate.ResolveEvidence(collabBatchEvidence("rs_a")).Named.Ambiguous)
	wrong := collabBatchEvidence("amsg_task")
	wrong.Named.Family = resilience.CollabFamilyCallArgs
	require.True(t, p.ResolveEvidence(wrong).Named.Ambiguous)
	items := gjson.GetBytes(p.Original, "input").Array()
	reordered, _ := sjson.SetRawBytes(p.Original, "input.0", []byte(items[1].Raw))
	reordered, _ = sjson.SetRawBytes(reordered, "input.1", []byte(items[0].Raw))
	r := collabObserveDerivedRejection(p, reordered, `{"error":{"code":"invalid_encrypted_content","param":"input[0]"}}`)
	require.Equal(t, "rs_b", p.ResolveEvidence(r.Evidence(p.Shape)).Named.Identity)

}

func TestCollabPropertyPermissions(t *testing.T) {
	t.Run("identity_dimensions", collabPropertyDisposedEndpoints)
	t.Run("controller_endpoints", collabPropertyControllerEndpoints)
	t.Run("changing_evidence", collabPropertyChangingPermissions)
	t.Run("named_endpoints", collabPropertyNamedEndpoints)
	t.Run("named_isolation", TestCollabExactNamedRepairIsolation)
	bodies := []string{
		`{"input":[` + collabBatchTask + `]}`,
		`{"input":[{"type":"reasoning","id":"rs_a","encrypted_content":"r"}]}`,
		`{"input":[{"type":"compaction","id":"cmp_a","encrypted_content":"c"}]}`,
		`{"input":[{"type":"function_call","id":"fc_a","encrypted_function_args":["a"],"arguments":"{}"}]}`,
		`{"input":[{"type":"function_call_output","id":"fco_a","call_id":"call_a","output":[{"type":"encrypted_content","encrypted_content":"o"}]}]}`,
	}
	for bi, raw := range bodies {
		for _, class := range []resilience.CollabItemClass{resilience.CollabItemClassProtected, resilience.CollabItemClassReplaceable, resilience.CollabItemClassUnreadMessage} {
			for _, grade := range []resilience.CollabEvidenceGrade{resilience.CollabEvidenceGradeUnclaimed, resilience.CollabEvidenceGradeClaimed, resilience.CollabEvidenceGradeVerified} {
				for _, owner := range []resilience.CollabOwnershipClass{resilience.CollabOwnershipClassNone, resilience.CollabOwnershipClassUnknownSuffix, resilience.CollabOwnershipClassRecognized, resilience.CollabOwnershipClassSourceConflict, resilience.CollabOwnershipClassCapacity, resilience.CollabOwnershipClassContinuation} {
					for _, name := range []string{"absent", "exact", "invalid"} {
						p := ParseCollabBody([]byte(raw))
						require.NotEmpty(t, p.Items)
						p.Items[0].Class = class
						e := resilience.CollabEvidence{Grade: grade, Ownership: owner}
						if owner != resilience.CollabOwnershipClassNone {
							e.Family = resilience.CollabFamilyOwnership
						}
						if name != "absent" {
							e.Named = &resilience.CollabNamedItem{Identity: p.Items[0].Identity}
							if name == "invalid" {
								e.Named.Identity += ".missing"
							}
						}
						blocked := grade == resilience.CollabEvidenceGradeUnclaimed || name == "invalid" || owner == resilience.CollabOwnershipClassCapacity || owner == resilience.CollabOwnershipClassContinuation
						claimed := grade == resilience.CollabEvidenceGradeClaimed
						blocked = blocked || claimed && !p.Shape.AgentCiphertext && !p.Shape.ToolOutputCiphertext
						switchOnly := !claimed && (owner == resilience.CollabOwnershipClassUnknownSuffix || owner != resilience.CollabOwnershipClassNone && !p.Shape.AgentCiphertext)
						local := !blocked && !claimed && !switchOnly
						label := fmt.Sprintf("body=%d class=%s grade=%s owner=%s name=%s", bi, class, grade, owner, name)
						require.Equal(t, local, p.LadderPolicy(e, CollabLadderFinalDisposal).LocalEnding, label)
						for _, action := range []resilience.CollabAction{resilience.CollabActionNamedRepair, resilience.CollabActionProbeReasoning, resilience.CollabActionDropUnprotected, resilience.CollabActionReplaceAll} {
							family := p.Items[0].Family
							bodyItem := family == resilience.CollabFamilyAgentMessage || family == resilience.CollabFamilyToolOutput
							want := !blocked && !switchOnly && family != resilience.CollabFamilyCallArgs
							switch action {
							case resilience.CollabActionNamedRepair:
								want = want && !claimed
							case resilience.CollabActionProbeReasoning:
								want = want && !claimed && family == resilience.CollabFamilyReasoning
							case resilience.CollabActionDropUnprotected:
								want = want && (family == resilience.CollabFamilyReasoning || bodyItem && class != resilience.CollabItemClassProtected)
								if owner == resilience.CollabOwnershipClassSourceConflict && !claimed {
									want = want && family == resilience.CollabFamilyAgentMessage && class == resilience.CollabItemClassReplaceable
								}
							}
							if claimed {
								want = want && bodyItem
							}
							_, changed := p.LadderPolicy(e).derivation(p, action).Derive(p.Original, action, p.Items[0].Identity, false)
							require.Equal(t, want, changed, "action=%s %s", action, label)
						}
						sends, hooks := 0, 0
						l := CollabLadder{Body: p, First: 1, Evidence: e, MaxAttempts: 4, Select: collabBatchSelect, BeforeDisposal: func(context.Context, resilience.CollabEvidence) bool { hooks++; return false }}
						l.Send = func(_ context.Context, _ int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
							sends++
							require.Less(t, sends, 12, label)
							if blocked {
								t.Fatalf("blocked action %s: %s", a, label)
							}
							if switchOnly {
								require.Equal(t, resilience.CollabActionSwitchOriginal, a, label)
								require.JSONEq(t, raw, string(b), label)
							}
							if claimed || p.Items[0].Family == resilience.CollabFamilyCallArgs {
								if !p.Shape.AgentCiphertext && !p.Shape.ToolOutputCiphertext {
									require.JSONEq(t, raw, string(b), label)
								}
							}
							return e, false
						}
						l.Run(context.Background())
						if blocked {
							require.Zero(t, sends, label)
						}
						if !local {
							require.Zero(t, hooks, label)
						}
					}
				}
			}
		}
	}
}

func TestCollabPropertyRequestState(t *testing.T) {
	t.Run("identity_dimensions", collabPropertyRepairIdentityDimensions)
	t.Run("original_repair_model", collabPropertyOriginalRepairModel)
	t.Run("anonymous_protocol_setter", TestCollabAnonymousProtocolSetter)
	t.Run("anonymous_occurrences", TestCollabAnonymousOccurrenceRepairs)
	t.Run("operations", collabPropertyRepairOperations)
	t.Run("nested_item", TestCollabFenceToolItemRepairSurvivesAccountChange)
	t.Run("anonymous_protocol", TestCollabAnonymousProtocolRepairIdentity)
	t.Run("nested_array", TestCollabExactNestedRepairs)
	for _, seed := range []int64{5, 23, 107} {
		rng := rand.New(rand.NewSource(seed))
		for iteration := 0; iteration < collabPropertyRuns(); iteration++ {
			raw := []byte(`{"top_p":0.4,"truncation":"auto","tools":[{"type":"function","name":"bad"}],"input":[{"type":"function_call_output","id":"fco_plain","call_id":"call_plain","output":"text"},` + strings.TrimSuffix(strings.TrimPrefix(collabBatchBody, `{"input":[`), `]}`) + `]}`)
			for i := 1; i < 4; i++ {
				raw, _ = sjson.SetRawBytes(raw, "input.-1", []byte(strings.ReplaceAll(collabBatchTask, "amsg_task", fmt.Sprintf("amsg_task_%d", i))))
			}
			p := ParseCollabBody(raw)
			progress := &CollabLadderProgress{}
			deleted := map[string]bool{}
			fields := []string{"top_p", "truncation", "tools", "input.0"}
			rng.Shuffle(len(fields), func(i, j int) { fields[i], fields[j] = fields[j], fields[i] })
			for step, field := range fields {
				account := int64(1 + rng.Intn(3))
				fixed := raw
				// Each account may begin with stale original bytes. Confirmed edits must
				// accumulate instead of being reset by that account's next rejection.
				fixed, _ = sjson.DeleteBytes(fixed, field)
				deleted[field] = true
				r := collabObserveDerivedRejection(p, fixed, `{"error":{"code":"invalid_encrypted_content","item_id":"amsg_task"}}`)
				r.AccountID = account
				progress.RememberRejection(r)
				named := "amsg_task"
				if step > 0 {
					named = fmt.Sprintf("amsg_task_%d", step)
				}
				l := CollabLadder{Body: p, First: account, Current: account, Evidence: collabBatchEvidence(named), MaxAttempts: 1, Progress: progress}
				target := int64(1 + (int(account) % 3))
				l.Select = func(context.Context, int64, map[int64]bool) (int64, func()) { return target, func() {} }
				sent := false
				l.Send = func(_ context.Context, _ int64, b []byte, _ resilience.CollabAction) (resilience.CollabEvidence, bool) {
					sent = true
					for removed := range deleted {
						if removed == "input.0" {
							for _, item := range gjson.GetBytes(b, "input").Array() {
								require.NotEqual(t, "fco_plain", item.Get("id").String(), "seed=%d iteration=%d step=%d", seed, iteration, step)
							}
						} else {
							require.False(t, gjson.GetBytes(b, removed).Exists(), "seed=%d iteration=%d step=%d restored=%s", seed, iteration, step, removed)
						}
					}
					return resilience.CollabEvidence{}, true
				}
				l.Run(context.Background())
				require.True(t, sent)
			}
			// A first named repair on any account spends the class once for all three.
			p = ParseCollabBody([]byte(collabBatchBody))
			names := []string{"rs_a", "rs_b"}
			if iteration%2 == 1 {
				p = ParseCollabBody([]byte(`{"input":[{"type":"agent_message","id":"amsg_old","content":[{"type":"input_text","text":"Message Type: MESSAGE\nTask name: child\nSender: parent\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"old"}]},{"type":"function_call_output","id":"fco_old","call_id":"call_old","output":[{"type":"encrypted_content","encrypted_content":"old"}]},{"type":"reasoning","id":"rs_a","encrypted_content":"r"},` + collabBatchTask + `]}`))
				names = []string{"amsg_old", "fco_old"}
			}
			progress = &CollabLadderProgress{}
			for index, id := range names {
				account := int64(1 + rng.Intn(3))
				l := CollabLadder{Body: p, First: account, Current: account, Evidence: collabBatchEvidence(id), MaxAttempts: 4, Progress: progress, Select: func(context.Context, int64, map[int64]bool) (int64, func()) { return account, func() {} }}
				l.Send = func(_ context.Context, _ int64, b []byte, _ resilience.CollabAction) (resilience.CollabEvidence, bool) {
					if index == 1 {
						require.False(t, gjson.GetBytes(b, "input.0.encrypted_content").Exists(), "seed=%d iteration=%d account=%d renewed named allowance", seed, iteration, account)
						if iteration%2 == 1 {
							require.False(t, gjson.GetBytes(b, "input.0.content.1.encrypted_content").Exists(), "second replaceable class must batch across accounts")
							require.False(t, gjson.GetBytes(b, "input.2.encrypted_content").Exists(), "second replaceable class must use drop-unprotected")
						}
					}
					return resilience.CollabEvidence{}, true
				}
				require.True(t, l.Run(context.Background()))
			}
		}
	}
}
