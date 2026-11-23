//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/repository"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type collabWiringSharedCache struct {
	service.GatewayCache
	shared resilience.CollabSharedRecordsStore
}

func (c collabWiringSharedCache) CollabRecords() resilience.CollabSharedRecordsStore { return c.shared }

func TestCollabWiringLocalSharedReservation(t *testing.T) {
	quietReplayLogs(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	shared := repository.NewGatewayCache(client).(service.CollabRecordsCache).CollabRecords()
	newInstance := func() *collabWiringFixture {
		memory := &replayMemory{bindings: map[string]int64{}, avoids: map[string]map[int64]time.Time{}, avoidUntil: map[string]map[int64]time.Time{}, failures: map[string]map[int64]int64{}}
		return newCollabWiringFixture(t, collabLocalSettings(), func(*http.Request, int64) (*http.Response, error) {
			return replayResponse(400, "application/json", collabLocalRejection), nil
		}, collabWiringSharedCache{GatewayCache: memory, shared: shared})
	}
	body := []byte(`{"model":"gpt-5.5","input":[` + replayTask + `]}`)
	first := newInstance()
	var state *resilience.RequestState
	for i := 0; i < 2; i++ {
		state = collabLocalRequest(first, body, false, httptest.NewRecorder())
	}
	key := state.CollabState().Items[0].Key
	query := resilience.CollabRecordQuery{Key: key}
	batch := resilience.CollabRecordBatch{Scope: key.Scope, Queries: []resilience.CollabRecordQuery{query}, Dispositions: true}
	// Ordinary observations are asynchronous; shutdown guarantees only the
	// bounded invalidation drain, not delivery of all queued observations.
	require.Eventually(t, func() bool {
		lookup, err := shared.LookupBatch(context.Background(), batch)
		counts := lookup.Dispositions[query].TerminalRejections
		return err == nil && len(counts) == 1 && counts[0].Count == 2
	}, time.Second, time.Millisecond)
	first.gateway.CloseCollabRecords()
	instances := []*collabWiringFixture{newInstance(), newInstance()}
	var wg sync.WaitGroup
	results := make(chan int16, 6)
	notices := make(chan string, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(f *collabWiringFixture) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			state := collabLocalRequest(f, body, false, rec)
			notices <- rec.Body.String()
			metadata, _ := state.ClaimGatewayRequest(false)
			results <- metadata.Result
		}(instances[i%2])
	}
	wg.Wait()
	close(results)
	close(notices)
	ended := 0
	for result := range results {
		if result == resilience.RequestLocalEnded {
			ended++
		}
	}
	require.Equal(t, 3, ended, "one atomic allowance across both gateways")
	for _, f := range instances {
		f.gateway.CloseCollabRecords()
	}
	restarted := newInstance()
	rec := httptest.NewRecorder()
	state = collabLocalRequest(restarted, body, false, rec)
	metadata, _ := state.ClaimGatewayRequest(false)
	require.Equal(t, resilience.RequestFailed, metadata.Result)
	require.NotContains(t, rec.Body.String(), "Gateway notice [")
	lookup, err := shared.LookupBatch(context.Background(), batch)
	require.NoError(t, err)
	require.Equal(t, 3, lookup.TreeLocalEndings)
	event := ""
	for notice := range notices {
		if token := regexp.MustCompile(`cle_[a-f0-9]{32}`).FindString(notice); token != "" {
			event = token
		}
	}
	require.NotEmpty(t, event)
	var sent []byte
	restarted.upstream.respond = func(req *http.Request, _ int64) (*http.Response, error) {
		sent, _ = io.ReadAll(req.Body)
		return replayResponse(200, "application/json", `{"id":"resp_sample","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`), nil
	}
	for _, receipt := range []string{"", event, ""} {
		input := replayTask
		if receipt != "" {
			input += `,{"type":"message","role":"assistant","content":"Gateway notice [` + receipt + `]."}`
		}
		collabLocalRequest(restarted, []byte(`{"model":"gpt-5.5","input":[`+input+`]}`), false, httptest.NewRecorder())
		if receipt == "" {
			require.Contains(t, string(sent), "cipher-task")
		} else {
			require.NotContains(t, string(sent), "cipher-task")
		}
	}
	server.Close()
	rec = httptest.NewRecorder()
	state = collabLocalRequest(newInstance(), body, false, rec)
	metadata, _ = state.ClaimGatewayRequest(false)
	require.Equal(t, resilience.RequestFailed, metadata.Result)
	require.NotContains(t, rec.Body.String(), "Gateway notice [", "unknown storage cannot authorize a notice")
}
