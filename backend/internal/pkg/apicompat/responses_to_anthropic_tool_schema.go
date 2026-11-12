package apicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// Object-only root unions are an established gateway extension. Lower their
// property vocabulary while preserving allOf required unions and anyOf/oneOf
// required intersections. Never discard non-object branches or rewrite them to
// empty schemas. Nested property schemas remain opaque JSON.
func anthropicToolSchema(raw json.RawMessage, field string) (json.RawMessage, error) {
	if len(normalizedRawJSON(raw)) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`), nil
	}
	schema, ok := rawJSONObject(raw)
	if !ok || schema == nil {
		return nil, unsupportedConversion(field, "expected an object tool schema")
	}
	if err := lowerAnthropicObjectSchema(schema, field); err != nil {
		return nil, err
	}
	return json.Marshal(schema)
}

func lowerAnthropicObjectSchema(schema map[string]json.RawMessage, field string) error {
	// An omitted/null type imposes no type constraint, so required-only union
	// branches are compatible with object. Normalize nullable object unions.
	if typ := normalizedRawJSON(schema["type"]); len(typ) > 0 && rawString(typ) != "object" {
		var types []string
		if json.Unmarshal(typ, &types) != nil || !slices.Contains(types, "object") {
			return unsupportedConversion(field+".type", "tool schema root must allow type object")
		}
	}
	for _, name := range []string{"properties", "required", "oneOf", "anyOf", "allOf"} {
		if raw, exists := schema[name]; exists && len(normalizedRawJSON(raw)) == 0 {
			delete(schema, name)
		}
	}
	var rootRequired []string
	if raw, exists := schema["required"]; exists && json.Unmarshal(raw, &rootRequired) != nil {
		return unsupportedConversion(field+".required", "expected an array of property names")
	}
	properties := map[string]json.RawMessage{}
	if raw, exists := schema["properties"]; exists {
		if json.Unmarshal(raw, &properties) != nil || properties == nil {
			return unsupportedConversion(field+".properties", "expected an object")
		}
	}
	var required []string
	merged := false
	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		raw, exists := schema[keyword]
		if !exists {
			continue
		}
		merged = true
		var branches []map[string]json.RawMessage
		if json.Unmarshal(raw, &branches) != nil || len(branches) == 0 {
			return unsupportedConversion(field+"."+keyword, "expected nonempty object schema branches")
		}
		var branchRequired []string
		branchProperties := map[string]json.RawMessage{}
		for i, branch := range branches {
			branchField := fmt.Sprintf("%s.%s[%d]", field, keyword, i)
			if branch == nil {
				return unsupportedConversion(branchField, "expected an object schema branch")
			}
			if err := lowerAnthropicObjectSchema(branch, branchField); err != nil {
				return err
			}
			var props map[string]json.RawMessage
			_ = json.Unmarshal(branch["properties"], &props)
			for name, value := range props {
				if previous, ok := branchProperties[name]; ok && !anthropicSchemaJSONEqual(previous, value) {
					join := "anyOf"
					if keyword == "allOf" {
						join = "allOf"
					}
					value, _ = json.Marshal(map[string][]json.RawMessage{join: {previous, value}})
				}
				branchProperties[name] = value
			}
			var names []string
			_ = json.Unmarshal(branch["required"], &names)
			if keyword == "allOf" {
				branchRequired = appendRequiredNames(branchRequired, names)
			} else if i == 0 {
				branchRequired = names
			} else {
				branchRequired = slices.DeleteFunc(branchRequired, func(name string) bool { return !slices.Contains(names, name) })
			}
		}
		for name, value := range branchProperties {
			if previous, ok := properties[name]; ok && !anthropicSchemaJSONEqual(previous, value) {
				value, _ = json.Marshal(map[string][]json.RawMessage{"allOf": {previous, value}})
			}
			properties[name] = value
		}
		required = appendRequiredNames(required, branchRequired)
		delete(schema, keyword)
	}
	schema["type"] = rawJSONString("object")
	if _, exists := schema["properties"]; !exists || merged {
		schema["properties"], _ = json.Marshal(properties)
	}
	if merged {
		required = appendRequiredNames(required, rootRequired)
		if len(required) > 0 {
			schema["required"], _ = json.Marshal(required)
		}
	}
	return nil
}

func appendRequiredNames(into, names []string) []string {
	for _, name := range names {
		if !slices.Contains(into, name) {
			into = append(into, name)
		}
	}
	return into
}

func anthropicSchemaJSONEqual(left, right json.RawMessage) bool {
	var a, b bytes.Buffer
	return json.Compact(&a, left) == nil && json.Compact(&b, right) == nil && a.String() == b.String()
}
