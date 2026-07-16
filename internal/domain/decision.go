package domain

import (
	"strings"
	"time"

	"rhizome-mcp/internal/ids"
)

// DecisionStatus is the lifecycle state of an immutable decision record.
type DecisionStatus string

const (
	DecisionStatusActive     DecisionStatus = "active"
	DecisionStatusSuperseded DecisionStatus = "superseded"
	DecisionStatusRejected   DecisionStatus = "rejected"
)

func (status DecisionStatus) Valid() bool {
	return status == DecisionStatusActive || status == DecisionStatusSuperseded || status == DecisionStatusRejected
}

// Decision is an append-only project- or issue-level decision.
type Decision struct {
	ID                 string         `json:"id"`
	IssueID            *string        `json:"issue_id"`
	Title              string         `json:"title"`
	Summary            string         `json:"summary"`
	Content            string         `json:"content"`
	Status             DecisionStatus `json:"status"`
	SupersedesID       *string        `json:"supersedes_id"`
	CreatedBySessionID *string        `json:"created_by_session_id"`
	CreatedAt          time.Time      `json:"created_at"`
}

type RecordDecisionInput struct {
	IssueID      *string
	Title        string
	Summary      string
	Content      string
	Status       DecisionStatus
	SupersedesID *string
	SessionID    *string
}

type RecordDecisionResult struct {
	Decision             Decision
	SupersededDecisionID *string
}

type ListDecisionsInput struct {
	IssueID *string
	Limit   int
	Cursor  string
}

func (input ListDecisionsInput) Validate() (ListDecisionsInput, error) {
	if input.Limit < 0 || input.Limit > 100 {
		return ListDecisionsInput{}, validationError("limit", "OUT_OF_RANGE", "must be 0 (default) or between 1 and 100")
	}
	issueID, err := normalizeOptionalSearchIssueID("issue_id", input.IssueID)
	if err != nil {
		return ListDecisionsInput{}, err
	}
	if input.Cursor != "" {
		if err := ValidateText("cursor", input.Cursor, 4096); err != nil {
			return ListDecisionsInput{}, err
		}
	}
	limit := input.Limit
	if limit == 0 {
		limit = 20
	}
	return ListDecisionsInput{IssueID: issueID, Limit: limit, Cursor: input.Cursor}, nil
}

type DecisionList struct {
	Items      []Decision
	NextCursor *string
	HasMore    bool
}

func (input RecordDecisionInput) Validate() (RecordDecisionInput, error) {
	var issueID *string
	if input.IssueID != nil {
		identifier, err := ParseIssueIdentifier(*input.IssueID)
		if err != nil {
			return RecordDecisionInput{}, err
		}
		issueID = &identifier.Value
	}
	if err := ValidateText("title", input.Title, MaxTitleRunes); err != nil {
		return RecordDecisionInput{}, err
	}
	if strings.TrimSpace(input.Title) == "" {
		return RecordDecisionInput{}, validationError("title", "REQUIRED", "is required")
	}
	if err := ValidateText("summary", input.Summary, MaxDecisionSummaryRunes); err != nil {
		return RecordDecisionInput{}, err
	}
	if strings.TrimSpace(input.Summary) == "" {
		return RecordDecisionInput{}, validationError("summary", "REQUIRED", "is required")
	}
	if err := ValidateText("content", input.Content, MaxDecisionContentRunes); err != nil {
		return RecordDecisionInput{}, err
	}
	status := input.Status
	if status == "" {
		status = DecisionStatusActive
	}
	if !status.Valid() {
		return RecordDecisionInput{}, validationError("status", "INVALID_VALUE", "is invalid")
	}
	var supersedesID *string
	if input.SupersedesID != nil {
		if _, err := ids.ParseStrict(*input.SupersedesID); err != nil {
			return RecordDecisionInput{}, validationError("supersedes_id", "INVALID_ULID", "must be a canonical ULID")
		}
		value := *input.SupersedesID
		supersedesID = &value
		if status != DecisionStatusActive {
			return RecordDecisionInput{}, validationError("supersedes_id", "INVALID_SHAPE", "requires active status")
		}
	}
	sessionID, err := copyOptionalSessionID(input.SessionID)
	if err != nil {
		return RecordDecisionInput{}, err
	}
	return RecordDecisionInput{
		IssueID: issueID, Title: input.Title, Summary: input.Summary, Content: input.Content,
		Status: status, SupersedesID: supersedesID, SessionID: sessionID,
	}, nil
}

func CloneDecision(decision Decision) Decision {
	decision.IssueID = copyOptionalString(decision.IssueID)
	decision.SupersedesID = copyOptionalString(decision.SupersedesID)
	decision.CreatedBySessionID = copyOptionalString(decision.CreatedBySessionID)
	return decision
}

func CloneDecisionList(list DecisionList) DecisionList {
	list.Items = append([]Decision(nil), list.Items...)
	for index := range list.Items {
		list.Items[index] = CloneDecision(list.Items[index])
	}
	list.NextCursor = copyOptionalString(list.NextCursor)
	if list.Items == nil {
		list.Items = []Decision{}
	}
	return list
}
