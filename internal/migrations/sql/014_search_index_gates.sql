-- ISSUE-175: index workflow-gate text for full-text search.
--
-- Workflow policies carry no free-text name or description in the locked
-- schema (docs/02 §17.2), so what is indexed is the text a human would
-- actually search for: the requirement keys, evidence keys, purposes, and
-- fields the policy declares, plus its selector. The raw requirement JSON is
-- not indexed wholesale -- JSON punctuation and key names would pollute the
-- index -- only the extracted values are. Policies are project-scoped, so
-- their issue_id is NULL (the search query already tolerates that for the
-- archived-issue filter).
--
-- The extraction is shape-guarded (json_type = 'array', element type =
-- 'object'): the storage CHECK only enforces json_valid, and indexing must
-- degrade to empty text on a wrong-shaped blob rather than turn the write
-- that carries it into a trigger error -- GetPolicy reports that corruption;
-- the index does not enforce it.
--
-- Gate evidence indexes its key as the title and its summary and details as
-- content, the same display-facing-text-only rule reservations follow
-- (migration 013). The update trigger fires on the columns
-- submit_gate_evidence's upsert can change.

CREATE TRIGGER search_index_workflow_policies_insert
AFTER INSERT ON workflow_policies
BEGIN
    INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
    VALUES ('workflow_policy', NEW.id, NULL,
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
        || char(10) || NEW.selector_json || char(10) || NEW.status);
END;

CREATE TRIGGER search_index_workflow_policies_update
AFTER UPDATE OF selector_json, requirements_json, status ON workflow_policies
BEGIN
    DELETE FROM search_index
    WHERE entity_type = 'workflow_policy' AND entity_id = OLD.id;

    INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
    VALUES ('workflow_policy', NEW.id, NULL,
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
        || char(10) || NEW.selector_json || char(10) || NEW.status);
END;

CREATE TRIGGER search_index_gate_evidence_insert
AFTER INSERT ON gate_evidence
BEGIN
    INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
    VALUES ('gate_evidence', NEW.id, NEW.issue_id, NEW.key,
        NEW.summary || char(10) || COALESCE(NEW.details, '') || char(10) || NEW.result);
END;

CREATE TRIGGER search_index_gate_evidence_update
AFTER UPDATE OF key, result, summary, details, artifact_ids_json, version ON gate_evidence
BEGIN
    DELETE FROM search_index
    WHERE entity_type = 'gate_evidence' AND entity_id = OLD.id;

    INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
    VALUES ('gate_evidence', NEW.id, NEW.issue_id, NEW.key,
        NEW.summary || char(10) || COALESCE(NEW.details, '') || char(10) || NEW.result);
END;

-- Backfill every row that predates these triggers.
INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
SELECT 'workflow_policy', id, NULL,
    CASE WHEN json_type(requirements_json) = 'array'
        THEN COALESCE((SELECT group_concat(json_extract(value, '$.key'), ' ')
            FROM json_each(requirements_json) WHERE type = 'object'), '')
        ELSE '' END,
    CASE WHEN json_type(requirements_json) = 'array'
        THEN COALESCE((SELECT group_concat(
            COALESCE(json_extract(value, '$.evidence_key'), '') || ' ' ||
            COALESCE(json_extract(value, '$.purpose'), '') || ' ' ||
            COALESCE(json_extract(value, '$.field'), ''), char(10))
            FROM json_each(requirements_json) WHERE type = 'object'), '')
        ELSE '' END
    || char(10) || selector_json || char(10) || status
FROM workflow_policies;

INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
SELECT 'gate_evidence', id, issue_id, key,
    summary || char(10) || COALESCE(details, '') || char(10) || result
FROM gate_evidence;
