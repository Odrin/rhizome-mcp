CREATE TABLE search_index_identity (
    fts_rowid INTEGER PRIMARY KEY,
    entity_type TEXT NOT NULL,
    entity_id TEXT NOT NULL,
    issue_id TEXT,
    UNIQUE (entity_type, entity_id)
) STRICT;

CREATE INDEX idx_search_index_identity_issue_type
ON search_index_identity(issue_id, entity_type);

INSERT INTO search_index_identity(fts_rowid, entity_type, entity_id, issue_id)
SELECT rowid, entity_type, entity_id, issue_id FROM search_index;

DROP TRIGGER search_index_issues_insert;
CREATE TRIGGER search_index_issues_insert
AFTER INSERT ON issues
BEGIN
    INSERT INTO search_index_identity(entity_type, entity_id, issue_id)
    VALUES ('issue', NEW.id, NEW.id);
    INSERT INTO search_index(rowid, entity_type, entity_id, issue_id, title, content)
    SELECT fts_rowid, 'issue', NEW.id, NEW.id, NEW.title, COALESCE(NEW.description, '')
    FROM search_index_identity WHERE entity_type = 'issue' AND entity_id = NEW.id;
END;

DROP TRIGGER search_index_issues_update;
CREATE TRIGGER search_index_issues_update
AFTER UPDATE OF title, description ON issues
WHEN OLD.title IS NOT NEW.title OR COALESCE(OLD.description, '') IS NOT COALESCE(NEW.description, '')
BEGIN
    UPDATE search_index SET title = NEW.title, content = COALESCE(NEW.description, '')
    WHERE rowid = (SELECT fts_rowid FROM search_index_identity WHERE entity_type = 'issue' AND entity_id = OLD.id);
END;

CREATE TRIGGER search_index_issues_review_title_update
AFTER UPDATE OF title ON issues
WHEN OLD.title IS NOT NEW.title
BEGIN
    UPDATE search_index SET title = NEW.title || ' review'
    WHERE rowid IN (SELECT fts_rowid FROM search_index_identity WHERE issue_id = NEW.id AND entity_type = 'review');
END;

DROP TRIGGER search_index_comments_insert;
CREATE TRIGGER search_index_comments_insert
AFTER INSERT ON comments
BEGIN
    INSERT INTO search_index_identity(entity_type, entity_id, issue_id)
    VALUES ('comment', NEW.id, NEW.issue_id);
    INSERT INTO search_index(rowid, entity_type, entity_id, issue_id, title, content)
    SELECT fts_rowid, 'comment', NEW.id, NEW.issue_id, '', NEW.content
    FROM search_index_identity WHERE entity_type = 'comment' AND entity_id = NEW.id;
END;

DROP TRIGGER search_index_decisions_insert;
CREATE TRIGGER search_index_decisions_insert
AFTER INSERT ON decisions
BEGIN
    INSERT INTO search_index_identity(entity_type, entity_id, issue_id)
    VALUES ('decision', NEW.id, NEW.issue_id);
    INSERT INTO search_index(rowid, entity_type, entity_id, issue_id, title, content)
    SELECT fts_rowid, 'decision', NEW.id, NEW.issue_id, NEW.title, NEW.summary || char(10) || NEW.content
    FROM search_index_identity WHERE entity_type = 'decision' AND entity_id = NEW.id;
END;

DROP TRIGGER search_index_attempt_notes_insert;
CREATE TRIGGER search_index_attempt_notes_insert
AFTER INSERT ON attempt_notes
BEGIN
    INSERT INTO search_index_identity(entity_type, entity_id, issue_id)
    SELECT 'attempt_note', NEW.id, issue_id FROM work_attempts WHERE id = NEW.attempt_id;
    INSERT INTO search_index(rowid, entity_type, entity_id, issue_id, title, content)
    SELECT fts_rowid, 'attempt_note', NEW.id, issue_id, '', NEW.content
    FROM search_index_identity WHERE entity_type = 'attempt_note' AND entity_id = NEW.id;
END;

DROP TRIGGER search_index_reviews_insert;
CREATE TRIGGER search_index_reviews_insert
AFTER INSERT ON review_requests
BEGIN
    INSERT INTO search_index_identity(entity_type, entity_id, issue_id)
    VALUES ('review', NEW.id, NEW.issue_id);
    INSERT INTO search_index(rowid, entity_type, entity_id, issue_id, title, content)
    SELECT identity.fts_rowid, 'review', NEW.id, NEW.issue_id, issues.title || ' review', NEW.status || char(10) || COALESCE(NEW.artifact_ids_json, '')
    FROM issues JOIN search_index_identity AS identity ON identity.entity_type = 'review' AND identity.entity_id = NEW.id
    WHERE issues.id = NEW.issue_id;
END;

DROP TRIGGER search_index_reviews_update;
CREATE TRIGGER search_index_reviews_update
AFTER UPDATE OF status, artifact_ids_json ON review_requests
WHEN OLD.status IS NOT NEW.status OR COALESCE(OLD.artifact_ids_json, '') IS NOT COALESCE(NEW.artifact_ids_json, '')
BEGIN
    UPDATE search_index SET title = (SELECT title || ' review' FROM issues WHERE id = NEW.issue_id),
        content = NEW.status || char(10) || COALESCE(NEW.artifact_ids_json, '')
    WHERE rowid = (SELECT fts_rowid FROM search_index_identity WHERE entity_type = 'review' AND entity_id = OLD.id);
END;

DROP TRIGGER search_index_reservations_insert;
CREATE TRIGGER search_index_reservations_insert
AFTER INSERT ON resource_reservations
BEGIN
    INSERT INTO search_index_identity(entity_type, entity_id, issue_id)
    VALUES ('reservation', NEW.id, NEW.issue_id);
    INSERT INTO search_index(rowid, entity_type, entity_id, issue_id, title, content)
    SELECT fts_rowid, 'reservation', NEW.id, NEW.issue_id, NEW.display_value, NEW.kind || char(10) || COALESCE(NEW.release_reason, '')
    FROM search_index_identity WHERE entity_type = 'reservation' AND entity_id = NEW.id;
END;

DROP TRIGGER search_index_reservations_update;
CREATE TRIGGER search_index_reservations_update
AFTER UPDATE OF display_value, kind, release_reason, status ON resource_reservations
WHEN OLD.display_value IS NOT NEW.display_value OR OLD.kind IS NOT NEW.kind
    OR COALESCE(OLD.release_reason, '') IS NOT COALESCE(NEW.release_reason, '')
BEGIN
    UPDATE search_index SET title = NEW.display_value, content = NEW.kind || char(10) || COALESCE(NEW.release_reason, '')
    WHERE rowid = (SELECT fts_rowid FROM search_index_identity WHERE entity_type = 'reservation' AND entity_id = OLD.id);
END;

DROP TRIGGER search_index_workflow_policies_insert;
CREATE TRIGGER search_index_workflow_policies_insert
AFTER INSERT ON workflow_policies
BEGIN
    INSERT INTO search_index_identity(entity_type, entity_id, issue_id)
    VALUES ('workflow_policy', NEW.id, NULL);
    INSERT INTO search_index(rowid, entity_type, entity_id, issue_id, title, content)
    SELECT fts_rowid, 'workflow_policy', NEW.id, NULL,
        CASE WHEN json_type(NEW.requirements_json) = 'array'
            THEN COALESCE((SELECT group_concat(json_extract(value, '$.key'), ' ')
                FROM json_each(NEW.requirements_json) WHERE type = 'object'), '')
            ELSE '' END,
        CASE WHEN json_type(NEW.requirements_json) = 'array'
            THEN COALESCE((SELECT group_concat(
                COALESCE(json_extract(value, '$.evidence_key'), '') || ' ' ||
                COALESCE(json_extract(value, '$.purpose'), '') || ' ' ||
                COALESCE(json_extract(value, '$.field'), ''), char(10))
                FROM json_each(NEW.requirements_json) WHERE type = 'object'), '')
            ELSE '' END
        || char(10) || NEW.selector_json || char(10) || NEW.status
    FROM search_index_identity WHERE entity_type = 'workflow_policy' AND entity_id = NEW.id;
END;

DROP TRIGGER search_index_workflow_policies_update;
CREATE TRIGGER search_index_workflow_policies_update
AFTER UPDATE OF selector_json, requirements_json, status ON workflow_policies
WHEN OLD.selector_json IS NOT NEW.selector_json OR OLD.requirements_json IS NOT NEW.requirements_json OR OLD.status IS NOT NEW.status
BEGIN
    UPDATE search_index SET title =
        CASE WHEN json_type(NEW.requirements_json) = 'array'
            THEN COALESCE((SELECT group_concat(json_extract(value, '$.key'), ' ')
                FROM json_each(NEW.requirements_json) WHERE type = 'object'), '')
            ELSE '' END,
        content = CASE WHEN json_type(NEW.requirements_json) = 'array'
            THEN COALESCE((SELECT group_concat(
                COALESCE(json_extract(value, '$.evidence_key'), '') || ' ' ||
                COALESCE(json_extract(value, '$.purpose'), '') || ' ' ||
                COALESCE(json_extract(value, '$.field'), ''), char(10))
                FROM json_each(NEW.requirements_json) WHERE type = 'object'), '')
            ELSE '' END
        || char(10) || NEW.selector_json || char(10) || NEW.status
    WHERE rowid = (SELECT fts_rowid FROM search_index_identity WHERE entity_type = 'workflow_policy' AND entity_id = OLD.id);
END;

DROP TRIGGER search_index_gate_evidence_insert;
CREATE TRIGGER search_index_gate_evidence_insert
AFTER INSERT ON gate_evidence
BEGIN
    INSERT INTO search_index_identity(entity_type, entity_id, issue_id)
    VALUES ('gate_evidence', NEW.id, NEW.issue_id);
    INSERT INTO search_index(rowid, entity_type, entity_id, issue_id, title, content)
    SELECT fts_rowid, 'gate_evidence', NEW.id, NEW.issue_id, NEW.key, NEW.summary || char(10) || COALESCE(NEW.details, '') || char(10) || NEW.result
    FROM search_index_identity WHERE entity_type = 'gate_evidence' AND entity_id = NEW.id;
END;

DROP TRIGGER search_index_gate_evidence_update;
CREATE TRIGGER search_index_gate_evidence_update
AFTER UPDATE OF key, result, summary, details, artifact_ids_json, version ON gate_evidence
WHEN OLD.key IS NOT NEW.key OR OLD.result IS NOT NEW.result OR OLD.summary IS NOT NEW.summary
    OR COALESCE(OLD.details, '') IS NOT COALESCE(NEW.details, '')
BEGIN
    UPDATE search_index SET title = NEW.key, content = NEW.summary || char(10) || COALESCE(NEW.details, '') || char(10) || NEW.result
    WHERE rowid = (SELECT fts_rowid FROM search_index_identity WHERE entity_type = 'gate_evidence' AND entity_id = OLD.id);
END;