package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/pagination"
	"rhizome-mcp/internal/ports"
)

type labelCursor struct {
	NormalizedName string `json:"normalized_name"`
	ID             string `json:"id"`
}

var labelCursorCodec = pagination.NewCodec[labelCursor](0)

// ListLabels returns labels in explicit ASCII-NOCASE normalized-name order.
func (repository *IssueRepository) ListLabels(ctx context.Context, command ports.ListLabelsCommand) (domain.LabelList, error) {
	input, err := command.Input.Validate()
	if err != nil {
		return domain.LabelList{}, err
	}
	var after *labelCursor
	if input.Cursor != "" {
		decoded, err := labelCursorCodec.Decode(input.Cursor)
		if err != nil {
			return domain.LabelList{}, labelCursorError(err)
		}
		after = &decoded
	}

	var result domain.LabelList
	err = repository.db.Read(ctx, func(ctx context.Context, query Queryer) error {
		where := make([]string, 0, 2)
		args := make([]any, 0, 5)
		if input.Query != "" {
			where = append(where, "substr(name, 1, length(?)) = ? COLLATE NOCASE")
			args = append(args, input.Query, input.Query)
		}
		if after != nil {
			where = append(where, `(name COLLATE NOCASE > ? COLLATE NOCASE
				OR (name COLLATE NOCASE = ? COLLATE NOCASE AND id > ?))`)
			args = append(args, after.NormalizedName, after.NormalizedName, after.ID)
		}
		statement := "SELECT id, name, description, created_at FROM labels"
		if len(where) != 0 {
			statement += " WHERE " + strings.Join(where, " AND ")
		}
		statement += " ORDER BY name COLLATE NOCASE ASC, id ASC LIMIT ?"
		args = append(args, input.Limit+1)
		rows, err := query.QueryContext(ctx, statement, args...)
		if err != nil {
			return err
		}
		defer rows.Close()

		items := make([]domain.Label, 0, input.Limit)
		for rows.Next() {
			label, err := scanLabel(rows)
			if err != nil {
				return err
			}
			items = append(items, label)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(items) > input.Limit {
			result.HasMore = true
			items = items[:input.Limit]
			last := items[len(items)-1]
			cursor, err := labelCursorCodec.Encode(labelCursor{NormalizedName: last.NormalizedName, ID: last.ID})
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageFailure, "cannot encode label cursor", false)
			}
			result.NextCursor = &cursor
		}
		result.Items = items
		return nil
	})
	if err != nil {
		return domain.LabelList{}, err
	}
	return result, nil
}

func resolveIssueLabels(ctx context.Context, tx Executor, names []string, createMissing bool, labelIDs []string, now time.Time) ([]domain.Label, error) {
	if err := validateLabelIDs(names, createMissing, labelIDs); err != nil {
		return nil, err
	}
	resolved := make([]domain.Label, 0, len(names))
	for index, name := range names {
		label, err := loadLabelByName(ctx, tx, name)
		if err == nil {
			resolved = append(resolved, label)
			continue
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
		if !createMissing {
			return nil, domain.NewError(
				domain.CodeLabelNotFound,
				"requested label does not exist",
				false,
				domain.Detail{Field: "labels", Code: domain.CodeLabelNotFound, Message: name},
			)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO labels(id, name, created_at) VALUES (?, ?, ?)
			ON CONFLICT DO NOTHING`, labelIDs[index], name, now.UTC().Format(time.RFC3339Nano)); err != nil {
			return nil, err
		}
		label, err = loadLabelByName(ctx, tx, name)
		if err == sql.ErrNoRows {
			return nil, domain.NewError(domain.CodeStorageConstraint, "label could not be created", false)
		}
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, label)
	}
	return resolved, nil
}

func validateLabelIDs(names []string, createMissing bool, labelIDs []string) error {
	if !createMissing {
		if len(labelIDs) != 0 {
			return domain.NewError(domain.CodeInvalidArgument, "label identifiers are invalid", false)
		}
		return nil
	}
	if len(labelIDs) != len(names) {
		return domain.NewError(domain.CodeIDGeneration, "cannot generate label identifier", false)
	}
	for _, labelID := range labelIDs {
		if _, err := ids.ParseStrict(labelID); err != nil {
			return domain.WrapError(err, domain.CodeIDGeneration, "cannot generate label identifier", false)
		}
	}
	return nil
}

func replaceIssueLabels(ctx context.Context, tx Executor, issueID string, labels []domain.Label) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM issue_labels WHERE issue_id = ?", issueID); err != nil {
		return err
	}
	for _, label := range labels {
		if _, err := tx.ExecContext(ctx, "INSERT INTO issue_labels(issue_id, label_id) VALUES (?, ?)", issueID, label.ID); err != nil {
			return err
		}
	}
	return nil
}

func loadIssueLabels(ctx context.Context, query Queryer, issueID string) ([]domain.Label, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT labels.id, labels.name, labels.description, labels.created_at
		FROM issue_labels
		JOIN labels ON labels.id = issue_labels.label_id
		WHERE issue_labels.issue_id = ?
		ORDER BY labels.name COLLATE NOCASE ASC, labels.id ASC`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	labels := make([]domain.Label, 0)
	for rows.Next() {
		label, err := scanLabel(rows)
		if err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return labels, nil
}

func loadLabelByName(ctx context.Context, query Queryer, name string) (domain.Label, error) {
	row := query.QueryRowContext(ctx, `
		SELECT id, name, description, created_at
		FROM labels WHERE name = ? COLLATE NOCASE`, name)
	return scanLabel(row)
}

type labelScanner interface {
	Scan(...any) error
}

func scanLabel(scanner labelScanner) (domain.Label, error) {
	var id, name, createdAt string
	var description sql.NullString
	if err := scanner.Scan(&id, &name, &description, &createdAt); err != nil {
		return domain.Label{}, err
	}
	displayName, normalizedName, err := domain.NormalizeLabelName(name)
	if err != nil {
		return domain.Label{}, domain.WrapError(err, domain.CodeStorageCorrupt, "stored label projection is invalid", false)
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return domain.Label{}, domain.WrapError(err, domain.CodeStorageCorrupt, "stored label projection is invalid", false,
			domain.Detail{Field: "created_at", Code: "INVALID_TIMESTAMP"})
	}
	if _, offset := created.Zone(); offset != 0 {
		return domain.Label{}, domain.NewError(domain.CodeStorageCorrupt, "stored label projection is invalid", false,
			domain.Detail{Field: "created_at", Code: "INVALID_TIMESTAMP"})
	}
	return domain.Label{
		ID:             id,
		Name:           displayName,
		NormalizedName: normalizedName,
		Description:    nullableStringPointer(description),
		CreatedAt:      created.UTC(),
	}, nil
}

func labelNames(labels []domain.Label) []string {
	if len(labels) == 0 {
		return nil
	}
	names := make([]string, len(labels))
	for i, label := range labels {
		names[i] = label.Name
	}
	return names
}

func labelCursorError(err error) error {
	code := "MALFORMED_CURSOR"
	if errors.Is(err, pagination.ErrCursorTooLarge) {
		code = "CURSOR_TOO_LARGE"
	} else if errors.Is(err, pagination.ErrUnsupportedVersion) {
		code = "UNSUPPORTED_CURSOR_VERSION"
	}
	return domain.NewError(domain.CodeInvalidArgument, "label cursor is invalid", false,
		domain.Detail{Field: "cursor", Code: code})
}
