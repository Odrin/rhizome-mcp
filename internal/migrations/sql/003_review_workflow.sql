CREATE TABLE review_targets (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    issue_id TEXT NOT NULL REFERENCES issues(id),
    issue_version INTEGER NOT NULL CHECK (issue_version >= 1),
    latest_event_id INTEGER NOT NULL CHECK (latest_event_id >= 0),
    artifact_ids_json TEXT NOT NULL CHECK (json_valid(artifact_ids_json)),
    version INTEGER NOT NULL CHECK (version >= 1),
    created_at TEXT NOT NULL
) STRICT;

CREATE UNIQUE INDEX idx_review_targets_issue_version
ON review_targets(issue_id, issue_version);

CREATE TABLE review_requests (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    target_id TEXT NOT NULL REFERENCES review_targets(id),
    issue_id TEXT NOT NULL REFERENCES issues(id),
    target_issue_version INTEGER NOT NULL CHECK (target_issue_version >= 1),
    target_event_id INTEGER NOT NULL CHECK (target_event_id >= 0),
    artifact_ids_json TEXT NOT NULL CHECK (json_valid(artifact_ids_json)),
    status TEXT NOT NULL CHECK (status IN ('open', 'claimed', 'approved', 'changes_requested', 'blocked', 'cancelled', 'superseded')),
    supersedes_id TEXT REFERENCES review_requests(id),
    active_attempt_id TEXT REFERENCES work_attempts(id),
    version INTEGER NOT NULL CHECK (version >= 1),
    created_at TEXT NOT NULL,
    resolved_at TEXT,
    CHECK ((status IN ('open', 'claimed') AND resolved_at IS NULL) OR (status NOT IN ('open', 'claimed') AND resolved_at IS NOT NULL)),
    CHECK ((status = 'claimed' AND active_attempt_id IS NOT NULL) OR (status <> 'claimed' AND active_attempt_id IS NULL)),
    CHECK (supersedes_id IS NULL OR supersedes_id <> id)
) STRICT;

CREATE UNIQUE INDEX idx_review_requests_active_target
ON review_requests(target_id)
WHERE status IN ('open', 'claimed');

CREATE UNIQUE INDEX idx_review_requests_active_attempt
ON review_requests(active_attempt_id)
WHERE active_attempt_id IS NOT NULL;

CREATE TABLE review_outcomes (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    request_id TEXT NOT NULL UNIQUE REFERENCES review_requests(id),
    attempt_id TEXT NOT NULL REFERENCES work_attempts(id),
    outcome TEXT NOT NULL CHECK (outcome IN ('approved', 'changes_requested', 'blocked')),
    reason TEXT,
    version INTEGER NOT NULL CHECK (version >= 1),
    created_at TEXT NOT NULL,
    CHECK ((outcome = 'blocked' AND reason IS NOT NULL AND length(trim(reason)) > 0) OR (outcome <> 'blocked' AND reason IS NULL))
) STRICT;

CREATE TABLE review_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id TEXT NOT NULL REFERENCES review_requests(id),
    target_id TEXT NOT NULL REFERENCES review_targets(id),
    attempt_id TEXT REFERENCES work_attempts(id),
    event_type TEXT NOT NULL CHECK (event_type IN ('review_requested', 'review_claimed', 'review_approved', 'review_changes_requested', 'review_blocked', 'review_cancelled', 'review_superseded')),
    payload TEXT NOT NULL CHECK (json_valid(payload)),
    created_at TEXT NOT NULL
) STRICT;

CREATE TRIGGER review_events_append_only_update
BEFORE UPDATE ON review_events
BEGIN
    SELECT RAISE(ABORT, 'review events are append-only');
END;

CREATE TRIGGER review_events_append_only_delete
BEFORE DELETE ON review_events
BEGIN
    SELECT RAISE(ABORT, 'review events are append-only');
END;
