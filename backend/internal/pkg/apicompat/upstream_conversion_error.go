package apicompat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// UpstreamConversionError describes a generated value that cannot be projected.
type UpstreamConversionError struct{ Field, Reason, SourceProtocol, SourceReason string }

func (e *UpstreamConversionError) Error() string {
	return fmt.Sprintf("upstream conversion failed at %s: %s", e.Field, e.Reason)
}

func ValidateToolArguments(kind, raw string, requireObject, opaque bool) error {
	invalid := func(reason string) error { return &UpstreamConversionError{Field: "arguments", Reason: reason} }
	switch kind {
	case "custom":
		return nil
	case "function":
	default:
		return invalid("unsupported tool kind")
	}
	if opaque {
		return nil
	}
	if !json.Valid([]byte(raw)) {
		return invalid("expected complete JSON")
	}
	if requireObject && !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return invalid("expected a JSON object")
	}
	return nil
}
