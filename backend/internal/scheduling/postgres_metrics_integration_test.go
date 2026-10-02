package scheduling

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This test deliberately uses the real JSONB update. An in-memory metrics
// recorder cannot detect fields silently removed by the persistence whitelist.
func TestPostgresNativeStreamMetricsIntegration(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	read := func(ticketID string) (map[string]any, bool) {
		t.Helper()
		var raw []byte
		var pending bool
		require.NoError(t, db.QueryRowContext(ctx, "SELECT metrics,usage_pending FROM scheduling_attempts WHERE ticket_id=$1", ticketID).Scan(&raw, &pending))
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		return got, pending
	}

	t.Run("preserves_native_delivery_and_trace_fields", func(t *testing.T) {
		ticket, err := s.BeginDispatch(ctx, testDispatch(1, ""))
		require.NoError(t, err)
		metrics := map[string]any{
			"http_request_id": strings.Repeat("h", 64), "client_request_id": "client.trace-1",
			"request_started_at":           "2026-10-02T20:00:00.123456789+02:00",
			"started_at":                   "2026-10-02T20:00:00.125+02:00",
			"sent_at":                      "2026-10-02T20:00:00.130+02:00",
			"first_downstream_flush_at":    "2026-10-02T20:00:00.151+02:00",
			"native_stream_policy_version": int64(2), "policy_version": int64(12), "priority": int64(-3),
			"headers_ms": int64(14), "attempt_committed_ms": int64(20),
			"first_protocol_event_ms": int64(20), "first_content_ms": int64(23),
			"unknown_event_count": int64(0), "gateway_flush_count": int64(9),
			"gateway_read_to_flush_ms": 0.03125, "gateway_read_to_flush_max_ms": 1.0625,
			"http_committed": true, "attempt_committed": true, "semantic_seen": false,
			"send_certainty": "externally_committed", "cancel_reason": "client_detached",
			"first_answer_ms": nil,
			"prompt":          "MUST_NOT_PERSIST", "authorization": "MUST_NOT_PERSIST",
			"unknown_nested": map[string]any{"token": "MUST_NOT_PERSIST"},
		}
		require.NoError(t, s.RecordAttemptMetrics(ctx, ticket.TicketID, metrics))
		got, pending := read(ticket.TicketID)
		require.True(t, pending)
		for key, value := range metrics {
			switch key {
			case "prompt", "authorization", "unknown_nested", "first_answer_ms":
				require.NotContains(t, got, key)
			case "request_started_at":
				require.Equal(t, "2026-10-02T18:00:00.123456789Z", got[key], key)
			case "started_at":
				require.Equal(t, "2026-10-02T18:00:00.125Z", got[key], key)
			case "sent_at":
				require.Equal(t, "2026-10-02T18:00:00.13Z", got[key], key)
			case "first_downstream_flush_at":
				require.Equal(t, "2026-10-02T18:00:00.151Z", got[key], key)
			default:
				if integer, ok := value.(int64); ok {
					require.Equal(t, float64(integer), got[key], key)
				} else {
					require.Equal(t, value, got[key], key)
				}
			}
		}
		require.NoError(t, s.RecordAttemptMetrics(ctx, ticket.TicketID, map[string]any{
			"first_content_ms": nil, "gateway_read_to_flush_ms": 0.0,
			"semantic_seen": true, "http_committed": false,
		}))
		got, _ = read(ticket.TicketID)
		require.Equal(t, float64(23), got["first_content_ms"], "unobserved values must not erase observed data")
		require.Equal(t, float64(0), got["gateway_read_to_flush_ms"])
		require.Equal(t, true, got["semantic_seen"])
		require.Equal(t, false, got["http_committed"])
	})

	t.Run("rejects_invalid_values_without_partial_write", func(t *testing.T) {
		cases := []struct {
			name, key string
			value     any
		}{
			{"negative_headers", "headers_ms", -1},
			{"negative_commit_time", "attempt_committed_ms", -1},
			{"negative_first_event", "first_protocol_event_ms", -1},
			{"negative_first_content", "first_content_ms", -1},
			{"negative_unknown_count", "unknown_event_count", -1},
			{"fractional_count", "gateway_flush_count", 1.5},
			{"negative_flush_count", "gateway_flush_count", -1},
			{"fractional_timing", "headers_ms", 1.5},
			{"boolean_timing", "headers_ms", true},
			{"string_delay", "gateway_read_to_flush_ms", "0.5"},
			{"negative_delay", "gateway_read_to_flush_ms", -0.5},
			{"negative_max_delay", "gateway_read_to_flush_max_ms", -0.5},
			{"nan_delay", "gateway_read_to_flush_ms", math.NaN()},
			{"positive_infinite_delay", "gateway_read_to_flush_max_ms", math.Inf(1)},
			{"negative_infinite_delay", "gateway_read_to_flush_ms", math.Inf(-1)},
			{"numeric_http_commit", "http_committed", 1},
			{"string_attempt_commit", "attempt_committed", "true"},
			{"numeric_semantic_seen", "semantic_seen", 0},
			{"invalid_flush_timestamp", "first_downstream_flush_at", "yesterday"},
			{"invalid_request_timestamp", "request_started_at", "2026-10-02T18:00:00"},
			{"invalid_started_timestamp", "started_at", 0},
			{"negative_native_version", "native_stream_policy_version", -1},
			{"empty_http_trace", "http_request_id", ""},
			{"blank_client_trace", "client_request_id", " "},
			{"untrimmed_trace", "client_request_id", " trace"},
			{"oversized_http_trace", "http_request_id", strings.Repeat("h", 65)},
			{"oversized_utf8_trace", "client_request_id", strings.Repeat("界", 22)},
			{"nested_trace", "http_request_id", map[string]string{"token": "secret"}},
			{"invalid_send_certainty", "send_certainty", "definitely_successful"},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				ticket, err := s.BeginDispatch(ctx, testDispatch(1, ""))
				require.NoError(t, err)
				require.NoError(t, s.RecordAttemptMetrics(ctx, ticket.TicketID, map[string]any{"reason": "before"}))
				before, pendingBefore := read(ticket.TicketID)
				err = s.RecordAttemptMetrics(ctx, ticket.TicketID, map[string]any{
					"reason": "must_not_write", "sent_at": "2026-10-02T18:00:00Z", test.key: test.value,
				})
				require.Error(t, err)
				after, pending := read(ticket.TicketID)
				require.Equal(t, before, after)
				require.Equal(t, pendingBefore, pending)
			})
		}
	})

	t.Run("retains_all_send_certainty_states", func(t *testing.T) {
		ticket, err := s.BeginDispatch(ctx, testDispatch(1, ""))
		require.NoError(t, err)
		for _, state := range []string{"not_sent", "sent_execution_unknown", "response_received", "externally_committed"} {
			require.NoError(t, s.RecordAttemptMetrics(ctx, ticket.TicketID, map[string]any{"send_certainty": state}))
			got, _ := read(ticket.TicketID)
			require.Equal(t, state, got["send_certainty"])
		}
	})
}
