package domain

import "time"

// Project is the database-backed metadata for the current project.
//
// Name and Instructions are nil when the corresponding nullable database
// columns are unset. LatestEventID is zero when the project has no issue
// events.
type Project struct {
	ID              string
	Name            *string
	Instructions    *string
	NextIssueNumber int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	SchemaVersion   int
	LatestEventID   int64
}
