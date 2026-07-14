package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

// ProjectRepository is the SQLite implementation of ports.ProjectRepository.
type ProjectRepository struct {
	db *DB
}

// NewProjectRepository returns a project metadata repository backed by database.
func NewProjectRepository(database *DB) (*ProjectRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "project database is required", false)
	}
	return &ProjectRepository{db: database}, nil
}

// GetProject reads the project row, applied migration version, and latest event
// from one SQLite snapshot. It performs no writes.
func (repository *ProjectRepository) GetProject(ctx context.Context) (domain.Project, error) {
	var project domain.Project
	err := repository.db.readSnapshot(ctx, func(ctx context.Context, query Queryer) error {
		row, err := readProjectRow(ctx, query)
		if err != nil {
			return err
		}

		var schemaVersion int64
		if err := query.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
		).Scan(&schemaVersion); err != nil {
			return corruptProjectProjection(err)
		}
		if schemaVersion < 0 || int64(int(schemaVersion)) != schemaVersion {
			return corruptProjectProjection(fmt.Errorf("invalid schema version %d", schemaVersion))
		}

		var latestEventID int64
		if err := query.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(id), 0) FROM issue_events",
		).Scan(&latestEventID); err != nil {
			return corruptProjectProjection(err)
		}
		if latestEventID < 0 {
			return corruptProjectProjection(fmt.Errorf("invalid latest event ID %d", latestEventID))
		}

		project = row
		project.SchemaVersion = int(schemaVersion)
		project.LatestEventID = latestEventID
		return nil
	})
	if err != nil {
		return domain.Project{}, err
	}
	return project, nil
}

func readProjectRow(ctx context.Context, query Queryer) (domain.Project, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT id, name, instructions, next_issue_number, created_at, updated_at
		FROM projects
		ORDER BY id ASC
		LIMIT 2`)
	if err != nil {
		return domain.Project{}, err
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return domain.Project{}, err
		}
		return domain.Project{}, domain.NewError(
			domain.CodeProjectNotInitialized,
			"project database is not initialized",
			false,
		)
	}

	var (
		name, instructions       sql.NullString
		nextIssueNumber          int64
		createdAt, updatedAt, id string
	)
	if err := rows.Scan(&id, &name, &instructions, &nextIssueNumber, &createdAt, &updatedAt); err != nil {
		return domain.Project{}, corruptProjectProjection(err)
	}
	if rows.Next() {
		return domain.Project{}, domain.NewError(
			domain.CodeStorageCorrupt,
			"stored project projection is invalid",
			false,
		)
	}
	if err := rows.Err(); err != nil {
		return domain.Project{}, err
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.Project{}, corruptProjectProjection(err)
	}
	if nextIssueNumber < 1 {
		return domain.Project{}, corruptProjectProjection(fmt.Errorf("invalid project values"))
	}

	created, err := parseProjectTimestamp("created_at", createdAt)
	if err != nil {
		return domain.Project{}, err
	}
	updated, err := parseProjectTimestamp("updated_at", updatedAt)
	if err != nil {
		return domain.Project{}, err
	}
	return domain.Project{
		ID:              id,
		Name:            nullableProjectString(name),
		Instructions:    nullableProjectString(instructions),
		NextIssueNumber: nextIssueNumber,
		CreatedAt:       created,
		UpdatedAt:       updated,
	}, nil
}

func nullableProjectString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func parseProjectTimestamp(field, value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, corruptProjectTimestamp(err, field)
	}
	if _, offset := parsed.Zone(); offset != 0 {
		return time.Time{}, corruptProjectTimestamp(nil, field)
	}
	return parsed.UTC(), nil
}

func corruptProjectTimestamp(cause error, field string) error {
	detail := domain.Detail{Field: field, Code: "INVALID_TIMESTAMP"}
	if cause != nil {
		return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored project projection is invalid", false, detail)
	}
	return domain.NewError(domain.CodeStorageCorrupt, "stored project projection is invalid", false, detail)
}

func corruptProjectProjection(cause error) error {
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored project projection is invalid", false)
}

var _ ports.ProjectRepository = (*ProjectRepository)(nil)
