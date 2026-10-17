-- Gateway resilience (0.4.0): remove the two balance alert rules seeded by
-- 276. Upstream balance failures now pause the account for
-- balance.pause_seconds and real traffic retries it afterwards; the balance
-- states the rules watched are gone.
--
-- 276 already ran on the 0.4.0-rc.1 databases and stays unchanged. A rule
-- is removed only when its name and metric type both match, so rules an
-- administrator renamed or pointed at another metric stay. Rolling back to
-- 0.3.0 is unaffected: it never had these rules.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

DELETE FROM ops_alert_rules
WHERE (name, metric_type) IN (
    ('上游余额不足', 'account_balance_exhausted_count'),
    ('上游余额长期未恢复', 'account_balance_unrecovered_count')
);
