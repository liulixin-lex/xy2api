package service

import (
	"github.com/tidwall/gjson"
	"strings"
)

// Missing and JSON null IDs denote independent input only on a named FCO.
func isNamedStandaloneFunctionCallOutput(itemType, callID, name any) bool {
	value, ok := name.(string)
	return itemType == "function_call_output" && callID == nil && ok && strings.TrimSpace(value) != ""
}

func functionCallOutputCallIDShape(item gjson.Result) string {
	switch {
	case !item.Exists():
		return "missing"
	case item.Type == gjson.Null:
		return "null"
	case item.Type != gjson.String:
		return "wrong_type"
	case item.String() == "":
		return "empty"
	case strings.TrimSpace(item.String()) == "":
		return "whitespace"
	default:
		return "string"
	}
}

func isNamedStandaloneFunctionCallOutputJSON(item gjson.Result) bool {
	name := item.Get("name")
	return name.Type == gjson.String && item.Get("call_id").Type == gjson.Null && isNamedStandaloneFunctionCallOutput(item.Get("type").String(), nil, name.Str)
}
