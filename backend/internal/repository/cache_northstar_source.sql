WITH raw AS MATERIALIZED (
 SELECT u.id,u.created_at,COALESCE(NULLIF(g.platform,''),a.platform,'unknown') AS platform,
        COALESCE(u.group_id,0) AS group_id,COALESCE(g.name,'') AS group_label,
        COALESCE(NULLIF(u.upstream_model,''),u.model) AS model_key,
        u.account_id,COALESCE(a.name,'') AS account_label,
        CASE WHEN a.type='oauth' THEN 'oauth:'||a.platform
             ELSE COALESCE(NULLIF(lower(substring(
               COALESCE(a.credentials->>'base_url','')
               from '^https?://(?:[^/@]*@)?([^:/?#]+)')),''),'unknown') END AS upstream_host,
        CASE
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%claude-cli%' THEN 'claude-cli'
          WHEN lower(COALESCE(u.user_agent,'')) ~ 'codex_cli_rs|codex-cli|codex/' THEN 'codex_cli_rs'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%cursor%' THEN 'cursor'
          WHEN lower(COALESCE(u.user_agent,'')) ~ 'cline|roo' THEN 'cline/roo'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%opencode%' THEN 'opencode'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%cherry%' THEN 'cherry'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%anthropic%python%' THEN 'anthropic-python-sdk'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%anthropic%node%' THEN 'anthropic-node-sdk'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%openai%python%' THEN 'openai-python-sdk'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%openai%node%' THEN 'openai-node-sdk'
          WHEN lower(COALESCE(u.user_agent,'')) ~ 'python-requests|python-httpx|aiohttp' THEN 'python-http-client'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%go-http-client%' THEN 'go-http-client'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%curl%' THEN 'curl'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%mozilla%' THEN 'browser'
          WHEN lower(COALESCE(u.user_agent,'')) = '' THEN 'missing'
          WHEN lower(COALESCE(u.user_agent,'')) ~ 'codex.*vscode|vscode.*codex' THEN 'codex-vscode'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%codex%' THEN 'codex-family'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%claude%' THEN 'claude-family'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%kilo%' THEN 'cline/roo/kilo'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%litellm%' THEN 'litellm'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%lobe%' THEN 'lobe'
          WHEN lower(COALESCE(u.user_agent,'')) ~ 'openclaw|clawdbot|moltbot' THEN 'openclaw'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%continue%' THEN 'continue'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%windsurf%' THEN 'windsurf'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%kiro%' THEN 'kiro'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%droid%' THEN 'droid'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%copilot%' THEN 'copilot'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%langchain%' THEN 'langchain'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%ollama%' THEN 'ollama'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%reqwest%' THEN 'rust-http-client'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%fasthttp%' THEN 'go-fasthttp'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%guzzle%' THEN 'php-guzzle'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%insomnia%' THEN 'insomnia'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%librechat%' THEN 'librechat'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%paseo%' THEN 'paseo'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%trae%' THEN 'trae'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%openai%' THEN 'openai-sdk-unspecified'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%anthropic%' THEN 'anthropic-sdk-unspecified'
          WHEN lower(COALESCE(u.user_agent,'')) ~ 'node|undici' THEN 'node-http-client'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%axios%' THEN 'axios'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%okhttp%' THEN 'okhttp'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%google%' THEN 'google-sdk'
          WHEN lower(COALESCE(u.user_agent,'')) LIKE '%postman%' THEN 'postman'
          ELSE 'other' END AS client_family,
        CASE WHEN u.openai_ws_mode THEN 'ws'
             WHEN u.inbound_endpoint LIKE '%/responses%' THEN 'responses_http'
             WHEN u.inbound_endpoint LIKE '%/chat/completions%' THEN 'chat_completions_http'
             WHEN u.inbound_endpoint LIKE '%/messages%' THEN 'messages'
             WHEN a.platform='gemini' THEN 'gemini'
             ELSE 'unknown' END AS protocol,
        u.input_tokens::bigint+u.cache_creation_tokens+u.cache_read_tokens AS p_tokens,
        u.input_tokens::bigint AS input_tokens_uncached,
        u.cache_creation_tokens::bigint AS creation_tokens,
        u.cache_read_tokens::bigint AS read_tokens,
        (u.image_count>0 OR u.video_count>0) AS has_media,
        CASE WHEN NULLIF(btrim(u.session_id),'') IS NOT NULL THEN
          substring(sha256(convert_to(
            u.api_key_id::text||':'||COALESCE(u.group_id,0)::text||':'||
            COALESCE(NULLIF(g.platform,''),a.platform,'unknown')||':'||u.session_id,'UTF8')) for 16)
        END AS session_key_hash,
        u.input_cost,u.cache_read_cost,u.cache_creation_cost,u.output_cost,u.total_cost,
        CASE WHEN u.total_cost<>0 THEN u.actual_cost/u.total_cost ELSE u.rate_multiplier END AS charged_multiplier,
        COALESCE(u.account_rate_multiplier,1) AS upstream_multiplier,
        (u.account_stats_cost IS NULL OR u.account_stats_cost=u.total_cost) AS upstream_comparable
 FROM usage_logs u
 LEFT JOIN accounts a ON a.id=u.account_id
 LEFT JOIN groups g ON g.id=u.group_id
 WHERE u.created_at >= $1::timestamptz - interval '1 hour' AND u.created_at < $2
), eligible AS MATERIALIZED (
 SELECT * FROM raw
 WHERE p_tokens>0
), pairs AS (
 SELECT *,lag(p_tokens) OVER session_order AS previous_p,
          lag(created_at) OVER session_order AS previous_at
 FROM eligible
 WINDOW session_order AS (PARTITION BY session_key_hash ORDER BY created_at,id)
), northstar_values AS (
 SELECT raw.*,
  CASE WHEN raw.session_key_hash IS NOT NULL
       AND pairs.previous_at >= raw.created_at-interval '1 hour'
       THEN greatest(least(pairs.previous_p,raw.p_tokens)-raw.read_tokens,0)
       ELSE 0 END AS o_recorded_tokens,
  CASE WHEN raw.p_tokens>=6144 THEN 6144 WHEN raw.p_tokens>=4096 THEN 4096
       WHEN raw.p_tokens>=2048 THEN 2048 WHEN raw.p_tokens>=1024 THEN 1024
       WHEN raw.p_tokens>=512 THEN 512 ELSE 0 END AS threshold_band,
  CASE WHEN raw.platform IN ('anthropic','gemini') AND raw.input_tokens_uncached=0
       AND raw.read_tokens>0 THEN raw.read_tokens ELSE 0 END AS read_maybe_rewritten,
  CASE WHEN abs(raw.input_cost+raw.output_cost+raw.cache_creation_cost+raw.cache_read_cost-raw.total_cost)
       > abs(raw.total_cost)*0.000001 THEN 1 ELSE 0 END AS component_mismatch
 FROM raw LEFT JOIN pairs ON pairs.id=raw.id AND pairs.created_at=raw.created_at
 WHERE raw.created_at >= $1 AND raw.created_at < $2
)
