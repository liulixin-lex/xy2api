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
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, nil, &Error{"invalid_request", 400, "Invalid request body"}
	}
	return value, canonical, nil
}
