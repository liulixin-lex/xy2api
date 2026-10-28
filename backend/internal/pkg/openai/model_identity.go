package openai

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ModelID struct {
	Base, Snapshot, Effort string
	Known                  bool
}

var modelBasePattern = regexp.MustCompile(`^gpt-(\d+)(?:\.(\d+))?(?:-(astra|sol|terra|luna))?$`)
var modelSnapshotPattern = regexp.MustCompile(`-(\d{4})-?(\d{2})-?(\d{2})$`)

// ModelIdentity recognizes explicit GPT identities, preserving snapshot and effort.
// Only the documented openai/ provider prefix is removed.
func ModelIdentity(raw string) ModelID {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" || len(v) > 200 {
		return ModelID{}
	}
	v = strings.TrimSpace(strings.TrimPrefix(v, "openai/"))
	if strings.Contains(v, "/") {
		return ModelID{}
	}
	v = strings.ReplaceAll(v, "_", "-")
	v = strings.Join(strings.Fields(v), "-")
	for strings.Contains(v, "--") {
		v = strings.ReplaceAll(v, "--", "-")
	}
	if len(v) > 3 && strings.HasPrefix(v, "gpt") && v[3] >= '0' && v[3] <= '9' {
		v = "gpt-" + v[3:]
	}
	id := ModelID{}
	stripEffort := func() {
		for _, e := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
			if strings.HasSuffix(v, "-"+e) {
				id.Effort, v = e, strings.TrimSuffix(v, "-"+e)
				return
			}
		}
	}
	stripEffort()
	if m := modelSnapshotPattern.FindStringSubmatch(v); m != nil {
		// Accept exactly the two documented date spellings.
		if len(m[0]) != 9 && len(m[0]) != 11 {
			return ModelID{}
		}
		id.Snapshot = m[1] + "-" + m[2] + "-" + m[3]
		if _, err := time.Parse("2006-01-02", id.Snapshot); err != nil || m[1] == "0000" {
			return ModelID{}
		}
		v = strings.TrimSuffix(v, m[0])
	}
	if id.Effort == "" {
		stripEffort()
	}
	m := modelBasePattern.FindStringSubmatch(v)
	if m == nil {
		return ModelID{}
	}
	major, err := strconv.Atoi(m[1])
	minor := 0
	if m[2] != "" {
		var minorErr error
		minor, minorErr = strconv.Atoi(m[2])
		if minorErr != nil {
			return ModelID{}
		}
	}
	if err != nil || major < 5 || major == 5 && minor < 6 {
		return ModelID{}
	}
	if m[3] == "" {
		switch v {
		case "gpt-6":
			v = "gpt-6-astra"
		case "gpt-5.6":
			v = "gpt-5.6-sol"
		default:
			return ModelID{}
		}
	}
	id.Base, id.Known = v, true
	return id
}

type DowngradeRule struct{ From, To string }

func DefaultDowngradeRules() []DowngradeRule {
	return []DowngradeRule{
		{"gpt-6-astra", "gpt-5.6-luna"},
		{"gpt-6.1-sol", "gpt-5.6-luna"},
		{"gpt-6-sol", "gpt-5.6-luna"},
		{"gpt-5.6-sol", "gpt-5.6-luna"},
		{"gpt-5.6-terra", "gpt-5.6-luna"},
	}
}

type ModelClass string

const (
	ModelUndeclared    ModelClass = "undeclared"
	ModelSame          ModelClass = "same"
	ModelUnrecognized  ModelClass = "unrecognized"
	ModelEquivalent    ModelClass = "equivalent"
	ModelSnapshotDrift ModelClass = "snapshot_drift"
	ModelDowngrade     ModelClass = "downgrade"
	ModelVersionUp     ModelClass = "version_up"
	ModelVersionDown   ModelClass = "version_down"
	ModelDifferent     ModelClass = "different"
	ModelEventConflict ModelClass = "event_conflict"
)

// ClassifyDeclaredModel uses the built-in downgrade pairs plus configured rules.
// No ordering of model sizes is inferred from a version or variant name.
func ClassifyDeclaredModel(sent, declared string, rules []DowngradeRule) ModelClass {
	if strings.TrimSpace(declared) == "" {
		return ModelUndeclared
	}
	if strings.EqualFold(strings.TrimSpace(sent), strings.TrimSpace(declared)) {
		return ModelSame
	}
	s, d := ModelIdentity(sent), ModelIdentity(declared)
	if !s.Known || !d.Known {
		return ModelUnrecognized
	}
	if s.Base == d.Base {
		if s.Snapshot == "" || s.Snapshot == d.Snapshot {
			return ModelEquivalent
		}
		return ModelSnapshotDrift
	}
	for _, pairs := range [][]DowngradeRule{DefaultDowngradeRules(), rules} {
		for _, rule := range pairs {
			if ModelIdentity(rule.From).Base == s.Base && ModelIdentity(rule.To).Base == d.Base {
				return ModelDowngrade
			}
		}
	}
	sm, dm := modelBasePattern.FindStringSubmatch(s.Base), modelBasePattern.FindStringSubmatch(d.Base)
	if sm[3] == dm[3] {
		sMajor, _ := strconv.Atoi(sm[1])
		dMajor, _ := strconv.Atoi(dm[1])
		sMinor, _ := strconv.Atoi(sm[2])
		dMinor, _ := strconv.Atoi(dm[2])
		if dMajor > sMajor || dMajor == sMajor && dMinor > sMinor {
			return ModelVersionUp
		}
		return ModelVersionDown
	}
	return ModelDifferent
}

// EquivalentModels is symmetric: an unpinned name may agree with a snapshot.
func EquivalentModels(a, b string) bool {
	if strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) {
		return true
	}
	x, y := ModelIdentity(a), ModelIdentity(b)
	return x.Known && y.Known && x.Base == y.Base && (x.Snapshot == "" || y.Snapshot == "" || x.Snapshot == y.Snapshot)
}

type DeclarationResult struct {
	Class         ModelClass
	ReportedModel string
	Source        string
	Flags         []string
	Wrong         bool
	Unknown       bool
}

// DeclarationSelection carries terminal declarations without changing event order.
// An absent terminal declaration selects the first nonempty value of that source.
type DeclarationSelection struct{ Body, Header string }

func ClassifyDeclarations(sent string, body, header []string, preferHeader bool, rules []DowngradeRule, selected DeclarationSelection) DeclarationResult {
	nonempty := func(values []string) []string {
		var out []string
		for _, v := range values {
			if strings.TrimSpace(v) != "" {
				out = append(out, v)
			}
		}
		return out
	}
	body, header = nonempty(body), nonempty(header)
	sel := selected
	r := DeclarationResult{Class: ModelUndeclared, Source: "body"}
	primary, other, value := body, header, sel.Body
	if preferHeader && len(header) > 0 {
		r.Source = "header"
		primary, other, value = header, body, sel.Header
	}
	if len(primary) == 0 {
		return r
	}
	if strings.TrimSpace(value) == "" {
		value = primary[0]
	}
	r.ReportedModel = value
	var distinct []string
	for _, v := range primary {
		found := false
		for _, u := range distinct {
			if EquivalentModels(v, u) {
				found = true
				break
			}
		}
		if !found {
			distinct = append(distinct, v)
		}
	}
	r.Class = ClassifyDeclaredModel(sent, value, rules)
	if len(distinct) > 1 {
		down := 0
		for _, v := range distinct {
			if ClassifyDeclaredModel(sent, v, rules) == ModelDowngrade {
				down++
			}
		}
		if down == len(distinct) {
			r.Class = ModelDowngrade
		} else {
			r.Class, r.Unknown = ModelEventConflict, down > 0
		}
	}
	r.Wrong = r.Class == ModelDowngrade
	disagreement := false
	for _, a := range primary {
		for _, b := range other {
			if !EquivalentModels(a, b) {
				disagreement = true
			}
		}
	}
	if disagreement {
		r.Flags = append(r.Flags, "source_disagreement")
	}
	if r.Class == ModelEventConflict {
		r.Flags = append(r.Flags, "event_conflict")
	}
	return r
}
