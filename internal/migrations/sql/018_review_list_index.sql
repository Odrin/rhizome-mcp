CREATE INDEX idx_review_requests_created_id
ON review_requests(created_at DESC, id DESC);

CREATE INDEX idx_review_requests_status_created_id
ON review_requests(status, created_at DESC, id DESC);

CREATE INDEX idx_review_requests_open_issue_created_id
ON review_requests(issue_id, created_at DESC, id DESC)
WHERE status = 'open';
