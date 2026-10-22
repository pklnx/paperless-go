-- +goose Up
-- The actor system (#49): what the server does on its own, such as the
-- consume folder. Like cli, a system entry has no client; it is by no user
-- either (user_id is who acted), and the user it concerns is its target.
--
-- The checks are on the partitioned table: adding one checks every
-- partition, drops and adds reach the partitions' inherited copies, and
-- later partitions (audit_log_ensure_partitions) inherit them. Validation
-- does not fire the append-only row triggers.

-- The unnamed check of 00005 (actor <> 'cli' OR no client) has a generated
-- name. It is the only check on exactly actor, client_ip and user_agent_id;
-- anything else stops the migration.
-- +goose StatementBegin
DO $$
DECLARE
    names text[];
BEGIN
    SELECT array_agg(c.conname::text) INTO names
    FROM pg_constraint c
    WHERE c.conrelid = 'audit_log'::regclass AND c.contype = 'c'
      AND ARRAY(SELECT a.attname::text FROM pg_attribute a
                WHERE a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
                ORDER BY a.attname) = ARRAY['actor', 'client_ip', 'user_agent_id'];
    IF coalesce(cardinality(names), 0) <> 1 THEN
        RAISE EXCEPTION 'audit_log: expected one check on actor, client_ip and user_agent_id, found %',
            coalesce(names::text, 'none');
    END IF;
    EXECUTE format('ALTER TABLE audit_log DROP CONSTRAINT %I', names[1]);
END $$;
-- +goose StatementEnd

ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_check
    CHECK (actor IN ('session', 'api_key', 'cli', 'system'));
ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_no_client
    CHECK (actor NOT IN ('cli', 'system') OR (client_ip IS NULL AND user_agent_id IS NULL));
ALTER TABLE audit_log ADD CONSTRAINT audit_log_system_no_user
    CHECK (actor <> 'system' OR user_id IS NULL);

-- +goose Down
-- The log is append-only, so its system entries cannot be dropped, and the
-- old checks would refuse them: going back needs a backup restore.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM audit_log WHERE actor = 'system') THEN
        RAISE EXCEPTION 'the audit log has entries by the server (actor system); they are not dropped by a rollback'
            USING HINT = 'Restore a backup made before the upgrade instead.';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE audit_log DROP CONSTRAINT audit_log_system_no_user;
ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_no_client;
ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_check
    CHECK (actor IN ('session', 'api_key', 'cli'));
ALTER TABLE audit_log ADD CHECK (actor <> 'cli' OR (client_ip IS NULL AND user_agent_id IS NULL));
