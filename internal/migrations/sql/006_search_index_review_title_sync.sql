UPDATE search_index
SET title = (SELECT issues.title || ' review' FROM issues WHERE issues.id = search_index.issue_id)
WHERE entity_type = 'review'
  AND EXISTS (SELECT 1 FROM issues WHERE issues.id = search_index.issue_id);

DROP TRIGGER search_index_issues_update;

CREATE TRIGGER search_index_issues_update
AFTER UPDATE OF title, description ON issues
BEGIN
    DELETE FROM search_index
    WHERE entity_type = 'issue' AND entity_id = OLD.id;

    INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
    VALUES ('issue', NEW.id, NEW.id, NEW.title, COALESCE(NEW.description, ''));

    DELETE FROM search_index
    WHERE entity_type = 'review' AND issue_id = NEW.id;

    INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
    SELECT 'review', review_requests.id, review_requests.issue_id, NEW.title || ' review', review_requests.status || char(10) || COALESCE(review_requests.artifact_ids_json, '')
    FROM review_requests
    WHERE review_requests.issue_id = NEW.id;
END;
