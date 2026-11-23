//go:build unit

package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// The reference model consumes ordinary JSON and exact strings. It does not
// call the classifier, resolver, identity builder or production locator table.
type collabReferenceModel struct{ ids, paths map[string]string }

func newCollabReferenceModel(input []map[string]any) collabReferenceModel {
	m := collabReferenceModel{map[string]string{}, map[string]string{}}
	var visit func(any, string, string, string)
	visit = func(value any, bracket, dot, id string) {
		m.paths[bracket], m.paths[dot] = id, id
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				visit(child, bracket+"."+key, dot+"."+key, id)
			}
		case []any:
			for i, child := range value {
				visit(child, fmt.Sprintf("%s[%d]", bracket, i), fmt.Sprintf("%s.%d", dot, i), id)
			}
		}
	}
	for i, item := range input {
		id := item["id"].(string)
		m.ids[id] = id
		visit(item, fmt.Sprintf("input[%d]", i), fmt.Sprintf("input.%d", i), id)
	}
	return m
}
func (m collabReferenceModel) resolve(ids, paths []any) string {
	candidate := ""
	for _, group := range []struct {
		values []any
		table  map[string]string
	}{{ids, m.ids}, {paths, m.paths}} {
		for _, value := range group.values {
			str, ok := value.(string)
			if !ok || group.table[str] == "" {
				return ""
			}
			id := group.table[str]
			if candidate != "" && id != candidate {
				return ""
			}
			candidate = id
		}
	}
	return candidate
}
func collabPropertyReferenceChannels(t *testing.T) {
	raw := []byte(`{"input":[{"type":"reasoning","id":"rs_A","encrypted_content":"one"},{"type":"reasoning","id":"rs_b","encrypted_content":"two"},{"type":"reasoning","id":"123","encrypted_content":"three"},{"type":"reasoning","id":"true","encrypted_content":"four"}]}`)
	var request struct {
		Input []map[string]any `json:"input"`
	}
	require.NoError(t, json.Unmarshal(raw, &request))
	model := newCollabReferenceModel(request.Input)
	p := ParseCollabBody(raw)
	variants := func(s string) []any {
		return []any{s, strings.ToUpper(s), strings.ToLower(s), " " + s, s + " ", s + ".missing", 123, true, nil, []any{s}, map[string]any{"id": s}}
	}
	for _, seed := range []int64{7, 43, 131} {
		rng := rand.New(rand.NewSource(seed))
		for n := 0; n < collabPropertyRuns(); n++ {
			first, second := rng.Intn(len(request.Input)), rng.Intn(len(request.Input))
			id := variants(request.Input[first]["id"].(string))[n%11]
			path := variants(fmt.Sprintf("input[%d].encrypted_content", second))[(n/11)%11]
			// Structured ID, prose ID, structured locator, prose locator, and all
			// intersections. Fixed cycling guarantees contradictory channel coverage.
			channel := n % 7
			fields := map[string]any{"code": "invalid_encrypted_content"}
			var ids, paths []any
			switch channel {
			case 0:
				fields["item_id"] = id
				ids = append(ids, id)
			case 1:
				if _, ok := id.(string); !ok {
					id = fmt.Sprint(id)
				}
				fields["message"] = fmt.Sprintf("Encrypted content could not be decrypted for item '%s'.", id)
				ids = append(ids, id)
			case 2:
				fields["param"] = path
				paths = append(paths, path)
			case 3:
				path = fmt.Sprintf("input[%d].encrypted_content", second)
				fields["message"] = fmt.Sprintf("Encrypted content could not be decrypted at %s.", path)
				paths = append(paths, path)
			case 4:
				fields["item_id"] = id
				fields["param"] = path
				ids = append(ids, id)
				paths = append(paths, path)
			case 5:
				a, b := fmt.Sprintf("input[%d]", first), fmt.Sprintf("input[%d]", second)
				fields["message"] = "Encrypted content could not be decrypted at " + a + "."
				fields["param"] = b
				paths = append(paths, a, b)
			case 6:
				fields["item_id"] = id
				ids = append(ids, id)
				messageID := request.Input[second]["id"].(string)
				fields["message"] = "Encrypted content could not be decrypted for item '" + messageID + "'."
				ids = append(ids, messageID)
			}
			// The same reference fields can arrive in each validated error envelope.
			var envelope any
			switch (n / 7) % 3 {
			case 0:
				envelope = map[string]any{"error": fields}
			case 1:
				envelope = map[string]any{"response": map[string]any{"error": fields}}
			case 2:
				fields["type"] = "error"
				envelope = fields
			}
			payload, _ := json.Marshal(envelope)
			got := p.ResolveEvidence((CollabRejection{Payload: payload, Status: 400}).Evidence(p.Shape))
			expected := model.resolve(ids, paths)
			label := fmt.Sprintf("seed=%d iteration=%d channel=%d id=%v path=%v", seed, n, channel, id, path)
			require.NotNil(t, got.Named, label)
			require.Equal(t, expected != "", got.Named.Located, label)
			if expected != "" {
				require.Equal(t, expected, got.Named.Identity, label)
			}
		}
	}
}

// Controller endpoint model, from final disposal and the handled-item rule.
// Evidence is data supplied by the generator, never production policy output.
type collabEndpointModel struct {
	stop             CollabLadderStop
	local, delivered bool
	latest           resilience.CollabEvidence
}

func collabModelEndpoint(endpoint string, latest resilience.CollabEvidence, handled bool) collabEndpointModel {
	m := collabEndpointModel{stop: CollabLadderFallback, latest: latest}
	switch endpoint {
	case "accepted":
		m.delivered = true
	case "attempts", "replaced":
		m.stop = CollabLadderFinalDisposal
	case "deadline", "cancel", "no_account":
		m.stop = CollabLadderDeliveryUnavailable
	case "handled":
		m.stop = CollabLadderHandledAgain
	}
	if (endpoint == "deadline" || endpoint == "cancel" || endpoint == "replaced") && handled && latest.Named != nil && latest.Named.Identity == "rs_a" {
		m.stop = CollabLadderHandledAgain
	}
	m.local = latest.Grade == resilience.CollabEvidenceGradeVerified && latest.Ownership != resilience.CollabOwnershipClassUnknownSuffix && latest.Ownership != resilience.CollabOwnershipClassCapacity && latest.Ownership != resilience.CollabOwnershipClassContinuation && (m.stop == CollabLadderFinalDisposal || m.stop == CollabLadderDeliveryUnavailable)
	return m
}
func collabPropertyControllerEndpoints(t *testing.T) {
	endpoints := []string{"accepted", "attempts", "deadline", "cancel", "no_account", "fallback", "handled", "replaced"}
	for _, seed := range []int64{11, 47, 137} {
		rng := rand.New(rand.NewSource(seed))
		for n := 0; n < collabPropertyRuns(); n++ {
			endpoint := endpoints[n%len(endpoints)]
			p := ParseCollabBody([]byte(collabBatchBody))
			ctx, cancel := context.WithCancel(context.Background())
			initial := resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeVerified, Named: &resilience.CollabNamedItem{Identity: "rs_a"}}
			latest := initial
			progress := &CollabLadderProgress{probed: map[int64]bool{}, replaced: map[int64]bool{}, handled: map[int64]map[string]bool{}}
			sends := 0
			keepGoing := true
			handled := false
			l := CollabLadder{Body: p, First: 1, Current: 1, Evidence: initial, Progress: progress, MaxAttempts: []int{0, 1, 4}[rng.Intn(3)], Select: collabBatchSelect, Continue: func() bool { return keepGoing }}
			switch endpoint {
			case "attempts":
				progress.optional = 3
				l.Evidence.Named = nil
				latest = l.Evidence
			case "replaced":
				progress.replaced[2] = true
				l.Evidence.Named = nil
				latest = l.Evidence
			case "no_account":
				l.Select = func(context.Context, int64, map[int64]bool) (int64, func()) { return 0, nil }
			case "handled":
				progress.handled[1] = map[string]bool{"rs_a": true}
				handled = true
			case "fallback":
				l.Evidence.Grade = resilience.CollabEvidenceGradeUnclaimed
				latest = l.Evidence
			}
			l.Send = func(_ context.Context, _ int64, _ []byte, _ resilience.CollabAction) (resilience.CollabEvidence, bool) {
				sends++
				require.Less(t, sends, 4)
				if endpoint == "accepted" {
					return resilience.CollabEvidence{}, true
				}
				if endpoint == "deadline" || endpoint == "cancel" {
					grade := []resilience.CollabEvidenceGrade{resilience.CollabEvidenceGradeVerified, resilience.CollabEvidenceGradeClaimed, resilience.CollabEvidenceGradeUnclaimed}[rng.Intn(3)]
					name := []string{"rs_a", "rs_b", "amsg_task", ""}[rng.Intn(4)]
					latest = resilience.CollabEvidence{Grade: grade}
					if name != "" {
						latest.Named = &resilience.CollabNamedItem{Identity: name}
					}
					if rng.Intn(4) == 0 {
						latest.Ownership = resilience.CollabOwnershipClassUnknownSuffix
						latest.Family = resilience.CollabFamilyOwnership
					}
					handled = true
					if endpoint == "cancel" {
						cancel()
					} else {
						keepGoing = false
					}
				}
				return latest, false
			}
			if (endpoint == "cancel" || endpoint == "deadline") && n%3 == 0 {
				if endpoint == "cancel" {
					cancel()
				} else {
					keepGoing = false
				}
			}
			got := l.Run(ctx)
			cancel()
			expected := collabModelEndpoint(endpoint, latest, handled)
			label := fmt.Sprintf("seed=%d iteration=%d endpoint=%s", seed, n, endpoint)
			require.Equal(t, expected.delivered, got, label)
			require.Equal(t, expected.stop, l.StopReason, label)
			require.Equal(t, latest.Grade, l.LastEvidence.Grade, label)
			require.Equal(t, latest.Ownership, l.LastEvidence.Ownership, label)
			if latest.Named == nil {
				require.Nil(t, l.LastEvidence.Named, label)
			} else {
				require.NotNil(t, l.LastEvidence.Named, label)
				require.Equal(t, latest.Named.Identity, l.LastEvidence.Named.Identity, label)
			}
			require.Equal(t, expected.local, p.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding, label)
		}
	}
}

// The repair oracle records edits as a tree over the original JSON, evaluates
// child edits before removing array slots, and makes deletion permanent. It has
// no dependency on the production diff, merge, path ordering or identity code.
type collabModelEdit struct {
	deleted, set bool
	value        any
	children     map[string]*collabModelEdit
}

func (m *collabModelEdit) record(path []string, value any, deleted bool) {
	if len(path) == 0 {
		if !m.deleted {
			m.deleted = deleted
			m.set = !deleted
			m.value = value
		}
		return
	}
	if m.children == nil {
		m.children = map[string]*collabModelEdit{}
	}
	if m.children[path[0]] == nil {
		m.children[path[0]] = &collabModelEdit{}
	}
	m.children[path[0]].record(path[1:], value, deleted)
}
func (m *collabModelEdit) apply(value any) (any, bool) {
	if m == nil {
		return value, true
	}
	if m.deleted {
		return nil, false
	}
	if m.set {
		value = m.value
	}
	switch value := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range value {
			out[k] = v
		}
		for k, e := range m.children {
			v, keep := e.apply(out[k])
			if keep {
				out[k] = v
			} else {
				delete(out, k)
			}
		}
		return out, true
	case []any:
		out := []any{}
		for i, v := range value {
			v, keep := m.children[strconv.Itoa(i)].apply(v)
			if keep {
				out = append(out, v)
			}
		}
		return out, true
	}
	return value, true
}
func collabPropertyOriginalRepairModel(t *testing.T) {
	for _, seed := range []int64{13, 53, 139} {
		rng := rand.New(rand.NewSource(seed))
		for n := 0; n < collabPropertyRuns(); n++ {
			raw := []byte(`{"parallel_tool_calls":true,"temperature":1,"tools":[{"name":"tool_a","bad":true},{"name":"tool_b"}],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"anonymous a","bad":true}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"anonymous b","bad":true}]},{"type":"message","id":"msg_plain","role":"user","content":[{"type":"input_text","text":"identified","bad":true}]},` + collabBatchTask + `]}`)
			var original map[string]any
			require.NoError(t, json.Unmarshal(raw, &original))
			// Digest+occurrence is assigned once, before any repair or reordering.
			counts := map[[32]byte]int{}
			identities := []string{}
			for _, item := range original["input"].([]any) {
				bytes, _ := json.Marshal(item)
				digest := sha256.Sum256(bytes)
				identities = append(identities, fmt.Sprintf("%x:%d", digest, counts[digest]))
				counts[digest]++
			}
			require.NotEqual(t, identities[0], identities[1])
			events := []struct {
				path    string
				value   any
				deleted bool
			}{
				{"parallel_tool_calls", false, false}, {"parallel_tool_calls", nil, true}, {"temperature", float64(0), false},
				{"input.0.content.0.bad", nil, true}, {"input.1.content.0.bad", nil, true},
				{"input.2.content.0.strict", false, false}, {"tools.0.bad", nil, true},
				{"tools", []any{map[string]any{"name": "tool_a", "bad": true}, map[string]any{"name": "tool_b"}}, false},
				{"input.2.content.0", map[string]any{"type": "input_text", "text": "identified", "bad": true, "extra": true}, false},
				{"input.2.content.0.bad", nil, true},
			}
			// Deletion-before-replacement and replacement-before-deletion both occur.
			rng.Shuffle(len(events), func(i, j int) { events[i], events[j] = events[j], events[i] })
			p := ParseCollabBody(raw)
			progress := &CollabLadderProgress{}
			model := &collabModelEdit{}
			accounts := 1 + rng.Intn(3)
			for step, event := range events {
				single := &collabModelEdit{}
				path := strings.Split(event.path, ".")
				single.record(path, event.value, event.deleted)
				current, _ := single.apply(original)
				body := current.(map[string]any)
				if step%2 == 0 {
					input := body["input"].([]any)
					input = append([]any(nil), input...)
					input[0], input[1] = input[1], input[0]
					body["input"] = input
				}
				encoded, _ := json.Marshal(body)
				codes := []string{"invalid_encrypted_content", "server_error", "invalid_parameter", "rate_limit_exceeded", "context_length_exceeded"}
				rejection := collabObserveDerivedRejection(p, encoded, `{"error":{"code":"`+codes[rng.Intn(len(codes))]+`"}}`)
				rejection.AccountID = int64(1 + rng.Intn(accounts))
				progress.RememberRejection(rejection)
				model.record(path, event.value, event.deleted)
				expected, _ := model.apply(original)
				for account := 1; account <= accounts; account++ {
					base := progress.derived[int64(account)]
					if base == nil {
						base = raw
					}
					actual := progress.repairs.apply(p, base)
					var decoded any
					require.NoError(t, json.Unmarshal(actual, &decoded))
					collabModelCanonicalItems(expected)
					collabModelCanonicalItems(decoded)
					require.Equal(t, expected, decoded, "seed=%d iteration=%d step=%d account=%d edit=%s", seed, n, step, account, event.path)
				}
			}
		}
	}
}

// Array order may remain the physical send order when no operation is needed.
// Compare complete items in their original semantic identity order, so anonymous
// reordering cannot hide a repair applied to the wrong original.
func collabModelCanonicalItems(value any) {
	root := value.(map[string]any)
	input := append([]any(nil), root["input"].([]any)...)
	root["input"] = input
	key := func(item any) string {
		m := item.(map[string]any)
		if id, ok := m["id"].(string); ok {
			return "id:" + id
		}
		return m["content"].([]any)[0].(map[string]any)["text"].(string)
	}
	sort.Slice(input, func(i, j int) bool { return key(input[i]) < key(input[j]) })
}
