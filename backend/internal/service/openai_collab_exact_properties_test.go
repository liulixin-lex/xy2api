//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func collabPropertyExactReferences(t *testing.T) {
	anonymous := ParseCollabBody([]byte(`{"input":[{"type":"reasoning","encrypted_content":"r"}]}`))
	for _, fields := range []string{`"item_id":"@input:0"`, `"item_id":"@input:0","param":"input[0]"`} {
		e := anonymous.ResolveEvidence((CollabRejection{Payload: []byte(`{"error":{"code":"invalid_encrypted_content",` + fields + `}}`)}).Evidence(anonymous.Shape))
		require.False(t, e.Named.Located, "internal handles are not wire IDs")
	}
	p := ParseCollabBody([]byte(collabBatchBody))
	// Independent oracle: enumerate JSON nodes without using the production table.
	var root struct {
		Input []map[string]any `json:"input"`
	}
	require.NoError(t, json.Unmarshal(p.Original, &root))
	table := map[string]string{}
	var paths []string
	var visit func(any, string, string, string)
	visit = func(v any, bracket, dot, id string) {
		table[bracket], table[dot] = id, id
		paths = append(paths, bracket, dot)
		switch v := v.(type) {
		case map[string]any:
			for key, child := range v {
				visit(child, bracket+"."+key, dot+"."+key, id)
			}
		case []any:
			for i, child := range v {
				visit(child, fmt.Sprintf("%s[%d]", bracket, i), fmt.Sprintf("%s.%d", dot, i), id)
			}
		}
	}
	for i, item := range root.Input {
		visit(item, fmt.Sprintf("input[%d]", i), fmt.Sprintf("input.%d", i), item["id"].(string))
	}
	sort.Strings(paths)
	for index := range root.Input {
		for _, id := range []string{"rs_a", "rs_b", "amsg_task"} {
			for _, family := range []resilience.CollabFamily{resilience.CollabFamilyOtherEncrypted, resilience.CollabFamilyCallArgs} {
				e := resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeVerified, Named: &resilience.CollabNamedItem{InputIndex: &index, Identity: id, Family: family}}
				got := p.ResolveEvidence(e)
				want := id == root.Input[index]["id"] && family != resilience.CollabFamilyCallArgs
				require.Equal(t, want, got.Named.Located)
			}
		}
	}
	mutate := []func(string) string{
		func(s string) string { return "body." + s }, func(s string) string { return "body/" + s },
		func(s string) string { return s + ".encrypted_content" }, func(s string) string { return s + ".missing" },
		strings.ToUpper, func(s string) string { return " " + s }, func(s string) string { return s + " " },
		func(s string) string { return "'" + s + "'" }, func(s string) string { return s + "/" + s },
		func(s string) string { return s + "," + s }, func(s string) string { return s + " " + s },
	}
	for _, seed := range []int64{3, 17, 101} {
		rng := rand.New(rand.NewSource(seed))
		for n := 0; n < collabPropertyRuns(); n++ {
			path := paths[rng.Intn(len(paths))]
			candidate := mutate[n%len(mutate)](path)
			for _, message := range []bool{false, true} {
				fields := map[string]string{"code": "invalid_encrypted_content"}
				if message {
					fields["message"] = "Encrypted content could not be decrypted: " + candidate + "."
				} else {
					fields["param"] = candidate
				}
				payload, _ := json.Marshal(map[string]any{"error": fields})
				e := p.ResolveEvidence((CollabRejection{Payload: payload}).Evidence(p.Shape))
				// Message quoting belongs to the prose envelope. Structured params must
				// always equal a table entry byte for byte, including whitespace.
				if !message || n%len(mutate) < 5 || n%len(mutate) > 7 {
					if e.Named != nil && e.Named.Located {
						require.NotEmpty(t, table[candidate], "seed=%d reference=%q", seed, candidate)
						require.Equal(t, table[candidate], e.Named.Identity)
					}
				}
			}
			for _, id := range []string{"rs_a", "rs_b", "amsg_task"} {
				for _, wording := range []string{"Encrypted content could not be decrypted.", "Encrypted function arguments could not be decrypted."} {
					fields := map[string]string{"code": "invalid_encrypted_content", "param": path, "item_id": id, "message": wording}
					payload, _ := json.Marshal(map[string]any{"error": fields})
					e := p.ResolveEvidence((CollabRejection{Payload: payload}).Evidence(p.Shape))
					if id != table[path] || strings.Contains(wording, "arguments") {
						require.False(t, e.Named.Located, "seed=%d id=%s path=%s message=%s", seed, id, path, wording)
					} else {
						require.True(t, e.Named.Located)
					}
				}
			}
		}
	}
}

func collabPropertyChangingPermissions(t *testing.T) {
	t.Run("unknown_owner_after_named", TestCollabFencePermissionChangesAfterRejection)
	p := ParseCollabBody([]byte(collabBatchBody))
	for _, seed := range []int64{11, 29, 109} {
		rng := rand.New(rand.NewSource(seed))
		for n := 0; n < collabPropertyRuns(); n++ {
			e := collabBatchEvidence("rs_a")
			progress := &CollabLadderProgress{}
			sends, hooks := 0, 0
			l := CollabLadder{Body: p, First: 1, Current: 1, Evidence: e, MaxAttempts: 4, Progress: progress, Select: collabRegressionSelect}
			l.BeforeDisposal = func(_ context.Context, latest resilience.CollabEvidence) bool {
				hooks++
				require.Equal(t, e.Grade, latest.Grade)
				require.Equal(t, resilience.CollabEvidenceGradeVerified, latest.Grade)
				require.NotEqual(t, resilience.CollabOwnershipClassUnknownSuffix, latest.Ownership)
				require.NotEqual(t, resilience.CollabOwnershipClassCapacity, latest.Ownership)
				return false
			}
			l.Send = func(_ context.Context, _ int64, b []byte, a resilience.CollabAction) (resilience.CollabEvidence, bool) {
				sends++
				require.Less(t, sends, 12)
				switch {
				case e.Grade == resilience.CollabEvidenceGradeUnclaimed:
					require.Equal(t, resilience.CollabActionReplaceAll, a)
				case e.Ownership == resilience.CollabOwnershipClassUnknownSuffix:
					require.Equal(t, resilience.CollabActionSwitchOriginal, a)
				case e.Grade == resilience.CollabEvidenceGradeClaimed:
					require.NotEqual(t, resilience.CollabActionNamedRepair, a)
					require.NotEqual(t, resilience.CollabActionProbeReasoning, a)
				}
				if sends == 1 {
					require.False(t, gjson.GetBytes(b, "input.0.encrypted_content").Exists())
					require.True(t, gjson.GetBytes(b, "input.1.encrypted_content").Exists(), "first named edit touched another item")
				}
				e = collabBatchEvidence([]string{"rs_a", "rs_b", "amsg_task", ""}[rng.Intn(4)])
				switch rng.Intn(6) {
				case 0:
					e.Grade = resilience.CollabEvidenceGradeUnclaimed
				case 1:
					e.Grade = resilience.CollabEvidenceGradeClaimed
				case 2:
					e.Family = resilience.CollabFamilyOwnership
					e.Ownership = resilience.CollabOwnershipClassUnknownSuffix
				case 3:
					e.Family = resilience.CollabFamilyOwnership
					e.Ownership = resilience.CollabOwnershipClassCapacity
				}
				return e, false
			}
			l.Run(context.Background())
			require.False(t, p.LadderPolicy(l.LastEvidence, CollabLadderHandledAgain).LocalEnding)
			require.False(t, p.LadderPolicy(l.LastEvidence, CollabLadderFallback).LocalEnding)
			_ = hooks
		}
	}
}

func collabPropertyRepairOperations(t *testing.T) {
	for _, seed := range []int64{5, 23, 107} {
		rng := rand.New(rand.NewSource(seed))
		for n := 0; n < collabPropertyRuns(); n++ {
			raw := []byte(`{"tools":[{"type":"function","name":"a","bad":true},{"type":"function","name":"b"}],"input":[{"type":"message","role":"user","content":"anonymous a","bad":true},{"type":"message","role":"user","content":"anonymous b"},` + collabBatchTask + `]}`)
			p := ParseCollabBody(raw)
			progress := &CollabLadderProgress{}
			fields := []string{"tools.0.bad", "tools.1", "input.0.bad", "input.1"}
			rng.Shuffle(len(fields), func(i, j int) { fields[i], fields[j] = fields[j], fields[i] })
			removed := map[string]bool{}
			for _, field := range fields {
				fixed, _ := sjson.DeleteBytes(raw, field)
				items := gjson.GetBytes(fixed, "input").Array()
				if len(items) > 1 {
					fixed, _ = sjson.SetRawBytes(fixed, "input.0", []byte(items[1].Raw))
					fixed, _ = sjson.SetRawBytes(fixed, "input.1", []byte(items[0].Raw))
				}
				codes := []string{"invalid_encrypted_content", "invalid_request_error", "server_error", "rate_limit_exceeded", "context_length_exceeded"}
				rejection := collabObserveDerivedRejection(p, fixed, `{"error":{"code":"`+codes[rng.Intn(len(codes))]+`"}}`)
				rejection.AccountID = int64(1 + rng.Intn(3))
				progress.RememberRejection(rejection)
				removed[field] = true
				for account := int64(1); account <= 3; account++ {
					base := progress.derived[account]
					if base == nil {
						base = raw
					}
					got := progress.repairs.apply(p, base)
					for path := range removed {
						switch path {
						case "input.0.bad":
							for _, item := range gjson.GetBytes(got, "input").Array() {
								if item.Get("content").String() == "anonymous a" {
									require.False(t, item.Get("bad").Exists())
								}
							}
						case "input.1":
							for _, item := range gjson.GetBytes(got, "input").Array() {
								require.NotEqual(t, "anonymous b", item.Get("content").String())
							}
						case "tools.0.bad":
							require.False(t, gjson.GetBytes(got, "tools.0.bad").Exists())
						case "tools.1":
							require.Len(t, gjson.GetBytes(got, "tools").Array(), 1)
						}
					}
				}
			}
		}
	}
	t.Run("ordinary_usage", TestCollabExactOrdinaryRejectionLosesState)
}

func collabPropertyNamedEndpoints(t *testing.T) {
	raw := strings.TrimSuffix(collabBatchBody, "]}") + `,{"type":"compaction","id":"cmp_a","encrypted_content":"c"}]}`
	p := ParseCollabBody([]byte(raw))
	for _, id := range []string{"amsg_task", "rs_a", "cmp_a"} {
		for _, alreadyReplaced := range []bool{false, true} {
			for _, grade := range []resilience.CollabEvidenceGrade{resilience.CollabEvidenceGradeVerified, resilience.CollabEvidenceGradeClaimed, resilience.CollabEvidenceGradeUnclaimed} {
				for _, applicable := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/replaced_%t/%s/applicable_%t", id, alreadyReplaced, grade, applicable), func(t *testing.T) {
						evidence := collabBatchEvidence(id)
						evidence.Grade = grade
						progress := &CollabLadderProgress{probed: map[int64]bool{}, replaced: map[int64]bool{1: alreadyReplaced}, handled: map[int64]map[string]bool{1: {id: true}}}
						entries := 0
						l := CollabLadder{Body: p, First: 1, Current: 1, Evidence: evidence, Progress: progress, MaxAttempts: 1, Select: collabBatchSelect}
						l.BeforeDisposal = func(context.Context, resilience.CollabEvidence) bool { entries++; return applicable }
						l.Send = func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
							return evidence, false
						}
						delivered := l.Run(context.Background())
						// The handler owns post-controller local delivery. Exercise its gate too.
						if !delivered && p.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding {
							delivered = l.BeforeDisposal(context.Background(), l.LastEvidence)
						}
						allowed := id == "amsg_task" && grade == resilience.CollabEvidenceGradeVerified
						require.Equal(t, allowed, entries > 0)
						require.Equal(t, allowed && applicable, delivered)
						if grade == resilience.CollabEvidenceGradeVerified && id != "amsg_task" {
							require.Equal(t, CollabLadderHandledAgain, l.StopReason)
						}
					})
				}
			}
		}
	}
	for _, grade := range []resilience.CollabEvidenceGrade{resilience.CollabEvidenceGradeVerified, resilience.CollabEvidenceGradeClaimed, resilience.CollabEvidenceGradeUnclaimed} {
		e := collabBatchEvidence("amsg_task")
		e.Grade = grade
		require.Equal(t, grade == resilience.CollabEvidenceGradeVerified, p.LadderPolicy(e, CollabLadderDeliveryUnavailable).LocalEnding)
		require.False(t, p.LadderPolicy(e, CollabLadderFallback).LocalEnding)
	}
}

func TestCollabRepairOperationsSurviveEvidenceLimit(t *testing.T) {
	raw := []byte(`{"truncation":"auto","input":[` + collabBatchTask + `]}`)
	p := ParseCollabBody(raw)
	observer := NewCollabSendObserver(p)
	for i := 0; i < 12; i++ {
		body := raw
		if i == 8 {
			body, _ = sjson.DeleteBytes(body, "truncation")
		}
		r := collabObserveDerivedRejection(p, body, `{"error":{"code":"server_error"}}`)
		observer.record(r, 0)
	}
	progress := &CollabLadderProgress{}
	for _, r := range observer.Rejections() {
		progress.RememberRejection(r)
	}
	require.False(t, gjson.GetBytes(progress.repairs.apply(p, raw), "truncation").Exists())
}
