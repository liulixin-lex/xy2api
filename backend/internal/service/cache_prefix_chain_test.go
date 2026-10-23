package service

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

const prefixTestKey uint64 = 12345678

func prefixTestBody(items ...string) []byte {
	return []byte(`{"model":"gpt-5.5","tools":[{"type":"function","name":"test"}],"instructions":"policy","input":[` + strings.Join(items, ",") + `]}`)
}
func prefixTestScan(body []byte) (cachePrefixChain, cachePrefixPositions, cachePrefixObservation) {
	return scanCachePrefix(body, "responses", prefixTestKey, nil)
}
func prefixTestCompare(old, next []byte) cachePrefixObservation {
	a, _, _ := prefixTestScan(old)
	b, offsets, o := prefixTestScan(next)
	a.anchor = "same"
	b.anchor = "same"
	a.prompt = 10000
	compareCachePrefix(b, offsets, [cachePrefixHistoryCount]cachePrefixChain{a}, &o)
	return o
}

func TestCachePrefixAppendModifyDelete(t *testing.T) {
	items := []string{`{"role":"user","content":"one"}`, `{"role":"assistant","content":"two"}`, `{"role":"user","content":"three"}`}
	old := prefixTestBody(items...)
	cases := []struct {
		name   string
		body   []byte
		block  int
		marker string
	}{
		{"append", prefixTestBody(append(append([]string{}, items...), `{"role":"assistant","content":"four"}`)...), 6, `{"role":"assistant","content":"four"}`},
		{"modify", prefixTestBody(items[0], `{"role":"assistant","content":"changed"}`, items[2]), 4, `{"role":"assistant","content":"changed"}`},
		{"delete-middle", prefixTestBody(items[0], items[2]), 4, items[2]},
		{"identical", old, 6, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := append([]byte(nil), tc.body...)
			o := prefixTestCompare(old, tc.body)
			want := len(tc.body)
			if tc.marker != "" {
				want = bytes.Index(tc.body, []byte(tc.marker))
			}
			if o.ForkBlock != tc.block || o.ForkRawByte != want || o.ForkRef != "prev1" {
				t.Fatalf("unexpected fork %+v want block=%d byte=%d", o, tc.block, want)
			}
			if !bytes.Equal(original, tc.body) {
				t.Fatal("body mutated")
			}
		})
	}
}

func TestCachePrefixRawToolsOrderAndSettings(t *testing.T) {
	a := []byte(`{"tools":[{"name":"a","description":"b"}],"instructions":"p","reasoning":{"context":"current_turn"},"input":[]}`)
	for _, tc := range []struct {
		old, new string
		block    int
	}{
		{`"name":"a","description":"b"`, `"description":"b","name":"a"`, 0},
		{`"instructions":"p"`, `"instructions":"q"`, 1},
		{`"current_turn"`, `"all_turns"`, 2},
	} {
		b := bytes.Replace(a, []byte(tc.old), []byte(tc.new), 1)
		o := prefixTestCompare(a, b)
		if o.ForkBlock != tc.block {
			t.Fatalf("fork=%d want=%d", o.ForkBlock, tc.block)
		}
	}
}

func TestCachePrefixInterleaving(t *testing.T) {
	a, _, _ := prefixTestScan(prefixTestBody(`{"role":"user","content":"A"}`))
	a.anchor = "a"
	a.prompt = 9000
	b, _, _ := prefixTestScan(prefixTestBody(`{"role":"user","content":"B"}`))
	b.anchor = "b"
	b.prompt = 8000
	next, offsets, o := prefixTestScan(prefixTestBody(`{"role":"user","content":"A"}`, `{"role":"assistant","content":"continue"}`))
	next.anchor = "a"
	compareCachePrefix(next, offsets, [3]cachePrefixChain{b, a}, &o)
	if o.ForkRef != "prevk" || o.PreviousPrompt != 9000 || o.ForkBlock != 4 {
		t.Fatalf("%+v", o)
	}
	next.anchor = "c"
	compareCachePrefix(next, offsets, [3]cachePrefixChain{b, a}, &o)
	if o.ForkRef != "other_conv" {
		t.Fatalf("%+v", o)
	}
}

func TestCachePrefixTruncation(t *testing.T) {
	items := make([]string, 620)
	for i := range items {
		items[i] = fmt.Sprintf(`{"role":"user","content":"%d"}`, i)
	}
	body := prefixTestBody(items...)
	chain, _, _ := prefixTestScan(body)
	if len(chain.hashes) != 512 || !chain.truncated || chain.blocks != 623 {
		t.Fatal("unbounded chain")
	}
	for _, index := range []int{5, 507, 508, 600} {
		changed := append([]string{}, items...)
		changed[index] = `{"role":"user","content":"changed"}`
		o := prefixTestCompare(body, prefixTestBody(changed...))
		if index+3 < 511 {
			if o.TruncatedUnknown || o.ForkBlock != index+3 {
				t.Fatalf("known fork %d: %+v", index, o)
			}
		} else if !o.TruncatedUnknown || o.ForkBlock != -1 {
			t.Fatalf("unknown tail fork %d: %+v", index, o)
		}
	}
	if o := prefixTestCompare(body, body); !o.TruncatedUnknown {
		t.Fatalf("truncated identical %+v", o)
	}
	// A short reference ending before the cap has a known append boundary.
	if o := prefixTestCompare(prefixTestBody(items[:10]...), body); o.TruncatedUnknown || o.ForkBlock != 13 {
		t.Fatalf("short reference %+v", o)
	}
}

func TestCachePrefixReasoningOldTurnAndRawOffsets(t *testing.T) {
	r1 := `{"type":"reasoning","encrypted_content":"private-reasoning"}`
	u := `{"role":"user","content":"next"}`
	r2 := `{"type":"reasoning","summary":[]}`
	for _, tc := range []struct {
		name  string
		items []string
		old   int
		tail  string
	}{
		{"user-tail", []string{r1, u}, 1, "user_msg"},
		{"tool-tail", []string{r1, u, r2, `{"type":"function_call_output","output":"private-tool"}`}, 1, "tool_output"},
		{"current-only", []string{u, r1}, 0, "other"},
		{"no-user", []string{r1, r2}, 0, "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := prefixTestBody(tc.items...)
			_, _, o := prefixTestScan(body)
			if o.OldReasoningN != tc.old || o.TailKind != tc.tail || o.ReasoningEncryptedN != 1 {
				t.Fatalf("%+v", o)
			}
			if tc.old > 0 && (o.FirstOldReasoningBlock != 3 || o.FirstOldReasoningRawByte != bytes.Index(body, []byte(r1))) {
				t.Fatalf("offset %+v", o)
			}
			if tc.old == 0 && o.FirstOldReasoningByte != -1 {
				t.Fatalf("current reasoning was counted as old: %+v", o)
			}
		})
	}
	body := []byte(`{"model":"gpt-5.6-mini","reasoning":{"context":"all_turns"},"include":["reasoning.encrypted_content"],"store":false,"input":"hello"}`)
	_, _, o := prefixTestScan(body)
	if o.ModelGen != "gpt56" || o.ReasoningContext != "all_turns" || !o.IncludeEncryptedReasoning || !o.StoreFalse || o.TailKind != "user_msg" {
		t.Fatalf("%+v", o)
	}
	body = []byte(`{"model":"gpt-6.1-sol","messages":[{"role":"assistant","reasoning_content":"private"},{"role":"user","content":"u"},{"role":"tool","content":"t"}],"reasoning":{"context":"current_turn"}}`)
	_, _, o = scanCachePrefix(body, "chat", prefixTestKey, nil)
	if o.ReasoningContext != "absent" || o.OldReasoningN != 1 || o.FirstOldReasoningBlock != 1 || o.TailKind != "tool_output" || o.ModelGen != "gpt6x" {
		t.Fatalf("%+v", o)
	}
}

func TestCachePrefixContextPathsAndModelFamilies(t *testing.T) {
	for model, want := range map[string]string{"gpt-5": "pre56", "gpt-5.5-lite": "pre56", "gpt-5.6": "gpt56", "gpt-5.60": "other_openai", "gpt-5.7": "other_openai", "gpt-6.1-sol": "gpt6x", "o3": "pre56", "gpt-7": "other_openai", "claude": "non_openai"} {
		if got := cachePrefixModelGeneration(model); got != want {
			t.Fatalf("%s: %s != %s", model, got, want)
		}
	}
	_, _, o := scanCachePrefix([]byte(`{"extra":{"context":"current_turn"},"input":[]}`), "responses", prefixTestKey, []string{"absent.path", "extra.context"})
	if o.ReasoningContext != "current_turn" {
		t.Fatalf("%+v", o)
	}
}

func cachePrefixBenchBody() []byte {
	items := make([]string, 200)
	for i := range items {
		items[i] = `{"role":"user","content":"` + strings.Repeat("x", 1000) + `"}`
	}
	return prefixTestBody(items...)
}
func BenchmarkCachePrefix200KB(b *testing.B) {
	body := cachePrefixBenchBody()
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	scanCachePrefix(body, "responses", prefixTestKey, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scanCachePrefix(body, "responses", prefixTestKey, nil)
	}
}
func TestCachePrefixAllocationBound(t *testing.T) {
	// The benchmark's bytes/op captures both hashes and temporary offsets.
	result := testing.Benchmark(func(b *testing.B) {
		body := cachePrefixBenchBody()
		scanCachePrefix(body, "responses", prefixTestKey, nil)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			scanCachePrefix(body, "responses", prefixTestKey, nil)
		}
	})
	if got := result.AllocedBytesPerOp(); got > 203*16 {
		t.Fatalf("alloc=%d, bound=%d", got, 203*16)
	}
}
func FuzzCachePrefixNoPanic(f *testing.F) {
	for _, s := range []string{`{}`, `{"input":[{},null,"x",{"type":"reasoning"}]}`, `{"input":"hello"}`, `{"tools":[1,2],"messages":[]}`, `invalid`} {
		f.Add([]byte(s), true)
	}
	f.Fuzz(func(t *testing.T, body []byte, responses bool) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		protocol := "chat"
		if responses {
			protocol = "responses"
		}
		c, offsets, o := scanCachePrefix(body, protocol, prefixTestKey, nil)
		if len(c.hashes) > 512 || offsets.n != len(c.hashes) || o.BytesOutRaw != len(body) {
			t.Fatal("bounds")
		}
	})
}

func BenchmarkCachePrefix800KB(b *testing.B) {
	items := make([]string, 400)
	for i := range items {
		items[i] = `{"role":"user","content":"` + strings.Repeat("x", 2000) + `"}`
	}
	body := prefixTestBody(items...)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scanCachePrefix(body, "responses", prefixTestKey, nil)
	}
}

func TestCachePrefixLogicalOrderIndependentOfWireOrder(t *testing.T) {
	input := `[{"type":"reasoning","encrypted_content":"private"},{"role":"user","content":"u"}]`
	tools := `[{"type":"function","name":"a"}]`
	first := []byte(`{"tools":` + tools + `,"input":` + input + `}`)
	last := []byte(`{"input":` + input + `,"tools":` + tools + `}`)
	a, offsetsA, oa := prefixTestScan(first)
	b, offsetsB, ob := prefixTestScan(last)
	if cachePrefixEqualBlocks(a, b) != a.blocks || offsetsA.n != offsetsB.n {
		t.Fatal("wire order changed logical prefix")
	}
	for i := range offsetsA.n {
		if offsetsA.logical[i] != offsetsB.logical[i] {
			t.Fatal("logical offsets depend on physical order")
		}
	}
	if !oa.BlockOrderPhysicalEqLogical || ob.BlockOrderPhysicalEqLogical {
		t.Fatal("order marker")
	}
	if oa.BytesOut != ob.BytesOut || oa.FirstOldReasoningByte != len(tools) || ob.FirstOldReasoningByte != len(tools) {
		t.Fatalf("logical sizes %+v %+v", oa, ob)
	}
	if oa.FirstOldReasoningRawByte == ob.FirstOldReasoningRawByte {
		t.Fatal("raw offsets did not preserve wire order")
	}
	changed := bytes.Replace(last, []byte(`"name":"a"`), []byte(`"name":"b"`), 1)
	o := prefixTestCompare(last, changed)
	if o.ForkBlock != 0 || o.ForkByte != 0 || o.ForkRawByte <= ob.FirstOldReasoningRawByte {
		t.Fatalf("tools fork %+v", o)
	}
}

// Includes comparison against K=3 histories and selected raw offset lookup.
func BenchmarkCachePrefixCompare200KB(b *testing.B) {
	body := cachePrefixBenchBody()
	old, _, _ := prefixTestScan(body)
	histories := [3]cachePrefixChain{old, old, old}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		current, offsets, o := prefixTestScan(body)
		compareCachePrefix(current, offsets, histories, &o)
	}
}
