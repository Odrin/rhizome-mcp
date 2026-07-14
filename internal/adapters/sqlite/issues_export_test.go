package sqlite

// SetGetIssueAfterProjectionHookForTest installs a test-only synchronization
// hook after GetIssue has read its base projection.
func SetGetIssueAfterProjectionHookForTest(repository *IssueRepository, hook func()) func() {
	previous := repository.afterGetIssueProjectionReadForTest
	repository.afterGetIssueProjectionReadForTest = hook
	return func() {
		repository.afterGetIssueProjectionReadForTest = previous
	}
}
