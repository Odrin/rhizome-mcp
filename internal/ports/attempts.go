package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

type ClaimIssueCommand struct {
	Identifier    domain.IssueIdentifier
	AttemptID     string
	SessionID     *string
	TokenHash     []byte
	LeaseToken    string
	LeaseDuration time.Duration
	OccurredAt    time.Time
	// Resources is optional (ISSUE-180): when non-empty, claiming the issue
	// and acquiring every listed resource happen inside the same write
	// transaction, so a conflict aborts the claim itself. Rejected outright
	// when the claim resolves to a review attempt (ISSUE-179's locked
	// lifecycle: only work attempts may own reservations).
	Resources      []ReservationResourceInput
	IdempotencyKey string
	RequestHash    []byte
}

type ClaimIssueResult struct {
	Issue        domain.Issue
	Projection   domain.IssueProjection
	Attempt      domain.WorkAttempt
	Reservations []domain.Reservation
	// LeaseToken is excluded from JSON encoding: it is the raw attempt
	// secret and must never be persisted (idempotency_records.response_json
	// stores this struct verbatim on a fresh claim).
	LeaseToken string `json:"-"`
}

type RenewAttemptCommand struct {
	AttemptID     string
	SessionID     *string
	TokenHash     []byte
	LeaseDuration time.Duration
	OccurredAt    time.Time
}

type RenewAttemptResult struct {
	LeaseExpiresAt time.Time
	ServerTime     time.Time
}

type SaveAttemptNoteCommand struct {
	NoteID         string
	AttemptID      string
	SessionID      *string
	TokenHash      []byte
	Kind           domain.AttemptNoteKind
	Content        string
	NextSteps      []string
	Important      bool
	Artifacts      []domain.Artifact
	OccurredAt     time.Time
	IdempotencyKey string
	RequestHash    []byte
}

type SaveAttemptNoteResult struct {
	Note      domain.AttemptNote
	Artifacts []domain.Artifact
}

type FinishAttemptCommand struct {
	AttemptID      string
	SessionID      *string
	TokenHash      []byte
	Input          domain.FinishAttemptInput
	Artifacts      []domain.Artifact
	IdempotencyKey string
	RequestHash    []byte
	OccurredAt     time.Time
}

type FinishAttemptResult struct {
	Attempt       domain.WorkAttempt
	Issue         domain.Issue
	Warnings      []string
	LatestEventID int64
	Artifacts     []domain.Artifact
}

type ForceReleaseAttemptCommand struct {
	AttemptID  string
	OccurredAt time.Time
}

type ForceReleaseAttemptResult struct {
	Attempt       domain.WorkAttempt
	LatestEventID int64
}

type ExpireAttemptsCommand struct {
	OccurredAt time.Time
}

type ExpireAttemptsResult struct {
	ExpiredAttemptCount int
}

// ListActiveAttemptsCommand bounds a project-wide read of currently active
// (leased, unexpired) attempts.
type ListActiveAttemptsCommand struct {
	Limit int
	Now   time.Time
}

// AttemptRepository executes all attempt lifecycle mutations atomically.
type AttemptRepository interface {
	// ClaimIssue is the sole entry point for both a fresh claim and an
	// idempotent replay: with LeaseToken never persisted, a replay of a
	// still-active attempt rotates the lease and returns a fresh token, so
	// replay must always go through the write path. See ClaimIssueCommand.
	ClaimIssue(context.Context, ClaimIssueCommand) (ClaimIssueResult, error)
	RenewAttempt(context.Context, RenewAttemptCommand) (RenewAttemptResult, error)
	SaveAttemptNote(context.Context, SaveAttemptNoteCommand) (SaveAttemptNoteResult, error)
	LookupSaveAttemptNote(context.Context, string, []byte) (SaveAttemptNoteResult, bool, error)
	LookupFinishedAttempt(context.Context, string, []byte) (FinishAttemptResult, bool, error)
	FinishAttempt(context.Context, FinishAttemptCommand) (FinishAttemptResult, error)
	ForceReleaseAttempt(context.Context, ForceReleaseAttemptCommand) (ForceReleaseAttemptResult, error)
	ExpireAttempts(context.Context, ExpireAttemptsCommand) (ExpireAttemptsResult, error)
	ListActiveAttempts(context.Context, ListActiveAttemptsCommand) (domain.ActiveAttemptList, error)
	// SubmitGateEvidence is a lease-authenticated, idempotent upsert (ISSUE-171):
	// see SubmitGateEvidenceCommand for the frozen-snapshot and same-attempt
	// artifact validation it performs inside the write transaction.
	SubmitGateEvidence(context.Context, SubmitGateEvidenceCommand) (SubmitGateEvidenceResult, error)
	LookupSubmitGateEvidence(context.Context, string, []byte) (SubmitGateEvidenceResult, bool, error)
	// ListAttemptEvidence returns every current evidence record for one
	// attempt, for atomic gate evaluation (ISSUE-172) and issue activity.
	ListAttemptEvidence(context.Context, ListAttemptEvidenceCommand) ([]domain.AttemptEvidence, error)
	// ReserveResources authenticates AttemptID's lease, then normalizes and
	// acquires every requested resource against the live active set,
	// all-or-nothing, in the same transaction (ISSUE-180).
	ReserveResources(context.Context, ReserveResourcesCommand) (ReserveResourcesResult, error)
	LookupReserveResources(ctx context.Context, key string, hash []byte) (ReserveResourcesResult, bool, error)
	// ReleaseResources authenticates AttemptID's lease, then releases the
	// named reservations (or every active one it owns, when ReservationIDs
	// is empty), all inside one transaction (ISSUE-180).
	ReleaseResources(context.Context, ReleaseResourcesCommand) (ReleaseResourcesResult, error)
	LookupReleaseResources(ctx context.Context, key string, hash []byte) (ReleaseResourcesResult, bool, error)
}

// SubmitGateEvidenceCommand authenticates and upserts one evidence record.
// EvidenceID is used only when no current record exists for (AttemptID, Key);
// an existing record's ID and Version are read and incremented in place.
type SubmitGateEvidenceCommand struct {
	EvidenceID     string
	AttemptID      string
	TokenHash      []byte
	Key            string
	Result         domain.EvidenceResult
	Summary        string
	Details        string
	ArtifactIDs    []string
	OccurredAt     time.Time
	IdempotencyKey string
	RequestHash    []byte
}

// SubmitGateEvidenceResult is the persisted evidence record after a
// successful submission.
type SubmitGateEvidenceResult struct {
	Evidence domain.AttemptEvidence
}

// ListAttemptEvidenceCommand identifies the attempt whose current evidence
// records to load.
type ListAttemptEvidenceCommand struct {
	AttemptID string
}

// ReserveResourcesCommand is a lease-authenticated, idempotent request to
// add resources to one active work attempt's reservations, atomically with
// every other resource in the same call (ISSUE-180: reserve_resources).
type ReserveResourcesCommand struct {
	AttemptID      string
	SessionID      *string
	TokenHash      []byte
	Resources      []ReservationResourceInput
	OccurredAt     time.Time
	IdempotencyKey string
	RequestHash    []byte
}

// ReserveResourcesResult is the reservations newly acquired by one
// ReserveResources call (not the attempt's full active set).
type ReserveResourcesResult struct {
	Reservations []domain.Reservation
}

// ReleaseResourcesCommand is a lease-authenticated, idempotent request to
// release reservations owned by one active work attempt. ReservationIDs
// empty means "release every active reservation this attempt owns"; a
// non-empty list releases exactly those IDs, and fails if any named ID is
// not both active and owned by AttemptID (ISSUE-180: release_resources).
type ReleaseResourcesCommand struct {
	AttemptID      string
	SessionID      *string
	TokenHash      []byte
	ReservationIDs []string
	OccurredAt     time.Time
	IdempotencyKey string
	RequestHash    []byte
}

// ReleaseResourcesResult is the reservations released by one
// ReleaseResources call.
type ReleaseResourcesResult struct {
	Reservations []domain.Reservation
}
