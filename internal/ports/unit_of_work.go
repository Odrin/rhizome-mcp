package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

// UnitOfWork is a single write transaction's tx-scoped write surface,
// obtained from Transactor.RunWrite and valid only for the duration of that
// call. It exposes the narrow, transaction-scoped primitives every write
// repository already hand-rolls -- idempotency lookup/store, issue event
// append, and the optimistic conditional issue update -- so an application
// service can compose operations that would otherwise live in different
// repositories' own independent DB.Write transactions into one atomic
// SQLite write. The gates epic (ISSUE-172) and reservations epic
// (ISSUE-178) both need this to join new tables to the claim/finish/expire
// transactions those repositories already run.
//
// UnitOfWork is deliberately narrow: it is not a substitute for a
// repository's own command method. A repository method that needs a
// transaction still calls Transactor.RunWrite and drives its own domain
// logic against the UnitOfWork's primitives, exactly as it drives DB.Write
// today; UnitOfWork exists so that logic can be reused from outside that
// repository's own package-private helpers.
type UnitOfWork interface {
	// LookupIdempotency returns a previously stored response for operation
	// and key, and whether one was found. When found, callers must compare
	// requestHash to their own canonical request hash and return
	// CodeIdempotencyConflict on a mismatch before trusting responseJSON.
	LookupIdempotency(ctx context.Context, operation, key string) (requestHash []byte, responseJSON string, found bool, err error)
	// StoreIdempotency persists responseJSON for operation and key.
	// Callers encode responseJSON themselves (json.Marshal) so this stays
	// agnostic to each operation's result shape.
	StoreIdempotency(ctx context.Context, operation, key string, requestHash []byte, responseJSON []byte, occurredAt time.Time) error
	// AppendIssueEvent inserts one issue_events row (source='issue') and
	// returns its id via INSERT ... RETURNING id, replacing the repeated
	// SELECT COALESCE(MAX(id), 0) FROM issue_events pattern. issueID is
	// nullable: some events (e.g. a project-scoped decision) are not
	// attached to any issue.
	AppendIssueEvent(ctx context.Context, issueID *string, eventType string, sessionID, attemptID *string, payload []byte, occurredAt time.Time) (eventID int64, err error)
	// ConditionalUpdateIssue runs setClause -- a caller-supplied, fully
	// parameterized "col = ?, col2 = ?, ..." SQL fragment, with args in the
	// same order as its placeholders -- against issues WHERE id = ? AND
	// version = ? AND archived_at IS NULL. On a zero-row result it
	// classifies why (issue not found, archived, or a genuine version
	// conflict) and returns the matching domain error; a one-row result
	// returns nil.
	ConditionalUpdateIssue(ctx context.Context, issueID string, expectedVersion int64, setClause string, args ...any) error
	// LoadIssueForMutation loads an issue by identifier for mutation in a
	// write transaction, returning the issue or a domain error if not found
	// or archived.
	LoadIssueForMutation(ctx context.Context, identifier domain.IssueIdentifier) (domain.Issue, error)
}

// Transactor runs fn inside one atomic SQLite write transaction, handing it
// a UnitOfWork scoped to that transaction.
//
// Retry contract: the underlying DB.Write retries the *entire* transaction
// -- including a full re-invocation of fn on a fresh connection -- when
// SQLite reports BUSY or LOCKED, up to the configured retry policy's delay
// count; any other error aborts immediately with no retry. fn must
// therefore be safe to run more than once for the same logical call: it
// must not accumulate into, or otherwise mutate, any Go state captured
// from outside its own parameters (a local accumulator declared inside fn
// is fine, since it starts fresh on every invocation; one declared by the
// caller and closed over is not). See internal/adapters/sqlite/attempts.go's
// ExpireAttempts for a fixed real instance of this hazard: its
// ExpiredAttemptCount used to live in a variable declared outside the
// DB.Write callback and would double-count on a retried transaction.
type Transactor interface {
	RunWrite(ctx context.Context, fn func(context.Context, UnitOfWork) error) error
}
