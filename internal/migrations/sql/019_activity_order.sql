CREATE INDEX idx_artifacts_issue_created_id
ON artifacts(issue_id, created_at DESC, id ASC);

CREATE INDEX idx_attempt_notes_attempt_created_id
ON attempt_notes(attempt_id, created_at DESC, id ASC);
