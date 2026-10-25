package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type CachePolicyHistoryItem struct {
	At            time.Time         `json:"at"`
	Key           string            `json:"key"`
	Action        string            `json:"action"`
	Actor         *CachePolicyActor `json:"actor"`
	StatusCode    int               `json:"status_code"`
	RevBefore     *int64            `json:"rev_before"`
	RevAfter      *int64            `json:"rev_after"`
	ChangedFields []string          `json:"changed_fields"`
	Reason        *string           `json:"reason"`
}
type CachePolicyHistory struct {
	Items                   []CachePolicyHistoryItem `json:"items"`
	HistoryComplete         bool                     `json:"history_complete"`
	HistoryIncompleteReason *string                  `json:"history_incomplete_reason"`
}
type CachePolicyHistoryQuery struct {
	Key      string
	Limit    int
	Before   *time.Time
	AfterRev *int64
}

func cachePolicyAction(key string) string { return "admin.cache.policies." + key + ".update" }
func auditPolicyRev(v any) *int64 {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var n int64
	if json.Unmarshal(b, &n) != nil || n < 0 {
		return nil
	}
	return &n
}
func (s *CachePolicyService) History(ctx context.Context, audit *AuditLogService, q CachePolicyHistoryQuery) (CachePolicyHistory, error) {
	result := CachePolicyHistory{Items: []CachePolicyHistoryItem{}, HistoryComplete: true}
	if q.Key != "" && policyIndex(q.Key) < 0 {
		return result, PolicyNotInBuild(q.Key)
	}
	if q.AfterRev != nil && (q.Key == "" || *q.AfterRev < 0) {
		return result, invalidPolicy([]CachePolicyFieldError{{Field: "after_rev", Code: "malformed_body"}})
	}
	if q.Limit <= 0 {
		q.Limit = 20
	}
	if q.Limit > 100 {
		q.Limit = 100
	}
	filter := &AuditLogFilter{Page: 1, PageSize: q.Limit, Action: "admin.cache.policies.", Method: "PUT", CachePolicyOnly: true, CachePolicyBefore: q.Before, CachePolicyAfterRev: q.AfterRev, IncludeRequestBody: true}
	if q.Key != "" {
		filter.Action = cachePolicyAction(q.Key)
	}
	logs, err := audit.List(ctx, filter)
	if err != nil {
		return result, err
	}
	for _, row := range logs.Logs {
		key := strings.TrimSuffix(strings.TrimPrefix(row.Action, "admin.cache.policies."), ".update")
		if policyIndex(key) < 0 || row.Action != cachePolicyAction(key) || row.Method != "PUT" {
			continue
		}
		item := CachePolicyHistoryItem{At: row.CreatedAt.UTC(), Key: key, Action: row.Action, StatusCode: row.StatusCode, ChangedFields: []string{}}
		if row.ActorUserID != nil {
			item.Actor = &CachePolicyActor{*row.ActorUserID, row.ActorEmail}
		}
		if row.StatusCode < 400 {
			item.RevBefore = auditPolicyRev(row.Extra["rev_before"])
			item.RevAfter = auditPolicyRev(row.Extra["rev_after"])
		}
		if fields, ok := row.Extra["changed_fields"].(string); ok {
			// Only known schema paths can leave the audit store through this DTO.
			for _, field := range strings.Split(fields, ",") {
				if cachePolicyHistoryField(key, field) {
					item.ChangedFields = append(item.ChangedFields, field)
				}
			}
		}
		var body struct {
			Reason *string `json:"reason"`
		}
		if json.Unmarshal([]byte(row.RequestBody), &body) == nil {
			item.Reason = body.Reason
		}
		result.Items = append(result.Items, item)
	}
	stored, err := s.repo.GetMultiple(ctx, cachePolicySettings[:])
	if err != nil {
		return result, err
	}
	now := s.now()
	// The retained chain is independent of pagination and the latest stored
	// revision. Only an empty successful history uses the five-second grace.
	for i, key := range cachePolicyKeys {
		if q.Key != "" && q.Key != key {
			continue
		}
		success := true
		retained, err := audit.List(ctx, &AuditLogFilter{
			Action: cachePolicyAction(key), Method: "PUT", Success: &success,
			CachePolicyOnly: true, CachePolicyRevisionOnly: true, CachePolicyCoverage: true,
		})
		if err != nil {
			return result, fmt.Errorf("read cache policy audit coverage: %w", err)
		}
		coverage := retained.PolicyRevisions
		if coverage == nil {
			return result, fmt.Errorf("cache policy audit coverage unavailable")
		}
		reason := cachePolicyHistoryIncompleteReason(*coverage, stored[cachePolicySettings[i]], now)
		if reason != "" {
			result.HistoryComplete = false
			// When listing several keys, any missing early history takes priority.
			if result.HistoryIncompleteReason == nil || reason == "earlier_missing" {
				result.HistoryIncompleteReason = &reason
			}
		}
	}
	return result, nil
}

// Historical records for later milestones remain readable after a rollback.
func cachePolicyHistoryField(key, field string) bool {
	fields := map[string]string{
		"master":             "enabled",
		"observability":      "enabled sample_percent chain_percent byte_budget_mib rate_mib_per_s platforms ingress_chain_percent persist_trace_hourly",
		"northstar":          "rollup_enabled backfill_days policy_version model_thresholds excluded_group_ids exclude_media unknown_threshold",
		"upstream_identity":  "mode account_ids rollout_percent chat_compat_uuid header_only_uuid forward_session_headers max_inflight_per_key",
		"anthropic_identity": "anchor_group_ids gemini_anchor metadata_group_ids rollout_percent",
		"routing":            "keep_sticky_on_busy.enabled keep_sticky_on_busy.group_ids keep_sticky_on_busy.rollout_percent affinity_mode",
	}
	for _, candidate := range strings.Fields(fields[key]) {
		if candidate == field {
			return true
		}
	}
	return false
}

// M1 has no migration baseline: its first successful write is revision 1.
func cachePolicyHistoryIncompleteReason(coverage CachePolicyRevisionCoverage, raw string, now time.Time) string {
	if coverage.Count == 0 {
		if raw == "" {
			return ""
		}
		var stored struct {
			Rev       int64      `json:"rev"`
			UpdatedAt *time.Time `json:"updated_at"`
		}
		if json.Unmarshal([]byte(raw), &stored) != nil {
			return "earlier_missing"
		}
		if stored.Rev == 0 {
			return ""
		}
		if stored.UpdatedAt != nil {
			age := now.Sub(*stored.UpdatedAt)
			if age >= -5*time.Second && age <= 5*time.Second {
				return ""
			}
		}
		return "earlier_missing"
	}
	if coverage.MinRev != 1 {
		return "earlier_missing"
	}
	if coverage.MaxRev-coverage.MinRev+1 != coverage.Count {
		return "gap"
	}
	return ""
}
