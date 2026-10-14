package runtime

import (
	"context"
	"fmt"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
)

// IssueTimestampNormalizationReport identifies the retained backup and repaired cells.
type IssueTimestampNormalizationReport struct {
	BackupPath      string
	NormalizedCount int
}

// NormalizeIssueTimestamps preserves a validated online backup before recovery.
func (project *Project) NormalizeIssueTimestamps(ctx context.Context, backupPath string) (IssueTimestampNormalizationReport, error) {
	backup, err := project.Backup(ctx, backupPath)
	if err != nil {
		return IssueTimestampNormalizationReport{}, err
	}
	report := IssueTimestampNormalizationReport{BackupPath: backup.OutputPath}
	project.mu.RLock()
	defer project.mu.RUnlock()
	if project.closed || project.Database == nil {
		return report, domain.NewError(domain.CodeStorageUnavailable, "project closed before timestamp normalization; backup retained at "+backup.OutputPath, false)
	}
	report.NormalizedCount, err = project.Database.NormalizeIssueTimestamps(ctx)
	if err != nil {
		return report, fmt.Errorf("timestamp normalization failed; backup retained at %q: %w", backup.OutputPath, err)
	}
	return report, nil
}

func checkIssueTimestampsQuery(ctx context.Context, query sqlite.Queryer) (string, error) {
	report, err := sqlite.CheckIssueTimestamps(ctx, query)
	if err != nil {
		return "", err
	}
	if report.NonCanonicalCount == 0 && report.InvalidCount == 0 {
		return "ok", nil
	}
	message := fmt.Sprintf("%d non-canonical and %d invalid issue timestamp cells", report.NonCanonicalCount, report.InvalidCount)
	for _, detail := range report.Details {
		message += "; " + detail.Message
	}
	return message, domain.NewError(domain.CodeStorageCorrupt, message, false, report.Details...)
}
