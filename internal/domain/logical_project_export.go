package domain

import (
	"encoding/json"
)

// LogicalProjectDocument is the version 1 logical project interchange document.
type LogicalProjectDocument struct {
	Format       string                `json:"format"`
	Version      int                   `json:"version"`
	ExportedAt   string                `json:"exported_at"`
	Project      LogicalProjectProject `json:"project"`
	Issues       []LogicalIssue        `json:"issues"`
	Labels       []LogicalLabel        `json:"labels"`
	IssueLabels  []LogicalIssueLabel   `json:"issue_labels"`
	Relations    []LogicalRelation     `json:"relations"`
	Comments     []LogicalComment      `json:"comments"`
	Decisions    []LogicalDecision     `json:"decisions"`
	Attempts     []LogicalAttempt      `json:"attempts"`
	AttemptNotes []LogicalAttemptNote  `json:"attempt_notes"`
	Artifacts    []LogicalArtifact     `json:"artifacts"`
	Events       []LogicalEvent        `json:"events"`
}

// LogicalProjectProject is the exported project metadata record.
type LogicalProjectProject struct {
	ID           string  `json:"id"`
	Name         *string `json:"name"`
	Instructions *string `json:"instructions"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// LogicalIssue is the exported issue record.
type LogicalIssue struct {
	ID                 string  `json:"id"`
	Type               string  `json:"type"`
	Title              string  `json:"title"`
	Description        *string `json:"description"`
	AcceptanceCriteria *string `json:"acceptance_criteria"`
	Status             string  `json:"status"`
	Priority           string  `json:"priority"`
	ParentID           *string `json:"parent_id"`
	BlockedReason      *string `json:"blocked_reason"`
	CreatedBySessionID *string `json:"created_by_session_id"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
	ClosedAt           *string `json:"closed_at"`
}

// LogicalLabel is the exported label record.
type LogicalLabel struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	CreatedAt   string  `json:"created_at"`
}

// LogicalIssueLabel is the exported issue-label link record.
type LogicalIssueLabel struct {
	IssueID string `json:"issue_id"`
	LabelID string `json:"label_id"`
}

// LogicalRelation is the exported relation record.
type LogicalRelation struct {
	ID                 string  `json:"id"`
	SourceIssueID      string  `json:"source_issue_id"`
	TargetIssueID      string  `json:"target_issue_id"`
	Type               string  `json:"type"`
	CreatedBySessionID *string `json:"created_by_session_id"`
	CreatedAt          string  `json:"created_at"`
}

// LogicalComment is the exported comment record.
type LogicalComment struct {
	ID                 string  `json:"id"`
	IssueID            string  `json:"issue_id"`
	Content            string  `json:"content"`
	CreatedBySessionID *string `json:"created_by_session_id"`
	AuthorLabel        *string `json:"author_label"`
	CreatedAt          string  `json:"created_at"`
	EditedAt           *string `json:"edited_at"`
}

// LogicalDecision is the exported decision record.
type LogicalDecision struct {
	ID                 string  `json:"id"`
	IssueID            *string `json:"issue_id"`
	Title              string  `json:"title"`
	Summary            string  `json:"summary"`
	Content            string  `json:"content"`
	Status             string  `json:"status"`
	SupersedesID       *string `json:"supersedes_id"`
	CreatedBySessionID *string `json:"created_by_session_id"`
	CreatedAt          string  `json:"created_at"`
}

// LogicalAttempt is the exported attempt record.
type LogicalAttempt struct {
	ID                     string   `json:"id"`
	IssueID                string   `json:"issue_id"`
	SessionID              *string  `json:"session_id"`
	AgentLabel             *string  `json:"agent_label"`
	Kind                   string   `json:"kind"`
	Status                 string   `json:"status"`
	IssueVersionAtStart    int64    `json:"issue_version_at_start"`
	ContextEventIDAtStart  int64    `json:"context_event_id_at_start"`
	LeaseExpiresAt         string   `json:"lease_expires_at"`
	StartedAt              string   `json:"started_at"`
	LastHeartbeatAt        string   `json:"last_heartbeat_at"`
	FinishedAt             *string  `json:"finished_at"`
	ResultSummary          *string  `json:"result_summary"`
	NextSteps              []string `json:"next_steps"`
	Verification           []string `json:"verification"`
	FailureReasonCode      *string  `json:"failure_reason_code"`
	InterruptionReasonCode *string  `json:"interruption_reason_code"`
	ReasonDetails          *string  `json:"reason_details"`
}

// LogicalAttemptNote is the exported attempt note record.
type LogicalAttemptNote struct {
	ID        string   `json:"id"`
	AttemptID string   `json:"attempt_id"`
	Kind      string   `json:"kind"`
	Content   string   `json:"content"`
	NextSteps []string `json:"next_steps"`
	Important bool     `json:"important"`
	CreatedAt string   `json:"created_at"`
}

// LogicalArtifact is the exported artifact record.
type LogicalArtifact struct {
	ID        string          `json:"id"`
	IssueID   string          `json:"issue_id"`
	AttemptID *string         `json:"attempt_id"`
	Type      string          `json:"type"`
	URI       string          `json:"uri"`
	Title     *string         `json:"title"`
	Metadata  json.RawMessage `json:"metadata"`
	CreatedAt string          `json:"created_at"`
}

// LogicalEvent is the exported issue event record.
type LogicalEvent struct {
	SourceID  int64           `json:"source_id"`
	IssueID   *string         `json:"issue_id"`
	EventType string          `json:"event_type"`
	SessionID *string         `json:"session_id"`
	AttemptID *string         `json:"attempt_id"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

// MarshalLogicalProjectDocument renders a logical project document with stable JSON formatting.
func MarshalLogicalProjectDocument(document LogicalProjectDocument) ([]byte, error) {
	normalized := document
	if normalized.Issues == nil {
		normalized.Issues = []LogicalIssue{}
	}
	if normalized.Labels == nil {
		normalized.Labels = []LogicalLabel{}
	}
	if normalized.IssueLabels == nil {
		normalized.IssueLabels = []LogicalIssueLabel{}
	}
	if normalized.Relations == nil {
		normalized.Relations = []LogicalRelation{}
	}
	if normalized.Comments == nil {
		normalized.Comments = []LogicalComment{}
	}
	if normalized.Decisions == nil {
		normalized.Decisions = []LogicalDecision{}
	}
	if normalized.Attempts == nil {
		normalized.Attempts = []LogicalAttempt{}
	}
	if normalized.AttemptNotes == nil {
		normalized.AttemptNotes = []LogicalAttemptNote{}
	}
	if normalized.Artifacts == nil {
		normalized.Artifacts = []LogicalArtifact{}
	}
	if normalized.Events == nil {
		normalized.Events = []LogicalEvent{}
	}
	return json.MarshalIndent(normalized, "", "  ")
}
