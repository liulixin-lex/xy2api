-- Explicit deployment hardening, never executed by the application/migrations.
-- Run as the function owner/superuser after normal migrations, in a transaction:
--   SELECT set_config('mcp_setup.writer_role', 'ACTUAL_APPLICATION_WRITER', false);
--   \i deploy/mcp-reader-function-privileges.sql
-- Review the affected functions and all application callers before execution.
-- If other trusted application roles use these functions, GRANT EXECUTE to them
-- explicitly first. REVOKE FROM the reader alone cannot override PUBLIC rights.
-- This preserves the configured writer's execution rights before narrowing
-- PUBLIC. Trigger-only functions are not directly callable and are untouched.
-- Re-run after any migration that creates or replaces executable functions.

BEGIN;
DO $mcp_reader_functions$
DECLARE
    writer_role TEXT := current_setting('mcp_setup.writer_role', true);
    reader_oid OID := (SELECT oid FROM pg_roles WHERE rolname = 'xy2api_mcp_reader');
    fn RECORD;
BEGIN
    IF writer_role IS NULL OR writer_role = '' OR writer_role = 'xy2api_mcp_reader'
       OR NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = writer_role) THEN
        RAISE EXCEPTION 'Set mcp_setup.writer_role to the actual trusted application writer first';
    END IF;
    IF reader_oid IS NULL THEN
        RAISE EXCEPTION 'Create xy2api_mcp_reader before hardening function privileges';
    END IF;
    FOR fn IN
        SELECT p.oid::regprocedure AS signature
        FROM pg_proc p
        JOIN pg_namespace n ON n.oid = p.pronamespace
        WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
          AND p.prorettype NOT IN ('pg_catalog.trigger'::regtype, 'pg_catalog.event_trigger'::regtype)
          AND (p.prosecdef OR p.provolatile <> 'i')
          AND has_function_privilege(reader_oid, p.oid, 'EXECUTE')
    LOOP
        EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO %I', fn.signature, writer_role);
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn.signature);
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM xy2api_mcp_reader', fn.signature);
    END LOOP;
END
$mcp_reader_functions$;
COMMIT;
