package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

// LoadIssueDetailSnapshotCommand selects one issue and clock instant for a
// single-snapshot detail read.
type LoadIssueDetailSnapshotCommand struct {
	Identifier domain.IssueIdentifier
	Now        time.Time
}

// BoardSnapshotLoader reads the full bounded board aggregate within one
// storage snapshot.
type BoardSnapshotLoader interface {
	LoadBoardSnapshot(ctx context.Context, now time.Time) (domain.BoardResult, error)
}

// IssueDetailSnapshotLoader reads one issue's full bounded detail aggregate
// within one storage snapshot.
type IssueDetailSnapshotLoader interface {
	LoadIssueDetailSnapshot(ctx context.Context, command LoadIssueDetailSnapshotCommand) (domain.IssueDetail, error)
}
