//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

const collabSafetyReasoning = "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\"}}\n\n"
const collabSafetyDelta = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"

func TestCollabRegressionObserverReasoningBeforeText(t *testing.T) {
	o := &CollabSendObserver{}
	body := &collabEvidenceBody{ReadCloser: io.NopCloser(strings.NewReader(collabSafetyReasoning + collabSafetyDelta)), observer: o, stream: true}
	_, err := io.ReadAll(body)
	require.NoError(t, err)
	t.Logf("accepted=%t semantic=%t passthrough=%t", o.accepted.Load(), o.semantic.Load(), body.passthrough.Load())
	if !body.passthrough.Load() {
		t.Error("reasoning prelude prevents permanent semantic fast path")
	}
}
func BenchmarkCollabRegressionReasoningPreludeStream(b *testing.B) {
	for _, size := range []int{4 << 20, 92 << 20} {
		for _, prelude := range []bool{false, true} {
			for _, enabled := range []bool{false, true} {
				b.Run(fmt.Sprintf("size=%d/reasoning=%t/on=%t", size, prelude, enabled), func(b *testing.B) {
					frame := []byte(collabSafetyDelta)
					wire := bytes.Repeat(frame, (size+len(frame)-1)/len(frame))
					if prelude {
						wire = append([]byte(collabSafetyReasoning), wire...)
					}
					buf := make([]byte, 32<<10)
					cfg := &config.Config{}
					cfg.JWT.Secret = "synthetic"
					svc := &OpenAIGatewayService{cfg: cfg}
					account := &Account{ID: 1, Platform: PlatformOpenAI}
					b.ReportAllocs()
					b.SetBytes(int64(len(wire)))
					b.ResetTimer()
					for b.Loop() {
						ctx := context.Background()
						if enabled {
							ctx = WithCollabSendObserver(ctx, &CollabSendObserver{})
						}
						resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(bytes.NewReader(wire))}
						svc.observeCollabHTTP(ctx, account, []byte("request"), true, resp)
						for {
							if _, e := resp.Body.Read(buf); e != nil {
								break
							}
						}
						_ = resp.Body.Close()
					}
				})
			}
		}
	}
}

func TestCollabRegressionOptionalSelectionFullMarks(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 0), selectionFixtureAccount(2, 1))
	f.loads.loadMap = map[int64]*AccountLoadInfo{1: {LoadRate: 100, CurrentConcurrency: 10}}
	ctx := resilience.WithCollabAttempt(resilience.WithRequestState(context.Background(), resilience.NewCollabChildState(resilience.NewRequestState())), resilience.CollabAttempt{Optional: true})
	result, err := f.svc.SelectCollabAccount(ctx, ptrInt64Selection(7), "m", OpenAIEndpointCapabilityResponses, 0, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	CombineSelectionRelease(result, nil)()
	t.Logf("selected=%d shared_full_marks=%v", result.Account.ID, f.svc.selectionFullMarks.at)
	if len(f.svc.selectionFullMarks.at) > 0 {
		t.Error("optional selection leaves shared full-account avoidance facts")
	}
	f.svc.selectionFullMarks.Mark(2, time.Now())
	before := maps.Clone(f.svc.selectionFullMarks.at)
	a, b := selectionFixtureAccount(1, 0), selectionFixtureAccount(2, 1)
	f.svc.pickOpenAILastResort(ctx, OpenAIAccountScheduleRequest{GroupID: ptrInt64Selection(7), RequestedModel: "m"}, []lastResortCandidate{{account: &a}, {account: &b}})
	require.Equal(t, before, f.svc.selectionFullMarks.at, "optional last-resort selection must neither write nor clear shared marks")
}

type collabSafetyUnavailableAliases struct{}

func (collabSafetyUnavailableAliases) Ready() bool { return true }
func (collabSafetyUnavailableAliases) Put(context.Context, CollabContinuationScope, string, CollabContinuationAlias) error {
	return fmt.Errorf("synthetic unavailable alias store")
}
func (collabSafetyUnavailableAliases) Get(context.Context, CollabContinuationScope, string) (CollabContinuationAlias, bool, error) {
	return CollabContinuationAlias{}, false, fmt.Errorf("synthetic unavailable alias store")
}
func TestCollabRegressionSwitchOffContinuationStoreFailure(t *testing.T) {
	old := resilience.Current().Clone()
	defer resilience.Publish(old)
	cfg := resilience.Defaults()
	cfg.Collab.Ladder = false
	resilience.Publish(cfg)
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader("{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[]}"))}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	svc.SetCollabContinuationStore(collabSafetyUnavailableAliases{})
	account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic", "base_url": "https://fixture.invalid"}, Status: StatusActive, Schedulable: true, Concurrency: 1}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := []byte("{\"model\":\"synthetic-model\",\"previous_response_id\":\"resp_ordinary_unaliased\",\"input\":\"continue\"}")
	c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
	_, err := svc.Forward(context.Background(), c, account, body)
	t.Logf("sent=%t err=%v", upstream.lastReq != nil, err)
	if upstream.lastReq == nil {
		t.Error("switch-off ordinary continuation blocked by new alias store")
	}
}
func BenchmarkCollabRegressionAliasLookupOff(b *testing.B) {
	body := []byte("{\"model\":\"synthetic-model\",\"previous_response_id\":\"resp_ordinary_unaliased\",\"input\":\"continue\"}")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	svc := &OpenAIGatewayService{openaiWSStateStore: NewOpenAIWSStateStore(nil)}
	for _, enabled := range []bool{false, true} {
		b.Run(fmt.Sprint("store=", enabled), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if enabled {
					_, _ = svc.restoreCollabContinuation(context.Background(), c, &Account{ID: 9}, body)
				}
			}
		})
	}
}

type collabCountingAliases struct {
	calls     int
	panicRead bool
}

func (*collabCountingAliases) Ready() bool { return true }
func (*collabCountingAliases) Put(context.Context, CollabContinuationScope, string, CollabContinuationAlias) error {
	return nil
}
func (s *collabCountingAliases) Get(context.Context, CollabContinuationScope, string) (CollabContinuationAlias, bool, error) {
	s.calls++
	if s.panicRead {
		panic("synthetic alias failure")
	}
	return CollabContinuationAlias{}, false, fmt.Errorf("synthetic alias failure")
}
func TestCollabRegressionAliasLookupOptional(t *testing.T) {
	old := resilience.Current().Clone()
	defer resilience.Publish(old)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	body := []byte(`{"previous_response_id":"resp_ordinary"}`)
	for _, enabled := range []bool{false, true} {
		settings := resilience.Defaults()
		settings.Collab.Ladder = enabled
		resilience.Publish(settings)
		ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
		for _, panics := range []bool{false, true} {
			store := &collabCountingAliases{panicRead: panics}
			svc := &OpenAIGatewayService{collabContinuations: store}
			if !enabled {
				allocs := testing.AllocsPerRun(100, func() { _, _ = svc.restoreCollabContinuation(ctx, c, nil, body) })
				require.Zero(t, allocs)
				require.Zero(t, store.calls)
			}
			require.NotPanics(t, func() {
				got, err := svc.restoreCollabContinuation(ctx, c, nil, body)
				require.NoError(t, err)
				require.Equal(t, body, got)
			})
			if enabled {
				require.Equal(t, 1, store.calls)
			} else {
				require.Zero(t, store.calls)
			}
		}
	}
}
