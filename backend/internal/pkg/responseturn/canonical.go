package responseturn

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
)

// NormalizedBodyHash removes insignificant whitespace and object key order,
// preserving exact number representation rather than losing integer precision.
func NormalizedBodyHash(body []byte) ([32]byte, error) {
	_, canonical, err := decodeBody(body)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(canonical), nil
}
func decodeBody(body []byte) (map[string]any, []byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var value map[string]any
	if err := dec.Decode(&value); err != nil || value == nil {
		return nil, nil, &Error{"invalid_request", 400, "Request body must be a JSON object"}
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, nil, &Error{"invalid_request", 400, "Request body contains trailing data"}
	}
	// Validate ordinary JSON (including its nesting bound) before examining
	// duplicate keys. Other protocol layers may use first-key-wins parsers.
	keys := json.NewDecoder(bytes.NewReader(body))
	keys.UseNumber()
	if err := rejectDuplicateKeys(keys); err != nil {
		return nil, nil, &Error{"invalid_request", 400, "Request body must have unique JSON object keys"}
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, nil, &Error{"invalid_request", 400, "Invalid request body"}
	}
	return value, canonical, nil
}

func rejectDuplicateKeys(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return ErrConflict
			}
			if _, duplicate := seen[name]; duplicate {
				return ErrConflict
			}
			seen[name] = struct{}{}
			if err := rejectDuplicateKeys(dec); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := rejectDuplicateKeys(dec); err != nil {
				return err
			}
		}
	default:
		return ErrConflict
	}
	_, err = dec.Token()
	return err
}
