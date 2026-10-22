-- +goose Up
-- Entries about a user (target_type 'user'), newest first, for that user's
-- own log (#39). A partial index on the partitioned table is created on
-- every partition, also on the ones audit_log_ensure_partitions creates
-- later. CONCURRENTLY is not possible on a partitioned table; the log is
-- small at this stage, so a plain create is fine.
CREATE INDEX audit_log_target_user_idx ON audit_log (target_id, at, id) WHERE target_type = 'user';

-- +goose Down
DROP INDEX audit_log_target_user_idx;
