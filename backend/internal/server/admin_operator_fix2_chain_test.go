//go:build unit

package server_test

import (
	"context"
	"errors"
	"maps"
	"strconv"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type chainRiskSettings struct {
	service.SettingRepository
	w *chainWrites
}

func (r *chainRiskSettings) Set(ctx context.Context, key, value string) error {
	r.w.record(ctx, "risk.set")
	return r.SettingRepository.Set(ctx, key, value)
}

type chainChannels struct {
	service.ChannelRepository
	w   *chainWrites
	row *service.Channel
}

func (r *chainChannels) GetByID(context.Context, int64) (*service.Channel, error) {
	return r.row.Clone(), nil
}
func (r *chainChannels) Update(ctx context.Context, c *service.Channel) error {
	r.w.record(ctx, "channel.update")
	r.row = c
	return nil
}
func (r *chainChannels) ExistsByNameExcluding(context.Context, string, int64) (bool, error) {
	return false, nil
}

type chainAttributes struct {
	service.UserAttributeDefinitionRepository
	w     *chainWrites
	reads int
}

func (r *chainAttributes) GetByID(context.Context, int64) (*service.UserAttributeDefinition, error) {
	r.reads++
	return &service.UserAttributeDefinition{ID: 10}, nil
}
func (r *chainAttributes) Delete(ctx context.Context, _ int64) error {
	r.w.record(ctx, "attribute.delete")
	return nil
}

type chainAttributeValues struct{ *chainAttributes }

func (r *chainAttributeValues) GetByUserIDs(_ context.Context, ids []int64) ([]service.UserAttributeValue, error) {
	r.reads++
	return []service.UserAttributeValue{{UserID: ids[0], AttributeID: 10, Value: "protected-value"}}, nil
}
func (r *chainAttributeValues) GetByUserID(context.Context, int64) ([]service.UserAttributeValue, error) {
	r.reads++
	return nil, nil
}
func (*chainAttributeValues) UpsertBatch(context.Context, int64, []service.UpdateUserAttributeInput) error {
	return errors.New("unexpected upsert")
}
func (r *chainAttributeValues) DeleteByAttributeID(ctx context.Context, _ int64) error {
	r.w.record(ctx, "attribute.values_delete")
	return nil
}
func (*chainAttributeValues) DeleteByUserID(context.Context, int64) error {
	return errors.New("unexpected delete")
}

func TestOperatorURLUserinfoEditRoundTrip(t *testing.T) {
	for _, flags := range [][]operatoraccess.Flag{nil, {operatoraccess.FlagAccountsUpstreamAddress}} {
		for _, typ := range []string{"apikey", "oauth"} {
			f := newOperatorChain(t, service.RoleOperator, true, flags, false)
			f.accounts.row.Type = typ
			f.accounts.row.Credentials["base_url"] = "https://u:p@203.0.113.10"
			f.accounts.row.Credentials["api_base_urls"] = map[string]any{"responses": "https://u:p@203.0.113.10/v1"}
			f.accounts.row.Extra["custom_base_url"] = "https://u:p@203.0.113.10/custom"
			rec := f.do("GET", "/accounts/10", "")
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.NotContains(t, rec.Body.String(), "u:p@")
			rec = f.do("PUT", "/accounts/10", `{"name":"renamed","credentials":{"base_url":"https://***@203.0.113.10","api_base_urls":{"responses":"https://***@203.0.113.10/v1"}},"extra":{"custom_base_url":"https://***@203.0.113.10/custom"}}`)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.Equal(t, "renamed", f.accounts.row.Name)
			require.Equal(t, "https://u:p@203.0.113.10", f.accounts.row.Credentials["base_url"])
			require.Equal(t, map[string]any{"responses": "https://u:p@203.0.113.10/v1"}, f.accounts.row.Credentials["api_base_urls"])
			require.Equal(t, "https://u:p@203.0.113.10/custom", f.accounts.row.Extra["custom_base_url"])
			f.w.calls = nil
			chainDeny(t, f, "PUT", "/accounts/10", `{"credentials":{"base_url":"https://***@203.0.113.11"}}`, 400, "ADMIN_FIELD_FORBIDDEN", "credentials.base_url")
			chainDeny(t, f, "POST", "/accounts/bulk-update", `{"account_ids":[10],"credentials":{"base_url":"https://***@203.0.113.11"}}`, 400, "ADMIN_FIELD_FORBIDDEN", "credentials.base_url")
			rec = f.do("POST", "/accounts/bulk-update", `{"account_ids":[10],"credentials":{"base_url":"https://***@203.0.113.10","api_base_urls":{"responses":"https://***@203.0.113.10/v1"}},"extra":{"custom_base_url":"https://***@203.0.113.10/custom"}}`)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.Equal(t, "https://u:p@203.0.113.10", f.accounts.row.Credentials["base_url"])
		}
	}
}

func TestOperatorMonitorBodyOverrideEditRoundTrip(t *testing.T) {
	for _, flags := range [][]operatoraccess.Flag{nil, {operatoraccess.FlagAccountsUpstreamAddress}} {
		f := newOperatorChain(t, service.RoleOperator, true, flags, false)
		secret := map[string]any{"authorization": "body-secret", "nested": []any{map[string]any{"password": "nested-secret"}}}
		f.monitors.row.Endpoint = "https://u:p@203.0.113.10"
		f.monitors.row.BodyOverrideMode = "merge"
		f.monitors.row.BodyOverride = secret
		f.templates.row.BodyOverrideMode = "merge"
		f.templates.row.BodyOverride = secret
		rec := f.do("GET", "/channel-monitors/10", "")
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `"body_override":"******"`)
		require.NotContains(t, rec.Body.String(), "body-secret")
		require.NotContains(t, rec.Body.String(), "u:p@")
		rec = f.do("PUT", "/channel-monitors/10", `{"name":"renamed","endpoint":"https://***@203.0.113.10","body_override":"******"}`)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.Equal(t, secret, f.monitors.row.BodyOverride)
		require.Equal(t, "https://u:p@203.0.113.10", f.monitors.row.Endpoint)
		rec = f.do("PUT", "/channel-monitor-templates/10", `{"name":"renamed","body_override":"******"}`)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.Equal(t, secret, f.templates.row.BodyOverride)
		f.w.calls = nil
		for _, path := range []string{"/channel-monitors/10", "/channel-monitor-templates/10"} {
			chainDeny(t, f, "PUT", path, `{"body_override":{"token":"different-******"}}`, 400, "ADMIN_FIELD_FORBIDDEN", "body_override")
		}
		chainDeny(t, f, "POST", "/channel-monitors", `{"body_override":{"token":"different-******"}}`, 400, "ADMIN_FIELD_FORBIDDEN", "body_override")
		if len(flags) > 0 {
			rec = f.do("PUT", "/channel-monitors/10", `{"endpoint":"https://203.0.113.11","api_key":"new","body_override":"******"}`)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			require.Empty(t, f.monitors.row.BodyOverride)
			require.Equal(t, "off", f.monitors.row.BodyOverrideMode)
		}
	}
	for _, action := range []string{"apply", "create", "update"} {
		f := newOperatorChain(t, service.RoleOperator, true, []operatoraccess.Flag{operatoraccess.FlagAccountsUpstreamAddress}, false)
		f.templates.row.ExtraHeaders = nil
		f.templates.row.BodyOverride = map[string]any{"token": "secret"}
		switch action {
		case "apply":
			chainDeny(t, f, "POST", "/channel-monitor-templates/10/apply", `{"monitor_ids":[10]}`, 403, "SUPER_ADMIN_REQUIRED", "")
		case "create":
			chainDeny(t, f, "POST", "/channel-monitors", `{"template_id":10}`, 403, "SUPER_ADMIN_REQUIRED", "")
		case "update":
			chainDeny(t, f, "PUT", "/channel-monitors/10", `{"template_id":10}`, 403, "SUPER_ADMIN_REQUIRED", "")
		}
	}
}

func TestOperatorRiskConfigEngineAndMaskedURLHTTP(t *testing.T) {
	for _, engine := range []string{"openai", "typesafe"} {
		f := newOperatorChain(t, service.RoleOperator, true, []operatoraccess.Flag{operatoraccess.FlagAccountsUpstreamAddress}, false)
		f.settingsRepo.all[service.SettingKeyContentModerationConfig] = `{"engine":"` + engine + `","base_url":"https://u:p@203.0.113.10","model":"test","typesafe":{"base_url":"https://a:b@203.0.113.11","model":"other","proxy_id":10}}`
		before := maps.Clone(f.settingsRepo.all)
		other := "typesafe"
		if engine == "typesafe" {
			other = "openai"
		}
		chainDeny(t, f, "PUT", "/risk-control/config", `{"engine":"`+other+`","proxy_id":0}`, 400, "ADMIN_FIELD_FORBIDDEN", "engine")
		require.Equal(t, before, f.settingsRepo.all)
		rec := f.do("GET", "/risk-control/config", "")
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.NotContains(t, rec.Body.String(), "u:p@")
		require.NotContains(t, rec.Body.String(), "a:b@")
		base := "https://***@203.0.113.10"
		stored := "https://u:p@203.0.113.10"
		if engine == "typesafe" {
			base = "https://***@203.0.113.11"
			stored = "https://a:b@203.0.113.11"
		}
		rec = f.do("PUT", "/risk-control/config", `{"engine":"`+engine+`","base_url":"`+base+`","engine_configs":{"openai":{"base_url":"https://***@203.0.113.10"},"typesafe":{"base_url":"https://***@203.0.113.11"}}}`)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.Contains(t, f.settingsRepo.all[service.SettingKeyContentModerationConfig], stored)
		require.NotContains(t, f.settingsRepo.all[service.SettingKeyContentModerationConfig], "***@")
		require.Contains(t, f.w.calls, "risk.set")
		f.w.calls = nil
		chainDeny(t, f, "PUT", "/risk-control/config", `{"base_url":"https://***@203.0.113.12"}`, 400, "ADMIN_FIELD_FORBIDDEN", "base_url")
		chainDeny(t, f, "PUT", "/risk-control/config", `{"engine_configs":{"typesafe":{"base_url":"https://***@203.0.113.12"}}}`, 400, "ADMIN_FIELD_FORBIDDEN", "engine_configs")
	}
}

func TestOperatorChannelStatsPricingHTTP(t *testing.T) {
	for _, field := range []string{"account_stats_pricing_rules", "apply_pricing_to_account_stats"} {
		f := newOperatorChain(t, service.RoleOperator, true, nil, false)
		body := `{"account_stats_pricing_rules":[{"group_ids":[1],"pricing":[{"platform":"openai","models":["*"],"billing_mode":"per_request","per_request_price":1000}]}]}`
		if field == "apply_pricing_to_account_stats" {
			body = `{"apply_pricing_to_account_stats":true}`
		}
		chainDeny(t, f, "PUT", "/channels/10", body, 400, "ADMIN_FIELD_FORBIDDEN", field)
	}
}

func TestOperatorAttributeOwnersAndBatchHTTP(t *testing.T) {
	for _, role := range []string{service.RoleAdmin, service.RoleOperator, service.RoleUser} {
		for _, deleted := range []bool{false, true} {
			if role == service.RoleUser && deleted {
				continue
			}
			f := newOperatorChain(t, service.RoleOperator, true, nil, false)
			owner := int64(2)
			if role == service.RoleUser {
				owner = 7
			}
			// IncludeDeleted is deliberately the only owner load at the guard boundary.
			if owner != 7 {
				f.users.users[owner] = &service.User{ID: owner, Role: role, Status: service.StatusActive}
			}
			if deleted {
				now := time.Now()
				f.users.users[owner].DeletedAt = &now
			}
			f.query.owners = []int64{1, owner}
			chainDeny(t, f, "DELETE", "/user-attributes/10", "", 403, "ADMIN_TARGET_FORBIDDEN", "")
			require.Zero(t, f.attributes.reads)
			chainDeny(t, f, "POST", "/user-attributes/batch", `{"user_ids":[1,`+strconv.FormatInt(owner, 10)+`]}`, 403, "ADMIN_TARGET_FORBIDDEN", "")
			require.Zero(t, f.attributes.reads)
		}
	}
	f := newOperatorChain(t, service.RoleOperator, true, nil, false)
	f.query.err = errors.New("owner load failed")
	chainDeny(t, f, "DELETE", "/user-attributes/10", "", 503, "OPERATOR_ACCESS_UNAVAILABLE", "")
	require.Zero(t, f.attributes.reads)
	// Warm the shared snapshot cache as admin; denial must occur even on a hit.
	admin := newOperatorChain(t, service.RoleAdmin, true, nil, false)
	rec := admin.do("POST", "/user-attributes/batch", `{"user_ids":[1,7]}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	rec = f.do("POST", "/user-attributes/batch", `{"user_ids":[1,7]}`)
	require.Equal(t, 403, rec.Code, rec.Body.String())
	require.Empty(t, rec.Header().Get("X-Snapshot-Cache"))
	require.NotContains(t, rec.Body.String(), "protected-value")
	require.Zero(t, f.attributes.reads)
	allowed := newOperatorChain(t, service.RoleOperator, true, nil, false)
	allowed.query.owners = []int64{1}
	rec = allowed.do("DELETE", "/user-attributes/10", "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, allowed.w.calls, "attribute.values_delete")
	require.Contains(t, allowed.w.calls, "attribute.delete")
}

func TestOperatorGroupReasoningAndPriceHTTP(t *testing.T) {
	f := newOperatorChain(t, service.RoleOperator, true, nil, false)
	chainDeny(t, f, "PUT", "/groups/1", `{"max_reasoning_effort":"high"}`, 400, "ADMIN_FIELD_FORBIDDEN", "max_reasoning_effort")
	chainDeny(t, f, "PUT", "/groups/1", `{"reasoning_effort_mappings":[{"from":"low","to":"high"}]}`, 400, "ADMIN_FIELD_FORBIDDEN", "reasoning_effort_mappings")
	f.groups.row.MaxReasoningEffort = "high"
	f.groups.row.ReasoningEffortMappings = []service.ReasoningEffortMapping{{From: "low", To: "high"}}
	rec := f.do("PUT", "/groups/1", `{"name":"renamed","max_reasoning_effort":" HIGH ","reasoning_effort_mappings":[{"from":" LOW ","to":" HIGH "}],"image_price_1k":-1}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Nil(t, f.groups.row.ImagePrice1K)
	require.Equal(t, "high", f.groups.row.MaxReasoningEffort)
	require.Contains(t, f.w.calls, "group.update")
}
