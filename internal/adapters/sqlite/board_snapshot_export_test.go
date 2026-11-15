package sqlite

// SetBoardSnapshotAfterFirstReadHookForTest installs a test-only
// synchronization hook executed immediately after the first read inside
// LoadBoardSnapshot and LoadIssueDetailSnapshot pins the WAL snapshot.
func SetBoardSnapshotAfterFirstReadHookForTest(repository *BoardSnapshotRepository, hook func()) func() {
	previous := repository.afterFirstReadForTest
	repository.afterFirstReadForTest = hook
	return func() {
		repository.afterFirstReadForTest = previous
	}
}

// SetPoolMaxOpenConnsForTest overrides the underlying database pool limit for
// single-connection snapshot tests.
func SetPoolMaxOpenConnsForTest(db *DB, maxOpenConns int) {
	db.pool.SetMaxOpenConns(maxOpenConns)
}
