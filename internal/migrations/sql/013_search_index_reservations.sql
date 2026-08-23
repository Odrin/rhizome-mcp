-- Index released and active resource reservations for full-text search.
--
-- Only display-facing values are indexed: the display value (caller
-- spelling), the resource kind, and the release reason when the row has
-- one. comparison_value and normalized_json are deliberately never
-- indexed -- they are case-folded comparison keys and glob automaton
-- internals, meaningless as search text and misleading if a user matched
-- one (docs/02 §18.4, ISSUE-182).
--
-- The update trigger fires on release, since that is the only mutation a
-- reservation row undergoes and it is what changes release_reason.

CREATE TRIGGER search_index_reservations_insert
AFTER INSERT ON resource_reservations
BEGIN
    INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
    VALUES ('reservation', NEW.id, NEW.issue_id, NEW.display_value, NEW.kind || char(10) || COALESCE(NEW.release_reason, ''));
END;

CREATE TRIGGER search_index_reservations_update
AFTER UPDATE OF display_value, release_reason, status ON resource_reservations
BEGIN
    DELETE FROM search_index
    WHERE entity_type = 'reservation' AND entity_id = OLD.id;

    INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
    VALUES ('reservation', NEW.id, NEW.issue_id, NEW.display_value, NEW.kind || char(10) || COALESCE(NEW.release_reason, ''));
END;

-- Backfill every reservation that predates these triggers.
INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
SELECT 'reservation', id, issue_id, display_value, kind || char(10) || COALESCE(release_reason, '')
FROM resource_reservations;
