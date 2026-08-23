CREATE TABLE resource_reservations (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    issue_id TEXT NOT NULL REFERENCES issues(id),
    attempt_id TEXT NOT NULL REFERENCES work_attempts(id),
    kind TEXT NOT NULL CHECK (kind IN ('file', 'directory', 'glob', 'logical')),
    display_value TEXT NOT NULL CHECK (length(display_value) > 0),
    comparison_value TEXT NOT NULL CHECK (length(comparison_value) > 0),
    normalized_json TEXT NOT NULL CHECK (json_valid(normalized_json)),
    status TEXT NOT NULL CHECK (status IN ('active', 'released')),
    version INTEGER NOT NULL CHECK (version >= 1),
    created_at TEXT NOT NULL,
    released_at TEXT,
    release_reason TEXT CHECK (release_reason IS NULL OR release_reason IN (
        'completed', 'failed', 'interrupted', 'expired', 'force_released', 'explicit'
    )),
    CHECK (
        (status = 'active' AND released_at IS NULL AND release_reason IS NULL)
        OR (status = 'released' AND released_at IS NOT NULL AND release_reason IS NOT NULL)
    )
) STRICT;

-- Fast, database-enforced conflict detection for the two resource kinds
-- whose overlap rule is exact equality (file, logical); directory and glob
-- overlap is not expressible as a unique index and is checked in
-- application code inside the same acquisition transaction, per docs/02
-- §18.4 and the ISSUE-178 locked schema.
CREATE UNIQUE INDEX idx_reservations_active_identity
ON resource_reservations(kind, comparison_value)
WHERE status = 'active' AND kind IN ('file', 'logical');

-- Supports loading the full active set inside an acquisition transaction in
-- a stable order, without a full-table scan once history accumulates.
CREATE INDEX idx_reservations_active
ON resource_reservations(id)
WHERE status = 'active';

CREATE INDEX idx_reservations_attempt
ON resource_reservations(attempt_id, status);

CREATE INDEX idx_reservations_issue
ON resource_reservations(issue_id, created_at);
