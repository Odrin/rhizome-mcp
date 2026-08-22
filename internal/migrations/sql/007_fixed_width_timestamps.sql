UPDATE projects SET created_at = substr(created_at, 1, 19) || '.' || substr((CASE WHEN instr(created_at, '.') = 0 THEN '' ELSE substr(created_at, instr(created_at, '.') + 1, length(created_at) - instr(created_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE created_at IS NOT NULL;
UPDATE projects SET updated_at = substr(updated_at, 1, 19) || '.' || substr((CASE WHEN instr(updated_at, '.') = 0 THEN '' ELSE substr(updated_at, instr(updated_at, '.') + 1, length(updated_at) - instr(updated_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE updated_at IS NOT NULL;

UPDATE agent_sessions SET started_at = substr(started_at, 1, 19) || '.' || substr((CASE WHEN instr(started_at, '.') = 0 THEN '' ELSE substr(started_at, instr(started_at, '.') + 1, length(started_at) - instr(started_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE started_at IS NOT NULL;
UPDATE agent_sessions SET last_seen_at = substr(last_seen_at, 1, 19) || '.' || substr((CASE WHEN instr(last_seen_at, '.') = 0 THEN '' ELSE substr(last_seen_at, instr(last_seen_at, '.') + 1, length(last_seen_at) - instr(last_seen_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE last_seen_at IS NOT NULL;
UPDATE agent_sessions SET ended_at = substr(ended_at, 1, 19) || '.' || substr((CASE WHEN instr(ended_at, '.') = 0 THEN '' ELSE substr(ended_at, instr(ended_at, '.') + 1, length(ended_at) - instr(ended_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE ended_at IS NOT NULL;

UPDATE issues SET created_at = substr(created_at, 1, 19) || '.' || substr((CASE WHEN instr(created_at, '.') = 0 THEN '' ELSE substr(created_at, instr(created_at, '.') + 1, length(created_at) - instr(created_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE created_at IS NOT NULL;
UPDATE issues SET updated_at = substr(updated_at, 1, 19) || '.' || substr((CASE WHEN instr(updated_at, '.') = 0 THEN '' ELSE substr(updated_at, instr(updated_at, '.') + 1, length(updated_at) - instr(updated_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE updated_at IS NOT NULL;
UPDATE issues SET closed_at = substr(closed_at, 1, 19) || '.' || substr((CASE WHEN instr(closed_at, '.') = 0 THEN '' ELSE substr(closed_at, instr(closed_at, '.') + 1, length(closed_at) - instr(closed_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE closed_at IS NOT NULL;
UPDATE issues SET archived_at = substr(archived_at, 1, 19) || '.' || substr((CASE WHEN instr(archived_at, '.') = 0 THEN '' ELSE substr(archived_at, instr(archived_at, '.') + 1, length(archived_at) - instr(archived_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE archived_at IS NOT NULL;

UPDATE labels SET created_at = substr(created_at, 1, 19) || '.' || substr((CASE WHEN instr(created_at, '.') = 0 THEN '' ELSE substr(created_at, instr(created_at, '.') + 1, length(created_at) - instr(created_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE created_at IS NOT NULL;

UPDATE issue_relations SET created_at = substr(created_at, 1, 19) || '.' || substr((CASE WHEN instr(created_at, '.') = 0 THEN '' ELSE substr(created_at, instr(created_at, '.') + 1, length(created_at) - instr(created_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE created_at IS NOT NULL;

UPDATE comments SET created_at = substr(created_at, 1, 19) || '.' || substr((CASE WHEN instr(created_at, '.') = 0 THEN '' ELSE substr(created_at, instr(created_at, '.') + 1, length(created_at) - instr(created_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE created_at IS NOT NULL;
UPDATE comments SET edited_at = substr(edited_at, 1, 19) || '.' || substr((CASE WHEN instr(edited_at, '.') = 0 THEN '' ELSE substr(edited_at, instr(edited_at, '.') + 1, length(edited_at) - instr(edited_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE edited_at IS NOT NULL;

UPDATE decisions SET created_at = substr(created_at, 1, 19) || '.' || substr((CASE WHEN instr(created_at, '.') = 0 THEN '' ELSE substr(created_at, instr(created_at, '.') + 1, length(created_at) - instr(created_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE created_at IS NOT NULL;

UPDATE work_attempts SET lease_expires_at = substr(lease_expires_at, 1, 19) || '.' || substr((CASE WHEN instr(lease_expires_at, '.') = 0 THEN '' ELSE substr(lease_expires_at, instr(lease_expires_at, '.') + 1, length(lease_expires_at) - instr(lease_expires_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE lease_expires_at IS NOT NULL;
UPDATE work_attempts SET started_at = substr(started_at, 1, 19) || '.' || substr((CASE WHEN instr(started_at, '.') = 0 THEN '' ELSE substr(started_at, instr(started_at, '.') + 1, length(started_at) - instr(started_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE started_at IS NOT NULL;
UPDATE work_attempts SET last_heartbeat_at = substr(last_heartbeat_at, 1, 19) || '.' || substr((CASE WHEN instr(last_heartbeat_at, '.') = 0 THEN '' ELSE substr(last_heartbeat_at, instr(last_heartbeat_at, '.') + 1, length(last_heartbeat_at) - instr(last_heartbeat_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE last_heartbeat_at IS NOT NULL;
UPDATE work_attempts SET finished_at = substr(finished_at, 1, 19) || '.' || substr((CASE WHEN instr(finished_at, '.') = 0 THEN '' ELSE substr(finished_at, instr(finished_at, '.') + 1, length(finished_at) - instr(finished_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE finished_at IS NOT NULL;

UPDATE attempt_notes SET created_at = substr(created_at, 1, 19) || '.' || substr((CASE WHEN instr(created_at, '.') = 0 THEN '' ELSE substr(created_at, instr(created_at, '.') + 1, length(created_at) - instr(created_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE created_at IS NOT NULL;

UPDATE artifacts SET created_at = substr(created_at, 1, 19) || '.' || substr((CASE WHEN instr(created_at, '.') = 0 THEN '' ELSE substr(created_at, instr(created_at, '.') + 1, length(created_at) - instr(created_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE created_at IS NOT NULL;

-- issue_events is append-only (issue_events_append_only_update raises on UPDATE); lift the guard for the rewrite only.
DROP TRIGGER issue_events_append_only_update;
UPDATE issue_events SET created_at = substr(created_at, 1, 19) || '.' || substr((CASE WHEN instr(created_at, '.') = 0 THEN '' ELSE substr(created_at, instr(created_at, '.') + 1, length(created_at) - instr(created_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE created_at IS NOT NULL;
CREATE TRIGGER issue_events_append_only_update
BEFORE UPDATE ON issue_events
BEGIN
    SELECT RAISE(ABORT, 'issue events are append-only');
END;

UPDATE idempotency_records SET created_at = substr(created_at, 1, 19) || '.' || substr((CASE WHEN instr(created_at, '.') = 0 THEN '' ELSE substr(created_at, instr(created_at, '.') + 1, length(created_at) - instr(created_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE created_at IS NOT NULL;

UPDATE review_targets SET created_at = substr(created_at, 1, 19) || '.' || substr((CASE WHEN instr(created_at, '.') = 0 THEN '' ELSE substr(created_at, instr(created_at, '.') + 1, length(created_at) - instr(created_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE created_at IS NOT NULL;

UPDATE review_requests SET created_at = substr(created_at, 1, 19) || '.' || substr((CASE WHEN instr(created_at, '.') = 0 THEN '' ELSE substr(created_at, instr(created_at, '.') + 1, length(created_at) - instr(created_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE created_at IS NOT NULL;
UPDATE review_requests SET resolved_at = substr(resolved_at, 1, 19) || '.' || substr((CASE WHEN instr(resolved_at, '.') = 0 THEN '' ELSE substr(resolved_at, instr(resolved_at, '.') + 1, length(resolved_at) - instr(resolved_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE resolved_at IS NOT NULL;

UPDATE review_outcomes SET created_at = substr(created_at, 1, 19) || '.' || substr((CASE WHEN instr(created_at, '.') = 0 THEN '' ELSE substr(created_at, instr(created_at, '.') + 1, length(created_at) - instr(created_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE created_at IS NOT NULL;

-- review_events is append-only (review_events_append_only_update raises on UPDATE); lift the guard for the rewrite only.
DROP TRIGGER review_events_append_only_update;
UPDATE review_events SET created_at = substr(created_at, 1, 19) || '.' || substr((CASE WHEN instr(created_at, '.') = 0 THEN '' ELSE substr(created_at, instr(created_at, '.') + 1, length(created_at) - instr(created_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE created_at IS NOT NULL;
CREATE TRIGGER review_events_append_only_update
BEFORE UPDATE ON review_events
BEGIN
    SELECT RAISE(ABORT, 'review events are append-only');
END;

UPDATE review_follow_ups SET created_at = substr(created_at, 1, 19) || '.' || substr((CASE WHEN instr(created_at, '.') = 0 THEN '' ELSE substr(created_at, instr(created_at, '.') + 1, length(created_at) - instr(created_at, '.') - 1) END) || '000000000', 1, 9) || 'Z' WHERE created_at IS NOT NULL;
