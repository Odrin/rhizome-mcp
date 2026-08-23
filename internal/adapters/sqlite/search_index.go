package sqlite

import (
	"context"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

// SearchIndexRepository maintains the derived FTS5 search projection.
type SearchIndexRepository struct {
	db *DB
}

// NewSearchIndexRepository returns a search-index repository backed by database.
func NewSearchIndexRepository(database *DB) (*SearchIndexRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "search index database is required", false)
	}
	return &SearchIndexRepository{db: database}, nil
}

// Rebuild replaces the complete derived FTS5 projection from authoritative rows
// in one write transaction.
func (repository *SearchIndexRepository) Rebuild(ctx context.Context) error {
	return repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		for _, statement := range []string{
			"DELETE FROM search_index",
			`INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
				SELECT 'issue', id, id, title, COALESCE(description, '')
				FROM issues`,
			`INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
				SELECT 'comment', id, issue_id, '', content
				FROM comments`,
			`INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
				SELECT 'decision', id, issue_id, title, summary || char(10) || content
				FROM decisions`,
			`INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
				SELECT 'review', review_requests.id, review_requests.issue_id, issues.title || ' review', review_requests.status || char(10) || COALESCE(review_requests.artifact_ids_json, '')
				FROM review_requests
				LEFT JOIN issues ON issues.id = review_requests.issue_id`,
			`INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
				SELECT 'attempt_note', attempt_notes.id, work_attempts.issue_id, '', attempt_notes.content
				FROM attempt_notes
				JOIN work_attempts ON work_attempts.id = attempt_notes.attempt_id`,
			// Display values only. comparison_value and normalized_json are
			// case-folded comparison keys and glob automaton internals; they
			// are never indexed. This must stay byte-identical in effect to
			// migration 013's triggers, or a rebuild would silently produce a
			// different index than live writes do.
			`INSERT INTO search_index(entity_type, entity_id, issue_id, title, content)
				SELECT 'reservation', id, issue_id, display_value, kind || char(10) || COALESCE(release_reason, '')
				FROM resource_reservations`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	})
}

var _ ports.SearchIndexRepository = (*SearchIndexRepository)(nil)
