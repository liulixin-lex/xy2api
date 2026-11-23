package service

import (
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Non-ciphertext edits are operations on the immutable original request.
// Input operations use IDs or original digests with occurrence numbers.
// Ciphertext paths (including deletion of their containing item) stay per account.
type collabRepairLocation struct {
	identity string // empty for request fields
	path     string
}
type collabRequestRepairs map[collabRepairLocation]string // empty value deletes

func (p CollabBody) independentRepairs(current []byte) collabRequestRepairs {
	return p.requestOperations(current, false)
}
func (p CollabBody) requestOperations(current []byte, ciphertext bool) collabRequestRepairs {
	positions, ok := p.inputPositions(current)
	if !ok && ciphertext {
		return nil
	}
	out := collabRequestRepairs{}
	var diff func(gjson.Result, gjson.Result, collabRepairLocation, []string)
	diff = func(before, after gjson.Result, location collabRepairLocation, protected []string) {
		if before.Raw == after.Raw {
			return
		}
		for _, path := range protected {
			if location.path == path || strings.HasPrefix(location.path, path+".") {
				return
			}
		}
		if before.IsObject() && after.IsObject() {
			keys := map[string]bool{}
			before.ForEach(func(k, _ gjson.Result) bool { keys[k.Str] = true; return true })
			after.ForEach(func(k, _ gjson.Result) bool { keys[k.Str] = true; return true })
			for key := range keys {
				// These select the transport/model; they are not field repairs.
				if location.identity == "" && location.path == "" && (key == "input" || key == "model" || key == "stream") {
					continue
				}
				if strings.ContainsAny(key, ".\\:*?[]") {
					continue
				}
				next := location
				if next.path != "" {
					next.path += "."
				}
				next.path += key
				diff(before.Get(key), after.Get(key), next, protected)
			}
			return
		}
		if before.IsArray() && after.IsArray() {
			previous, values := before.Array(), after.Array()
			matches := collabArrayPositions(previous, values)
			remaining := map[int]gjson.Result{}
			for physical, original := range matches {
				remaining[original] = values[physical]
			}
			for index, value := range previous {
				next := location
				if next.path != "" {
					next.path += "."
				}
				next.path += strconv.Itoa(index)
				diff(value, remaining[index], next, protected)
			}
			// New elements are appended after the original identity space.
			added := len(previous)
			for physical, value := range values {
				if _, exists := matches[physical]; exists {
					continue
				}
				next := location
				if next.path != "" {
					next.path += "."
				}
				next.path += strconv.Itoa(added)
				diff(gjson.Result{}, value, next, protected)
				added++
			}
			return
		}
		// Do not promote an account's ciphertext deletion or placeholder into
		// a request-wide replacement of its containing array/object.
		for _, path := range protected {
			if location.path == "" || strings.HasPrefix(path, location.path+".") {
				return
			}
		}
		out[location] = after.Raw
	}
	diff(gjson.ParseBytes(p.Original), gjson.ParseBytes(current), collabRepairLocation{}, nil)
	if !ok {
		return out
	}
	original := gjson.GetBytes(p.Original, "input").Array()
	identities := collabInputIdentities(p.Original)
	remaining := map[int]gjson.Result{}
	for physical, item := range gjson.GetBytes(current, "input").Array() {
		remaining[positions[physical]] = item
	}
	for index, before := range original {
		var protected []string
		for _, item := range p.Items {
			if ciphertext || item.Index != index {
				continue
			}
			prefix := "input." + strconv.Itoa(index)
			for _, path := range item.Paths {
				protected = append(protected, strings.TrimPrefix(strings.TrimPrefix(path, prefix), "."))
			}
			// Call arguments are immutable as well.
			if len(item.Paths) == 0 {
				protected = append(protected, "")
			}
		}
		diff(before, remaining[index], collabRepairLocation{identity: identities[index]}, protected)
	}
	return out
}

func (r *collabRequestRepairs) merge(next collabRequestRepairs) {
	if len(next) == 0 {
		return
	}
	if *r == nil {
		*r = collabRequestRepairs{}
	}
	for location, value := range next {
		if prior, exists := (*r)[location]; exists && prior == "" {
			continue
		}
		(*r)[location] = value
	}
}

// Rebuild from the original so all array indices in operations address original
// elements. Account ciphertext operations apply first; request operations win.
// Parent setters precede child setters. All deletions follow all setters, with
// array elements removed in descending order. Removed ancestors stay removed.
func (r collabRequestRepairs) apply(original CollabBody, body []byte) []byte {
	if len(r) == 0 {
		return body
	}
	operations := original.requestOperations(body, true)
	if operations == nil {
		return body
	}
	operations.merge(r)
	identities := collabInputIdentities(original.Original)
	indices := map[string]int{}
	for i, id := range identities {
		indices[id] = i
	}
	type operation struct{ path, value string }
	var sets, deletes []operation
	for location, value := range operations {
		path := location.path
		if location.identity != "" {
			index, exists := indices[location.identity]
			if !exists {
				continue
			}
			path = "input." + strconv.Itoa(index)
			if location.path != "" {
				path += "." + location.path
			}
		}
		op := operation{path, value}
		if value == "" {
			deletes = append(deletes, op)
		} else {
			sets = append(sets, op)
		}
	}
	sort.Slice(sets, func(i, j int) bool {
		a, b := sets[i].path, sets[j].path
		if strings.Count(a, ".") != strings.Count(b, ".") {
			return strings.Count(a, ".") < strings.Count(b, ".")
		}
		return collabPathLess(a, b)
	})
	sort.Slice(deletes, func(i, j int) bool { return collabPathLess(deletes[j].path, deletes[i].path) })
	out := original.Original
	for _, op := range sets {
		if next, err := sjson.SetRawBytes(out, op.path, []byte(op.value)); err == nil {
			out = next
		}
	}
	for _, op := range deletes {
		if next, err := sjson.DeleteBytes(out, op.path); err == nil {
			out = next
		}
	}
	// Record the original identity of surviving anonymous derivations. Deletions
	// are accounted for before assigning the resulting physical positions.
	if original.lineage != nil {
		physical := 0
		for index, id := range identities {
			if value, exists := operations[collabRepairLocation{identity: id}]; exists && value == "" {
				continue
			}
			item := gjson.GetBytes(out, "input."+strconv.Itoa(physical))
			if item.Get("id").String() == "" {
				original.lineage.remember(item.Raw, index)
			}
			physical++
		}
	}
	return out
}

func collabPathLess(a, b string) bool {
	x, y := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] == y[i] {
			continue
		}
		n, ne := strconv.Atoi(x[i])
		m, me := strconv.Atoi(y[i])
		if ne == nil && me == nil {
			return n < m
		}
		return x[i] < y[i]
	}
	return len(x) < len(y)
}

// Nested arrays are diffed by element identity before any positional field
// updates. This turns truncation into removals instead of parent replacements.
func collabArrayPositions(before, after []gjson.Result) map[int]int {
	matches, used := map[int]int{}, map[int]bool{}
	for i, value := range after {
		for j, prior := range before {
			if used[j] {
				continue
			}
			same := value.Raw == prior.Raw
			for _, key := range []string{"id", "name"} {
				same = same || value.Get(key).String() != "" && value.Get(key).String() == prior.Get(key).String()
			}
			if same {
				matches[i], used[j] = j, true
				break
			}
		}
	}
	if len(before) == len(after) {
		var available []int
		for j := range before {
			if !used[j] {
				available = append(available, j)
			}
		}
		for i := range after {
			if _, ok := matches[i]; !ok {
				matches[i], available = available[0], available[1:]
			}
		}
	}
	return matches
}
