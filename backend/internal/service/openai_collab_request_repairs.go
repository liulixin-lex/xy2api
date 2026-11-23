package service

import (
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Non-ciphertext edits belong to the request. Input edits use original positions,
// so removing a tool output cannot turn a later edit into an edit of its neighbor.
// Ciphertext paths (including deletion of their containing item) stay per account.
type collabRepairLocation struct {
	input bool
	index int
	path  string
}
type collabRequestRepairs map[collabRepairLocation]string // empty value deletes

func (p CollabBody) independentRepairs(current []byte) collabRequestRepairs {
	positions, ok := p.inputPositions(current)
	if !ok {
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
				if !location.input && location.path == "" && (key == "input" || key == "model" || key == "stream") {
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
		if before.IsArray() && after.IsArray() && len(before.Array()) == len(after.Array()) {
			for index, value := range before.Array() {
				next := location
				if next.path != "" {
					next.path += "."
				}
				next.path += strconv.Itoa(index)
				diff(value, after.Array()[index], next, protected)
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
	original := gjson.GetBytes(p.Original, "input").Array()
	remaining := map[int]gjson.Result{}
	for physical, item := range gjson.GetBytes(current, "input").Array() {
		remaining[positions[physical]] = item
	}
	for index, before := range original {
		var protected []string
		for _, item := range p.Items {
			if item.Index != index {
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
		diff(before, remaining[index], collabRepairLocation{input: true, index: index}, protected)
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

func (r collabRequestRepairs) apply(original CollabBody, body []byte) []byte {
	if len(r) == 0 {
		return body
	}
	positions, ok := original.inputPositions(body)
	if !ok {
		return body
	}
	indices := map[int]int{}
	for physical, identity := range positions {
		indices[identity] = physical
	}
	locations := make([]collabRepairLocation, 0, len(r))
	for location := range r {
		locations = append(locations, location)
	}
	// Item deletions must run last, in descending physical order.
	sort.Slice(locations, func(i, j int) bool {
		a, b := locations[i], locations[j]
		if a.input != b.input {
			return !a.input
		}
		if a.index != b.index {
			return indices[a.index] > indices[b.index]
		}
		return a.path > b.path
	})
	for _, location := range locations {
		path := location.path
		if location.input {
			physical, exists := indices[location.index]
			if !exists {
				continue
			}
			path = "input." + strconv.Itoa(physical)
			if location.path != "" {
				path += "." + location.path
			}
		}
		var next []byte
		var err error
		if r[location] == "" {
			next, err = sjson.DeleteBytes(body, path)
		} else {
			next, err = sjson.SetRawBytes(body, path, []byte(r[location]))
		}
		if err == nil {
			body = next
		}
	}
	return body
}
