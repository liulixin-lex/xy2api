package responseturn

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"
)

func backgroundOptions(store bool) CreateOptions {
	o := testOptions()
	o.Body = []byte(`{"background":true,"stream":false,"store":true,"model":"fixture"}`)
	if !store {
		o.Body = []byte(`{"background":true,"stream":false,"store":false,"model":"fixture"}`)
	}
	o.Capabilities = Capabilities{}
	return o
}

func TestReviewCancelledBackgroundLateAcknowledgementKeepsControl(t *testing.T) {
	for _, store := range []bool{false, true} {
		for _, reason := range []Reason{ReasonClientDetached, ReasonUserStop, ReasonAdminCancel} {
			t.Run(fmt.Sprintf("store=%v/%s", store, reason), func(t *testing.T) {
				m, clock := testManager(t, Config{})
				turn, _, err := m.Create(backgroundOptions(store))
				if err != nil {
					t.Fatal(err)
				}
				if reason == ReasonAdminCancel {
					if err := m.Close(); err != nil {
						t.Fatal(err)
					}
				} else {
					turn.Cancel(reason)
				}
				before := turn.Snapshot()
				finished := turn.finished
				clock.Add(time.Second)
				queued := []byte(`{"id":"resp-late","background":true,"status":"queued","output":[]}`)
				caps := Capabilities{Protocol: "responses_http", Verified: true, Retrieve: true}
				err = turn.AcceptBackground(42, "resp-late", queued, caps)
				after := turn.Snapshot()
				canCancel := turn.BeginUpstreamCancel()
				t.Logf("ack=%v state=%s reason=%s owner=%d response=%s accepted=%v cancel=%v body=%d", err, after.State, after.Reason, after.OwnerAccountID, after.UpstreamResponseID, after.BackgroundAccepted, canCancel, len(after.Result))
				if !errors.Is(err, ErrTerminal) || !canCancel || turn.BeginUpstreamCancel() || !after.BackgroundAccepted || after.OwnerAccountID != 42 || after.UpstreamResponseID != "resp-late" {
					t.Fatal("late verified acknowledgement lost original background cancellation identity")
				}
				if after.State != before.State || after.Reason != reason || !turn.finished.Equal(finished) || len(after.Result) != 0 || m.ResourceUsage().ProcessBytes != 0 || turn.Context().Err() == nil || after.Recoverable {
					t.Fatal("late acknowledgement changed cancelled lifecycle or retained body")
				}
			})
		}
	}
}

func TestReviewCancelledBackgroundLateAcknowledgementRejectsForeignProof(t *testing.T) {
	caps := Capabilities{Protocol: "responses_http", Verified: true, Retrieve: true}
	queued := []byte(`{"id":"resp-original","background":true,"status":"queued"}`)
	for _, kind := range []string{"owner", "identity", "invalid_json", "no_background", "no_capability", "ordinary", "stream", "completed", "removed_handle"} {
		t.Run(kind, func(t *testing.T) {
			m, clock := testManager(t, Config{IdempotencyTTL: time.Second, TerminalTTL: time.Second})
			o := backgroundOptions(true)
			if kind == "ordinary" {
				o.Body = []byte(`{"stream":false}`)
			}
			if kind == "stream" {
				o.Body = []byte(`{"background":true,"stream":true}`)
			}
			turn := mustCreate(t, m, o)
			if kind == "completed" {
				if err := turn.Finish(StateCompleted, "", nil); err != nil {
					t.Fatal(err)
				}
			} else {
				turn.Cancel(ReasonUserStop)
			}
			owner, id, body, proof := int64(42), "resp-original", queued, caps
			switch kind {
			case "owner":
				owner = 43
			case "identity":
				id = "resp-foreign"
				body = []byte(`{"id":"resp-foreign","background":true,"status":"queued"}`)
			case "invalid_json":
				body = []byte(`{`)
			case "no_background":
				body = []byte(`{"id":"resp-original","status":"queued"}`)
			case "no_capability":
				proof = Capabilities{}
			case "removed_handle":
				clock.Add(2 * time.Second)
				m.Sweep()
			}
			if err := turn.AcceptBackground(owner, id, body, proof); err == nil {
				t.Fatal("foreign or ineligible late proof accepted")
			}
			if turn.Snapshot().BackgroundAccepted || turn.BeginUpstreamCancel() {
				t.Fatal("foreign proof acquired cancellation authority")
			}
		})
	}
}

func TestReviewCancelledSSEBackgroundLateAcknowledgementKeepsControl(t *testing.T) {
	for _, reason := range []Reason{ReasonClientDetached, ReasonUserStop, ReasonAdminCancel} {
		t.Run(string(reason), func(t *testing.T) {
			m, clock := testManager(t, Config{})
			o := testOptions()
			o.Capabilities = Capabilities{}
			turn, _, err := m.Create(o)
			if err != nil {
				t.Fatal(err)
			}
			if reason == ReasonAdminCancel {
				if err := m.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				turn.Cancel(reason)
			}
			before := turn.Snapshot()
			finished := turn.finished
			clock.Add(time.Second)
			proof := []byte(`{"id":"resp-sse-late","background":true,"status":"in_progress"}`)
			caps := Capabilities{Protocol: "responses_http_sse", Verified: true, Retrieve: true, NativeCursor: true}
			if err := turn.RecordCancelledBackgroundControl(42, "resp-sse-late", proof, caps); err != nil {
				t.Fatal(err)
			}
			after := turn.Snapshot()
			if !after.BackgroundAccepted || !turn.BeginUpstreamCancel() || turn.BeginUpstreamCancel() || after.OwnerAccountID != 42 || after.UpstreamResponseID != "resp-sse-late" {
				t.Fatal("late SSE proof lost original cancellation identity")
			}
			if after.State != before.State || after.Reason != reason || after.Recoverable || !turn.finished.Equal(finished) || len(after.Result) != 0 || after.JournalEvents != 0 || after.Attachments != before.Attachments || m.ResourceUsage().ProcessBytes != 0 || turn.Context().Err() == nil {
				t.Fatal("late SSE proof changed lifecycle or recovery state")
			}
		})
	}
}

func TestReviewCancelledBackgroundControlRejectsUnverifiedIdentity(t *testing.T) {
	for _, kind := range []string{"running", "completed", "owner", "identity", "invalid_json", "no_background", "no_capability", "ordinary", "removed_handle"} {
		t.Run(kind, func(t *testing.T) {
			m, clock := testManager(t, Config{IdempotencyTTL: time.Second, TerminalTTL: time.Second})
			o := testOptions()
			if kind == "ordinary" {
				o.Body = []byte(`{"stream":true}`)
			}
			turn := mustCreate(t, m, o)
			if kind == "completed" {
				if err := turn.Finish(StateCompleted, "", nil); err != nil {
					t.Fatal(err)
				}
			} else if kind != "running" {
				turn.Cancel(ReasonUserStop)
			}
			owner, id := int64(42), "resp-original"
			proof := []byte(`{"id":"resp-original","background":true,"status":"in_progress"}`)
			caps := Capabilities{Protocol: "responses_http_sse", Verified: true, Retrieve: true, NativeCursor: true}
			switch kind {
			case "owner":
				owner = 43
			case "identity":
				id = "resp-foreign"
				proof = []byte(`{"id":"resp-foreign","background":true,"status":"queued"}`)
			case "invalid_json":
				proof = []byte(`{`)
			case "no_background":
				proof = []byte(`{"id":"resp-original","status":"in_progress"}`)
			case "no_capability":
				caps = Capabilities{}
			case "removed_handle":
				clock.Add(2 * time.Second)
				m.Sweep()
			}
			if err := turn.RecordCancelledBackgroundControl(owner, id, proof, caps); err == nil {
				t.Fatal("unverified or ineligible control proof accepted")
			}
			if turn.Snapshot().BackgroundAccepted || turn.BeginUpstreamCancel() {
				t.Fatal("invalid proof acquired cancellation authority")
			}
		})
	}
}

func TestReviewBackgroundAcceptancePollingAndTerminal(t *testing.T) {
	m, clock := testManager(t, Config{})
	o := backgroundOptions(true)
	turn := mustCreate(t, m, o)
	if turn.Snapshot().BackgroundAccepted || turn.BeginUpstreamCancel() {
		t.Fatal("request intent counted as accepted background execution")
	}
	queued := []byte(`{"id":"resp-original","background":true,"status":"queued","output":[]}`)
	caps := Capabilities{Protocol: "responses_http", Verified: true, Retrieve: true}
	if err := turn.AcceptBackground(42, "resp-original", queued, caps); err != nil {
		t.Fatal(err)
	}
	a := mustAttach(t, turn, nil, false)
	a.Close()
	clock.Add(10 * time.Minute)
	snapshot := turn.Snapshot()
	if !snapshot.BackgroundAccepted || snapshot.State != StateRunning || snapshot.Recoverable || snapshot.OfflineUsed != 0 || !bytes.Equal(snapshot.Result, queued) {
		t.Fatalf("accepted background state: %+v", snapshot)
	}
	if duplicate, created, err := m.Create(o); err != nil || created || duplicate != turn {
		t.Fatalf("active duplicate creates generation: %v %v", created, err)
	}
	running := []byte(`{"id":"resp-original","background":true,"status":"in_progress","output":[]}`)
	if err := turn.UpdateBackgroundSnapshot(running); err != nil {
		t.Fatal(err)
	}
	if usage := m.ResourceUsage(); usage.ProcessBytes != int64(len(running)) {
		t.Fatalf("poll snapshot duplicated accounting: %+v", usage)
	}
	complete := []byte(`{"id":"resp-original","background":true,"status":"completed","output":[]}`)
	if err := turn.Finish(StateCompleted, "", complete); err != nil {
		t.Fatal(err)
	}
	if got := turn.Snapshot(); got.State != StateCompleted || !bytes.Equal(got.Result, complete) {
		t.Fatalf("terminal snapshot: %+v", got)
	}
	if err := turn.UpdateBackgroundSnapshot(running); !errors.Is(err, ErrTerminal) {
		t.Fatalf("terminal execution revived: %v", err)
	}
}

func TestReviewBackgroundCapabilityIdentityAndStorePolicy(t *testing.T) {
	queued := []byte(`{"id":"resp-original","background":true,"status":"queued"}`)
	caps := Capabilities{Protocol: "responses_http", Verified: true, Retrieve: true}
	for _, store := range []bool{true, false} {
		m, _ := testManager(t, Config{})
		turn := mustCreate(t, m, backgroundOptions(store))
		if err := turn.AcceptBackground(42, "resp-original", queued, Capabilities{}); !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
		if err := turn.AcceptBackground(43, "resp-original", queued, caps); !errors.Is(err, ErrOwner) {
			t.Fatal(err)
		}
		if err := turn.AcceptBackground(42, "resp-other", queued, caps); !errors.Is(err, ErrConsistency) {
			t.Fatal(err)
		}
		if err := turn.AcceptBackground(42, "resp-original", queued, caps); err != nil {
			t.Fatal(err)
		}
		if err := turn.UpdateBackgroundSnapshot([]byte(`{"id":"resp-other","background":true,"status":"in_progress"}`)); !errors.Is(err, ErrConsistency) {
			t.Fatal(err)
		}
		if turn.Snapshot().Recoverable {
			t.Fatal("retrieve capability became stream resume capability")
		}
		turn.Cancel(ReasonUserStop)
		if !turn.BeginUpstreamCancel() || turn.BeginUpstreamCancel() {
			t.Fatal("native cancellation not fenced")
		}
		if !store && (len(turn.Snapshot().Result) != 0 || m.ResourceUsage().ProcessBytes != 0) {
			t.Fatal("store:false retained result body")
		}
	}
}

func TestReviewBackgroundSnapshotQuotaAndDeadline(t *testing.T) {
	m, _ := testManager(t, Config{MaxTurnBytes: 32})
	turn := mustCreate(t, m, backgroundOptions(true))
	queued := []byte(`{"id":"resp-original","background":true,"status":"queued"}`)
	caps := Capabilities{Protocol: "responses_http", Verified: true, Retrieve: true}
	if err := turn.AcceptBackground(42, "resp-original", queued, caps); CancellationReason(err) != ReasonQuota {
		t.Fatal(err)
	}
	if !turn.BeginUpstreamCancel() || turn.Snapshot().Reason != ReasonQuota || m.ResourceUsage().ProcessBytes != 0 || turn.Context().Err() == nil {
		t.Fatal("overquota accepted background execution stayed live")
	}
}

func TestReviewBackgroundCancellationResultPreservesTerminal(t *testing.T) {
	for _, store := range []bool{false, true} {
		m, clock := testManager(t, Config{TerminalTTL: time.Second})
		turn := mustCreate(t, m, backgroundOptions(store))
		queued := []byte(`{"id":"resp-original","background":true,"status":"queued"}`)
		caps := Capabilities{Protocol: "responses_http", Verified: true, Retrieve: true}
		if err := turn.AcceptBackground(42, "resp-original", queued, caps); err != nil {
			t.Fatal(err)
		}
		confirmed := []byte(`{"id":"resp-original","status":"cancelled","usage":{"total_tokens":12}}`)
		if err := turn.RecordCancellationResult(confirmed); !errors.Is(err, ErrTerminal) {
			t.Fatalf("live execution accepted cancellation result: %v", err)
		}
		turn.Cancel(ReasonAdminCancel)
		before := turn.Snapshot()
		if err := turn.RecordCancellationResult(queued); !errors.Is(err, ErrConsistency) {
			t.Fatal(err)
		}
		if err := turn.RecordCancellationResult(confirmed); err != nil {
			t.Fatal(err)
		}
		after := turn.Snapshot()
		if !after.CancelConfirmed || after.State != StateCancelled || after.Reason != ReasonAdminCancel || !after.FinishedAt.Equal(before.FinishedAt) || turn.Context().Err() == nil {
			t.Fatalf("late confirmation revived or replaced original terminal: %+v", after)
		}
		if store && !bytes.Equal(after.Result, confirmed) {
			t.Fatal("verified cancellation body lost")
		}
		if !store && (len(after.Result) > 0 || m.ResourceUsage().ProcessBytes != 0) {
			t.Fatal("store:false cancellation body retained")
		}
		clock.Add(2 * time.Second)
		m.Sweep()
		if store {
			if err := turn.RecordCancellationResult(confirmed); !errors.Is(err, ErrExpired) {
				t.Fatalf("late cancellation extended retention: %v", err)
			}
		}
	}
}
