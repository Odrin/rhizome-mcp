ALTER TABLE issue_events ADD COLUMN source TEXT NOT NULL DEFAULT 'issue' CHECK (source IN ('issue', 'review'));

INSERT INTO issue_events (issue_id, event_type, session_id, attempt_id, payload, created_at, source)
SELECT review_requests.issue_id, review_events.event_type, NULL, review_events.attempt_id, review_events.payload, review_events.created_at, 'review'
FROM review_events
JOIN review_requests ON review_requests.id = review_events.request_id
ORDER BY review_events.created_at ASC, review_events.id ASC;

DROP TABLE review_events;
