// Package fixture is scan input for
// TestGateEnforcementScanReportsUngatedSiteInsideAnAllowlistedFile. It lives
// under testdata so the go tool never builds it. It imitates the shape of the
// real internal/adapters/sqlite/issues.go: one allowlisted site (CreateIssue)
// plus one site added inside the same file without a gate, which is exactly
// the case a file-granularity scan cannot see.
package fixture

import "context"

func CreateIssue(ctx context.Context, tx execer) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO issues(id, status) VALUES (?, ?)`, "id", "open")
	return err
}

func sneakUngatedStatusWrite(ctx context.Context, tx execer) error {
	_, err := tx.ExecContext(ctx, `UPDATE issues SET status = ? WHERE id = ?`, "done", "id")
	return err
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (any, error)
}
