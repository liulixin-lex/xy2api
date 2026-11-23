//go:build unit

package resilience

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func collabCatalogBody(s string) []byte {
	return []byte(`{"input":[{"role":"developer","content":"` + s + `"}]}`)
}

func TestCollabIngressWorkBudget(t *testing.T) {
	body := []byte(`{"tools":[{"name":"collaboration.send_message"}],"input":[{"role":"developer","content":"collaboration alias namespace"}]}`)
	before := bytes.Clone(body)
	count := func() int64 {
		var total int64
		for _, row := range Snapshot(time.Hour) {
			if row.Name == "collab_ingress_untracked" {
				total += row.Count
			}
		}
		return total
	}
	full := int64(len(body))*512 + 512
	budget := collabIngressBudget{remaining: full}
	if got := inspectCollabIngress(body, CollabIngressValidationPassed, &budget); got != collabIngressUnfilteredReference(body) {
		t.Fatalf("full budget lost observation: %+v", got)
	}
	for _, limit := range []int64{0, 1, int64(len(body)) * 34, full - budget.remaining - 1} {
		got := inspectCollabIngress(body, CollabIngressValidationPassed, &collabIngressBudget{remaining: limit})
		if got != (CollabIngressObservation{Untracked: true}) {
			t.Fatalf("budget=%d retained partial evidence: %+v", limit, got)
		}
		if got.Persist(false) {
			t.Fatal("untracked ingress requested persistence")
		}
		prior := count()
		CountCollabIngress(got, CollabRouteResponsesHttp, CollabRequestKindNormal)
		if count() != prior+1 {
			t.Fatal("exhaustion was not counted as untracked")
		}
	}
	if !bytes.Equal(before, body) {
		t.Fatal("budget exhaustion changed request")
	}
	if got := InspectCollabIngress(body, CollabIngressValidationPassed); !got.Declared || !got.UnrecognizedCatalog || got.Untracked {
		t.Fatalf("budget leaked between requests: %+v", got)
	}
	for _, validation := range []CollabIngressValidation{CollabIngressValidationUnknown, CollabIngressValidationFailed, 255} {
		if got := inspectCollabIngress(body, validation, &collabIngressBudget{}); got != (CollabIngressObservation{}) {
			t.Fatalf("unvalidated body inspected: %+v", got)
		}
	}
}

func TestCollabIngressNestedAndTextRegression(t *testing.T) {
	bodies := []string{
		`{"input":[{"role":"developer","content":"\u000collaboration namespace"}]}`,
		`{"input":[{"role":"developer","content":"collaboration \u006eamespace"}]}`,
	}
	for _, first := range []string{`null`, `{}`, `[]`, `0`, `false`, `"other"`, `{"name":null}`, `{"name":""}`, `{"name":"other"}`} {
		bodies = append(bodies, `{"tools":[{"function":`+first+`,"fun\u0063tion":{"name":"collaboration.send_message"}}]}`)
	}
	for _, text := range []string{`["collaboration","namespace"]`, `{"collaboration":"namespace"}`, `["namespace collaboration"]`, `["\u0063ollaboration","namespace"]`, `null`, `true`, `1e300`, `[]`, `{}`} {
		bodies = append(bodies, `{"input":[{"role":"developer","content":[{"text":`+text+`}]}]}`)
	}
	for _, raw := range bodies {
		body := []byte(raw)
		if !json.Valid(body) {
			t.Fatalf("invalid fixture: %s", body)
		}
		if got, want := InspectCollabIngress(body, CollabIngressValidationPassed), collabIngressUnfilteredReference(body); got != want {
			t.Errorf("body=%s got=%+v want=%+v", body, got, want)
		}
	}
}

func collabAdversarialBody(size int, kind string) []byte {
	switch kind {
	case "escape_letters":
		return collabCatalogBody(strings.Repeat(`\u0061`, size/6))
	case "escape_controls":
		prefix := `{"input":[{"type":"function_call_output","output":"synthetic"},{"type":"reasoning","encrypted_content":"synthetic"},{"role":"developer","content":"collaboration namespace `
		suffix := `"}]}`
		return []byte(prefix + strings.Repeat(`\u001b`, (size-len(prefix)-len(suffix))/6) + suffix)
	case "backslashes":
		return collabCatalogBody(strings.Repeat(`\\`, size/2) + "collaboration alias namespace")
	case "nesting":
		return []byte(`{"unused":` + strings.Repeat("[", size/2) + "0" + strings.Repeat("]", size/2) + `,"tools":[{"name":"collaboration.send_message"}]}`)
	case "short_strings":
		return []byte(`{"input":[` + strings.Repeat(`{"role":"developer","content":"c"},`, size/35) + `{"role":"developer","content":"collaboration alias namespace"}]}`)
	default:
		return collabCatalogBody(strings.Repeat(kind, size) + "collaboration alias namespace")
	}
}

func collabMeasure(scan func(), repeats int) time.Duration {
	best := time.Duration(1<<63 - 1)
	for i := 0; i < 3; i++ {
		start := time.Now()
		for j := 0; j < repeats; j++ {
			scan()
		}
		elapsed := time.Since(start) / time.Duration(repeats)
		if elapsed < best {
			best = elapsed
		}
	}
	return best
}

func TestCollabIngressAdversarialScaling(t *testing.T) {
	// c and n start every keyword searched as a substring. Equality and prefix
	// checks are covered by the generated metadata fixtures.
	for _, kind := range []string{"escape_letters", "escape_controls", "c", "n", "backslashes", "nesting", "short_strings"} {
		t.Run(kind, func(t *testing.T) {
			var previousWork, previousGrowth int64
			for _, size := range []int{128 << 10, 256 << 10, 512 << 10} {
				body := collabAdversarialBody(size, kind)
				if !gjson.ValidBytes(body) {
					t.Fatal("invalid fixture")
				}
				limit := 512*int64(len(body)) + 512
				budget := collabIngressBudget{remaining: limit}
				if got, want := inspectCollabIngress(body, CollabIngressValidationPassed, &budget), collabIngressUnfilteredReference(body); got != want {
					t.Fatalf("got=%+v want=%+v", got, want)
				}
				work := limit - budget.remaining
				// Compare increments so the fixed history prefix does not distort
				// the ratio. Allow only the cost of rounding a repeated fragment.
				growth := work - previousWork
				if previousGrowth > 0 && growth > 2*previousGrowth+4096 {
					t.Errorf("doubling added work grew from %d to %d", previousGrowth, growth)
				}
				t.Logf("bytes=%d work=%d", len(body), work)
				if previousWork > 0 {
					previousGrowth = growth
				}
				previousWork = work
			}
			// Wall time between sizes 16 times apart: linear costs about 16 times,
			// quadratic about 256. Shared test machines are noisy, so the bound is
			// wide and each side is the best of several runs.
			small, large := collabAdversarialBody(256<<10, kind), collabAdversarialBody(4<<20, kind)
			fast := collabMeasure(func() { InspectCollabIngress(small, CollabIngressValidationPassed) }, 5)
			slow := collabMeasure(func() { InspectCollabIngress(large, CollabIngressValidationPassed) }, 2)
			t.Logf("256 KiB %s, 4 MiB %s", fast, slow)
			if slow > 48*fast+2*time.Millisecond {
				t.Errorf("16 times the input took %s against %s", slow, fast)
			}
		})
	}
}

func TestCollabIngressAdversarialLarge(t *testing.T) {
	body := collabAdversarialBody(10<<20, "escape_controls")
	before := bytes.Clone(body)
	start := time.Now()
	got := InspectCollabIngress(body, CollabIngressValidationPassed)
	elapsed := time.Since(start)
	start = time.Now()
	want := collabIngressUnfilteredReference(body)
	reference := time.Since(start)
	t.Logf("bytes=%d inspect=%s reference=%s", len(body), elapsed, reference)
	if got != want {
		t.Errorf("got=%+v want=%+v", got, want)
	}
	if !bytes.Equal(before, body) {
		t.Fatal("body changed")
	}
	if elapsed > 10*reference+5*time.Millisecond {
		t.Errorf("inspection %s exceeds reference order of magnitude %s", elapsed, reference)
	}
	if allocations := collabMinAllocsPerRun(3, func() { InspectCollabIngress(body, CollabIngressValidationPassed) }); allocations != 0 {
		t.Errorf("adversarial body allocated %g times", allocations)
	}
}

func BenchmarkCollabIngressAdversarial(b *testing.B) {
	for _, kind := range []string{"escape_letters", "escape_controls", "c", "n", "backslashes", "nesting", "short_strings"} {
		for _, size := range []int{1 << 20, 2 << 20, 10 << 20} {
			b.Run(fmt.Sprintf("%s/%dMiB", kind, size>>20), func(b *testing.B) {
				body := collabAdversarialBody(size, kind)
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					InspectCollabIngress(body, CollabIngressValidationPassed)
				}
			})
		}
	}
}

func TestCollabIngressUnicodeDifferential(t *testing.T) {
	total, mismatches := 0, 0
	check := func(body []byte) {
		if !json.Valid(body) || !gjson.ValidBytes(body) {
			t.Fatalf("bad fixture %q", body)
		}
		total++
		got, want := InspectCollabIngress(body, CollabIngressValidationPassed), collabIngressUnfilteredReference(body)
		if got != want {
			mismatches++
			if mismatches <= 8 {
				t.Logf("mismatch body=%s got=%+v want=%+v", body, got, want)
			}
		}
	}
	for n := 0; n < 65536; n++ {
		for _, f := range []string{`\u%04xollaboration namespace`, `namespace alias \u%04xcollaboration`, `collaboration \u%04xnamespace`, `namespace collabora\u%04xtion`} {
			check(collabCatalogBody(fmt.Sprintf(f, n)))
		}
	}
	rng := rand.New(rand.NewSource(149))
	atoms := []string{`\"`, `\\`, `\/`, `\b`, `\f`, `\n`, `\r`, `\t`, `\u0000`, `\u0063`, `\u006E`, `\ud800`, `\udfff`, `\uD83D\uDE80`, `é`, `🚀`, `collaboration`, `namespace`, `namespace collaboration`, `boration`, `ollaboration`, ` alias `}
	for n := 0; n < 50000; n++ {
		var s strings.Builder
		for j := 0; j < 12; j++ {
			s.WriteString(atoms[rng.Intn(len(atoms))])
		}
		check(collabCatalogBody(s.String()))
	}
	t.Logf("valid documents=%d mismatches=%d", total, mismatches)
	if mismatches > 0 {
		t.Fail()
	}
}

func collabQuotedKey(s string, r *rand.Rand) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		if c < 128 && r.Intn(3) == 0 {
			fmt.Fprintf(&b, `\u%04x`, c)
		} else {
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func TestCollabIngressShapeDifferential(t *testing.T) {
	r := rand.New(rand.NewSource(419))
	total, bad := 0, 0
	vals := []string{`null`, `false`, `true`, `0`, `-0`, `1.00`, `1e300`, `-2E-4`, `[]`, `{}`, `""`, `"collaboration"`, `"namespace"`, `"collaboration.send_message"`, `"spawn_agent"`, `"developer"`, `"agent_message"`, `"encrypted_content"`, `"function_call_output"`, `"additional_tools"`, `"response.create"`, `"namespace alias collaboration"`, `{"name":"collaboration.send_message"}`, `{"text":["collaboration","namespace"]}`}
	var val func(int) string
	keys := []string{"type", "name", "namespace", "function", "tools", "role", "content", "output", "encrypted_content", "text", "input", "response"}
	val = func(d int) string {
		if d == 0 || r.Intn(3) == 0 {
			return vals[r.Intn(len(vals))]
		}
		var parts []string
		for j := 0; j < 1+r.Intn(5); j++ {
			if d%2 == 0 {
				parts = append(parts, collabQuotedKey(keys[r.Intn(len(keys))], r)+" : "+val(d-1))
			} else {
				parts = append(parts, val(d-1))
			}
		}
		if d%2 == 0 {
			return "{\n" + strings.Join(parts, ",\t") + "}"
		}
		return "[ " + strings.Join(parts, ",\r\n") + " ]"
	}
	check := func(body []byte) {
		if !json.Valid(body) {
			t.Fatalf("invalid generator %q", body)
		}
		total++
		got, want := InspectCollabIngress(body, CollabIngressValidationPassed), collabIngressUnfilteredReference(body)
		if got != want {
			bad++
			if bad < 7 {
				t.Logf("mismatch %s got=%+v want=%+v", body, got, want)
			}
		}
	}
	for n := 0; n < 50000; n++ {
		check([]byte(val(4)))
	}
	for n := 0; n < 20000; n++ {
		a, b := val(2), val(2)
		body := `{"tools":[` + a + `,` + b + `],"input":[` + a + `,` + b + `],"type":"response.create","response":{"tools":[` + a + `],"input":[` + b + `]}}`
		check(collabEscapeJSONLetters([]byte(body), r, 2))
	}
	for _, key := range keys {
		for _, a := range vals {
			for _, b := range vals {
				obj := `{` + collabQuotedKey(key, r) + `:` + a + `,` + collabQuotedKey(key, r) + `:` + b + `}`
				check([]byte(`{"tools":[` + obj + `],"input":[` + obj + `]}`))
			}
		}
	}
	t.Logf("valid shape documents=%d mismatches=%d", total, bad)
	if bad > 0 {
		t.Fail()
	}
}
