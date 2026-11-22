package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// NewCollabLocalEndingEvent generates an independent, non-UUID notice number.
// Failure must fall back to the saved endpoint before any local bytes are sent.
func NewCollabLocalEndingEvent() (string, error) {
	return newCollabLocalID("cle_", rand.Reader)
}

func newCollabLocalID(prefix string, entropy io.Reader) (string, error) {
	var id [16]byte
	if _, err := io.ReadFull(entropy, id[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(id[:]), nil
}

// CollabLocalEndingResponseState describes bytes already delivered, not merely
// received or buffered. If CreatedDelivered is false, an empty ResponseID asks
// the writer to generate an identity and creation time. Otherwise both are
// preserved, including a zero creation time. NextSequence is the next unused
// event number, including after any delivered lifecycle events.
type CollabLocalEndingResponseState struct {
	CreatedDelivered bool
	ResponseID       string
	CreatedAt        int64
	NextSequence     int64
}

type collabLocalPart struct {
	Type        string   `json:"type"`
	Text        string   `json:"text"`
	Annotations []string `json:"annotations"`
}

type collabLocalMessage struct {
	ID      string            `json:"id"`
	Type    string            `json:"type"`
	Role    string            `json:"role"`
	Status  string            `json:"status"`
	Phase   string            `json:"phase"`
	Content []collabLocalPart `json:"content"`
}

// Dedicated wire types intentionally cannot carry usage or continuation data.
type collabLocalResponse struct {
	ID        string               `json:"id"`
	Object    string               `json:"object"`
	CreatedAt int64                `json:"created_at"`
	Model     string               `json:"model"`
	Status    string               `json:"status"`
	Error     *json.RawMessage     `json:"error,omitempty"`
	Output    []collabLocalMessage `json:"output"`
}

type collabLocalEvent struct {
	Type           string               `json:"type"`
	SequenceNumber int64                `json:"sequence_number"`
	Response       *collabLocalResponse `json:"response,omitempty"`
	OutputIndex    *int                 `json:"output_index,omitempty"`
	ContentIndex   *int                 `json:"content_index,omitempty"`
	ItemID         string               `json:"item_id,omitempty"`
	Item           *collabLocalMessage  `json:"item,omitempty"`
	Part           *collabLocalPart     `json:"part,omitempty"`
	Delta          *string              `json:"delta,omitempty"`
	Text           *string              `json:"text,omitempty"`
}

// WriteCollabLocalEnding writes one local notice. Before calling, the owner must
// stop and join heartbeat writers, close the old body, discard undelivered
// buffers, freeze upstream usage/generation, and recheck the eligibility facts.
// claimTerminal must atomically claim the request's shared terminal immediately
// before its final write and retain the claim on failure. It runs on the request
// goroutine. No billing, account effects, records or delivery hooks run here.
// The tree's count is reserved before this is called. Only complete=true
// permits registering the provisional termination.
// Flush always goes through the writer itself: wrappers may withhold or count
// output, and not all of them can be unwrapped. ctx is the client request's
// context and is checked after each one: HTTP/1 cancels it synchronously when a
// flush cannot be written.
//
// complete means handed to the connection, not received: HTTP/2 and proxies
// accept bytes the client may never get. So a termination is effective for a
// later request only if its history carries the notice's event number; without
// that receipt the original is sent first.
func WriteCollabLocalEnding(ctx context.Context, w gin.ResponseWriter, state CollabLocalEndingResponseState, model, notice string, stream bool, claimTerminal func() bool) (complete bool, err error) {
	if ctx == nil || w == nil || claimTerminal == nil || state.NextSequence < 0 || state.NextSequence > math.MaxInt64-8 ||
		state.CreatedDelivered && (state.ResponseID == "" || !stream) {
		return false, errors.New("invalid local ending response state")
	}
	if err := context.Cause(ctx); err != nil {
		return false, err
	}
	if state.ResponseID == "" {
		state.ResponseID, err = newCollabLocalID("resp_", rand.Reader)
		if err != nil {
			return false, err
		}
		state.CreatedAt = time.Now().Unix()
	}
	part := collabLocalPart{Type: "output_text", Text: notice, Annotations: []string{}}
	message := collabLocalMessage{
		ID: collabLocalMessageID(state.ResponseID), Type: "message", Role: "assistant",
		Status: "completed", Phase: "final_answer", Content: []collabLocalPart{part},
	}
	null := json.RawMessage("null")
	response := collabLocalResponse{
		ID: state.ResponseID, Object: "response", CreatedAt: state.CreatedAt,
		Model: model, Status: "completed", Error: &null, Output: []collabLocalMessage{message},
	}
	if !stream {
		body, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return false, marshalErr
		}
		if !claimTerminal() {
			return false, errors.New("local ending terminal already claimed")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Del("Content-Length")
		w.WriteHeader(http.StatusOK)
		err = writeCollabLocalBytes(ctx, w, body)
		return err == nil, err
	}

	zero := 0
	emptyPart := collabLocalPart{Type: "output_text", Text: "", Annotations: []string{}}
	openingMessage := message
	openingMessage.Status, openingMessage.Content = "in_progress", []collabLocalPart{}
	openingResponse := response
	openingResponse.Status, openingResponse.Error, openingResponse.Output = "in_progress", nil, []collabLocalMessage{}
	events := []collabLocalEvent{}
	if !state.CreatedDelivered {
		events = append(events, collabLocalEvent{Type: "response.created", Response: &openingResponse})
	}
	events = append(events,
		collabLocalEvent{Type: "response.output_item.added", OutputIndex: &zero, Item: &openingMessage},
		collabLocalEvent{Type: "response.content_part.added", ItemID: message.ID, OutputIndex: &zero, ContentIndex: &zero, Part: &emptyPart},
		collabLocalEvent{Type: "response.output_text.delta", ItemID: message.ID, OutputIndex: &zero, ContentIndex: &zero, Delta: &notice},
		collabLocalEvent{Type: "response.output_text.done", ItemID: message.ID, OutputIndex: &zero, ContentIndex: &zero, Text: &notice},
		collabLocalEvent{Type: "response.content_part.done", ItemID: message.ID, OutputIndex: &zero, ContentIndex: &zero, Part: &part},
		collabLocalEvent{Type: "response.output_item.done", OutputIndex: &zero, Item: &message},
		collabLocalEvent{Type: "response.completed", Response: &response},
	)
	for i, event := range events {
		event.SequenceNumber = state.NextSequence + int64(i)
		payload, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			return false, marshalErr
		}
		if event.Type == "response.completed" && !claimTerminal() {
			return false, errors.New("local ending terminal already claimed")
		}
		if i == 0 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Del("Content-Length")
			w.WriteHeader(http.StatusOK)
		}
		frame := append([]byte("event: "+event.Type+"\ndata: "), payload...)
		frame = append(frame, '\n', '\n')
		if err := writeCollabLocalBytes(ctx, w, frame); err != nil {
			return false, err
		}
	}
	return true, nil
}

func writeCollabLocalBytes(ctx context.Context, w gin.ResponseWriter, data []byte) error {
	n, err := w.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	w.Flush()
	return context.Cause(ctx)
}

// The client keeps this ID in its history and replays it as a message item,
// and upstreams refuse a message ID without the msg prefix: any other shape
// would fail every later turn of the thread. Hashing also bounds the length.
func collabLocalMessageID(responseID string) string {
	sum := sha256.Sum256([]byte("collab-local-ending:" + responseID))
	return "msg_" + hex.EncodeToString(sum[:16])
}
