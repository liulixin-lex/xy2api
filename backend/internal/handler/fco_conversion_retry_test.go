//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/repository"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type fcoConversionUpstream struct {
	*conversionRetryUpstream
	t *testing.T
}

func (u *fcoConversionUpstream) Do(req *http.Request, proxy string, account int64, concurrency int) (*http.Response, error) {
	reader, err := req.GetBody()
	require.NoError(u.t, err)
	wire, err := io.ReadAll(reader)
	require.NoError(u.t, err)
	require.NoError(u.t, reader.Close())
	resp, err := u.conversionRetryUpstream.Do(req, proxy, account, concurrency)
	require.Equal(u.t, wire, u.bodies[len(u.bodies)-1], "GetBody and transport must agree")
	if gjson.GetBytes(wire, "input.1.type").String() == "message" {
		require.Equal(u.t, len(wire), resilience.StateFrom(req.Context()).AttemptProfile().BodyBytes)
	}
	return resp, err
}

func fcoCoreSentCount() int64 {
	for _, row := range resilience.Snapshot(time.Hour) {
		if row.Name == resilience.MetricFCOCompat && row.Label == "core:map:sent" {
			return row.Count
		}
	}
	return 0
}

func TestFCOAndConversionRetryEndToEnd(t *testing.T) {
	const refusal = "{\"error\":{\"type\":\"invalid_request_error\",\"message\":\"function_call_output requires call_id on HTTP requests\"}}"
	for _, stream := range []bool{false, true} {
		for _, scenario := range []string{"proactive_conversion", "reactive_only", "reactive_then_conversion", "proactive_conversion_then_repair"} {
			t.Run(fmt.Sprintf("%s/stream=%t", scenario, stream), func(t *testing.T) {
				mode := resilience.FCOCompatModeProactive
				if strings.HasPrefix(scenario, "reactive") {
					mode = resilience.FCOCompatModeReactive
				}
				withHandlerResilience(t, func(s *resilience.Settings) { s.FCOCompat.Mode = mode })
				up := &fcoConversionUpstream{t: t, conversionRetryUpstream: &conversionRetryUpstream{
					responses: []string{conversionConflictStream(false), conversionSuccessStream},
					statuses:  []int{200, 200},
				}}
				wantAccounts := []int64{1, 2}
				wantRetries := 1
				switch scenario {
				case "reactive_only":
					up.responses[0], up.statuses[0] = refusal, 400
					wantAccounts = []int64{1, 1}
				case "reactive_then_conversion":
					up.responses = []string{refusal, conversionConflictStream(false), conversionSuccessStream}
					up.statuses = []int{400, 200, 200}
					wantAccounts, wantRetries = []int64{1, 1, 2}, 2
				case "proactive_conversion_then_repair":
					up.responses = []string{conversionConflictStream(false), "{\"error\":{\"message\":\"Unsupported parameter: 'temperature'.\",\"param\":\"temperature\"}}", conversionSuccessStream}
					up.statuses = []int{200, 400, 200}
					wantAccounts = []int64{1, 2, 2}
				}
				h := newConversionRetryHandler(t, up, func(accounts []service.Account) {
					for i := range accounts {
						conversionAPIKey(&accounts[i])
						accounts[i].Credentials["base_url"] = "https://relay.example"
					}
				})
				ctx := budgetCtx(t, resilience.KindText, 3131)
				ctx = service.WithGroupSystemPrompt(ctx, service.GroupSystemPromptConfig{Prompt: "group fixture"}, "gpt-4.1")
				c, rec := newOpenAIResponsesFailoverTestContext(t, ctx)
				body := fmt.Sprintf("{\"model\":\"gpt-4.1\",\"stream\":%t,\"instructions\":\"fixture\",\"temperature\":0.4,\"input\":[{\"role\":\"user\",\"content\":\"hello\"},{\"type\":\"function_call_output\",\"namespace\":\"agent\",\"name\":\"browser_info\",\"output\":\"fixture context\"}]}", stream)
				c.Request.URL.Path = "/v1/chat/completions"
				c.Request.Body = io.NopCloser(strings.NewReader(body))
				c.Request.ContentLength = int64(len(body))
				service.InstallDeliveryTracking(c, time.Now())
				before := fcoCoreSentCount()
				h.ChatCompletions(c)
				require.Equal(t, wantAccounts, up.accounts, rec.Body.String())
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Contains(t, rec.Body.String(), "retry-success")
				require.NotContains(t, rec.Body.String(), "\"error\"")
				require.Equal(t, wantRetries, resilience.StateFrom(ctx).Budget().Attempts())
				mappedStart := 0
				if mode == resilience.FCOCompatModeReactive {
					mappedStart = 1
					require.Equal(t, "function_call_output", gjson.GetBytes(up.bodies[0], "input.1.type").String())
				}
				require.EqualValues(t, len(up.bodies)-mappedStart, fcoCoreSentCount()-before, "each actual mapped dispatch is counted")
				for _, wire := range up.bodies[mappedStart:] {
					require.Equal(t, "message", gjson.GetBytes(wire, "input.1.type").String())
					require.Equal(t, "<external_browser_info>fixture context</external_browser_info>", gjson.GetBytes(wire, "input.1.content.0.text").String())
					require.Equal(t, "group fixture\n\nfixture", gjson.GetBytes(wire, "instructions").String())
				}
				if scenario != "reactive_only" {
					require.Equal(t, up.bodies[mappedStart], up.bodies[mappedStart+1], "conversion retry must preserve the repaired final wire")
				}
				if scenario == "proactive_conversion_then_repair" {
					require.False(t, gjson.GetBytes(up.bodies[2], "temperature").Exists())
				}
				if mode == resilience.FCOCompatModeReactive {
					raw, ok := c.Get(service.OpsUpstreamErrorsKey)
					require.True(t, ok)
					found := false
					for _, event := range raw.([]*service.OpsUpstreamErrorEvent) {
						if event.Reason == "fco_standalone_rejected" {
							found = true
							require.True(t, event.Resent)
						}
					}
					require.True(t, found)
				}
			})
		}
	}
}

func TestFCOConversionRetryHonorsCandidateGate(t *testing.T) {
	for _, kind := range []string{"off", "official"} {
		t.Run(kind, func(t *testing.T) {
			withHandlerResilience(t, func(s *resilience.Settings) { s.FCOCompat.Mode = resilience.FCOCompatModeProactive })
			up := &conversionRetryUpstream{responses: []string{conversionConflictStream(false), conversionSuccessStream}}
			h := newConversionRetryHandler(t, up, func(accounts []service.Account) {
				for i := range accounts {
					conversionAPIKey(&accounts[i])
					accounts[i].Credentials["base_url"] = "https://relay.example"
				}
				if kind == "off" {
					accounts[1].Extra[service.OpenAIFCOCompatExtraKey] = resilience.FCOCompatModeOff
				} else {
					accounts[1].Credentials["base_url"] = "https://api.openai.com"
				}
			})
			body := "{\"model\":\"gpt-4.1\",\"stream\":false,\"input\":[{\"role\":\"user\",\"content\":\"hello\"},{\"type\":\"function_call_output\",\"namespace\":\"agent\",\"name\":\"browser_info\",\"output\":\"fixture context\"}]}"
			result, event := runConversionChat(t, h, body)
			require.Len(t, up.bodies, 1, result)
			require.Contains(t, result, service.OpenAIUpstreamProtocolErrorCode)
			require.Equal(t, "candidate_wire", event["skip_reason"])
		})
	}
}

func TestFCOConversionRetryWithSchedulerProjection(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, skip := range []bool{false, true} {
				for _, mode := range []string{resilience.FCOCompatModeProactive, resilience.FCOCompatModeReactive} {
					t.Run(fmt.Sprintf("batch=%t/stream=%t/skip=%t/%s", batch, stream, skip, mode), func(t *testing.T) {
						withHandlerResilience(t, func(s *resilience.Settings) { s.FCOCompat.Mode = mode })
						count := 2
						if skip {
							count = 5
						}
						up := &conversionRetryUpstream{responses: []string{conversionConflictStream(false), conversionSuccessStream}, statuses: []int{200, 200}}
						want := []int64{1, int64(count)}
						if mode == resilience.FCOCompatModeReactive {
							up.responses = append([]string{`{"error":{"message":"function_call_output requires call_id on HTTP requests"}}`}, up.responses...)
							up.statuses = append([]int{400}, up.statuses...)
							want = append([]int64{1}, want...)
						}
						h := newFCOProjectionHandler(t, up, count, batch)
						body := fmt.Sprintf(`{"model":"gpt-4.1","stream":%t,"input":[{"role":"user","content":"hello"},{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"context"}]}`, stream)
						c, rec := newOpenAIResponsesFailoverTestContext(t, budgetCtx(t, resilience.KindText, 3131))
						c.Request.URL.Path = "/v1/chat/completions"
						c.Request.Body = io.NopCloser(strings.NewReader(body))
						c.Request.ContentLength = int64(len(body))
						service.InstallDeliveryTracking(c, time.Now())
						h.ChatCompletions(c)
						result := rec.Body.String()
						require.Equal(t, http.StatusOK, rec.Code, result)
						require.Equal(t, want, up.accounts, result)
						require.Contains(t, result, "retry-success")
						require.Equal(t, up.bodies[len(want)-2], up.bodies[len(want)-1])
						require.Equal(t, "message", gjson.GetBytes(up.bodies[len(want)-1], "input.1.type").String())
					})
				}
			}
		}
	}
}

func newFCOProjectionHandler(t *testing.T, up service.HTTPUpstream, count int, batch bool) *OpenAIGatewayHandler {
	t.Helper()
	accounts := make([]service.Account, count)
	for i := range accounts {
		a := &accounts[i]
		*a = service.Account{ID: int64(i + 1), Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{3131}}
		conversionAPIKey(a)
		a.Credentials["base_url"] = "https://relay.example"
		if i > 0 && i < count-1 {
			if i%2 == 0 {
				a.Credentials["base_url"] = "https://api.openai.com"
			} else {
				a.Extra[service.OpenAIFCOCompatExtraKey] = resilience.FCOCompatModeOff
			}
		}
	}
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := repository.NewSchedulerCache(client)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.Scheduling.LoadBatchEnabled = batch
	bucket := service.SchedulerBucket{Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	ctx := context.Background()
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, accounts))
	projection, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, projection, count)
	for _, a := range projection {
		require.NotContains(t, a.Credentials, "base_url")
		require.NotContains(t, a.Extra, service.OpenAIFCOCompatExtraKey)
	}
	repo := openAIImagesFailoverAccountRepo{accounts: accounts}
	// A cache miss cannot silently load the complete candidate list from the fixture.
	snapshot := service.NewSchedulerSnapshotService(cache, nil, nil, nil, cfg)
	concurrency := service.NewConcurrencyService(repository.NewConcurrencyCache(client, 10, 60))
	return conversionHandlerForAccounts(t, up, repo, cfg, snapshot, concurrency)
}
