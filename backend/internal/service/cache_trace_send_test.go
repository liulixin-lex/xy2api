package service

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestCacheTraceMarkSentDisabledHasNoWork(t *testing.T) {
	s := &OpenAIGatewayService{}
	c := &gin.Context{Request: httptest.NewRequest("POST", "/v1/responses", nil)}
	allocs := testing.AllocsPerRun(100, func() { s.markCacheTraceSent(c) })
	if allocs != 0 || s.openaiCacheTelemetry != nil || s.cacheTraceWorkers != nil {
		t.Fatalf("disabled mark allocated or initialized observation state: allocs=%v", allocs)
	}
}

func TestCacheTraceMarkSentCompleteEventsAndBudgetRejection(t *testing.T) {
	for _, tc := range []struct {
		name, transport, reason    string
		skip, excluded, wantTiming bool
	}{
		{"admitted", "http", "", false, false, true},
		{"rejected", "http", "budget_full", true, false, false},
		{"chain_percent", "http", "chain_percent", true, true, true},
		{"plugin", "plugin", "budget_full", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &OpenAIGatewayService{}
			trace := &cacheTrace{sessionKey: "s", skipUsage: tc.skip,
				window: cacheTraceWindow{thread: 7, sequence: 1, valid: true},
				async:  &cacheTraceAsync{chainExcluded: tc.excluded},
				data:   cacheTraceData{AccountID: 7, ProtocolOut: "responses", Transport: tc.transport, NAReason: tc.reason}}
			c := &gin.Context{Request: httptest.NewRequest("POST", "/v1/responses", nil)}
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), cacheTraceKey{}, trace))
			before := time.Now()
			s.markCacheTraceSent(c)
			after := time.Now()
			if !tc.wantTiming {
				if s.openaiCacheTelemetry != nil || !trace.timing.sentAt.IsZero() {
					t.Fatal("budget rejection visited history")
				}
				return
			}
			if trace.timing.sequence == 0 || trace.timing.sentAt.Before(before) || trace.timing.sentAt.After(after) ||
				trace.timing.owner != nil || len(s.cacheTelemetry().sessions) != 0 || trace.data.WindowSequenceDelta != -1 {
				t.Fatalf("unexpected cold dispatch observation: %#v", trace.timing)
			}
			firstSequence := trace.timing.sequence
			s.markCacheTraceSent(c)
			if trace.timing.sequence != firstSequence {
				t.Fatal("retry allocated another logical sequence")
			}
		})
	}
}
