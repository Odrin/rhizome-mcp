CREATE TABLE gate_evidence (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    attempt_id TEXT NOT NULL REFERENCES work_attempts(id),
    issue_id TEXT NOT NULL REFERENCES issues(id),
    key TEXT NOT NULL CHECK (length(key) > 0),
    result TEXT NOT NULL CHECK (result IN ('satisfied', 'not_applicable')),
    summary TEXT NOT NULL CHECK (length(summary) > 0),
    details TEXT,
    artifact_ids_json TEXT NOT NULL CHECK (json_valid(artifact_ids_json)),
    version INTEGER NOT NULL CHECK (version >= 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (attempt_id, key)
) STRICT;

CREATE INDEX idx_gate_evidence_issue
ON gate_evidence(issue_id, updated_at);

CREATE TRIGGER gate_evidence_immutable_after_attempt_inactive
BEFORE UPDATE ON gate_evidence
WHEN (SELECT status FROM work_attempts WHERE id = OLD.attempt_id) <> 'active'
BEGIN
    SELECT RAISE(ABORT, 'gate evidence is immutable once its attempt is no longer active');
END;

CREATE TRIGGER gate_evidence_no_delete
BEFORE DELETE ON gate_evidence
BEGIN
    SELECT RAISE(ABORT, 'gate evidence is never deleted');
END;

CREATE TABLE gate_evidence_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    evidence_id TEXT NOT NULL REFERENCES gate_evidence(id),
    attempt_id TEXT NOT NULL REFERENCES work_attempts(id),
    issue_id TEXT NOT NULL REFERENCES issues(id),
    key TEXT NOT NULL CHECK (length(key) > 0),
    event_type TEXT NOT NULL CHECK (event_type IN ('evidence_submitted', 'evidence_replaced')),
    version INTEGER NOT NULL CHECK (version >= 1),
    payload TEXT NOT NULL CHECK (json_valid(payload)),
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX idx_gate_evidence_events_attempt
ON gate_evidence_events(attempt_id, id);

CREATE TRIGGER gate_evidence_events_append_only_update
BEFORE UPDATE ON gate_evidence_events
BEGIN
    SELECT RAISE(ABORT, 'gate evidence events are append-only');
END;

CREATE TRIGGER gate_evidence_events_append_only_delete
BEFORE DELETE ON gate_evidence_events
BEGIN
    SELECT RAISE(ABORT, 'gate evidence events are append-only');
END;
