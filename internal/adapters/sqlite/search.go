package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/pagination"
	"rhizome-mcp/internal/ports"
)

type SearchRepository struct {
	db *DB
}

type searchCursor struct {
	Score      float64 `json:"score"`
	EntityType string  `json:"entity_type"`
	EntityID   string  `json:"entity_id"`
}

var searchCursorCodec = pagination.NewCodec[searchCursor](0)

func NewSearchRepository(database *DB) (*SearchRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "search database is required", false)
	}
	return &SearchRepository{db: database}, nil
}

func (repository *SearchRepository) Search(ctx context.Context, command ports.SearchCommand) (domain.SearchPage, error) {
	input, err := command.Input.Validate()
	if err != nil {
		return domain.SearchPage{}, err
	}
	var after *searchCursor
	if input.Cursor != "" {
		decoded, err := searchCursorCodec.Decode(input.Cursor)
		if err != nil || !validSearchCursor(decoded) {
			if err == nil {
				err = errors.New("search cursor payload is invalid")
			}
			return domain.SearchPage{}, searchCursorError(err)
		}
		after = &decoded
	}

	var result domain.SearchPage
	err = repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		where := []string{"search_index MATCH ?"}
		args := []any{input.Query}
		if len(input.EntityTypes) > 0 {
			appendSearchInFilter(&where, &args, "search_index.entity_type", input.EntityTypes)
		}
		if input.IssueID != nil {
			issueID, err := resolveSearchIssueID(ctx, query, *input.IssueID)
			if err != nil {
				return err
			}
			where = append(where, "search_index.issue_id = ?")
			args = append(args, issueID)
		}
		if input.EpicID != nil {
			epicID, err := resolveSearchIssueID(ctx, query, *input.EpicID)
			if err != nil {
				return err
			}
			where = append(where, "issues.parent_id = ?")
			args = append(args, epicID)
		}
		if !input.IncludeArchived {
			where = append(where, "(search_index.issue_id IS NULL OR issues.archived_at IS NULL)")
		}
		appendSearchInFilter(&where, &args, "issues.status", input.Statuses)
		if len(input.Labels) > 0 {
			conditions := make([]string, len(input.Labels))
			for index, label := range input.Labels {
				conditions[index] = "labels.name = ? COLLATE NOCASE"
				args = append(args, label)
			}
			where = append(where, `EXISTS (
				SELECT 1 FROM issue_labels
				JOIN labels ON labels.id = issue_labels.label_id
				WHERE issue_labels.issue_id = issues.id AND (`+strings.Join(conditions, " OR ")+`)
			)`)
		}

		statement := `WITH matches AS MATERIALIZED (
			SELECT search_index.entity_type, search_index.entity_id, search_index.issue_id,
				search_index.title,
				substr(snippet(search_index, -1, '[', ']', '...', 64), 1, ?) AS snippet,
				bm25(search_index) AS score
			FROM search_index
			LEFT JOIN issues ON issues.id = search_index.issue_id
			WHERE ` + strings.Join(where, " AND ") + `
		)
		SELECT entity_type, entity_id, issue_id, title, snippet, score
		FROM matches`
		queryArgs := make([]any, 0, len(args)+6)
		queryArgs = append(queryArgs, input.SnippetLength)
		queryArgs = append(queryArgs, args...)
		if after != nil {
			statement += ` WHERE score > ? OR (score = ? AND
				(entity_type > ? OR (entity_type = ? AND entity_id > ?)))`
			queryArgs = append(queryArgs, after.Score, after.Score, after.EntityType, after.EntityType, after.EntityID)
		}
		statement += " ORDER BY score ASC, entity_type ASC, entity_id ASC LIMIT ?"
		queryArgs = append(queryArgs, input.Limit+1)

		rows, err := query.QueryContext(ctx, statement, queryArgs...)
		if err != nil {
			if isFTSQueryError(err) {
				return domain.NewError(domain.CodeInvalidArgument, "search query is invalid", false,
					domain.Detail{Field: "query", Code: "INVALID_FTS_QUERY"})
			}
			return err
		}
		defer rows.Close()
		result.Results = make([]domain.SearchResult, 0, input.Limit)
		for rows.Next() {
			item, err := scanSearchResult(rows)
			if err != nil {
				return err
			}
			result.Results = append(result.Results, item)
		}
		if err := rows.Err(); err != nil {
			if isFTSQueryError(err) {
				return domain.NewError(domain.CodeInvalidArgument, "search query is invalid", false,
					domain.Detail{Field: "query", Code: "INVALID_FTS_QUERY"})
			}
			return searchCorrupt(err)
		}
		if len(result.Results) > input.Limit {
			result.HasMore = true
			result.Results = result.Results[:input.Limit]
			last := result.Results[len(result.Results)-1]
			cursor, err := searchCursorCodec.Encode(searchCursor{
				Score: last.Score, EntityType: string(last.EntityType), EntityID: last.EntityID,
			})
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode search cursor", false)
			}
			result.NextCursor = &cursor
		}
		return nil
	})
	if err != nil {
		return domain.SearchPage{}, err
	}
	return domain.CloneSearchPage(result), nil
}

func (repository *SearchRepository) GetChanges(ctx context.Context, command ports.GetChangesCommand) (domain.ChangesPage, error) {
	input, err := command.Input.Validate()
	if err != nil {
		return domain.ChangesPage{}, err
	}
	var result domain.ChangesPage
	err = repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		if err := query.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM issue_events").Scan(&result.LatestEventID); err != nil {
			return err
		}
		where := []string{"id > ?"}
		args := []any{input.SinceEventID}
		if input.IssueID != nil {
			issueID, err := resolveSearchIssueID(ctx, query, *input.IssueID)
			if err != nil {
				return err
			}
			where = append(where, "issue_id = ?")
			args = append(args, issueID)
		}
		appendSearchInFilter(&where, &args, "event_type", input.EventTypes)
		args = append(args, input.Limit+1)
		rows, err := query.QueryContext(ctx, `SELECT id, issue_id, event_type, session_id, attempt_id, payload, created_at
			FROM issue_events WHERE `+strings.Join(where, " AND ")+` ORDER BY id ASC LIMIT ?`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		result.Events = make([]domain.IssueEvent, 0, input.Limit)
		for rows.Next() {
			event, err := scanChangeEvent(rows)
			if err != nil {
				return err
			}
			result.Events = append(result.Events, event)
		}
		if err := rows.Err(); err != nil {
			return searchCorrupt(err)
		}
		if len(result.Events) > input.Limit {
			result.HasMore = true
			result.Events = result.Events[:input.Limit]
		}
		if len(result.Events) == 0 {
			result.NextEventID = result.LatestEventID
		} else if result.HasMore {
			result.NextEventID = result.Events[len(result.Events)-1].ID
		} else {
			result.NextEventID = result.LatestEventID
		}
		return nil
	})
	if err != nil {
		return domain.ChangesPage{}, err
	}
	return domain.CloneChangesPage(result), nil
}

func validSearchCursor(cursor searchCursor) bool {
	if math.IsNaN(cursor.Score) || math.IsInf(cursor.Score, 0) {
		return false
	}
	if !domain.SearchEntityType(cursor.EntityType).Valid() {
		return false
	}
	_, err := ids.ParseStrict(cursor.EntityID)
	return err == nil
}

func resolveSearchIssueID(ctx context.Context, query Queryer, raw string) (string, error) {
	identifier, err := domain.ParseIssueIdentifier(raw)
	if err != nil {
		return "", err
	}
	var id string
	switch identifier.Kind {
	case domain.IssueIdentifierInternalID:
		err = query.QueryRowContext(ctx, "SELECT id FROM issues WHERE id = ?", identifier.Value).Scan(&id)
	case domain.IssueIdentifierDisplayID:
		err = query.QueryRowContext(ctx, "SELECT id FROM issues WHERE sequence_no = ?", identifier.SequenceNo).Scan(&id)
	}
	if err == sql.ErrNoRows {
		return "", domain.NewError(domain.CodeIssueNotFound, "issue not found", false)
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func appendSearchInFilter[T ~string](where *[]string, args *[]any, column string, values []T) {
	if len(values) == 0 {
		return
	}
	placeholders := make([]string, len(values))
	for index, value := range values {
		placeholders[index] = "?"
		*args = append(*args, value)
	}
	*where = append(*where, column+" IN ("+strings.Join(placeholders, ",")+")")
}

func scanSearchResult(scanner scanner) (domain.SearchResult, error) {
	var entityType, entityID, title, snippet string
	var issueID sql.NullString
	var score float64
	if err := scanner.Scan(&entityType, &entityID, &issueID, &title, &snippet, &score); err != nil {
		return domain.SearchResult{}, searchCorrupt(err)
	}
	kind := domain.SearchEntityType(entityType)
	if !kind.Valid() {
		return domain.SearchResult{}, searchCorruptField(nil, "entity_type", "INVALID_ENUM")
	}
	if _, err := ids.ParseStrict(entityID); err != nil {
		return domain.SearchResult{}, searchCorruptField(err, "entity_id", "INVALID_ULID")
	}
	if issueID.Valid {
		if _, err := ids.ParseStrict(issueID.String); err != nil {
			return domain.SearchResult{}, searchCorruptField(err, "issue_id", "INVALID_ULID")
		}
	}
	if err := domain.ValidateText("title", title, domain.MaxTitleRunes); err != nil {
		return domain.SearchResult{}, searchCorrupt(err)
	}
	if err := domain.ValidateText("snippet", snippet, domain.MaxSearchSnippetRunes); err != nil {
		return domain.SearchResult{}, searchCorrupt(err)
	}
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return domain.SearchResult{}, searchCorruptField(nil, "score", "INVALID_VALUE")
	}
	return domain.SearchResult{
		EntityType: kind, EntityID: entityID, IssueID: nullableStringPointer(issueID),
		Title: title, Snippet: snippet, Score: score,
	}, nil
}

func scanChangeEvent(scanner scanner) (domain.IssueEvent, error) {
	var event domain.IssueEvent
	var issueID, sessionID, attemptID sql.NullString
	var payload, createdAt string
	if err := scanner.Scan(&event.ID, &issueID, &event.EventType, &sessionID, &attemptID, &payload, &createdAt); err != nil {
		return domain.IssueEvent{}, searchCorrupt(err)
	}
	if event.ID <= 0 || strings.TrimSpace(event.EventType) == "" || !json.Valid([]byte(payload)) {
		return domain.IssueEvent{}, searchCorruptField(nil, "event", "INVALID_VALUE")
	}
	for _, value := range []struct {
		field string
		value sql.NullString
	}{{"issue_id", issueID}, {"session_id", sessionID}, {"attempt_id", attemptID}} {
		if value.value.Valid {
			if _, err := ids.ParseStrict(value.value.String); err != nil {
				return domain.IssueEvent{}, searchCorruptField(err, value.field, "INVALID_ULID")
			}
		}
	}
	parsed, err := parseIssueTimestamp("created_at", createdAt)
	if err != nil {
		return domain.IssueEvent{}, err
	}
	event.IssueID = nullableStringPointer(issueID)
	event.SessionID = nullableStringPointer(sessionID)
	event.AttemptID = nullableStringPointer(attemptID)
	event.Payload = json.RawMessage(payload)
	event.CreatedAt = parsed
	return event, nil
}

func isFTSQueryError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "fts5") || strings.Contains(message, "unterminated string")
}

func searchCursorError(err error) error {
	code := "MALFORMED_CURSOR"
	if errors.Is(err, pagination.ErrCursorTooLarge) {
		code = "CURSOR_TOO_LARGE"
	} else if errors.Is(err, pagination.ErrUnsupportedVersion) {
		code = "UNSUPPORTED_CURSOR_VERSION"
	}
	return domain.NewError(domain.CodeInvalidArgument, "search cursor is invalid", false,
		domain.Detail{Field: "cursor", Code: code})
}

func searchCorrupt(cause error) error {
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored search data is invalid", false)
}

func searchCorruptField(cause error, field, code string) error {
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored search data is invalid", false,
		domain.Detail{Field: field, Code: code})
}

var _ ports.SearchRepository = (*SearchRepository)(nil)
