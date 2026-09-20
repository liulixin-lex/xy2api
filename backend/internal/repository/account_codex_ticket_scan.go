package repository

import (
	"context"
	"encoding/json"

	"github.com/liulixin-lex/xy2api/internal/service"
)

// Keyset pages do not hydrate groups, tokens or raw STATE. Proxy identity is read
// in the same statement, including in-place edits that leave proxy_id unchanged.
func (r *accountRepository) ListCodexTicketScanPage(ctx context.Context, after int64, limit int) ([]service.Account, error) {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	rows, err := r.client.QueryContext(ctx, `SELECT a.id,a.platform,a.type,a.status,a.schedulable,
 a.proxy_id,a.expires_at,a.auto_pause_on_expired,a.rate_limit_reset_at,a.overload_until,a.temp_unschedulable_until,
 a.iq_check,
 COALESCE((SELECT jsonb_object_agg(key,value) FROM jsonb_each(COALESCE(a.credentials,'{}'::jsonb))
 WHERE key IN ('chatgpt_account_id','chatgpt_user_id','email','client_id','oauth_type','openai_auth_type','agent_identity','header_override_enabled','header_overrides','user_agent','device_id','base_url')
 OR (a.type='setup-token' AND key='access_token')),'{}'::jsonb),
 COALESCE((SELECT jsonb_object_agg(key,CASE WHEN key LIKE 'codex_turn_ticket:%' THEN value-'state' ELSE value END)
 FROM jsonb_each(COALESCE(a.extra,'{}'::jsonb))
 WHERE key LIKE 'codex_turn_ticket:%' OR key IN ('codex_ticket_config','codex_ticket_quality','codex_ticket_runtime','codex_fingerprint_mode','codex_fingerprint_seed','enable_tls_fingerprint','tls_fingerprint_profile_id','openai_responses_mode','openai_passthrough')),'{}'::jsonb),
 CASE WHEN p.id IS NULL THEN NULL ELSE jsonb_build_object('ID',p.id,'Protocol',p.protocol,'Host',p.host,'Port',p.port,'Username',p.username,'Password',p.password,'Status',p.status) END
 FROM accounts a LEFT JOIN proxies p ON p.id=a.proxy_id AND p.deleted_at IS NULL
 WHERE a.id>$1 AND a.deleted_at IS NULL AND a.platform='openai' AND a.parent_account_id IS NULL
 AND a.extra->'codex_ticket_config'->>'enabled'='true'
 ORDER BY a.id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.Account, 0, limit)
	for rows.Next() {
		var a service.Account
		var iq, credentials, extra, proxy []byte
		if err = rows.Scan(&a.ID, &a.Platform, &a.Type, &a.Status, &a.Schedulable, &a.ProxyID, &a.ExpiresAt, &a.AutoPauseOnExpired, &a.RateLimitResetAt, &a.OverloadUntil, &a.TempUnschedulableUntil, &iq, &credentials, &extra, &proxy); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(iq, &a.IQCheck); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(credentials, &a.Credentials); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(extra, &a.Extra); err != nil {
			return nil, err
		}
		if len(proxy) > 0 {
			if err = json.Unmarshal(proxy, &a.Proxy); err != nil {
				return nil, err
			}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
