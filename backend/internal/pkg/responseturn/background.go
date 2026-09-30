package responseturn

// AcceptBackground records a verified upstream acknowledgement of a non-stream
// background response. Retrieval capability does not imply native cursor or
// streaming recovery. The host must keep the original account reservation and
// execution deadline while polling this response, never create a replacement.
func (t *Turn) AcceptBackground(accountID int64, responseID string, result []byte, capabilities Capabilities) error {
	if err := validateBackgroundControl(accountID, responseID, result, capabilities); err != nil {
		return err
	}
	m := t.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepTurnLocked(t, m.cfg.Now())
	if m.turns[t.id] != t {
		return ErrExpired
	}
	if !t.background || t.stream {
		return ErrUnavailable
	}
	if t.state == StateCancelled {
		if err := t.recordCancelledBackgroundControlLocked(accountID, responseID); err != nil {
			return err
		}
		return ErrTerminal
	}
	if m.closed {
		return ErrClosed
	}
	if t.isTerminalLocked() || t.expired {
		return ErrTerminal
	}
	if err := t.bindOwnerLocked(accountID, responseID); err != nil {
		return err
	}
	t.backgroundAccepted = true
	return t.updateBackgroundSnapshotLocked(result)
}

// RecordCancelledBackgroundControl records only the verified control identity of
// a background acknowledgement that arrived after cancellation or shutdown. Both
// JSON and SSE adapters may use it to cancel the original accepted generation.
// It does not revive execution, extend retention, retain body, or enable recovery.
func (t *Turn) RecordCancelledBackgroundControl(accountID int64, responseID string, proof []byte, capabilities Capabilities) error {
	if err := validateBackgroundControl(accountID, responseID, proof, capabilities); err != nil {
		return err
	}
	m := t.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepTurnLocked(t, m.cfg.Now())
	return t.recordCancelledBackgroundControlLocked(accountID, responseID)
}

func validateBackgroundControl(accountID int64, responseID string, proof []byte, capabilities Capabilities) error {
	if accountID <= 0 || responseID == "" || !capabilities.Verified || capabilities.Protocol == "" || !capabilities.Retrieve {
		return ErrUnavailable
	}
	resultID, err := decodeBackgroundSnapshot(proof)
	if err != nil {
		return err
	}
	if resultID != responseID {
		return ErrConsistency
	}
	return nil
}

func (t *Turn) recordCancelledBackgroundControlLocked(accountID int64, responseID string) error {
	if t.manager.turns[t.id] != t {
		return ErrExpired
	}
	if !t.background {
		return ErrUnavailable
	}
	if t.state != StateCancelled {
		return ErrTerminal
	}
	if err := t.bindOwnerLocked(accountID, responseID); err != nil {
		return err
	}
	t.backgroundAccepted = true
	return nil
}

// UpdateBackgroundSnapshot replaces only the bounded query result. It is not a
// protocol event, a completed generation, or a new billing/attempt identity.
func (t *Turn) UpdateBackgroundSnapshot(result []byte) error {
	responseID, err := decodeBackgroundSnapshot(result)
	if err != nil {
		return err
	}
	m := t.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	m.sweepTurnLocked(t, m.cfg.Now())
	if t.isTerminalLocked() || t.expired {
		return ErrTerminal
	}
	if !t.backgroundAccepted || t.stream {
		return ErrUnavailable
	}
	if responseID != t.responseID {
		return ErrConsistency
	}
	return t.updateBackgroundSnapshotLocked(result)
}

func decodeBackgroundSnapshot(result []byte) (string, error) {
	body, _, err := decodeBody(result)
	responseID, _ := body["id"].(string)
	if err != nil || responseID == "" || body["background"] != true {
		return "", ErrConsistency
	}
	if status := body["status"]; status != "queued" && status != "in_progress" {
		return "", ErrConsistency
	}
	return responseID, nil
}

func (t *Turn) updateBackgroundSnapshotLocked(result []byte) error {
	if !t.store {
		return nil
	}
	if !t.reserveResultLocked(result) {
		t.recoverable = false
		t.unavailable = "recovery_quota_exceeded"
		t.cancelLocked(ReasonQuota, t.manager.cfg.Now())
		return &Cancellation{ReasonQuota}
	}
	return nil
}

// RecordCancellationResult retains a verified cancellation of the original
// response during bounded cleanup, without reviving execution or extending TTL.
func (t *Turn) RecordCancellationResult(result []byte) error {
	body, _, err := decodeBody(result)
	if err != nil {
		return ErrConsistency
	}
	m := t.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	m.sweepTurnLocked(t, m.cfg.Now())
	if (t.expired && t.store) || m.turns[t.id] != t {
		return ErrExpired
	}
	if t.state != StateCancelled || !t.backgroundAccepted || t.responseID == "" {
		return ErrTerminal
	}
	if body["id"] != t.responseID || body["status"] != "cancelled" {
		return ErrConsistency
	}
	t.cancelConfirmed = true
	if !t.store {
		return nil
	}
	if !t.reserveResultLocked(result) {
		t.unavailable = "recovery_quota_exceeded"
		t.recoverable = false
		return &Cancellation{ReasonQuota}
	}
	return nil
}
