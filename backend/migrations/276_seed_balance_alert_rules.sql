-- Gateway resilience (0.4.0): default alert rules for upstream balance states
-- (tasks 2.6). An account that runs out at its relay fires the first rule,
-- which resolves after a top-up and recheck; the second fires when an account
-- stays out longer than balance.notify_after_hours (default 72 hours).
--
-- Idempotent via ON CONFLICT (name) DO NOTHING. Rolling back to 0.3.0 keeps
-- the rows; its evaluator does not know the metric types and skips them.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

INSERT INTO ops_alert_rules (
    name, description, enabled, metric_type, operator, threshold,
    window_minutes, sustained_minutes, severity, notify_email, cooldown_minutes,
    created_at, updated_at
) VALUES (
    '上游余额不足',
    '有账号在上游中转处余额不足（EXHAUSTED）时触发；充值并复检恢复后自动解除',
    true, 'account_balance_exhausted_count', '>', 0, 5, 1, 'P2', true, 60, NOW(), NOW()
) ON CONFLICT (name) DO NOTHING;

INSERT INTO ops_alert_rules (
    name, description, enabled, metric_type, operator, threshold,
    window_minutes, sustained_minutes, severity, notify_email, cooldown_minutes,
    created_at, updated_at
) VALUES (
    '上游余额长期未恢复',
    '有账号余额不足超过设定时长（网关韧性 balance.notify_after_hours，默认 72 小时）仍未恢复时触发',
    true, 'account_balance_unrecovered_count', '>', 0, 5, 1, 'P1', true, 1440, NOW(), NOW()
) ON CONFLICT (name) DO NOTHING;
