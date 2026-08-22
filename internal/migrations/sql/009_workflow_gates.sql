CREATE TABLE workflow_policies (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    selector_json TEXT NOT NULL CHECK (json_valid(selector_json)),
    requirements_json TEXT NOT NULL CHECK (json_valid(requirements_json)),
    status TEXT NOT NULL CHECK (status IN ('active', 'archived')),
    version INTEGER NOT NULL CHECK (version >= 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX idx_workflow_policies_status_created
ON workflow_policies(status, created_at, id);

CREATE TABLE workflow_policy_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    policy_id TEXT NOT NULL REFERENCES workflow_policies(id),
    event_type TEXT NOT NULL CHECK (event_type IN ('policy_created', 'policy_updated', 'policy_archived')),
    session_id TEXT REFERENCES agent_sessions(id),
    prior_version INTEGER CHECK (prior_version IS NULL OR prior_version >= 1),
    new_version INTEGER NOT NULL CHECK (new_version >= 1),
    payload TEXT NOT NULL CHECK (json_valid(payload)),
    created_at TEXT NOT NULL,
    CHECK ((event_type = 'policy_created' AND prior_version IS NULL) OR (event_type <> 'policy_created' AND prior_version IS NOT NULL))
) STRICT;

CREATE INDEX idx_workflow_policy_events_policy
ON workflow_policy_events(policy_id, id);

CREATE TRIGGER workflow_policy_events_append_only_update
BEFORE UPDATE ON workflow_policy_events
BEGIN
    SELECT RAISE(ABORT, 'workflow policy events are append-only');
END;

CREATE TRIGGER workflow_policy_events_append_only_delete
BEFORE DELETE ON workflow_policy_events
BEGIN
    SELECT RAISE(ABORT, 'workflow policy events are append-only');
END;

CREATE TABLE attempt_gate_snapshots (
    attempt_id TEXT PRIMARY KEY REFERENCES work_attempts(id),
    requirements_json TEXT NOT NULL CHECK (json_valid(requirements_json)),
    source_policies_json TEXT NOT NULL CHECK (json_valid(source_policies_json)),
    fingerprint TEXT NOT NULL CHECK (length(fingerprint) = 64),
    issue_version INTEGER NOT NULL CHECK (issue_version >= 1),
    created_at TEXT NOT NULL
) STRICT;

CREATE TRIGGER attempt_gate_snapshots_immutable_update
BEFORE UPDATE ON attempt_gate_snapshots
BEGIN
    SELECT RAISE(ABORT, 'attempt gate snapshots are immutable');
END;

CREATE TRIGGER attempt_gate_snapshots_immutable_delete
BEFORE DELETE ON attempt_gate_snapshots
BEGIN
    SELECT RAISE(ABORT, 'attempt gate snapshots are immutable');
END;

CREATE TABLE review_target_gate_snapshots (
    target_id TEXT PRIMARY KEY REFERENCES review_targets(id),
    requirements_json TEXT NOT NULL CHECK (json_valid(requirements_json)),
    source_policies_json TEXT NOT NULL CHECK (json_valid(source_policies_json)),
    fingerprint TEXT NOT NULL CHECK (length(fingerprint) = 64),
    issue_version INTEGER NOT NULL CHECK (issue_version >= 1),
    created_at TEXT NOT NULL
) STRICT;

CREATE TRIGGER review_target_gate_snapshots_immutable_update
BEFORE UPDATE ON review_target_gate_snapshots
BEGIN
    SELECT RAISE(ABORT, 'review target gate snapshots are immutable');
END;

CREATE TRIGGER review_target_gate_snapshots_immutable_delete
BEFORE DELETE ON review_target_gate_snapshots
BEGIN
    SELECT RAISE(ABORT, 'review target gate snapshots are immutable');
END;
