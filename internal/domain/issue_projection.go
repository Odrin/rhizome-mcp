package domain

import "time"

// Issue is the persisted current projection of an issue.
type Issue struct {
	ID                  string
	DisplayID           string
	SequenceNo          int64
	Type                Type
	Title               string
	Description         *string
	AcceptanceCriteria  *string
	Status              Status
	Priority            Priority
	ParentID            *string
	BlockedReason       *string
	Version             int64
	CreatedBySessionID  *string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	ClosedAt            *time.Time
	ArchivedAt          *time.Time
	ArchivedBySessionID *string
}
