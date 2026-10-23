package service

import (
	"crypto/rand"
	"encoding/binary"
	"strings"

	"github.com/cespare/xxhash/v2"
	"github.com/tidwall/gjson"
)

const cachePrefixMaxBlocks = 512
const cachePrefixHistoryCount = 3

// Only hashes and scalar metadata survive the synchronous body scan.
// Offsets are request-local; histories do not retain payloads or gjson views.
type cachePrefixChain struct {
	hashes    []uint64
	blocks    int
	truncated bool
	anchor    string
	prompt    int
}

// Fixed scratch returned by value stays on the caller stack. Raw and logical
// positions are collected in the same scan and never stored with history.
type cachePrefixPositions struct {
	logical [cachePrefixMaxBlocks]uint32
	raw     [cachePrefixMaxBlocks]int32
	n       int
}

type cachePrefixObservation struct {
	BlockN, BytesOut, ForkBlock, ForkByte                                         int
	BytesOutRaw, ForkRawByte, FirstOldReasoningRawByte                            int
	BlockOrderPhysicalEqLogical                                                   bool
	ForkRef                                                                       string
	Sampled, HistoryUnsampled, TruncatedUnknown                                   bool
	PreviousPrompt                                                                int
	TailKind, ReasoningContext, ModelGen                                          string
	ReasoningItemsN, OldReasoningN, FirstOldReasoningBlock, FirstOldReasoningByte int
	IncludeEncryptedReasoning, StoreFalse                                         bool
	ReasoningEncryptedN                                                           int
}

// Block hashes never leave the process: they are comparison state, not log
// identifiers. PM v1.2.2 authorizes a random process seed and xxhash64 here.
// conv_anchor, session_digest and request_id_hmac remain HMAC digests.
var cachePrefixProcessSeed = func() uint64 {
	var seed [8]byte
	_, _ = rand.Read(seed[:])
	return binary.LittleEndian.Uint64(seed[:])
}()

func cachePrefixContext(v gjson.Result) string {
	if !v.Exists() {
		return "absent"
	}
	switch v.Str {
	case "auto":
		return "auto"
	case "current_turn":
		return "current_turn"
	case "all_turns":
		return "all_turns"
	}
	return "other"
}

// Explicit family rules. Unknown future families never inherit a reasoning
// default. The upstream model is inspected but is never added to trace logs.
func cachePrefixModelGeneration(model string) string {
	model = strings.ToLower(model)
	family := func(prefix string) bool {
		return model == prefix || strings.HasPrefix(model, prefix+"-") || strings.HasPrefix(model, prefix+".")
	}
	if family("gpt-6") {
		return "gpt6x"
	}
	if family("gpt-5.6") {
		return "gpt56"
	}
	for _, prefix := range []string{"gpt-5", "gpt-5.1", "gpt-5.2", "gpt-5.3", "gpt-5.4", "gpt-5.5", "gpt-4", "gpt-4o", "o1", "o3", "o4"} {
		// Do not treat future GPT-5 minors as pre-5.6.
		if prefix == "gpt-5" && strings.HasPrefix(model, "gpt-5.") {
			continue
		}
		if family(prefix) {
			return "pre56"
		}
	}
	if strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "chatgpt-") {
		return "other_openai"
	}
	return "non_openai"
}

// scanCachePrefix hashes raw JSON slices in protocol order. It never remarshal
// or mutate the wire body. A fixed framing length disambiguates absent fields
// and multiple settings in the combined settings block.
func scanCachePrefix(body []byte, protocol string, seed uint64, paths []string) (cachePrefixChain, cachePrefixPositions, cachePrefixObservation) {
	root := parseRawJSONView(body)
	o := cachePrefixObservation{BytesOutRaw: len(body), ForkRawByte: -1, FirstOldReasoningRawByte: -1, BlockOrderPhysicalEqLogical: true, ForkBlock: -1, ForkByte: -1, ForkRef: "none", Sampled: true,
		TailKind: "other", ReasoningContext: "absent", ModelGen: "non_openai",
		FirstOldReasoningBlock: -1, FirstOldReasoningByte: -1}
	// One top-level scan; do not repeatedly search the full body for missing
	// optional fields. Array items are visited once, with old-turn counts updated
	// when a user item is reached. Fixed stack storage avoids a counting pass.
	var items, tools, instructions, textFormat, reasoning, toolChoice, include gjson.Result
	root.ForEach(func(k, v gjson.Result) bool {
		switch k.Str {
		case "model":
			o.ModelGen = cachePrefixModelGeneration(v.Str)
		case "tools":
			tools = v
		case "instructions":
			instructions = v
		case "text":
			textFormat = v
		case "reasoning":
			reasoning = v
		case "tool_choice":
			toolChoice = v
		case "include":
			include = v
		case "store":
			o.StoreFalse = v.Type == gjson.False
		case "input":
			if protocol != "chat" {
				items = v
			}
		case "messages":
			if protocol == "chat" {
				items = v
			}
		}
		return true
	})
	if protocol != "chat" {
		if len(paths) == 0 {
			o.ReasoningContext = cachePrefixContext(reasoning.Get("context"))
		}
		for _, path := range paths {
			var v gjson.Result
			if path == "reasoning.context" {
				v = reasoning.Get("context")
			} else {
				v = root.Get(path)
			}
			if v.Exists() {
				o.ReasoningContext = cachePrefixContext(v)
				break
			}
		}
	}
	include.ForEach(func(_, v gjson.Result) bool {
		if v.Str == "reasoning.encrypted_content" {
			o.IncludeEncryptedReasoning = true
		}
		return true
	})
	var hashes [cachePrefixMaxBlocks]uint64
	var positions cachePrefixPositions
	var h xxhash.Digest
	var frame [16]byte
	binary.LittleEndian.PutUint64(frame[:8], seed)
	var previous uint64
	blockIndex, logicalStart, lastRawOffset, rawStart := 0, 0, -1, -1
	begin := func() {
		logicalStart = o.BytesOut
		rawStart = -1
		h.Reset()
		binary.LittleEndian.PutUint64(frame[8:], previous)
		_, _ = h.Write(frame[:])
	}
	write := func(v gjson.Result) {
		o.BytesOut += len(v.Raw)
		if v.Exists() && (rawStart < 0 || v.Index < rawStart) {
			rawStart = v.Index
		}
		var size [8]byte
		binary.LittleEndian.PutUint64(size[:], uint64(len(v.Raw)))
		_, _ = h.Write(size[:])
		if v.Index >= 0 && v.Index+len(v.Raw) <= len(body) {
			_, _ = h.Write(body[v.Index : v.Index+len(v.Raw)])
		}
	}
	end := func() {
		if rawStart >= 0 {
			if rawStart < lastRawOffset {
				o.BlockOrderPhysicalEqLogical = false
			}
			lastRawOffset = rawStart
		}
		previous = h.Sum64()
		slot := min(blockIndex, cachePrefixMaxBlocks-1)
		hashes[slot] = previous
		// First 511 exact blocks plus an aggregate final hash. The tail hash must
		// never be binary-searched as if it identified a single block.
		if blockIndex < cachePrefixMaxBlocks {
			positions.logical[slot] = uint32(logicalStart)
			positions.raw[slot] = int32(rawStart)
		}
		blockIndex++
	}
	add := func(v gjson.Result) { begin(); write(v); end() }
	add(tools)
	if protocol != "chat" {
		add(instructions)
		begin()
		for _, v := range [3]gjson.Result{textFormat, reasoning, toolChoice} {
			write(v)
		}
		end()
	}
	firstReasoningBlock, firstReasoningByte, firstReasoningRawByte := -1, -1, -1
	visit := func(item gjson.Result) {
		role := item.Get("role").Str
		kind := ""
		// In the input union, role-bearing items are messages. Looking for a
		// missing type in every long message would rescan its entire content.
		if role == "" {
			kind = item.Get("type").Str
		}
		if role == "user" {
			o.OldReasoningN = o.ReasoningItemsN
			if o.OldReasoningN > 0 {
				o.FirstOldReasoningBlock = firstReasoningBlock
				o.FirstOldReasoningByte = firstReasoningByte
				o.FirstOldReasoningRawByte = firstReasoningRawByte
			}
		}
		isReasoning := kind == "reasoning"
		if protocol == "chat" {
			isReasoning = role == "assistant" && item.Get("reasoning_content").Exists()
		}
		if isReasoning {
			o.ReasoningItemsN++
			if firstReasoningBlock < 0 {
				firstReasoningBlock = blockIndex
				firstReasoningByte = o.BytesOut
				firstReasoningRawByte = item.Index
			}
			if v := item.Get("encrypted_content"); v.Type == gjson.String && v.Str != "" {
				o.ReasoningEncryptedN++
			}
		}
		o.TailKind = "other"
		if role == "user" || item.Type == gjson.String {
			o.TailKind = "user_msg"
		}
		if kind == "function_call_output" || kind == "custom_tool_call_output" || protocol == "chat" && role == "tool" {
			o.TailKind = "tool_output"
		}
		add(item)
	}
	if items.IsArray() {
		items.ForEach(func(_, item gjson.Result) bool { visit(item); return true })
	} else if items.Exists() {
		visit(items)
	}
	n := min(blockIndex, cachePrefixMaxBlocks)
	chain := cachePrefixChain{hashes: make([]uint64, n), blocks: blockIndex, truncated: blockIndex > cachePrefixMaxBlocks}
	copy(chain.hashes, hashes[:n])
	positions.n = n
	o.BlockN = blockIndex
	return chain, positions, o
}

func cachePrefixComparableBlocks(c cachePrefixChain) int {
	if c.truncated {
		return len(c.hashes) - 1
	}
	return len(c.hashes)
}

// Chain hashes make equality monotonic, permitting O(log n) lookup.
func cachePrefixEqualBlocks(a, b cachePrefixChain) int {
	lo, hi := 0, min(cachePrefixComparableBlocks(a), cachePrefixComparableBlocks(b))
	for lo < hi {
		mid := lo + (hi-lo)/2
		if a.hashes[mid] == b.hashes[mid] {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func compareCachePrefix(current cachePrefixChain, offsets cachePrefixPositions, histories [cachePrefixHistoryCount]cachePrefixChain, o *cachePrefixObservation) {
	best, common := -1, -1
	for i, old := range histories {
		if len(old.hashes) == 0 {
			continue
		}
		matched := cachePrefixEqualBlocks(current, old)
		if matched > common || matched == common && best >= 0 && old.anchor == current.anchor && histories[best].anchor != current.anchor {
			best, common = i, matched
		}
	}
	if best < 0 {
		return
	}
	old := histories[best]
	o.PreviousPrompt = old.prompt
	o.ForkRef = "prev1"
	if best > 0 {
		o.ForkRef = "prevk"
	}
	if old.anchor != current.anchor {
		o.ForkRef = "other_conv"
	}
	o.TruncatedUnknown = (current.truncated || old.truncated) && common == min(cachePrefixComparableBlocks(current), cachePrefixComparableBlocks(old)) && common >= cachePrefixMaxBlocks-1
	if o.TruncatedUnknown {
		return
	}
	o.ForkBlock = common
	o.ForkByte = o.BytesOut
	o.ForkRawByte = o.BytesOutRaw
	if common < offsets.n {
		o.ForkByte = int(offsets.logical[common])
		o.ForkRawByte = int(offsets.raw[common])
	}
}
