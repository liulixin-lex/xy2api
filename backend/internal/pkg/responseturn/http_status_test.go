package responseturn

import (
	"context"
	"errors"
	"testing"
)

func TestReviewNonstreamHTTPStatusPreservedForIdempotency(t *testing.T) {
	for _, status := range []int{0, 200, 201, 400, 429, 500, 599} {
		m, _ := testManager(t, Config{})
		o := testOptions()
		o.Body = []byte(`{"stream":false}`)
		turn := mustCreate(t, m, o)
		a := mustAttach(t, turn, nil, false)
		frame := Event{Data: []byte(`{"error":{"code":"fixture_error"}}`), HTTPStatus: status, Terminal: StateFailed, Result: []byte(`{"error":{"code":"fixture_error"}}`)}
		mustPublish(t, turn, frame)
		_ = next(t, a)
		duplicate, created, err := m.Create(o)
		if err != nil || created || duplicate != turn {
			t.Fatalf("duplicate execution: %v %v", created, err)
		}
		want := status
		if want == 0 {
			want = 200
		}
		if got := duplicate.Snapshot(); got.HTTPStatus != want || string(got.Result) != string(frame.Result) {
			t.Fatalf("status/result changed: %+v", got)
		}
	}
}

func TestReviewHTTPStatusRejectsInvalidAndConflictingValues(t *testing.T) {
	for _, status := range []int{-1, 101, 199, 600, 999} {
		m, _ := testManager(t, Config{})
		o := testOptions()
		o.Body = []byte(`{"stream":false}`)
		turn := mustCreate(t, m, o)
		if _, err := turn.Publish(context.Background(), Event{HTTPStatus: status}); !errors.Is(err, ErrConsistency) {
			t.Fatalf("invalid status %d accepted: %v", status, err)
		}
		if turn.Snapshot().HTTPStatus != 200 {
			t.Fatal("invalid status polluted snapshot")
		}
	}
	m, _ := testManager(t, Config{})
	o := testOptions()
	o.Body = []byte(`{"stream":false}`)
	turn := mustCreate(t, m, o)
	a := mustAttach(t, turn, nil, false)
	mustPublish(t, turn, Event{HTTPStatus: 201, Data: []byte(`{"status":"queued"}`)})
	_ = next(t, a)
	if _, err := turn.Publish(context.Background(), Event{HTTPStatus: 500}); !errors.Is(err, ErrConsistency) {
		t.Fatalf("response status changed: %v", err)
	}
	if turn.Snapshot().HTTPStatus != 201 {
		t.Fatal("committed status overwritten")
	}
}

func TestReviewStreamingEventCannotChangeHTTPStatus(t *testing.T) {
	m, _ := testManager(t, Config{})
	turn := mustCreate(t, m, testOptions())
	a := mustAttach(t, turn, nil, false)
	frame := event(0, "response.created")
	frame.HTTPStatus = 429
	mustPublish(t, turn, frame)
	_ = next(t, a)
	if turn.Snapshot().HTTPStatus != 200 {
		t.Fatal("stream event rewrote HTTP status")
	}
	if _, err := turn.Publish(context.Background(), Event{HTTPStatus: 400, JSONResponse: true, Data: []byte(`{"error":{}}`)}); !errors.Is(err, ErrConsistency) {
		t.Fatalf("late JSON replaced committed SSE: %v", err)
	}
	if turn.Snapshot().HTTPStatus != 200 {
		t.Fatal("late JSON overwrote SSE status")
	}
}

func TestReviewStreamRequestInitialJSONErrorPreservesStatus(t *testing.T) {
	m, _ := testManager(t, Config{})
	o := testOptions()
	turn := mustCreate(t, m, o)
	a := mustAttach(t, turn, nil, false)
	body := []byte(`{"error":{"code":"invalid_request"}}`)
	mustPublish(t, turn, Event{HTTPStatus: 400, JSONResponse: true, Data: body, Result: body, Terminal: StateFailed})
	_ = next(t, a)
	duplicate, created, err := m.Create(o)
	if err != nil || created || duplicate != turn {
		t.Fatalf("duplicate stream request: %v %v", created, err)
	}
	if got := duplicate.Snapshot(); got.HTTPStatus != 400 || string(got.Result) != string(body) {
		t.Fatalf("requested stream erased initial JSON error status: %+v", got)
	}
}
