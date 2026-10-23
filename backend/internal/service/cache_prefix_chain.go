package service

import (
	"crypto/rand"
	"encoding/binary"
	"strings"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/tidwall/gjson"
)

const cachePrefixMaxBlocks = 512
const cachePrefixHistoryCount = 3

// Only hashes and scalar metadata survive the asynchronous body scan.
// Offsets are request-local; histories do not retain payloads or gjson views.
type cachePrefixChain struct {
	hashes    []uint64
	blocks    int
	truncated bool
	anchor    string
	prompt    int
	final     uint64
	bytes     int
	stride    int
	settledAt time.Time
}

// Fixed scratch returned by value stays on the caller stack. Raw and logical
// positions are collected in the same scan and never stored with history.
type cachePrefixPositions struct {
	logical  [cachePrefixMaxBlocks]uint32
	raw      [cachePrefixMaxBlocks]int32
	n        int
	matches  [cachePrefixHistoryCount]cachePrefixMatch
	compared bool
}

type cachePrefixObservation struct {
	pckExists, pckUUID                                                            bool
	BlockN, BytesOut, ForkBlock, ForkByte                                         int
	BytesOutRaw, ForkRawByte, FirstOldReasoningRawByte                            int
	BlockOrderPhysicalEqLogical                                                   bool
	ForkRef                                                                       string
	Sampled, HistoryUnsampled, TruncatedUnknown                                   bool
	PreviousPrompt, HistoryAgeSeconds                                             int
	ForkPrecision, ForkKind                                                       string
	ForkBlockHi, ForkByteHi                                                       int
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
	model = strings.TrimPrefix(strings.ToLower(model), "openai/")
	if model == "codex-mini-latest" || strings.HasPrefix(model, "codex-mini-") {
		return "pre56"
	}
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
func scanCachePrefix(body []byte, protocol string, seed uint64, paths []string, history ...[cachePrefixHistoryCount]cachePrefixChain) (cachePrefixChain, cachePrefixPositions, cachePrefixObservation) {
	root := parseRawJSONView(body)
	o := cachePrefixObservation{BytesOutRaw: len(body), ForkRawByte: -1, FirstOldReasoningRawByte: -1, BlockOrderPhysicalEqLogical: true, ForkBlock: -1, ForkByte: -1, ForkRef: "none", ForkPrecision: "none", ForkKind: "none", ForkBlockHi: -1, ForkByteHi: -1, PreviousPrompt: -1, HistoryAgeSeconds: -1, Sampled: true,
		TailKind: "other", ReasoningContext: "absent", ModelGen: "non_openai",
		FirstOldReasoningBlock: -1, FirstOldReasoningByte: -1}
	// One top-level scan; do not repeatedly search the full body for missing
	// optional fields. Array items are visited once, with old-turn counts updated
	// when a user item is reached. Fixed stack storage avoids a counting pass.
	var items, tools, instructions, textFormat, reasoning, toolChoice, include gjson.Result
	root.ForEach(func(k, v gjson.Result) bool {
		switch k.Str {
		case "prompt_cache_key":
			o.pckExists = v.Exists()
			o.pckUUID = v.Type == gjson.String && cacheTraceUUID.MatchString(v.Str)
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
	var histories [cachePrefixHistoryCount]cachePrefixChain
	if len(history) > 0 {
		histories = history[0]
		positions.compared = true
	}
	for i := range positions.matches {
		positions.matches[i] = cachePrefixMatch{lowerRaw: -1}
	}
	stored, stride := 0, 1
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
		_, _ = h.WriteString(v.Raw)
	}
	end := func() {
		if rawStart >= 0 {
			if rawStart < lastRawOffset {
				o.BlockOrderPhysicalEqLogical = false
			}
			lastRawOffset = rawStart
		}
		previous = h.Sum64()

		if blockIndex < cachePrefixMaxBlocks {
			positions.logical[blockIndex] = uint32(logicalStart)
			positions.raw[blockIndex] = int32(rawStart)
		}
		for i, old := range histories {
			m := &positions.matches[i]
			if old.blocks == 0 || m.different {
				continue
			}
			if blockIndex == m.lower {
				m.lowerRaw = rawStart
			}
			checkpoint := blockIndex < 64 || old.stride <= 1 || (blockIndex-63)%old.stride == 0
			if blockIndex >= old.blocks {
				continue
			}
			var expected uint64
			if blockIndex == old.blocks-1 {
				expected = old.final
			} else if checkpoint {
				slot := blockIndex
				if slot >= 64 {
					slot = 63 + (blockIndex-63)/max(1, old.stride)
				}
				if slot >= len(old.hashes) {
					continue
				}
				expected = old.hashes[slot]
			} else {
				continue
			}
			if previous == expected {
				m.lower, m.lowerByte = blockIndex+1, o.BytesOut
				m.complete = blockIndex == old.blocks-1
			} else {
				m.different, m.upper, m.upperByte = true, blockIndex, logicalStart
			}
		}
		if blockIndex < 64 || (blockIndex-63)%stride == 0 {
			if stored == cachePrefixMaxBlocks {
				// Keep the first 64 dense checkpoints and every second tail
				// checkpoint. Indices are implicit; no per-checkpoint offsets
				// are retained, preserving the original 512*8-byte bound.
				keep := 64
				for i := 65; i < stored; i += 2 {
					hashes[keep] = hashes[i]
					keep++
				}
				stored = keep
				stride *= 2
			}
			if blockIndex < 64 || (blockIndex-63)%stride == 0 {
				hashes[stored] = previous
				stored++
			}
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

		role, kind, encrypted, hasReasoning := cachePrefixItemMetadata(item.Raw)
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
			isReasoning = role == "assistant" && hasReasoning
		}
		if isReasoning {
			o.ReasoningItemsN++
			if firstReasoningBlock < 0 {
				firstReasoningBlock = blockIndex
				firstReasoningByte = o.BytesOut
				firstReasoningRawByte = item.Index
			}
			if encrypted {
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
	n := stored
	chain := cachePrefixChain{hashes: make([]uint64, n), blocks: blockIndex, truncated: stride > 1, stride: stride, final: previous, bytes: o.BytesOut}
	copy(chain.hashes, hashes[:n])
	positions.n = min(blockIndex, cachePrefixMaxBlocks)
	o.BlockN = blockIndex
	return chain, positions, o
}

// Per-history comparison state is scratch, never retained in the LRU.
type cachePrefixMatch struct {
	lower, lowerByte, lowerRaw int
	upper, upperByte           int
	different, complete        bool
}

func compareCachePrefix(current cachePrefixChain, offsets cachePrefixPositions, histories [cachePrefixHistoryCount]cachePrefixChain, o *cachePrefixObservation) {
	best, common := -1, -1
	bestExact := false
	for i, old := range histories {
		if old.blocks == 0 {
			continue
		}
		m := offsets.matches[i]
		// Small-chain callers may compare after scanning; production passes the
		// histories into the scanner so it can compare every historical checkpoint.
		if !offsets.compared {
			for j := 0; j < min(current.blocks, old.blocks, 64); j++ {
				if current.hashes[j] != old.hashes[j] {
					m.different = true
					m.upper = j
					m.upperByte = int(offsets.logical[j])
					break
				}
				m.lower = j + 1
				if m.lower < offsets.n {
					m.lowerByte = int(offsets.logical[m.lower])
					m.lowerRaw = int(offsets.raw[m.lower])
				} else {
					m.lowerByte = o.BytesOut
					m.lowerRaw = o.BytesOutRaw
				}
			}
			m.complete = m.lower == old.blocks
			offsets.matches[i] = m
		}
		// PM: a fully proven candidate (including identical/append "none")
		// outranks a range, then choose longest lower bound and latest settlement.
		exact := m.complete || m.different && m.lower == m.upper || !m.different && m.lower == current.blocks
		if best < 0 || exact && !bestExact || exact == bestExact && (m.lower > common || m.lower == common && old.settledAt.After(histories[best].settledAt)) {
			best, common, bestExact = i, m.lower, exact
		}
	}
	if best < 0 {
		return
	}
	old, m := histories[best], offsets.matches[best]
	o.PreviousPrompt = old.prompt
	o.HistoryAgeSeconds = -1
	if !old.settledAt.IsZero() {
		o.HistoryAgeSeconds = max(0, int(time.Since(old.settledAt).Seconds()))
	}
	o.ForkRef = "prev1"
	if best > 0 {
		o.ForkRef = "prevk"
	}
	if old.anchor != current.anchor {
		o.ForkRef = "other_conv"
	}
	o.ForkPrecision = "none"
	o.ForkBlock, o.ForkByte, o.ForkRawByte, o.ForkBlockHi, o.ForkByteHi = -1, -1, -1, -1, -1
	if m.complete {
		o.ForkKind = "append"
		if current.blocks == old.blocks {
			o.ForkKind = "identical"
		}
		return
	}
	o.ForkKind = "diverge"
	if !m.different {
		o.ForkKind = "tail_unproven"
		if m.lower == current.blocks {
			o.ForkKind = "cur_prefix"
		}
	}
	if !m.different {
		m.upper, m.upperByte = current.blocks, o.BytesOut
		if m.lower == current.blocks {
			m.lowerByte = o.BytesOut
			m.lowerRaw = o.BytesOutRaw
		}
	}
	o.ForkBlock, o.ForkByte, o.ForkRawByte = m.lower, m.lowerByte, m.lowerRaw
	o.ForkPrecision = "exact"
	if m.upper > m.lower {
		o.ForkPrecision = "range"
		o.ForkBlockHi, o.ForkByteHi = m.upper, m.upperByte
	}
	// Whether an uncertain range changes loss_class is decided at settlement,
	// with the reported P/read. A range alone is not n/a_truncated.
	o.TruncatedUnknown = false
}

// cachePrefixItemMetadata skips unrelated raw values without decoding strings.
// gjson.ForEach eagerly unescapes every string, including multi-MB tool output,
// even though only four metadata fields matter here. Hashing still consumes the
// original item.Raw unchanged. This helper is used only by the async worker.
func cachePrefixItemMetadata(raw string) (role, kind string, encrypted, hasReasoning bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return
	}
	for i := 1; i < len(raw); {
		for i < len(raw) && (raw[i] <= ' ' || raw[i] == ',') {
			i++
		}
		if i >= len(raw) || raw[i] != '"' {
			return
		}
		keyStart := i
		i = cachePrefixJSONStringEnd(raw, i)
		if i > len(raw) || i <= keyStart+1 {
			return
		}
		keyRaw := raw[keyStart:i]
		key := keyRaw[1 : len(keyRaw)-1]
		if strings.ContainsRune(key, '\\') {
			key = gjson.Parse(keyRaw).Str
		}
		for i < len(raw) && raw[i] <= ' ' {
			i++
		}
		if i >= len(raw) || raw[i] != ':' {
			return
		}
		i++
		for i < len(raw) && raw[i] <= ' ' {
			i++
		}
		start := i
		depth := 0
		for i < len(raw) {
			ch := raw[i]
			if ch == '"' {
				i = cachePrefixJSONStringEnd(raw, i)
				continue
			}
			if ch == '{' || ch == '[' {
				depth++
			}
			if ch == '}' || ch == ']' {
				if depth == 0 {
					break
				}
				depth--
			}
			if ch == ',' && depth == 0 {
				break
			}
			i++
		}
		value := strings.TrimSpace(raw[start:i])
		switch key {
		case "role":
			role = gjson.Parse(value).Str
		case "type":
			kind = gjson.Parse(value).Str
		case "encrypted_content":
			encrypted = len(value) >= 2 && value[0] == '"' && value != `""`
		case "reasoning_content":
			hasReasoning = gjson.Parse(value).Exists()
		}
	}
	return
}

// Return the exclusive end of a raw JSON string. Quote searches avoid copying
// or unescaping the (usually large) ignored tool output. An unterminated string
// consumes the remainder, matching the scanner's best-effort observation role.
func cachePrefixJSONStringEnd(raw string, start int) int {
	for at := start + 1; at < len(raw); {
		n := strings.IndexByte(raw[at:], '"')
		if n < 0 {
			return len(raw)
		}
		end := at + n
		backslashes := 0
		for j := end - 1; j > start && raw[j] == '\\'; j-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return end + 1
		}
		at = end + 1
	}
	return len(raw)
}
