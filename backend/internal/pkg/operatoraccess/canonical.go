package operatoraccess

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CanonicalError identifies the body or query field rejected before binding.
// The service/middleware maps it to ADMIN_FIELD_FORBIDDEN, cause noncanonical.
type CanonicalError struct{ Field string }

func (e *CanonicalError) Error() string {
	return fmt.Sprintf("operatoraccess: noncanonical %s", e.Field)
}

func CheckCanonical(contentType string, body []byte, query url.Values, queryKeys []string) error {
	if len(bytes.TrimSpace(body)) > 0 {
		media, _, err := mime.ParseMediaType(contentType)
		if err != nil || media != "application/json" || !utf8.Valid(body) {
			return &CanonicalError{Field: "body"}
		}
		d := json.NewDecoder(bytes.NewReader(body))
		d.UseNumber()
		tok, err := d.Token()
		if err != nil || tok != json.Delim('{') {
			return &CanonicalError{Field: "body"}
		}
		members := 0
		if err = canonicalObject(d, 1, &members); err != nil {
			return &CanonicalError{Field: "body"}
		}
		if _, err = d.Token(); err != io.EOF {
			return &CanonicalError{Field: "body"}
		}
	}
	for _, k := range queryKeys {
		if len(query[k]) > 1 {
			return &CanonicalError{Field: k}
		}
	}
	return nil
}

func canonicalObject(d *json.Decoder, depth int, members *int) error {
	if depth > 64 {
		return fmt.Errorf("too deep")
	}
	keys := map[string]struct{}{}
	for d.More() {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok {
			return fmt.Errorf("invalid key")
		}
		*members++
		if *members > 100000 {
			return fmt.Errorf("too many members")
		}
		folded := foldCanonical(key)
		if _, seen := keys[folded]; seen {
			return fmt.Errorf("duplicate key")
		}
		keys[folded] = struct{}{}
		if err = canonicalValue(d, depth, members); err != nil {
			return err
		}
	}
	tok, err := d.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim('}') {
		return fmt.Errorf("invalid object")
	}
	return nil
}
func canonicalValue(d *json.Decoder, depth int, members *int) error {
	tok, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		return canonicalObject(d, depth+1, members)
	case '[':
		if depth+1 > 64 {
			return fmt.Errorf("too deep")
		}
		for d.More() {
			if err = canonicalValue(d, depth+1, members); err != nil {
				return err
			}
		}
		tok, err = d.Token()
		if err != nil {
			return err
		}
		if tok != json.Delim(']') {
			return fmt.Errorf("invalid array")
		}
		return nil
	default:
		return fmt.Errorf("invalid value")
	}
}

// Field matches encoding/json's case folding. CheckCanonical must run first
// whenever the result is used to authorize a request.
func Field[V any](obj map[string]V, name string) (V, bool) {
	if v, ok := obj[name]; ok {
		return v, true
	}
	for k, v := range obj {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	var zero V
	return zero, false
}

// foldCanonical gives every Unicode SimpleFold orbit one representative.
func foldCanonical(key string) string {
	return strings.Map(func(r rune) rune {
		smallest := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < smallest {
				smallest = next
			}
		}
		return smallest
	}, key)
}
