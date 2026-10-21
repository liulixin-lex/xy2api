package sharesched

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Scheduler selects which algorithm serves a group.
type Scheduler string

const (
	// SchedulerSub2API keeps the original Sub2API selection untouched.
	SchedulerSub2API Scheduler = "sub2api"
	// SchedulerShadow is the retired shadow comparison of 0.3.0–0.4.3. A
	// stored shadow policy runs the original scheduler and is never written.
	SchedulerShadow Scheduler = "shadow"
	// SchedulerShare serves with share scheduling.
	SchedulerShare Scheduler = "share"
)

// PolicySchema is the only policy document schema.
const PolicySchema = 1

// Policy is one group's share-scheduling configuration. It is stored as JSON
// and versioned by the repository. A share group takes tiers from account
// priorities and weights from account load factors, so the policy holds only
// the scheduler and session keeping.
//
// The remaining fields are the configuration of 0.3.0–0.4.3. They are
// decoded and ignored so stored policies keep loading, and saves keep them
// untouched so a rollback reads the original rules (design §3).
type Policy struct {
	Schema    int       `json:"schema"`
	Scheduler Scheduler `json:"scheduler"`
	Affinity  Affinity  `json:"affinity"`

	Accounts json.RawMessage `json:"accounts,omitempty"`
	Defaults json.RawMessage `json:"defaults,omitempty"`
	Cache    json.RawMessage `json:"cache,omitempty"`
	Tiers    json.RawMessage `json:"tiers,omitempty"`
	Balance  json.RawMessage `json:"balance,omitempty"`
}

// Affinity holds session keeping. Enabled=false or Key=none turns it off;
// the retired keys session and prefix keep sessions as auto does.
type Affinity struct {
	Enabled bool   `json:"enabled"`
	Key     string `json:"key"`

	TTLSeconds          json.RawMessage `json:"ttl_seconds,omitempty"`
	Wait                json.RawMessage `json:"wait,omitempty"`
	OnSpill             json.RawMessage `json:"on_spill,omitempty"`
	SoftSpillMaxSeconds json.RawMessage `json:"soft_spill_max_seconds,omitempty"`
}

const (
	AffinityKeyAuto = "auto"
	AffinityKeyNone = "none"
	// affinityKeySession and affinityKeyPrefix are retired keys that are still
	// read, as auto.
	affinityKeySession = "session"
	affinityKeyPrefix  = "prefix"
)

// DefaultPolicy is the policy of a group without a stored one: the original
// scheduler with session keeping on.
func DefaultPolicy() Policy {
	return Policy{Schema: PolicySchema, Scheduler: SchedulerSub2API, Affinity: Affinity{Enabled: true, Key: AffinityKeyAuto}}
}

// Effective is the scheduler that serves the group: a stored shadow
// policy runs the original scheduler.
func (p Policy) Effective() Scheduler {
	if p.Scheduler == SchedulerShare {
		return SchedulerShare
	}
	return SchedulerSub2API
}

// SessionKeep reports whether sessions are bound to accounts.
func (p Policy) SessionKeep() bool {
	return p.Affinity.Enabled && p.Affinity.Key != AffinityKeyNone
}

// DecodePolicy strictly decodes a policy document over the defaults. Fields
// of earlier versions are accepted and ignored; unknown fields, trailing data
// and invalid values are rejected.
func DecodePolicy(data []byte) (Policy, error) {
	policy := DefaultPolicy()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("invalid share scheduling policy: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Policy{}, errors.New("invalid share scheduling policy: trailing data")
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

// ValidationError lists every problem in a policy.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return "invalid share scheduling policy: " + e.Problems[0]
	}
	return fmt.Sprintf("invalid share scheduling policy: %d problems, first: %s", len(e.Problems), e.Problems[0])
}

// Validate checks the fields that still have meaning.
func (p Policy) Validate() error {
	var problems []string
	if p.Schema != PolicySchema {
		problems = append(problems, fmt.Sprintf("schema must be %d", PolicySchema))
	}
	switch p.Scheduler {
	case SchedulerSub2API, SchedulerShadow, SchedulerShare:
	default:
		problems = append(problems, "scheduler must be sub2api, shadow or share")
	}
	switch p.Affinity.Key {
	case AffinityKeyAuto, AffinityKeyNone, affinityKeySession, affinityKeyPrefix:
	default:
		problems = append(problems, "affinity.key must be auto, session, prefix or none")
	}
	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

// NewPolicyDocument is the document of a group without a stored policy: the
// smallest one the decoder of 0.3.0–0.4.3 also accepts.
func NewPolicyDocument(scheduler Scheduler, sessionKeep bool) []byte {
	key := AffinityKeyAuto
	if !sessionKeep {
		key = AffinityKeyNone
	}
	raw, _ := json.Marshal(map[string]any{
		"schema": PolicySchema, "scheduler": scheduler, "accounts": []any{},
		"affinity": map[string]any{"enabled": true, "key": key},
	})
	return raw
}

// PatchPolicyDocument sets the scheduler and session keeping of a stored
// document and leaves every other field as stored, so that an earlier
// version reads its original rules after a rollback. It adds no field the
// 0.4.3 decoder rejects. A document that no longer decodes is replaced.
func PatchPolicyDocument(stored []byte, scheduler Scheduler, sessionKeep bool) ([]byte, error) {
	if scheduler != SchedulerSub2API && scheduler != SchedulerShare {
		return nil, fmt.Errorf("scheduler must be %s or %s", SchedulerSub2API, SchedulerShare)
	}
	current, err := DecodePolicy(stored)
	var doc map[string]json.RawMessage
	if err == nil {
		err = json.Unmarshal(stored, &doc)
	}
	if err != nil || doc == nil {
		return NewPolicyDocument(scheduler, sessionKeep), nil
	}
	doc["scheduler"], _ = json.Marshal(scheduler)
	if current.SessionKeep() != sessionKeep {
		affinity := map[string]json.RawMessage{}
		if raw, ok := doc["affinity"]; ok {
			if err := json.Unmarshal(raw, &affinity); err != nil {
				return nil, fmt.Errorf("decode affinity: %w", err)
			}
		}
		if affinity == nil {
			// "affinity": null decodes to the defaults.
			affinity = map[string]json.RawMessage{}
		}
		if sessionKeep {
			affinity["enabled"] = json.RawMessage(`true`)
			affinity["key"] = json.RawMessage(`"` + AffinityKeyAuto + `"`)
		} else {
			affinity["key"] = json.RawMessage(`"` + AffinityKeyNone + `"`)
		}
		doc["affinity"], _ = json.Marshal(affinity)
	}
	return json.Marshal(doc)
}
