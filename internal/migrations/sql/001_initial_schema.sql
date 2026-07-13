CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL,
    applied_at TEXT NOT NULL
) STRICT;

CREATE TABLE projects (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    name TEXT,
    instructions TEXT,
    next_issue_number INTEGER NOT NULL CHECK (next_issue_number >= 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE agent_sessions (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    client_name TEXT NOT NULL CHECK (length(client_name) > 0),
    client_version TEXT,
    agent_label TEXT,
    model TEXT,
    instance_key TEXT,
    started_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    ended_at TEXT
) STRICT;

CREATE TABLE issues (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    sequence_no INTEGER NOT NULL UNIQUE CHECK (sequence_no > 0),
    type TEXT NOT NULL CHECK (type IN ('epic', 'task', 'bug')),
    title TEXT NOT NULL CHECK (length(title) > 0),
    description TEXT,
    acceptance_criteria TEXT,
    status TEXT NOT NULL CHECK (status IN ('open', 'ready', 'blocked', 'review', 'done', 'cancelled')),
    priority TEXT NOT NULL CHECK (priority IN ('low', 'medium', 'high', 'critical')),
    parent_id TEXT REFERENCES issues(id),
    blocked_reason TEXT,
    version INTEGER NOT NULL CHECK (version >= 1),
    created_by_session_id TEXT REFERENCES agent_sessions(id),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    closed_at TEXT,
    archived_at TEXT,
    archived_by_session_id TEXT REFERENCES agent_sessions(id),
    CHECK (parent_id IS NULL OR parent_id <> id),
    CHECK ((type = 'epic' AND parent_id IS NULL) OR type IN ('task', 'bug')),
    CHECK (
        (status = 'blocked' AND blocked_reason IS NOT NULL AND length(trim(blocked_reason)) > 0)
        OR (status <> 'blocked' AND blocked_reason IS NULL)
    )
) STRICT;

CREATE TRIGGER issues_sequence_no_immutable
BEFORE UPDATE OF sequence_no ON issues
WHEN NEW.sequence_no <> OLD.sequence_no
BEGIN
    SELECT RAISE(ABORT, 'issue sequence number is immutable');
END;

CREATE TABLE labels (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    description TEXT,
    created_at TEXT NOT NULL
) STRICT;

CREATE UNIQUE INDEX idx_labels_name_nocase
ON labels(name COLLATE NOCASE);

CREATE TABLE issue_labels (
    issue_id TEXT NOT NULL REFERENCES issues(id),
    label_id TEXT NOT NULL REFERENCES labels(id),
    PRIMARY KEY (issue_id, label_id)
) STRICT;

CREATE INDEX idx_issue_labels_label
ON issue_labels(label_id, issue_id);

CREATE TABLE issue_relations (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    source_issue_id TEXT NOT NULL REFERENCES issues(id),
    target_issue_id TEXT NOT NULL REFERENCES issues(id),
    type TEXT NOT NULL CHECK (type IN ('blocks', 'related_to', 'duplicates')),
    created_by_session_id TEXT REFERENCES agent_sessions(id),
    created_at TEXT NOT NULL,
    CHECK (source_issue_id <> target_issue_id),
    CHECK (type <> 'related_to' OR source_issue_id < target_issue_id),
    UNIQUE (source_issue_id, target_issue_id, type)
) STRICT;

CREATE TABLE comments (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    issue_id TEXT NOT NULL REFERENCES issues(id),
    content TEXT NOT NULL CHECK (length(content) > 0),
    created_by_session_id TEXT REFERENCES agent_sessions(id),
    author_label TEXT,
    created_at TEXT NOT NULL,
    edited_at TEXT
) STRICT;

CREATE TABLE decisions (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    issue_id TEXT REFERENCES issues(id),
    title TEXT NOT NULL CHECK (length(title) > 0),
    summary TEXT NOT NULL CHECK (length(summary) > 0),
    content TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'superseded', 'rejected')),
    supersedes_id TEXT REFERENCES decisions(id),
    created_by_session_id TEXT REFERENCES agent_sessions(id),
    created_at TEXT NOT NULL,
    CHECK (supersedes_id IS NULL OR supersedes_id <> id)
) STRICT;

CREATE UNIQUE INDEX idx_decisions_supersedes
ON decisions(supersedes_id)
WHERE supersedes_id IS NOT NULL;

CREATE TABLE work_attempts (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    issue_id TEXT NOT NULL REFERENCES issues(id),
    session_id TEXT REFERENCES agent_sessions(id),
    agent_label TEXT,
    kind TEXT NOT NULL CHECK (kind IN ('work', 'review')),
    status TEXT NOT NULL CHECK (status IN ('active', 'completed', 'failed', 'interrupted', 'expired', 'cancelled')),
    issue_version_at_start INTEGER NOT NULL CHECK (issue_version_at_start >= 1),
    context_event_id_at_start INTEGER NOT NULL CHECK (context_event_id_at_start >= 0),
    lease_token_hash BLOB NOT NULL CHECK (length(lease_token_hash) > 0),
    lease_expires_at TEXT NOT NULL,
    started_at TEXT NOT NULL,
    last_heartbeat_at TEXT NOT NULL,
    finished_at TEXT,
    result_summary TEXT,
    next_steps_json TEXT CHECK (next_steps_json IS NULL OR json_valid(next_steps_json)),
    verification_json TEXT CHECK (verification_json IS NULL OR json_valid(verification_json)),
    failure_reason_code TEXT CHECK (failure_reason_code IS NULL OR failure_reason_code IN (
        'implementation_error', 'environment_error', 'missing_dependency', 'invalid_requirements',
        'tests_failed', 'context_lost', 'timeout', 'other'
    )),
    interruption_reason_code TEXT CHECK (interruption_reason_code IS NULL OR interruption_reason_code IN (
        'handoff', 'user_request', 'context_limit', 'client_shutdown', 'environment_change', 'other'
    )),
    reason_details TEXT,
    CHECK ((status = 'active' AND finished_at IS NULL) OR (status <> 'active' AND finished_at IS NOT NULL)),
    CHECK ((status = 'failed' AND failure_reason_code IS NOT NULL) OR (status <> 'failed' AND failure_reason_code IS NULL)),
    CHECK ((status = 'interrupted' AND interruption_reason_code IS NOT NULL) OR (status <> 'interrupted' AND interruption_reason_code IS NULL))
) STRICT;

CREATE TABLE attempt_notes (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    attempt_id TEXT NOT NULL REFERENCES work_attempts(id),
    kind TEXT NOT NULL CHECK (kind IN ('progress', 'finding', 'warning', 'checkpoint')),
    content TEXT NOT NULL CHECK (length(content) > 0),
    next_steps_json TEXT CHECK (next_steps_json IS NULL OR json_valid(next_steps_json)),
    important INTEGER NOT NULL CHECK (important IN (0, 1)),
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE artifacts (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    issue_id TEXT NOT NULL REFERENCES issues(id),
    attempt_id TEXT REFERENCES work_attempts(id),
    type TEXT NOT NULL CHECK (type IN ('file', 'directory', 'url', 'commit', 'branch', 'pull_request', 'log', 'other')),
    uri TEXT NOT NULL CHECK (length(uri) > 0),
    title TEXT,
    metadata TEXT CHECK (metadata IS NULL OR json_valid(metadata)),
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE issue_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id TEXT REFERENCES issues(id),
    event_type TEXT NOT NULL CHECK (length(event_type) > 0),
    session_id TEXT REFERENCES agent_sessions(id),
    attempt_id TEXT REFERENCES work_attempts(id),
    payload TEXT NOT NULL CHECK (json_valid(payload)),
    created_at TEXT NOT NULL
) STRICT;

CREATE TRIGGER issue_events_append_only_update
BEFORE UPDATE ON issue_events
BEGIN
    SELECT RAISE(ABORT, 'issue events are append-only');
END;

CREATE TRIGGER issue_events_append_only_delete
BEFORE DELETE ON issue_events
BEGIN
    SELECT RAISE(ABORT, 'issue events are append-only');
END;

CREATE TABLE idempotency_records (
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) > 0),
    operation TEXT NOT NULL CHECK (length(operation) > 0),
    request_hash BLOB NOT NULL CHECK (length(request_hash) > 0),
    response_json TEXT NOT NULL CHECK (json_valid(response_json)),
    created_at TEXT NOT NULL,
    PRIMARY KEY (operation, idempotency_key)
) STRICT;

CREATE VIRTUAL TABLE search_index USING fts5(
    entity_type UNINDEXED,
    entity_id UNINDEXED,
    issue_id UNINDEXED,
    title,
    content
);

CREATE INDEX idx_issues_status_priority
ON issues(status, priority);

CREATE INDEX idx_issues_parent
ON issues(parent_id);

CREATE INDEX idx_issues_archived
ON issues(archived_at);

CREATE INDEX idx_comments_issue_created
ON comments(issue_id, created_at);

CREATE INDEX idx_decisions_issue_status
ON decisions(issue_id, status);

CREATE INDEX idx_attempts_issue_started
ON work_attempts(issue_id, started_at DESC);

CREATE UNIQUE INDEX idx_one_active_attempt_per_issue
ON work_attempts(issue_id)
WHERE status = 'active';

CREATE INDEX idx_attempts_active_lease
ON work_attempts(lease_expires_at)
WHERE status = 'active';

CREATE INDEX idx_relations_source
ON issue_relations(source_issue_id, type);

CREATE INDEX idx_relations_target
ON issue_relations(target_issue_id, type);

CREATE INDEX idx_events_issue_id
ON issue_events(issue_id, id);
