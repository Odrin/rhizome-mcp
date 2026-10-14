package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"rhizome-mcp/internal/domain"
)

const maxIssueTimestampDetails = 10

// IssueTimestampReport counts invalid and non-canonical issue timestamp cells.
// Details contain only the first ten problems in sequence/column order.
type IssueTimestampReport struct {
	NonCanonicalCount int
	InvalidCount      int
	Details           []domain.Detail
}

func issueTimestampDetail(issueID string, sequenceNo int64, field, value, code string) domain.Detail {
	if len(value) > 160 {
		value = value[:160] + "..."
	}
	return domain.Detail{
		Field: field,
		Code:  code,
		Message: fmt.Sprintf("ISSUE-%d (%s): %s=%q. Run rhizome-mcp maintenance normalize-issue-timestamps --backup PATH for recognized legacy UTC values; otherwise restore the correct UTC value from a trusted backup.",
			sequenceNo, issueID, field, value),
	}
}

func canonicalIssueTimestamp(value string) (string, error) {
	parsed, err := parseStorageTime(value)
	if err != nil {
		// Only SQLite's exact whole-second UTC form is a recovery candidate.
		const layout = "2006-01-02 15:04:05"
		if len(value) != len(layout) {
			return "", err
		}
		parsed, err = time.Parse(layout, value)
		if err != nil {
			return "", err
		}
		if parsed.Format(layout) != value {
			return "", errors.New("timestamp is not an exact SQLite UTC timestamp")
		}
	}
	if index := strings.IndexAny(value, ".,"); index >= 0 {
		end := index + 1
		for end < len(value) && value[end] >= '0' && value[end] <= '9' {
			end++
		}
		if end-index-1 > 9 {
			return "", errors.New("timestamp precision exceeds nanoseconds")
		}
	}
	return formatStorageTime(parsed), nil
}

func scanIssueTimestamps(ctx context.Context, query Queryer, visit func(string, int64, string, string) error) error {
	rows, err := query.QueryContext(ctx, `SELECT id, sequence_no, created_at, updated_at, closed_at, archived_at
		FROM issues ORDER BY sequence_no`)
	if err != nil {
		return err
	}
	defer rows.Close()
	fields := [...]string{"created_at", "updated_at", "closed_at", "archived_at"}
	for rows.Next() {
		var id string
		var sequenceNo int64
		var values [4]sql.NullString
		if err := rows.Scan(&id, &sequenceNo, &values[0], &values[1], &values[2], &values[3]); err != nil {
			return err
		}
		for index, value := range values {
			if !value.Valid {
				continue
			}
			if err := visit(id, sequenceNo, fields[index], value.String); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}

// CheckIssueTimestamps inspects raw cells without loading issue projections.
func CheckIssueTimestamps(ctx context.Context, query Queryer) (IssueTimestampReport, error) {
	report := IssueTimestampReport{Details: []domain.Detail{}}
	err := scanIssueTimestamps(ctx, query, func(id string, sequenceNo int64, field, value string) error {
		canonical, err := canonicalIssueTimestamp(value)
		code := "NON_CANONICAL_TIMESTAMP"
		switch {
		case err != nil:
			report.InvalidCount++
			code = "INVALID_TIMESTAMP"
		case canonical != value:
			report.NonCanonicalCount++
		default:
			return nil
		}
		if len(report.Details) < maxIssueTimestampDetails {
			report.Details = append(report.Details, issueTimestampDetail(id, sequenceNo, field, value, code))
		}
		return nil
	})
	return report, err
}

// NormalizeIssueTimestamps is the transactional storage half of recovery.
// Its runtime caller must create a validated backup before invoking it.
func (db *DB) NormalizeIssueTimestamps(ctx context.Context) (int, error) {
	var normalized int
	err := db.Write(ctx, func(ctx context.Context, tx Executor) error {
		type repair struct{ id, field, value string }
		repairs := []repair{}
		if err := scanIssueTimestamps(ctx, tx, func(id string, sequenceNo int64, field, value string) error {
			canonical, err := canonicalIssueTimestamp(value)
			if err != nil {
				return domain.WrapError(err, domain.CodeStorageCorrupt, "issue timestamp normalization refused; no timestamps were changed", false,
					issueTimestampDetail(id, sequenceNo, field, value, "INVALID_TIMESTAMP"))
			}
			if canonical != value {
				repairs = append(repairs, repair{id: id, field: field, value: canonical})
			}
			return nil
		}); err != nil {
			return err
		}
		for _, repair := range repairs {
			// field comes only from scanIssueTimestamps' fixed column list.
			result, err := tx.ExecContext(ctx, `UPDATE issues SET `+repair.field+` = ? WHERE id = ?`, repair.value, repair.id)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if affected != 1 {
				return domain.NewError(domain.CodeStorageCorrupt, "issue timestamp normalization did not update the expected row; no timestamps were changed", false,
					domain.Detail{Field: repair.field, Code: "UPDATE_FAILED", Message: "issue_id=" + repair.id})
			}
		}
		normalized = len(repairs)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return normalized, nil
}
