//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCollabWiringObservationAuxiliaryLoss(t *testing.T) {
	quietReplayLogs(t)
	for _, family := range []resilience.CollabFamily{resilience.CollabFamilyReasoning, resilience.CollabFamilyCompaction} {
		for _, outcome := range []string{"accepted", "rejected", "unchanged", "mixed", "records_off", "ladder_off"} {
			t.Run(string(family)+"_"+outcome, func(t *testing.T) {
				settings := resilience.Defaults()
				settings.Collab.Ladder = outcome != "ladder_off"
				settings.Collab.LosslessFirst = settings.Collab.Ladder
				settings.Collab.Records = outcome != "records_off"
				settings.Budget.PreOutputBudgetSeconds = 60
				unchanged := outcome == "unchanged" || outcome == "ladder_off"
				id := "rs_sample"
				if family == resilience.CollabFamilyCompaction {
					id = "cmp_sample"
				}
				item := map[string]any{"type": string(family), "id": id, "encrypted_content": "cipher-sample"}
				if family == resilience.CollabFamilyReasoning {
					item["summary"] = []any{map[string]string{"type": "summary_text", "text": "keep readable summary"}}
				}
				input := []any{item}
				if outcome == "mixed" {
					other := "reasoning"
					if family == resilience.CollabFamilyReasoning {
						other = "compaction"
					}
					otherID := "rs_other"
					if other == "compaction" {
						otherID = "cmp_other"
					}
					input = append(input, map[string]any{"type": other, "id": otherID, "encrypted_content": "cipher-other"})
				}
				rejection, err := json.Marshal(map[string]any{"error": map[string]string{"code": "invalid_encrypted_content", "message": "Encrypted content could not be verified for item " + id}})
				require.NoError(t, err)
				sends, optional, removed := 0, 0, 0
				f := newCollabWiringFixture(t, settings, func(req *http.Request, _ int64) (*http.Response, error) {
					sends++
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					attempt := resilience.CollabAttemptFrom(req.Context())
					if attempt.Optional {
						optional++
					}
					physical := gjson.GetBytes(body, "input.0")
					if attempt.Optional && !physical.Get("encrypted_content").Exists() {
						removed++
						if family == resilience.CollabFamilyReasoning {
							require.Equal(t, id, physical.Get("id").String())
							require.Equal(t, "reasoning", physical.Get("type").String())
							require.Equal(t, "keep readable summary", physical.Get("summary.0.text").String())
						} else {
							require.False(t, physical.Get("id").Exists(), "message normalization strips the compaction ID")
							require.Equal(t, "message", physical.Get("type").String())
							require.Equal(t, "input_text", physical.Get("content.0.type").String())
							require.NotEmpty(t, physical.Get("content.0.text").String())
						}
					}
					if !unchanged && (!attempt.Optional || physical.Get("encrypted_content").Exists() || outcome == "rejected") {
						return replayResponse(400, "application/json", string(rejection)), nil
					}
					return replayResponse(200, "application/json", `{"id":"resp_observe","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`), nil
				})
				body, err := json.Marshal(map[string]any{"model": "gpt-5.5", "input": input})
				require.NoError(t, err)
				rec, state := f.request(body)
				meta, _ := state.ClaimGatewayRequest(false)
				if outcome == "ladder_off" {
					require.Equal(t, http.StatusOK, rec.Code)
					require.Equal(t, 1, sends)
					require.Zero(t, removed)
					require.Empty(t, meta.CollabObservation)
					return
				}
				var obs resilience.CollabObservation
				require.NoError(t, json.Unmarshal(meta.CollabObservation, &obs))
				t.Logf("status=%d sends=%d optional=%d removed=%d observed=%d final_mode=%s replacements=%+v deliveries=%d", rec.Code, sends, optional, removed, obs.SendTotal, obs.FinalMode, obs.Replacements, len(obs.Deliveries))
				require.Equal(t, sends, obs.SendTotal)
				for i, send := range obs.Sends {
					require.EqualValues(t, i+1, send.Seq)
					require.EqualValues(t, i+1, send.Generation)
				}
				// Auxiliary observations must not add consumed or replaced storage writes.
				items := state.CollabState().Items
				for _, observed := range items {
					query := resilience.CollabRecordQuery{Key: observed.Key}
					lookup := f.gateway.CollabRecords().LookupBatch(context.Background(), resilience.CollabRecordBatch{Scope: observed.Key.Scope, Queries: []resilience.CollabRecordQuery{query}, Records: true, Dispositions: true})
					require.Empty(t, lookup.Records[query].Consumed)
					require.Empty(t, lookup.Dispositions[query].Replaced)
				}
				if outcome == "rejected" {
					require.GreaterOrEqual(t, rec.Code, 400)
					require.Positive(t, removed, "the rejected send must actually remove the ciphertext")
					require.Empty(t, obs.Deliveries)
					require.Equal(t, resilience.CollabReplacementCounts{}, obs.Replacements)
					require.Equal(t, resilience.CollabFinalModeNone, obs.FinalMode)
					return
				}
				require.Equal(t, http.StatusOK, rec.Code)
				if unchanged {
					require.Equal(t, 1, sends)
					require.Zero(t, removed)
					require.Equal(t, resilience.CollabReplacementCounts{}, obs.Replacements)
					require.Equal(t, resilience.CollabFinalModeCiphertext, obs.FinalMode)
					for _, delivery := range obs.Deliveries {
						require.Equal(t, resilience.CollabDeliveryOutcomeAcceptedOriginal, delivery.Outcome)
					}
					return
				}
				require.Equal(t, 2, sends)
				require.Equal(t, 1, optional)
				require.Equal(t, 1, removed)
				wantCounts := resilience.CollabReplacementCounts{Reasoning: 1}
				path, scope := "input.0.encrypted_content", resilience.CollabScopeFragment
				if family == resilience.CollabFamilyCompaction {
					wantCounts = resilience.CollabReplacementCounts{Compaction: 1}
					path, scope = "input.0", resilience.CollabScopeWholeItem
				}
				require.Equal(t, wantCounts, obs.Replacements, "count only the accepted loss, in its own family")
				mode, deliveries := resilience.CollabFinalModePlaceholder, 1
				if outcome == "mixed" {
					mode, deliveries = resilience.CollabFinalModeMixed, 2
				}
				require.Equal(t, mode, obs.FinalMode)
				require.Len(t, obs.Deliveries, deliveries)
				var losses []resilience.CollabDeliveryObservation
				for _, delivery := range obs.Deliveries {
					if delivery.Outcome == resilience.CollabDeliveryOutcomePlaceholder {
						losses = append(losses, delivery)
					}
				}
				require.Len(t, losses, 1)
				loss := losses[0]
				require.Equal(t, items[0].Key.Delivery, loss.Delivery)
				require.Equal(t, items[0].Key.Item, loss.Item)
				require.Equal(t, items[0].Key.Digest, loss.Digest)
				require.Equal(t, resilience.CollabMessageKindUnknown, loss.Kind)
				require.Equal(t, resilience.CollabPositionMessage, loss.Position)
				require.Equal(t, resilience.CollabFormCiphertext, loss.Form)
				require.Equal(t, scope, loss.Scope)
				require.EqualValues(t, len(gjson.GetBytes(body, path).Raw), loss.Bytes)
				require.NotContains(t, string(meta.CollabObservation), "cipher-sample")
				require.NotContains(t, string(meta.CollabObservation), "keep readable summary")
			})
		}
	}
}
