// Package domain contains pure rhizome business primitives.
package domain

import (
	"fmt"
	"strings"
)

// Type is an issue's execution and hierarchy category.
type Type string

const (
	// TypeEpic is a non-executable grouping issue with no parent.
	TypeEpic Type = "epic"
	// TypeTask is an executable task that may belong to an epic.
	TypeTask Type = "task"
	// TypeBug is an executable defect that may belong to an epic.
	TypeBug Type = "bug"
)

// ParseType parses a supported issue type.
func ParseType(value string) (Type, error) {
	parsed := Type(value)
	if !parsed.Valid() {
		return "", invalidEnum("type", value)
	}
	return parsed, nil
}

// Valid reports whether t is a supported issue type.
func (t Type) Valid() bool {
	switch t {
	case TypeEpic, TypeTask, TypeBug:
		return true
	default:
		return false
	}
}

// Status is an issue status persisted in storage.
type Status string

const (
	// StatusOpen means the issue is not yet ready to execute.
	StatusOpen Status = "open"
	// StatusReady means the issue is available for an attempt if otherwise claimable.
	StatusReady Status = "ready"
	// StatusBlocked means an external condition manually blocks the issue.
	StatusBlocked Status = "blocked"
	// StatusReview means implementation is available for a review attempt.
	StatusReview Status = "review"
	// StatusDone means the issue is completed.
	StatusDone Status = "done"
	// StatusCancelled means the issue is no longer required.
	StatusCancelled Status = "cancelled"
)

// ParseStatus parses a stored status. It deliberately rejects in_progress.
func ParseStatus(value string) (Status, error) {
	parsed := Status(value)
	if !parsed.Valid() {
		return "", invalidEnum("status", value)
	}
	return parsed, nil
}

// Valid reports whether s is a supported stored status.
func (s Status) Valid() bool {
	switch s {
	case StatusOpen, StatusReady, StatusBlocked, StatusReview, StatusDone, StatusCancelled:
		return true
	default:
		return false
	}
}

// Terminal reports whether s ends ordinary issue execution.
func (s Status) Terminal() bool {
	return s == StatusDone || s == StatusCancelled
}

// EffectiveStatus is the externally observed status, including derived work state.
type EffectiveStatus string

const (
	// EffectiveStatusOpen corresponds to stored open.
	EffectiveStatusOpen EffectiveStatus = "open"
	// EffectiveStatusReady corresponds to stored ready.
	EffectiveStatusReady EffectiveStatus = "ready"
	// EffectiveStatusBlocked corresponds to stored blocked.
	EffectiveStatusBlocked EffectiveStatus = "blocked"
	// EffectiveStatusReview corresponds to stored review.
	EffectiveStatusReview EffectiveStatus = "review"
	// EffectiveStatusDone corresponds to stored done.
	EffectiveStatusDone EffectiveStatus = "done"
	// EffectiveStatusCancelled corresponds to stored cancelled.
	EffectiveStatusCancelled EffectiveStatus = "cancelled"
	// EffectiveStatusInProgress is derived from an active, unexpired work attempt.
	EffectiveStatusInProgress EffectiveStatus = "in_progress"
)

// ParseEffectiveStatus parses a supported effective status.
func ParseEffectiveStatus(value string) (EffectiveStatus, error) {
	parsed := EffectiveStatus(value)
	if !parsed.Valid() {
		return "", invalidEnum("effective_status", value)
	}
	return parsed, nil
}

// Valid reports whether s is a supported effective status.
func (s EffectiveStatus) Valid() bool {
	switch s {
	case EffectiveStatusOpen, EffectiveStatusReady, EffectiveStatusBlocked,
		EffectiveStatusReview, EffectiveStatusDone, EffectiveStatusCancelled,
		EffectiveStatusInProgress:
		return true
	default:
		return false
	}
}

// EffectiveStatusFor derives an effective status from valid stored state.
func EffectiveStatusFor(stored Status, hasActiveAttempt bool) (EffectiveStatus, error) {
	if !stored.Valid() {
		return "", invalidEnum("status", string(stored))
	}
	if hasActiveAttempt {
		return EffectiveStatusInProgress, nil
	}
	return EffectiveStatus(stored), nil
}

// Priority is an issue's urgency classification.
type Priority string

const (
	// PriorityLow is below ordinary urgency.
	PriorityLow Priority = "low"
	// PriorityMedium is ordinary urgency.
	PriorityMedium Priority = "medium"
	// PriorityHigh is elevated urgency.
	PriorityHigh Priority = "high"
	// PriorityCritical is the highest urgency.
	PriorityCritical Priority = "critical"
)

// ParsePriority parses a supported issue priority.
func ParsePriority(value string) (Priority, error) {
	parsed := Priority(value)
	if !parsed.Valid() {
		return "", invalidEnum("priority", value)
	}
	return parsed, nil
}

// Valid reports whether p is a supported priority.
func (p Priority) Valid() bool {
	switch p {
	case PriorityLow, PriorityMedium, PriorityHigh, PriorityCritical:
		return true
	default:
		return false
	}
}

// CanTransition reports whether the stored status transition is allowed.
func CanTransition(from, to Status) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	switch from {
	case StatusOpen:
		return to == StatusReady || to == StatusCancelled
	case StatusReady:
		return to == StatusBlocked || to == StatusReview || to == StatusDone || to == StatusCancelled
	case StatusBlocked:
		return to == StatusReady || to == StatusCancelled
	case StatusReview:
		return to == StatusReady || to == StatusBlocked || to == StatusDone || to == StatusCancelled
	case StatusDone:
		return to == StatusReady
	case StatusCancelled:
		return to == StatusOpen
	default:
		return false
	}
}

// ApplyStatusTransition validates a transition and returns the blocked reason to
// persist. Entering blocked requires a non-blank reason; every other target
// clears the reason.
func ApplyStatusTransition(from, to Status, blockedReason string) (string, error) {
	if !CanTransition(from, to) {
		return "", NewError(
			CodeInvalidTransition,
			fmt.Sprintf("cannot transition issue status from %q to %q", from, to),
			false,
			Detail{Field: "status", Code: CodeInvalidTransition},
		)
	}
	if to == StatusBlocked {
		if strings.TrimSpace(blockedReason) == "" {
			return "", NewError(
				CodeInvalidArgument,
				"blocked_reason is required when status is blocked",
				false,
				Detail{Field: "blocked_reason", Code: "REQUIRED"},
			)
		}
		return blockedReason, nil
	}
	return "", nil
}

func invalidEnum(field, value string) *Error {
	return NewError(
		CodeInvalidArgument,
		fmt.Sprintf("unsupported %s %q", field, value),
		false,
		Detail{Field: field, Code: "INVALID_ENUM", Message: value},
	)
}
