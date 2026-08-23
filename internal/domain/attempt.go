package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

const (
	// DefaultLeaseSeconds is used when a lease duration is omitted.
	DefaultLeaseSeconds = 900
	// MinLeaseSeconds and MaxLeaseSeconds bound short-lived attempt leases.
	MinLeaseSeconds = 60
	MaxLeaseSeconds = 3600
	// MaxLeaseTokenRunes bounds an opaque token supplied to renewal.
	MaxLeaseTokenRunes = 512
	// MaxAttemptNoteNextSteps bounds the compact recovery steps stored with a note.
	MaxAttemptNoteNextSteps = 20
	// MaxAttemptNoteNextStepRunes bounds each compact recovery step.
	MaxAttemptNoteNextStepRunes = 1_000
	MaxVerificationItems        = 20
	MaxVerificationRunes        = 1_000
)

type AttemptKind string

const (
	AttemptKindWork   AttemptKind = "work"
	AttemptKindReview AttemptKind = "review"
)

func (value AttemptKind) Valid() bool { return value == AttemptKindWork || value == AttemptKindReview }

type AttemptStatus string

const (
	AttemptStatusActive      AttemptStatus = "active"
	AttemptStatusCompleted   AttemptStatus = "completed"
	AttemptStatusFailed      AttemptStatus = "failed"
	AttemptStatusInterrupted AttemptStatus = "interrupted"
	AttemptStatusExpired     AttemptStatus = "expired"
	AttemptStatusCancelled   AttemptStatus = "cancelled"
)

func (value AttemptStatus) Valid() bool {
	switch value {
	case AttemptStatusActive, AttemptStatusCompleted, AttemptStatusFailed, AttemptStatusInterrupted, AttemptStatusExpired, AttemptStatusCancelled:
		return true
	default:
		return false
	}

}

type AttemptOutcome string

const (
	AttemptOutcomeCompleted   AttemptOutcome = "completed"
	AttemptOutcomeFailed      AttemptOutcome = "failed"
	AttemptOutcomeInterrupted AttemptOutcome = "interrupted"
)

func (v AttemptOutcome) Valid() bool {
	return v == AttemptOutcomeCompleted || v == AttemptOutcomeFailed || v == AttemptOutcomeInterrupted
}

type ReviewOutcome string

const (
	ReviewOutcomeApproved         ReviewOutcome = "approved"
	ReviewOutcomeChangesRequested ReviewOutcome = "changes_requested"
	ReviewOutcomeBlocked          ReviewOutcome = "blocked"
)

func (v ReviewOutcome) Valid() bool {
	return v == ReviewOutcomeApproved || v == ReviewOutcomeChangesRequested || v == ReviewOutcomeBlocked
}

type FailureReasonCode string

const (
	FailureReasonImplementationError     FailureReasonCode = "implementation_error"
	FailureReasonEnvironmentError        FailureReasonCode = "environment_error"
	FailureReasonMissingDependency       FailureReasonCode = "missing_dependency"
	FailureReasonInvalidRequirements     FailureReasonCode = "invalid_requirements"
	FailureReasonTestsFailed             FailureReasonCode = "tests_failed"
	FailureReasonContextLost             FailureReasonCode = "context_lost"
	FailureReasonTimeout                 FailureReasonCode = "timeout"
	FailureReasonOther                   FailureReasonCode = "other"
	FailureReasonCodeImplementationError                   = FailureReasonImplementationError
	FailureReasonCodeEnvironmentError                      = FailureReasonEnvironmentError
	FailureReasonCodeMissingDependency                     = FailureReasonMissingDependency
	FailureReasonCodeInvalidRequirements                   = FailureReasonInvalidRequirements
	FailureReasonCodeTestsFailed                           = FailureReasonTestsFailed
	FailureReasonCodeContextLost                           = FailureReasonContextLost
	FailureReasonCodeTimeout                               = FailureReasonTimeout
	FailureReasonCodeOther                                 = FailureReasonOther
)

func (v FailureReasonCode) Valid() bool {
	switch v {
	case FailureReasonImplementationError, FailureReasonEnvironmentError, FailureReasonMissingDependency, FailureReasonInvalidRequirements, FailureReasonTestsFailed, FailureReasonContextLost, FailureReasonTimeout, FailureReasonOther:
		return true
	}
	return false
}

type InterruptionReasonCode string

const (
	InterruptionReasonHandoff               InterruptionReasonCode = "handoff"
	InterruptionReasonUserRequest           InterruptionReasonCode = "user_request"
	InterruptionReasonContextLimit          InterruptionReasonCode = "context_limit"
	InterruptionReasonClientShutdown        InterruptionReasonCode = "client_shutdown"
	InterruptionReasonEnvironmentChange     InterruptionReasonCode = "environment_change"
	InterruptionReasonOther                 InterruptionReasonCode = "other"
	InterruptionReasonCodeHandoff                                  = InterruptionReasonHandoff
	InterruptionReasonCodeUserRequest                              = InterruptionReasonUserRequest
	InterruptionReasonCodeContextLimit                             = InterruptionReasonContextLimit
	InterruptionReasonCodeClientShutdown                           = InterruptionReasonClientShutdown
	InterruptionReasonCodeEnvironmentChange                        = InterruptionReasonEnvironmentChange
	InterruptionReasonCodeOther                                    = InterruptionReasonOther
)

func (v InterruptionReasonCode) Valid() bool {
	switch v {
	case InterruptionReasonHandoff, InterruptionReasonUserRequest, InterruptionReasonContextLimit, InterruptionReasonClientShutdown, InterruptionReasonEnvironmentChange, InterruptionReasonOther:
		return true
	}
	return false
}

// WorkAttempt is the durable attempt record. It intentionally excludes the raw
// lease token; only its hash is persisted.
type WorkAttempt struct {
	ID                     string
	IssueID                string
	SessionID              *string
	AgentLabel             *string
	Kind                   AttemptKind
	Status                 AttemptStatus
	IssueVersionAtStart    int64
	ContextEventIDAtStart  int64
	LeaseExpiresAt         time.Time
	StartedAt              time.Time
	LastHeartbeatAt        time.Time
	FinishedAt             *time.Time
	ResultSummary          *string
	NextSteps              []string
	Verification           []string
	FailureReasonCode      *FailureReasonCode
	InterruptionReasonCode *InterruptionReasonCode
	ReasonDetails          *string
}

type AttemptAcknowledgement struct {
	IssueVersion  int64
	LatestEventID int64
}
type FinishAttemptInput struct {
	AttemptID              string
	LeaseToken             string
	SessionID              *string
	Outcome                AttemptOutcome
	ResultSummary          string
	NextSteps              []string
	Verification           []string
	TargetIssueStatus      *Status
	BlockedReason          *string
	ReviewOutcome          *ReviewOutcome
	FailureReasonCode      *FailureReasonCode
	InterruptionReasonCode *InterruptionReasonCode
	ReasonDetails          *string
	AcknowledgedChanges    *AttemptAcknowledgement
	Artifacts              []ArtifactInput
	IdempotencyKey         *string
}

func (input FinishAttemptInput) Validate() (FinishAttemptInput, error) {
	id, err := ulid.ParseStrict(input.AttemptID)
	if err != nil || len(input.AttemptID) != 26 || id.String() != input.AttemptID {
		return FinishAttemptInput{}, validationError("attempt_id", "INVALID_ULID", "must be a canonical ULID")
	}
	if strings.TrimSpace(input.LeaseToken) == "" {
		return FinishAttemptInput{}, validationError("lease_token", "REQUIRED", "is required")
	}
	if err := ValidateText("lease_token", input.LeaseToken, MaxLeaseTokenRunes); err != nil {
		return FinishAttemptInput{}, err
	}
	if !input.Outcome.Valid() {
		return FinishAttemptInput{}, validationError("outcome", "INVALID_ENUM", "must be completed, failed, or interrupted")
	}
	if strings.TrimSpace(input.ResultSummary) == "" {
		return FinishAttemptInput{}, validationError("result_summary", "REQUIRED", "is required")
	}
	if err := ValidateText("result_summary", input.ResultSummary, MaxAttemptNoteRunes); err != nil {
		return FinishAttemptInput{}, err
	}
	next, err := CopyBounded("next_steps", input.NextSteps, MaxAttemptNoteNextSteps)
	if err != nil {
		return FinishAttemptInput{}, err
	}
	for _, v := range next {
		if strings.TrimSpace(v) == "" {
			return FinishAttemptInput{}, validationError("next_steps", "REQUIRED", "items must be nonblank")
		}
		if err := ValidateText("next_steps", v, MaxAttemptNoteNextStepRunes); err != nil {
			return FinishAttemptInput{}, err
		}
	}
	verification, err := CopyBounded("verification", input.Verification, MaxVerificationItems)
	if err != nil {
		return FinishAttemptInput{}, err
	}
	for _, v := range verification {
		if strings.TrimSpace(v) == "" {
			return FinishAttemptInput{}, validationError("verification", "REQUIRED", "items must be nonblank")
		}
		if err := ValidateText("verification", v, MaxVerificationRunes); err != nil {
			return FinishAttemptInput{}, err
		}
	}
	artifacts, err := ValidateArtifactInputs("artifacts", input.Artifacts)
	if err != nil {
		return FinishAttemptInput{}, err
	}
	if input.TargetIssueStatus != nil && (*input.TargetIssueStatus == StatusOpen || *input.TargetIssueStatus == StatusCancelled || !input.TargetIssueStatus.Valid()) {
		return FinishAttemptInput{}, validationError("target_issue_status", "INVALID_ENUM", "must be done, review, ready, or blocked")
	}
	if input.ReviewOutcome != nil && !input.ReviewOutcome.Valid() {
		return FinishAttemptInput{}, validationError("review_outcome", "INVALID_ENUM", "is invalid")
	}
	if input.FailureReasonCode != nil && !input.FailureReasonCode.Valid() {
		return FinishAttemptInput{}, validationError("failure_reason_code", "INVALID_ENUM", "is invalid")
	}
	if input.InterruptionReasonCode != nil && !input.InterruptionReasonCode.Valid() {
		return FinishAttemptInput{}, validationError("interruption_reason_code", "INVALID_ENUM", "is invalid")
	}
	if input.BlockedReason != nil {
		if err := ValidateText("blocked_reason", *input.BlockedReason, MaxAttemptNoteRunes); err != nil {
			return FinishAttemptInput{}, err
		}
	}
	if input.ReasonDetails != nil {
		if err := ValidateText("reason_details", *input.ReasonDetails, MaxAttemptNoteRunes); err != nil {
			return FinishAttemptInput{}, err
		}
	}
	if input.Outcome == AttemptOutcomeFailed {
		if input.FailureReasonCode == nil {
			return FinishAttemptInput{}, validationError("failure_reason_code", "REQUIRED", "is required")
		}
		if input.TargetIssueStatus != nil {
			return FinishAttemptInput{}, validationError("target_issue_status", "FORBIDDEN", "is not allowed for failed attempts")
		}
		if input.ReviewOutcome != nil {
			return FinishAttemptInput{}, validationError("review_outcome", "FORBIDDEN", "is not allowed for failed attempts")
		}
		if input.BlockedReason != nil {
			return FinishAttemptInput{}, validationError("blocked_reason", "FORBIDDEN", "is not allowed for failed attempts")
		}
		if input.InterruptionReasonCode != nil {
			return FinishAttemptInput{}, validationError("interruption_reason_code", "FORBIDDEN", "is not allowed for failed attempts")
		}
	}
	if input.Outcome == AttemptOutcomeInterrupted {
		if input.InterruptionReasonCode == nil {
			return FinishAttemptInput{}, validationError("interruption_reason_code", "REQUIRED", "is required")
		}
		if input.TargetIssueStatus != nil {
			return FinishAttemptInput{}, validationError("target_issue_status", "FORBIDDEN", "is not allowed for interrupted attempts")
		}
		if input.ReviewOutcome != nil {
			return FinishAttemptInput{}, validationError("review_outcome", "FORBIDDEN", "is not allowed for interrupted attempts")
		}
		if input.BlockedReason != nil {
			return FinishAttemptInput{}, validationError("blocked_reason", "FORBIDDEN", "is not allowed for interrupted attempts")
		}
		if input.FailureReasonCode != nil {
			return FinishAttemptInput{}, validationError("failure_reason_code", "FORBIDDEN", "is not allowed for interrupted attempts")
		}
	}
	if input.AcknowledgedChanges != nil && (input.AcknowledgedChanges.IssueVersion < 1 || input.AcknowledgedChanges.LatestEventID < 0) {
		return FinishAttemptInput{}, validationError("acknowledged_changes", "INVALID_VALUE", "version and event id are out of range")
	}
	normalized := input
	normalized.NextSteps, normalized.Verification = next, verification
	normalized.Artifacts = artifacts
	normalized.TargetIssueStatus = copyFinishStatus(input.TargetIssueStatus)
	normalized.BlockedReason = copyFinishString(input.BlockedReason)
	normalized.ReviewOutcome = copyFinishReview(input.ReviewOutcome)
	normalized.FailureReasonCode = copyFinishFailure(input.FailureReasonCode)
	normalized.InterruptionReasonCode = copyFinishInterruption(input.InterruptionReasonCode)
	normalized.ReasonDetails = copyFinishString(input.ReasonDetails)
	if input.IdempotencyKey != nil {
		if err := ValidateText("idempotency_key", *input.IdempotencyKey, MaxIdempotencyKeyRunes); err != nil {
			return FinishAttemptInput{}, err
		}
		key := strings.TrimSpace(*input.IdempotencyKey)
		if key == "" {
			return FinishAttemptInput{}, validationError("idempotency_key", "REQUIRED", "must not be blank")
		}
		normalized.IdempotencyKey = &key
	}
	if input.AcknowledgedChanges != nil {
		ack := *input.AcknowledgedChanges
		normalized.AcknowledgedChanges = &ack
	}

	normalized.SessionID, err = copyOptionalSessionID(input.SessionID)
	if err != nil {
		return FinishAttemptInput{}, err
	}
	return normalized, nil
}

// CanonicalFinishAttemptRequest returns deterministic JSON for a normalized
// finish request. Transient session identity and generated persistence values
// are intentionally excluded.
func CanonicalFinishAttemptRequest(input FinishAttemptInput) ([]byte, error) {
	type canonicalArtifact struct {
		Type     ArtifactType    `json:"type"`
		URI      string          `json:"uri"`
		Title    *string         `json:"title"`
		Metadata json.RawMessage `json:"metadata"`
	}
	type canonicalAcknowledgement struct {
		IssueVersion  int64 `json:"issue_version"`
		LatestEventID int64 `json:"latest_event_id"`
	}
	request := struct {
		AttemptID              string                    `json:"attempt_id"`
		LeaseToken             string                    `json:"lease_token"`
		Outcome                AttemptOutcome            `json:"outcome"`
		ResultSummary          string                    `json:"result_summary"`
		NextSteps              []string                  `json:"next_steps"`
		Verification           []string                  `json:"verification"`
		TargetIssueStatus      *Status                   `json:"target_issue_status"`
		BlockedReason          *string                   `json:"blocked_reason"`
		ReviewOutcome          *ReviewOutcome            `json:"review_outcome"`
		FailureReasonCode      *FailureReasonCode        `json:"failure_reason_code"`
		InterruptionReasonCode *InterruptionReasonCode   `json:"interruption_reason_code"`
		ReasonDetails          *string                   `json:"reason_details"`
		AcknowledgedChanges    *canonicalAcknowledgement `json:"acknowledged_changes"`
		Artifacts              []canonicalArtifact       `json:"artifacts"`
	}{
		AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, Outcome: input.Outcome,
		ResultSummary: input.ResultSummary, NextSteps: input.NextSteps, Verification: input.Verification,
		TargetIssueStatus: input.TargetIssueStatus, BlockedReason: input.BlockedReason,
		ReviewOutcome: input.ReviewOutcome, FailureReasonCode: input.FailureReasonCode,
		InterruptionReasonCode: input.InterruptionReasonCode, ReasonDetails: input.ReasonDetails,
	}
	if input.AcknowledgedChanges != nil {
		request.AcknowledgedChanges = &canonicalAcknowledgement{
			IssueVersion:  input.AcknowledgedChanges.IssueVersion,
			LatestEventID: input.AcknowledgedChanges.LatestEventID,
		}
	}
	if input.Artifacts != nil {
		request.Artifacts = make([]canonicalArtifact, len(input.Artifacts))
		for index, artifact := range input.Artifacts {
			request.Artifacts[index] = canonicalArtifact{
				Type: artifact.Type, URI: artifact.URI, Title: copyFinishString(artifact.Title),
				Metadata: append(json.RawMessage(nil), artifact.Metadata...),
			}
		}
	}
	return json.Marshal(request)
}

// ValidateFinishAttemptForKind applies the completion shape rules that require persisted attempt kind.
func ValidateFinishAttemptForKind(input FinishAttemptInput, kind AttemptKind) error {
	if _, err := input.Validate(); err != nil {
		return err
	}
	if !kind.Valid() {
		return validationError("kind", "INVALID_ENUM", "is invalid")
	}
	if input.Outcome != AttemptOutcomeCompleted {
		return nil
	}
	if kind == AttemptKindWork {
		if input.TargetIssueStatus == nil {
			return validationError("target_issue_status", "REQUIRED", "is required for work completion")
		}
		if input.ReviewOutcome != nil {
			return validationError("review_outcome", "FORBIDDEN", "is not allowed for work completion")
		}
		if input.FailureReasonCode != nil {
			return validationError("failure_reason_code", "FORBIDDEN", "is not allowed for work completion")
		}
		if input.InterruptionReasonCode != nil {
			return validationError("interruption_reason_code", "FORBIDDEN", "is not allowed for work completion")
		}
		if *input.TargetIssueStatus == StatusBlocked {
			if input.BlockedReason == nil || strings.TrimSpace(*input.BlockedReason) == "" {
				return validationError("blocked_reason", "REQUIRED", "is required")
			}
		} else if input.BlockedReason != nil {
			return validationError("blocked_reason", "FORBIDDEN", "is only allowed for blocked outcomes")
		}
		if input.ReasonDetails != nil && *input.TargetIssueStatus != StatusBlocked {
			return validationError("reason_details", "FORBIDDEN", "is only allowed for blocked outcomes")
		}
		return nil
	}
	if input.ReviewOutcome == nil {
		return validationError("review_outcome", "REQUIRED", "is required for review completion")
	}
	if input.TargetIssueStatus != nil {
		return validationError("target_issue_status", "FORBIDDEN", "is not allowed for review completion")
	}
	if input.FailureReasonCode != nil {
		return validationError("failure_reason_code", "FORBIDDEN", "is not allowed for review completion")
	}
	if input.InterruptionReasonCode != nil {
		return validationError("interruption_reason_code", "FORBIDDEN", "is not allowed for review completion")
	}
	if *input.ReviewOutcome == ReviewOutcomeBlocked {
		if input.BlockedReason == nil || strings.TrimSpace(*input.BlockedReason) == "" {
			return validationError("blocked_reason", "REQUIRED", "is required")
		}
	} else if input.BlockedReason != nil {
		return validationError("blocked_reason", "FORBIDDEN", "is only allowed for blocked outcomes")
	}
	if input.ReasonDetails != nil && *input.ReviewOutcome != ReviewOutcomeBlocked {
		return validationError("reason_details", "FORBIDDEN", "is only allowed for blocked outcomes")
	}
	return nil
}

func ValidateFinishAttempt(input FinishAttemptInput, kind AttemptKind) error {
	return ValidateFinishAttemptForKind(input, kind)
}

func copyFinishString(v *string) *string {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}
func copyFinishStatus(v *Status) *Status {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}
func copyFinishReview(v *ReviewOutcome) *ReviewOutcome {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}
func copyFinishFailure(v *FailureReasonCode) *FailureReasonCode {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}
func copyFinishInterruption(v *InterruptionReasonCode) *InterruptionReasonCode {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}

type AttemptNoteKind string

const (
	AttemptNoteKindProgress   AttemptNoteKind = "progress"
	AttemptNoteKindFinding    AttemptNoteKind = "finding"
	AttemptNoteKindWarning    AttemptNoteKind = "warning"
	AttemptNoteKindCheckpoint AttemptNoteKind = "checkpoint"
)

func (value AttemptNoteKind) Valid() bool {
	switch value {
	case AttemptNoteKindProgress, AttemptNoteKindFinding, AttemptNoteKindWarning, AttemptNoteKindCheckpoint:
		return true
	default:
		return false
	}
}

// AttemptNote is the durable, append-only recovery note associated with one attempt.
type AttemptNote struct {
	ID        string
	AttemptID string
	Kind      AttemptNoteKind
	Content   string
	NextSteps []string
	Important bool
	CreatedAt time.Time
}

type ClaimIssueInput struct {
	IssueID      string
	LeaseSeconds *int
	SessionID    *string
	// Resources is optional (ISSUE-180): when non-empty, claiming the issue
	// and acquiring every listed resource happen in one transaction, so a
	// conflict aborts the claim itself rather than leaving an attempt with
	// no reservations. nil/empty means "claim only," unchanged from before
	// this field existed.
	Resources      []Resource
	IdempotencyKey *string
}

func (input ClaimIssueInput) Validate() (ClaimIssueInput, error) {
	if _, err := ParseIssueIdentifier(input.IssueID); err != nil {
		return ClaimIssueInput{}, err
	}
	lease, err := validateLeaseSeconds(input.LeaseSeconds)
	if err != nil {
		return ClaimIssueInput{}, err
	}
	sessionID, err := copyOptionalSessionID(input.SessionID)
	if err != nil {
		return ClaimIssueInput{}, err
	}
	var resources []Resource
	if len(input.Resources) > 0 {
		// PrepareReservationRequest's result is discarded here: it exists to
		// surface a malformed or internally-overlapping resource list as an
		// ordinary validation error before any repository call, matching
		// domain.ValidateArtifactInputs' role for SaveAttemptNoteInput. The
		// repository re-normalizes from the raw list regardless (every write
		// command re-validates inline; see ports.CreateReviewRequestCommand's
		// Purposes convention), so nothing is lost by not threading the
		// prepared/normalized form through.
		if _, err := PrepareReservationRequest(input.Resources); err != nil {
			return ClaimIssueInput{}, err
		}
		resources = append([]Resource(nil), input.Resources...)
	}
	var idempotencyKey *string
	if input.IdempotencyKey != nil {
		if err := ValidateText("idempotency_key", *input.IdempotencyKey, MaxIdempotencyKeyRunes); err != nil {
			return ClaimIssueInput{}, err
		}
		key := strings.TrimSpace(*input.IdempotencyKey)
		if key == "" {
			return ClaimIssueInput{}, validationError("idempotency_key", "REQUIRED", "must not be blank")
		}
		idempotencyKey = &key
	}
	return ClaimIssueInput{
		IssueID: input.IssueID, LeaseSeconds: lease, SessionID: sessionID,
		Resources: resources, IdempotencyKey: idempotencyKey,
	}, nil
}

// CanonicalClaimIssueRequest returns deterministic JSON for a normalized claim
// request. The idempotency key, transient session identity, and generated
// lease values are intentionally excluded. Resources are included (ISSUE-180):
// two claims against the same issue that request different resource sets are
// different requests, matching CanonicalSaveAttemptNoteRequest's inclusion of
// every caller-supplied mutation field.
func CanonicalClaimIssueRequest(input ClaimIssueInput) ([]byte, error) {
	resources, err := canonicalizeReservationResources(input.Resources)
	if err != nil {
		return nil, err
	}
	request := struct {
		IssueID      string                         `json:"issue_id"`
		LeaseSeconds int                            `json:"lease_seconds"`
		Resources    []canonicalReservationResource `json:"resources,omitempty"`
	}{IssueID: input.IssueID, LeaseSeconds: *input.LeaseSeconds, Resources: resources}
	return json.Marshal(request)
}

type RenewAttemptInput struct {
	AttemptID    string
	LeaseToken   string
	LeaseSeconds *int
	SessionID    *string
}

func (input RenewAttemptInput) Validate() (RenewAttemptInput, error) {
	if _, err := ulid.ParseStrict(input.AttemptID); err != nil || len(input.AttemptID) != 26 {
		return RenewAttemptInput{}, validationError("attempt_id", "INVALID_ULID", "must be a canonical ULID")
	}
	if strings.TrimSpace(input.LeaseToken) == "" {
		return RenewAttemptInput{}, validationError("lease_token", "REQUIRED", "is required")
	}
	if err := ValidateText("lease_token", input.LeaseToken, MaxLeaseTokenRunes); err != nil {
		return RenewAttemptInput{}, err
	}
	lease, err := validateLeaseSeconds(input.LeaseSeconds)
	if err != nil {
		return RenewAttemptInput{}, err
	}
	sessionID, err := copyOptionalSessionID(input.SessionID)
	if err != nil {
		return RenewAttemptInput{}, err
	}
	return RenewAttemptInput{AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, LeaseSeconds: lease, SessionID: sessionID}, nil
}

type SaveAttemptNoteInput struct {
	AttemptID      string
	LeaseToken     string
	SessionID      *string
	Kind           AttemptNoteKind
	Content        string
	NextSteps      []string
	Important      bool
	Artifacts      []ArtifactInput
	IdempotencyKey *string
}

func (input SaveAttemptNoteInput) Validate() (SaveAttemptNoteInput, error) {
	attemptID, err := ulid.ParseStrict(input.AttemptID)
	if err != nil || len(input.AttemptID) != 26 || attemptID.String() != input.AttemptID {
		return SaveAttemptNoteInput{}, validationError("attempt_id", "INVALID_ULID", "must be a canonical ULID")
	}
	if strings.TrimSpace(input.LeaseToken) == "" {
		return SaveAttemptNoteInput{}, validationError("lease_token", "REQUIRED", "is required")
	}
	if err := ValidateText("lease_token", input.LeaseToken, MaxLeaseTokenRunes); err != nil {
		return SaveAttemptNoteInput{}, err
	}
	if !input.Kind.Valid() {
		return SaveAttemptNoteInput{}, validationError("kind", "INVALID_ENUM", "must be progress, finding, warning, or checkpoint")
	}
	if strings.TrimSpace(input.Content) == "" {
		return SaveAttemptNoteInput{}, validationError("content", "REQUIRED", "is required")
	}
	if err := ValidateText("content", input.Content, MaxAttemptNoteRunes); err != nil {
		return SaveAttemptNoteInput{}, err
	}
	nextSteps, err := CopyBounded("next_steps", input.NextSteps, MaxAttemptNoteNextSteps)
	if err != nil {
		return SaveAttemptNoteInput{}, err
	}
	for _, nextStep := range nextSteps {
		field := "next_steps"
		if strings.TrimSpace(nextStep) == "" {
			return SaveAttemptNoteInput{}, validationError(field, "REQUIRED", "items must be nonblank")
		}
		if err := ValidateText(field, nextStep, MaxAttemptNoteNextStepRunes); err != nil {
			return SaveAttemptNoteInput{}, err
		}
	}
	artifacts, err := ValidateArtifactInputs("artifacts", input.Artifacts)
	if err != nil {
		return SaveAttemptNoteInput{}, err
	}
	sessionID, err := copyOptionalSessionID(input.SessionID)
	if err != nil {
		return SaveAttemptNoteInput{}, err
	}
	var idempotencyKey *string
	if input.IdempotencyKey != nil {
		if err := ValidateText("idempotency_key", *input.IdempotencyKey, MaxIdempotencyKeyRunes); err != nil {
			return SaveAttemptNoteInput{}, err
		}
		key := strings.TrimSpace(*input.IdempotencyKey)
		if key == "" {
			return SaveAttemptNoteInput{}, validationError("idempotency_key", "REQUIRED", "must not be blank")
		}
		idempotencyKey = &key
	}
	return SaveAttemptNoteInput{
		AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, SessionID: sessionID, Kind: input.Kind, Content: input.Content,
		NextSteps: nextSteps, Important: input.Important, Artifacts: artifacts, IdempotencyKey: idempotencyKey,
	}, nil
}

// CanonicalSaveAttemptNoteRequest returns deterministic JSON for a normalized
// save-attempt-note request. The lease-token proof and caller artifact fields
// are included; the idempotency key and transient session identity are
// intentionally excluded.
func CanonicalSaveAttemptNoteRequest(input SaveAttemptNoteInput) ([]byte, error) {
	type canonicalArtifact struct {
		Type     ArtifactType    `json:"type"`
		URI      string          `json:"uri"`
		Title    *string         `json:"title"`
		Metadata json.RawMessage `json:"metadata"`
	}
	request := struct {
		AttemptID  string              `json:"attempt_id"`
		LeaseToken string              `json:"lease_token"`
		Kind       AttemptNoteKind     `json:"kind"`
		Content    string              `json:"content"`
		NextSteps  []string            `json:"next_steps"`
		Important  bool                `json:"important"`
		Artifacts  []canonicalArtifact `json:"artifacts"`
	}{
		AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, Kind: input.Kind,
		Content: input.Content, NextSteps: input.NextSteps, Important: input.Important,
	}
	if input.Artifacts != nil {
		request.Artifacts = make([]canonicalArtifact, len(input.Artifacts))
		for index, artifact := range input.Artifacts {
			request.Artifacts[index] = canonicalArtifact{
				Type: artifact.Type, URI: artifact.URI, Title: copyFinishString(artifact.Title),
				Metadata: append(json.RawMessage(nil), artifact.Metadata...),
			}
		}
	}
	return json.Marshal(request)
}

// MaxReleaseResourceIDs bounds the reservation_ids list on
// ReleaseResourcesInput, matching MaxReservationResources since one release
// call can name at most as many reservations as one acquisition could have
// created.
const MaxReleaseResourceIDs = MaxReservationResources

// ReserveResourcesInput requests an all-or-nothing addition of resources to
// one active work attempt (ISSUE-180's locked API: reserve_resources).
type ReserveResourcesInput struct {
	AttemptID      string
	LeaseToken     string
	SessionID      *string
	Resources      []Resource
	IdempotencyKey *string
}

func (input ReserveResourcesInput) Validate() (ReserveResourcesInput, error) {
	attemptID, err := ulid.ParseStrict(input.AttemptID)
	if err != nil || len(input.AttemptID) != 26 || attemptID.String() != input.AttemptID {
		return ReserveResourcesInput{}, validationError("attempt_id", "INVALID_ULID", "must be a canonical ULID")
	}
	if strings.TrimSpace(input.LeaseToken) == "" {
		return ReserveResourcesInput{}, validationError("lease_token", "REQUIRED", "is required")
	}
	if err := ValidateText("lease_token", input.LeaseToken, MaxLeaseTokenRunes); err != nil {
		return ReserveResourcesInput{}, err
	}
	// PrepareReservationRequest also rejects an empty list -- reserve_resources
	// always adds at least one resource, matching its "adds a bounded
	// all-or-nothing set" contract; release_resources' empty-means-all-active
	// convention does not apply here.
	if _, err := PrepareReservationRequest(input.Resources); err != nil {
		return ReserveResourcesInput{}, err
	}
	sessionID, err := copyOptionalSessionID(input.SessionID)
	if err != nil {
		return ReserveResourcesInput{}, err
	}
	var idempotencyKey *string
	if input.IdempotencyKey != nil {
		if err := ValidateText("idempotency_key", *input.IdempotencyKey, MaxIdempotencyKeyRunes); err != nil {
			return ReserveResourcesInput{}, err
		}
		key := strings.TrimSpace(*input.IdempotencyKey)
		if key == "" {
			return ReserveResourcesInput{}, validationError("idempotency_key", "REQUIRED", "must not be blank")
		}
		idempotencyKey = &key
	}
	return ReserveResourcesInput{
		AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, SessionID: sessionID,
		Resources: append([]Resource(nil), input.Resources...), IdempotencyKey: idempotencyKey,
	}, nil
}

// CanonicalReserveResourcesRequest returns deterministic JSON for a
// normalized reserve-resources request. The lease-token proof is included,
// matching CanonicalSaveAttemptNoteRequest's convention for every
// lease-authenticated attempt mutation; the idempotency key and transient
// session identity are excluded.
func CanonicalReserveResourcesRequest(input ReserveResourcesInput) ([]byte, error) {
	resources, err := canonicalizeReservationResources(input.Resources)
	if err != nil {
		return nil, err
	}
	request := struct {
		AttemptID  string                         `json:"attempt_id"`
		LeaseToken string                         `json:"lease_token"`
		Resources  []canonicalReservationResource `json:"resources"`
	}{AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, Resources: resources}
	return json.Marshal(request)
}

// ReleaseResourcesInput requests release of specific reservations owned by
// one active work attempt, or -- when ReservationIDs is empty -- every
// active reservation it currently owns (ISSUE-180's locked API:
// release_resources).
type ReleaseResourcesInput struct {
	AttemptID      string
	LeaseToken     string
	SessionID      *string
	ReservationIDs []string
	IdempotencyKey *string
}

func (input ReleaseResourcesInput) Validate() (ReleaseResourcesInput, error) {
	attemptID, err := ulid.ParseStrict(input.AttemptID)
	if err != nil || len(input.AttemptID) != 26 || attemptID.String() != input.AttemptID {
		return ReleaseResourcesInput{}, validationError("attempt_id", "INVALID_ULID", "must be a canonical ULID")
	}
	if strings.TrimSpace(input.LeaseToken) == "" {
		return ReleaseResourcesInput{}, validationError("lease_token", "REQUIRED", "is required")
	}
	if err := ValidateText("lease_token", input.LeaseToken, MaxLeaseTokenRunes); err != nil {
		return ReleaseResourcesInput{}, err
	}
	reservationIDs, err := CopyBounded("reservation_ids", input.ReservationIDs, MaxReleaseResourceIDs)
	if err != nil {
		return ReleaseResourcesInput{}, err
	}
	seen := make(map[string]bool, len(reservationIDs))
	for index, id := range reservationIDs {
		parsed, parseErr := ulid.ParseStrict(id)
		if parseErr != nil || len(id) != 26 || parsed.String() != id {
			idx := index
			return ReleaseResourcesInput{}, NewError(CodeInvalidArgument, fmt.Sprintf("reservation id %q is not a canonical ULID", id), false,
				Detail{EntityIndex: &idx, Field: "reservation_ids", Code: "INVALID_ULID"})
		}
		if seen[id] {
			idx := index
			return ReleaseResourcesInput{}, NewError(CodeInvalidArgument, fmt.Sprintf("reservation id %q is repeated", id), false,
				Detail{EntityIndex: &idx, Field: "reservation_ids", Code: "DUPLICATE"})
		}
		seen[id] = true
	}
	sessionID, err := copyOptionalSessionID(input.SessionID)
	if err != nil {
		return ReleaseResourcesInput{}, err
	}
	var idempotencyKey *string
	if input.IdempotencyKey != nil {
		if err := ValidateText("idempotency_key", *input.IdempotencyKey, MaxIdempotencyKeyRunes); err != nil {
			return ReleaseResourcesInput{}, err
		}
		key := strings.TrimSpace(*input.IdempotencyKey)
		if key == "" {
			return ReleaseResourcesInput{}, validationError("idempotency_key", "REQUIRED", "must not be blank")
		}
		idempotencyKey = &key
	}
	return ReleaseResourcesInput{
		AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, SessionID: sessionID,
		ReservationIDs: reservationIDs, IdempotencyKey: idempotencyKey,
	}, nil
}

// CanonicalReleaseResourcesRequest returns deterministic JSON for a
// normalized release-resources request, following
// CanonicalReserveResourcesRequest's same lease-proof-included convention.
func CanonicalReleaseResourcesRequest(input ReleaseResourcesInput) ([]byte, error) {
	request := struct {
		AttemptID      string   `json:"attempt_id"`
		LeaseToken     string   `json:"lease_token"`
		ReservationIDs []string `json:"reservation_ids"`
	}{AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, ReservationIDs: input.ReservationIDs}
	return json.Marshal(request)
}

// canonicalReservationResource is the shared canonical-JSON shape for one
// requested resource, used by both CanonicalClaimIssueRequest and
// CanonicalReserveResourcesRequest.
type canonicalReservationResource struct {
	Kind      ResourceKind `json:"kind"`
	Path      string       `json:"path,omitempty"`
	Namespace string       `json:"namespace,omitempty"`
	Name      string       `json:"name,omitempty"`
}

// canonicalizeReservationResources normalizes every resource (Normalize's
// redundant "." and empty path-segment removal; trimmed logical name) before
// it enters an idempotency hash, per ISSUE-180's locked instruction that
// "idempotency request hashes include normalized resources": two requests
// that name the same resource with different but equivalent spelling (e.g.
// "./src/foo.go" vs "src/foo.go") must hash identically. Case is preserved
// (Display, not the ASCII-folded Key, used for path kinds) since folding
// case here would instead make two genuinely different resources on a
// case-sensitive filesystem collide onto the same idempotency hash. A nil
// or empty input returns a nil slice so a plain claim_issue request with no
// resources keeps hashing exactly as it did before this field existed.
func canonicalizeReservationResources(resources []Resource) ([]canonicalReservationResource, error) {
	if len(resources) == 0 {
		return nil, nil
	}
	canonical := make([]canonicalReservationResource, len(resources))
	for index, resource := range resources {
		normalized, err := Normalize(resource)
		if err != nil {
			return nil, wrapResourceRequestError(index, err)
		}
		if normalized.Kind() == ResourceKindLogical {
			canonical[index] = canonicalReservationResource{
				Kind: normalized.Kind(), Namespace: normalized.Namespace(), Name: normalized.Name(),
			}
			continue
		}
		canonical[index] = canonicalReservationResource{Kind: normalized.Kind(), Path: normalized.Display()}
	}
	return canonical, nil
}

func copyOptionalSessionID(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := ulid.ParseStrict(*value)
	if err != nil || len(*value) != 26 || parsed.String() != *value {
		return nil, validationError("session_id", "INVALID_ULID", "must be a canonical ULID")
	}
	copied := *value
	return &copied, nil
}

func validateLeaseSeconds(value *int) (*int, error) {
	seconds := DefaultLeaseSeconds
	if value != nil {
		seconds = *value
	}
	if seconds < MinLeaseSeconds || seconds > MaxLeaseSeconds {
		return nil, validationError("lease_seconds", "OUT_OF_RANGE", "must be between 60 and 3600")
	}
	return &seconds, nil
}
