DROP INDEX idx_review_targets_issue_version;

CREATE INDEX idx_review_targets_issue_version
ON review_targets(issue_id, issue_version);

CREATE UNIQUE INDEX idx_review_requests_active_issue_version
ON review_requests(issue_id, target_issue_version)
WHERE status IN ('open', 'claimed');