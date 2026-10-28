package repository

// iqTargetIdentityKeys are account credentials/transport fields whose change
// invalidates all model IQ target verdicts.
var iqTargetIdentityKeys = []string{"api_key", "base_url", "access_token", "refresh_token", "chatgpt_account_id", "header_override_enabled", "header_overrides", "user_agent", "model_mapping"}
