CREATE TABLE review_follow_ups (
    id TEXT PRIMARY KEY CHECK (length(id) = 26),
    request_id TEXT NOT NULL REFERENCES review_requests(id),
    attempt_id TEXT NOT NULL REFERENCES work_attempts(id),
    outcome TEXT NOT NULL CHECK (outcome = 'changes_requested'),
    reason TEXT,
    version INTEGER NOT NULL CHECK (version >= 1),
    created_at TEXT NOT NULL
) STRICT;

CREATE TRIGGER search_index_reviews_insert
AFTER INSERT ON review_requests
BEGIN
    INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
    SELECT 'review', NEW.id, NEW.issue_id, issues.title || ' review', NEW.status || char(10) || COALESCE(NEW.artifact_ids_json, '')
    FROM issues
    WHERE issues.id = NEW.issue_id;
END;

CREATE TRIGGER search_index_reviews_update
AFTER UPDATE OF status, artifact_ids_json ON review_requests
BEGIN
    DELETE FROM search_index
    WHERE entity_type = 'review' AND entity_id = OLD.id;

    INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
    SELECT 'review', NEW.id, NEW.issue_id, issues.title || ' review', NEW.status || char(10) || COALESCE(NEW.artifact_ids_json, '')
    FROM issues
    WHERE issues.id = NEW.issue_id;
END;

INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
SELECT 'review', review_requests.id, review_requests.issue_id, issues.title || ' review', review_requests.status || char(10) || COALESCE(review_requests.artifact_ids_json, '')
FROM review_requests
LEFT JOIN issues ON issues.id = review_requests.issue_id;
