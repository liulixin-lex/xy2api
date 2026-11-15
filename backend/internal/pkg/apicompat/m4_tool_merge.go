package apicompat

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

type bridgeToolDeclaration struct {
	value    map[string]json.RawMessage
	batch    int
	children []*bridgeToolDeclaration
	childKey string
}

func mergeBridgeToolBatches(batches [][]json.RawMessage, record func(string)) ([]json.RawMessage, error) {
	var merged []*bridgeToolDeclaration
	seen := make(map[string]*bridgeToolDeclaration)
	for batch, declarations := range batches {
		path := "tools"
		if batch > 0 {
			path = fmt.Sprintf("input[%d].tools", batch-1)
		}
		var err error
		merged, err = mergeBridgeToolBatch(merged, seen, declarations, nil, path, batch, record)
		if err != nil {
			return nil, err
		}
	}
	return marshalBridgeToolDeclarations(merged)
}

func mergeBridgeToolBatch(out []*bridgeToolDeclaration, seen map[string]*bridgeToolDeclaration, declarations []json.RawMessage, namespace []string, path string, batch int, record func(string)) ([]*bridgeToolDeclaration, error) {
	for i, raw := range declarations {
		var value map[string]json.RawMessage
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", path, i, err)
		}
		kind := strings.ToLower(strings.TrimSpace(rawString(value["type"])))
		name := rawString(value["name"])
		identity, _ := json.Marshal([]any{namespace, kind, name})
		key := string(identity)
		entryPath := fmt.Sprintf("%s[%d]", path, i)
		previous := seen[key]
		if previous != nil && previous.batch != batch && kind != "namespace" {
			fields := []string(nil)
			switch kind {
			case "function":
				fields = []string{"parameters", "strict"}
			case "custom":
				fields = []string{"format"}
			}
			for _, field := range fields {
				if len(value[field]) != 0 && !bridgeSchemaEqual(previous.value[field], value[field]) && record != nil {
					record(entryPath + "." + field)
				}
			}
			continue
		}
		entry := &bridgeToolDeclaration{value: value, batch: batch}
		if kind == "namespace" {
			childKey := "tools"
			var children []json.RawMessage
			if err := json.Unmarshal(value[childKey], &children); err != nil && len(value[childKey]) != 0 {
				return nil, fmt.Errorf("%s.tools: %w", entryPath, err)
			}
			if len(children) == 0 {
				childKey = "children"
				if err := json.Unmarshal(value[childKey], &children); err != nil && len(value[childKey]) != 0 {
					return nil, fmt.Errorf("%s.children: %w", entryPath, err)
				}
			}
			entry.childKey = childKey
			if previous != nil && previous.batch != batch {
				entry = previous
			}
			childNamespace := append(append([]string(nil), namespace...), name)
			var err error
			entry.children, err = mergeBridgeToolBatch(entry.children, seen, children, childNamespace, entryPath+"."+childKey, batch, record)
			if err != nil {
				return nil, err
			}
			if entry == previous {
				continue
			}
		}
		out = append(out, entry)
		if previous == nil {
			seen[key] = entry
		}
	}
	return out, nil
}

func marshalBridgeToolDeclarations(entries []*bridgeToolDeclaration) ([]json.RawMessage, error) {
	result := make([]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		if entry.childKey != "" {
			children, err := marshalBridgeToolDeclarations(entry.children)
			if err != nil {
				return nil, err
			}
			entry.value[entry.childKey], err = json.Marshal(children)
			if err != nil {
				return nil, err
			}
		}
		raw, err := json.Marshal(entry.value)
		if err != nil {
			return nil, err
		}
		result = append(result, raw)
	}
	return result, nil
}

func bridgeSchemaEqual(left, right json.RawMessage) bool {
	var a, b any
	if len(left) == 0 || len(right) == 0 {
		return len(left) == len(right)
	}
	if json.Unmarshal(left, &a) != nil || json.Unmarshal(right, &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}
