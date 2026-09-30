package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

const (
	maxOpenAIConcatenatedJSONDocuments = 16
	maxOpenAIConcatenatedJSONBytes     = 16 * 1024 * 1024
)

// splitOpenAIConcatenatedJSONDocuments recognizes the narrow corruption shape
// produced when multiple complete Responses events arrive in one transport
// message. Other malformed payloads are left untouched for normal error paths.
func splitOpenAIConcatenatedJSONDocuments(payload []byte) ([][]byte, bool) {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || len(payload) > maxOpenAIConcatenatedJSONBytes || json.Valid(payload) {
		return nil, false
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	documents := make([][]byte, 0, 2)
	for {
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		if err != nil {
			if err == io.EOF && len(documents) > 1 {
				return documents, true
			}
			return nil, false
		}
		raw = bytes.TrimSpace(raw)
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, false
		}
		eventType := strings.TrimSpace(envelope.Type)
		if eventType == "" || strings.ContainsAny(eventType, "\r\n") {
			return nil, false
		}
		if len(documents) == maxOpenAIConcatenatedJSONDocuments {
			return nil, false
		}
		documents = append(documents, raw)
	}
}

type openAISSEJSONDocumentScanner struct {
	scanner *bufio.Scanner
	pending []string
	current string
}

func newOpenAISSEJSONDocumentScanner(scanner *bufio.Scanner) *openAISSEJSONDocumentScanner {
	return &openAISSEJSONDocumentScanner{scanner: scanner}
}

func (s *openAISSEJSONDocumentScanner) Scan() bool {
	if len(s.pending) > 0 {
		s.current = s.pending[0]
		s.pending = s.pending[1:]
		return true
	}
	if s.scanner == nil || !s.scanner.Scan() {
		return false
	}

	// The scanner owns these bytes until its next Scan. Validate/repair them
	// synchronously, then retain only the immutable string exposed to callers.
	line := s.scanner.Bytes()
	if !bytes.HasPrefix(line, []byte("data:")) {
		s.current = string(line)
		return true
	}
	data := line[len("data:"):]
	for len(data) > 0 && (data[0] == ' ' || data[0] == '\t') {
		data = data[1:]
	}
	if len(data) > maxOpenAIConcatenatedJSONBytes {
		s.current = string(line)
		return true
	}
	documents, repaired := splitOpenAIConcatenatedJSONDocuments(data)
	if !repaired {
		s.current = string(line)
		return true
	}

	expanded := make([]string, 0, len(documents)*3)
	for i, document := range documents {
		if i > 0 {
			var envelope struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(document, &envelope)
			expanded = append(expanded, "event: "+strings.TrimSpace(envelope.Type))
		}
		expanded = append(expanded, "data: "+string(document), "")
	}
	s.current = expanded[0]
	s.pending = expanded[1:]
	return true
}

func (s *openAISSEJSONDocumentScanner) Text() string {
	return s.current
}

func (s *openAISSEJSONDocumentScanner) Err() error {
	if s.scanner == nil {
		return nil
	}
	return s.scanner.Err()
}
