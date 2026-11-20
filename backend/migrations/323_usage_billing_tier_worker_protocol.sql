SET LOCAL lock_timeout = '5s';

CREATE FUNCTION enforce_billing_intent_worker_protocol()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF (jsonb_typeof(OLD.command #> '{Usage,ServiceTierDetails}') IS DISTINCT FROM 'object'
        AND jsonb_typeof(NEW.command #> '{Usage,ServiceTierDetails}') IS DISTINCT FROM 'object') OR
       current_setting('xy2api.billing_worker_protocol', true) = '2' THEN
        RETURN NEW;
    END IF;

    -- Legacy recovery leases via UPDATE ... RETURNING. A skipped row is
    -- neither leased nor returned to a worker that cannot recover its evidence.
    IF NEW.attempts > OLD.attempts AND NEW.state = OLD.state AND
       NEW.command IS NOT DISTINCT FROM OLD.command THEN
        RETURN NULL;
    END IF;

    -- Also guard the settlement of a protected intent reached by direct retry.
    RAISE EXCEPTION 'billing intent requires a newer worker protocol'
        USING ERRCODE = 'check_violation';
END;
$$;

CREATE TRIGGER enforce_billing_intent_worker_protocol
BEFORE UPDATE ON usage_billing_intents
FOR EACH ROW EXECUTE FUNCTION enforce_billing_intent_worker_protocol();
