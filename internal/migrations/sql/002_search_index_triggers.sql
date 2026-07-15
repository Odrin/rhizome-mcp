CREATE TRIGGER search_index_issues_insert
AFTER INSERT ON issues
BEGIN
    INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
    VALUES ('issue', NEW.id, NEW.id, NEW.title, COALESCE(NEW.description, ''));
END;

CREATE TRIGGER search_index_issues_update
AFTER UPDATE OF title, description ON issues
BEGIN
    DELETE FROM search_index
    WHERE entity_type = 'issue' AND entity_id = OLD.id;

    INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
    VALUES ('issue', NEW.id, NEW.id, NEW.title, COALESCE(NEW.description, ''));
END;

CREATE TRIGGER search_index_comments_insert
AFTER INSERT ON comments
BEGIN
    INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
    VALUES ('comment', NEW.id, NEW.issue_id, '', NEW.content);
END;

CREATE TRIGGER search_index_decisions_insert
AFTER INSERT ON decisions
BEGIN
    INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
    VALUES ('decision', NEW.id, NEW.issue_id, NEW.title, NEW.summary || char(10) || NEW.content);
END;

CREATE TRIGGER search_index_attempt_notes_insert
AFTER INSERT ON attempt_notes
BEGIN
    INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
    SELECT 'attempt_note', NEW.id, work_attempts.issue_id, '', NEW.content
    FROM work_attempts
    WHERE work_attempts.id = NEW.attempt_id;
END;

INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
SELECT 'issue', id, id, title, COALESCE(description, '')
FROM issues;

INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
SELECT 'comment', id, issue_id, '', content
FROM comments;

INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
SELECT 'decision', id, issue_id, title, summary || char(10) || content
FROM decisions;

INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
SELECT 'attempt_note', attempt_notes.id, work_attempts.issue_id, '', attempt_notes.content
FROM attempt_notes
JOIN work_attempts ON work_attempts.id = attempt_notes.attempt_id;
