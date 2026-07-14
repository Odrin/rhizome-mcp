package domain

import (
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

// WorkAttempt is the durable attempt record. It intentionally excludes the raw
// lease token; only its hash is persisted.
type WorkAttempt struct {
	ID                    string
	IssueID               string
	SessionID             *string
	AgentLabel            *string
	Kind                  AttemptKind
	Status                AttemptStatus
	IssueVersionAtStart   int64
	ContextEventIDAtStart int64
	LeaseExpiresAt        time.Time
	StartedAt             time.Time
	LastHeartbeatAt       time.Time
	FinishedAt            *time.Time
}

type ClaimIssueInput struct {
	IssueID      string
	LeaseSeconds *int
}

func (input ClaimIssueInput) Validate() (ClaimIssueInput, error) {
	if _, err := ParseIssueIdentifier(input.IssueID); err != nil {
		return ClaimIssueInput{}, err
	}
	lease, err := validateLeaseSeconds(input.LeaseSeconds)
	if err != nil {
		return ClaimIssueInput{}, err
	}
	return ClaimIssueInput{IssueID: input.IssueID, LeaseSeconds: lease}, nil
}

type RenewAttemptInput struct {
	AttemptID    string
	LeaseToken   string
	LeaseSeconds *int
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
	return RenewAttemptInput{AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, LeaseSeconds: lease}, nil
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
