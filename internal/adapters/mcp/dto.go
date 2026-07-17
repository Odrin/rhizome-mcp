package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

type exportProjectInput struct{}

type validateImportInput struct {
	Document string `json:"document"`
}

type getProjectInput struct {
	IncludeInstructions bool `json:"include_instructions,omitempty"`
}

type listLabelsInput struct {
	Query  *string `json:"query,omitempty"`
	Limit  int     `json:"limit,omitempty"`
	Cursor *string `json:"cursor,omitempty"`
}

type createIssueInput struct {
	Type                string   `json:"type"`
	Title               string   `json:"title"`
	Description         *string  `json:"description,omitempty"`
	AcceptanceCriteria  *string  `json:"acceptance_criteria,omitempty"`
	Status              string   `json:"status,omitempty"`
	Priority            string   `json:"priority,omitempty"`
	ParentIssueID       *string  `json:"parent_issue_id,omitempty"`
	BlockedReason       *string  `json:"blocked_reason,omitempty"`
	Labels              []string `json:"labels,omitempty"`
	CreateMissingLabels bool     `json:"create_missing_labels,omitempty"`
	IdempotencyKey      *string  `json:"idempotency_key,omitempty"`
}

type updateIssueInput struct {
	IssueID             string     `json:"issue_id"`
	ExpectedVersion     int64      `json:"expected_version"`
	Changes             patchInput `json:"changes"`
	CreateMissingLabels bool       `json:"create_missing_labels,omitempty"`
	IdempotencyKey      *string    `json:"idempotency_key,omitempty"`
}

type getIssueInput struct {
	IssueID string                     `json:"issue_id"`
	View    string                     `json:"view,omitempty"`
	Include []string                   `json:"include,omitempty"`
	Limits  map[string]json.RawMessage `json:"limits,omitempty"`
}

type getIssueActivityInput struct {
	IssueID string   `json:"issue_id"`
	Types   []string `json:"types,omitempty"`
	Limit   int      `json:"limit,omitempty"`
	Cursor  *string  `json:"cursor,omitempty"`
	Order   string   `json:"order,omitempty"`
}

type searchInput struct {
	Query           string   `json:"query"`
	EntityTypes     []string `json:"entity_types,omitempty"`
	IssueID         *string  `json:"issue_id,omitempty"`
	EpicID          *string  `json:"epic_id,omitempty"`
	Statuses        []string `json:"statuses,omitempty"`
	Labels          []string `json:"labels,omitempty"`
	IncludeArchived bool     `json:"include_archived,omitempty"`
	Limit           int      `json:"limit,omitempty"`
	Cursor          *string  `json:"cursor,omitempty"`
	SnippetLength   int      `json:"snippet_length,omitempty"`
}

type getChangesInput struct {
	SinceEventID int64    `json:"since_event_id"`
	IssueID      *string  `json:"issue_id,omitempty"`
	EventTypes   []string `json:"event_types,omitempty"`
	Limit        int      `json:"limit,omitempty"`
}

type listIssuesInput struct {
	Types             []string `json:"types,omitempty"`
	Statuses          []string `json:"statuses,omitempty"`
	EffectiveStatuses []string `json:"effective_statuses,omitempty"`
	Priorities        []string `json:"priorities,omitempty"`
	Labels            []string `json:"labels,omitempty"`
	ParentIssueID     *string  `json:"parent_issue_id,omitempty"`
	IsBlocked         *bool    `json:"is_blocked,omitempty"`
	IsClaimable       *bool    `json:"is_claimable,omitempty"`
	IncludeArchived   bool     `json:"include_archived,omitempty"`
	Limit             int      `json:"limit,omitempty"`
	Cursor            *string  `json:"cursor,omitempty"`
	View              string   `json:"view,omitempty"`
}

type getWorkContextInput struct {
	IssueID string                  `json:"issue_id"`
	Include []string                `json:"include,omitempty"`
	Limits  *workContextLimitsInput `json:"limits,omitempty"`
}

type workContextLimitsInput struct {
	RelatedIssueSummaries       *int `json:"related_issue_summaries,omitempty"`
	RecentComments              *int `json:"recent_comments,omitempty"`
	RecentAttemptNotes          *int `json:"recent_attempt_notes,omitempty"`
	DecisionContent             *int `json:"decision_content,omitempty"`
	AttemptHistory              *int `json:"attempt_history,omitempty"`
	Artifacts                   *int `json:"artifacts,omitempty"`
	ChangesSincePreviousAttempt *int `json:"changes_since_previous_attempt,omitempty"`
}

type archiveIssueInput struct {
	IssueID         string  `json:"issue_id"`
	ExpectedVersion int64   `json:"expected_version"`
	IdempotencyKey  *string `json:"idempotency_key,omitempty"`
}

type addCommentInput struct {
	IssueID        string  `json:"issue_id"`
	Content        string  `json:"content"`
	IdempotencyKey *string `json:"idempotency_key,omitempty"`
}

type recordDecisionInput struct {
	IssueID        *string `json:"issue_id,omitempty"`
	Title          string  `json:"title"`
	Summary        string  `json:"summary"`
	Content        string  `json:"content"`
	Status         string  `json:"status,omitempty"`
	SupersedesID   *string `json:"supersedes_id,omitempty"`
	IdempotencyKey *string `json:"idempotency_key,omitempty"`
}

type listDecisionsInput struct {
	IssueID *string `json:"issue_id,omitempty"`
	Limit   int     `json:"limit,omitempty"`
	Cursor  *string `json:"cursor,omitempty"`
}

type claimIssueInput struct {
	IssueID        string  `json:"issue_id"`
	LeaseSeconds   *int    `json:"lease_seconds,omitempty"`
	IdempotencyKey *string `json:"idempotency_key,omitempty"`
}

type renewAttemptInput struct {
	AttemptID    string `json:"attempt_id"`
	LeaseToken   string `json:"lease_token"`
	LeaseSeconds *int   `json:"lease_seconds,omitempty"`
}

type artifactInput struct {
	Type     string          `json:"type"`
	URI      string          `json:"uri"`
	Title    *string         `json:"title,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type saveAttemptNoteInput struct {
	AttemptID      string          `json:"attempt_id"`
	LeaseToken     string          `json:"lease_token"`
	Kind           string          `json:"kind"`
	Content        string          `json:"content"`
	NextSteps      []string        `json:"next_steps,omitempty"`
	Important      bool            `json:"important,omitempty"`
	Artifacts      []artifactInput `json:"artifacts,omitempty"`
	IdempotencyKey *string         `json:"idempotency_key,omitempty"`
}

type acknowledgementInput struct {
	IssueVersion  int64 `json:"issue_version"`
	LatestEventID int64 `json:"latest_event_id"`
}

type finishAttemptInput struct {
	AttemptID              string                `json:"attempt_id"`
	LeaseToken             string                `json:"lease_token"`
	Outcome                string                `json:"outcome"`
	ResultSummary          string                `json:"result_summary"`
	NextSteps              []string              `json:"next_steps,omitempty"`
	Verification           []string              `json:"verification,omitempty"`
	TargetIssueStatus      *string               `json:"target_issue_status,omitempty"`
	BlockedReason          *string               `json:"blocked_reason,omitempty"`
	ReviewOutcome          *string               `json:"review_outcome,omitempty"`
	FailureReasonCode      *string               `json:"failure_reason_code,omitempty"`
	InterruptionReasonCode *string               `json:"interruption_reason_code,omitempty"`
	ReasonDetails          *string               `json:"reason_details,omitempty"`
	AcknowledgedChanges    *acknowledgementInput `json:"acknowledged_changes,omitempty"`
	Artifacts              []artifactInput       `json:"artifacts,omitempty"`
	IdempotencyKey         *string               `json:"idempotency_key,omitempty"`
}

type manageIssueRelationInput struct {
	Action         string  `json:"action"`
	SourceIssueID  string  `json:"source_issue_id"`
	TargetIssueID  string  `json:"target_issue_id"`
	RelationType   string  `json:"relation_type"`
	IdempotencyKey *string `json:"idempotency_key,omitempty"`
}

type getIssueGraphInput struct {
	RootIssueID      string   `json:"root_issue_id"`
	Depth            *int     `json:"depth,omitempty"`
	Direction        string   `json:"direction,omitempty"`
	RelationTypes    []string `json:"relation_types,omitempty"`
	IncludeHierarchy *bool    `json:"include_hierarchy,omitempty"`
	IncludeTerminal  *bool    `json:"include_terminal,omitempty"`
	MaxNodes         *int     `json:"max_nodes,omitempty"`
	View             string   `json:"view,omitempty"`
}

type getPlanningGraphInput struct {
	RootIssueID    *string `json:"root_issue_id,omitempty"`
	Depth          *int    `json:"depth,omitempty"`
	MaxNodes       *int    `json:"max_nodes,omitempty"`
	IncludeReview  *bool   `json:"include_review,omitempty"`
	IncludeRelated *bool   `json:"include_related,omitempty"`
}

type issuePlanInput struct {
	Issues    []planIssueInput    `json:"issues"`
	Relations []planRelationInput `json:"relations"`
	Decisions []planDecisionInput `json:"decisions"`
}

type applyIssuePlanInput struct {
	Issues         []planIssueInput    `json:"issues"`
	Relations      []planRelationInput `json:"relations"`
	Decisions      []planDecisionInput `json:"decisions"`
	IdempotencyKey string              `json:"idempotency_key"`
}

func (input applyIssuePlanInput) domainPlan() domain.IssuePlan {
	return issuePlanInput{Issues: input.Issues, Relations: input.Relations, Decisions: input.Decisions}.domainPlan()
}

type planIssueInput struct {
	Ref                 string   `json:"ref,omitempty"`
	Type                string   `json:"type"`
	Title               string   `json:"title"`
	Description         *string  `json:"description,omitempty"`
	AcceptanceCriteria  *string  `json:"acceptance_criteria,omitempty"`
	Status              string   `json:"status,omitempty"`
	Priority            string   `json:"priority,omitempty"`
	ParentRef           *string  `json:"parent_ref,omitempty"`
	BlockedReason       *string  `json:"blocked_reason,omitempty"`
	Labels              []string `json:"labels,omitempty"`
	CreateMissingLabels bool     `json:"create_missing_labels,omitempty"`
}
type planRelationInput struct {
	SourceRef string `json:"source_ref"`
	TargetRef string `json:"target_ref"`
	Type      string `json:"type"`
}
type planDecisionInput struct {
	IssueRef *string `json:"issue_ref,omitempty"`
	Title    string  `json:"title"`
	Summary  string  `json:"summary"`
	Content  string  `json:"content"`
	Status   string  `json:"status,omitempty"`
}

func (input issuePlanInput) domainPlan() domain.IssuePlan {
	plan := domain.IssuePlan{
		Issues:    make([]domain.PlannedIssue, len(input.Issues)),
		Relations: make([]domain.PlannedRelation, len(input.Relations)),
		Decisions: make([]domain.PlannedDecision, len(input.Decisions)),
	}
	for i, issue := range input.Issues {
		plan.Issues[i] = domain.PlannedIssue{Ref: issue.Ref, Type: domain.Type(issue.Type), Title: issue.Title,
			Description: issue.Description, AcceptanceCriteria: issue.AcceptanceCriteria, Status: domain.Status(issue.Status),
			Priority: domain.Priority(issue.Priority), ParentRef: issue.ParentRef, BlockedReason: issue.BlockedReason,
			Labels: issue.Labels, CreateMissingLabels: issue.CreateMissingLabels}
	}
	for i, relation := range input.Relations {
		plan.Relations[i] = domain.PlannedRelation{SourceRef: relation.SourceRef, TargetRef: relation.TargetRef, Type: domain.RelationType(relation.Type)}
	}
	for i, decision := range input.Decisions {
		plan.Decisions[i] = domain.PlannedDecision{IssueRef: decision.IssueRef, Title: decision.Title, Summary: decision.Summary, Content: decision.Content, Status: decision.Status}
	}
	return plan
}

// patchInput records field presence independently from a null value.
type patchInput struct {
	Title              optionalString
	Description        optionalNullableString
	AcceptanceCriteria optionalNullableString
	Type               optionalString
	Priority           optionalString
	Status             optionalString
	ParentIssueID      optionalNullableString
	BlockedReason      optionalNullableString
	Labels             optionalStrings
}

type optionalString struct {
	set   bool
	value string
}

type optionalNullableString struct {
	set   bool
	value *string
}

type optionalStrings struct {
	set   bool
	value []string
}

func (input *patchInput) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name, raw := range fields {
		switch name {
		case "title":
			input.Title.set, input.Title.value = true, ""
			if err := json.Unmarshal(raw, &input.Title.value); err != nil {
				return fmt.Errorf("title: %w", err)
			}
		case "description":
			if err := unmarshalNullableString(raw, &input.Description); err != nil {
				return fmt.Errorf("description: %w", err)
			}
		case "acceptance_criteria":
			if err := unmarshalNullableString(raw, &input.AcceptanceCriteria); err != nil {
				return fmt.Errorf("acceptance_criteria: %w", err)
			}
		case "type":
			input.Type.set, input.Type.value = true, ""
			if err := json.Unmarshal(raw, &input.Type.value); err != nil {
				return fmt.Errorf("type: %w", err)
			}
		case "priority":
			input.Priority.set, input.Priority.value = true, ""
			if err := json.Unmarshal(raw, &input.Priority.value); err != nil {
				return fmt.Errorf("priority: %w", err)
			}
		case "status":
			input.Status.set, input.Status.value = true, ""
			if err := json.Unmarshal(raw, &input.Status.value); err != nil {
				return fmt.Errorf("status: %w", err)
			}
		case "parent_issue_id":
			if err := unmarshalNullableString(raw, &input.ParentIssueID); err != nil {
				return fmt.Errorf("parent_issue_id: %w", err)
			}
		case "blocked_reason":
			if err := unmarshalNullableString(raw, &input.BlockedReason); err != nil {
				return fmt.Errorf("blocked_reason: %w", err)
			}
		case "labels":
			input.Labels.set, input.Labels.value = true, nil
			if bytes.Equal(raw, []byte("null")) {
				continue
			}
			if err := json.Unmarshal(raw, &input.Labels.value); err != nil {
				return fmt.Errorf("labels: %w", err)
			}
		default:
			return fmt.Errorf("unknown patch field %q", name)
		}
	}
	return nil
}

func unmarshalNullableString(raw json.RawMessage, destination *optionalNullableString) error {
	destination.set, destination.value = true, nil
	if bytes.Equal(raw, []byte("null")) {
		return nil
	}
	return json.Unmarshal(raw, &destination.value)
}

func (input patchInput) domainPatch() domain.IssuePatch {
	return domain.IssuePatch{
		Title:              domain.OptionalValue[string]{Set: input.Title.set, Value: input.Title.value},
		Description:        domain.OptionalString{Set: input.Description.set, Value: input.Description.value},
		AcceptanceCriteria: domain.OptionalString{Set: input.AcceptanceCriteria.set, Value: input.AcceptanceCriteria.value},
		Type:               domain.OptionalValue[domain.Type]{Set: input.Type.set, Value: domain.Type(input.Type.value)},
		Priority:           domain.OptionalValue[domain.Priority]{Set: input.Priority.set, Value: domain.Priority(input.Priority.value)},
		Status:             domain.OptionalValue[domain.Status]{Set: input.Status.set, Value: domain.Status(input.Status.value)},
		ParentID:           domain.OptionalString{Set: input.ParentIssueID.set, Value: input.ParentIssueID.value},
		BlockedReason:      domain.OptionalString{Set: input.BlockedReason.set, Value: input.BlockedReason.value},
		Labels:             domain.OptionalValue[[]string]{Set: input.Labels.set, Value: input.Labels.value},
	}
}

type errorOutput struct {
	Code      string          `json:"code"`
	Message   string          `json:"message"`
	Details   []domain.Detail `json:"details"`
	Retryable bool            `json:"retryable"`
}

type projectOutput struct {
	Project                projectDTO     `json:"project"`
	Session                any            `json:"session"`
	AppVersion             string         `json:"app_version"`
	SchemaVersion          int            `json:"schema_version"`
	ConfigVersion          int            `json:"config_version"`
	Limits                 limitsDTO      `json:"limits"`
	SupportedIssueTypes    []string       `json:"supported_issue_types"`
	SupportedStatuses      []string       `json:"supported_statuses"`
	SupportedRelationTypes []string       `json:"supported_relation_types"`
	SupportedPriorities    []string       `json:"supported_priorities"`
	LatestEventID          int64          `json:"latest_event_id"`
	Guides                 []guideLinkDTO `json:"guides"`
	NextActions            []string       `json:"next_actions"`
}

type guideLinkDTO struct {
	URI         string `json:"uri"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type projectDTO struct {
	ID              string    `json:"id"`
	Name            *string   `json:"name"`
	Instructions    *string   `json:"instructions"`
	NextIssueNumber int64     `json:"next_issue_number"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type limitsDTO struct {
	DefaultIssueListLimit int `json:"default_issue_list_limit"`
	DefaultLabelListLimit int `json:"default_label_list_limit"`
	MaxCollectionLimit    int `json:"max_collection_limit"`
}

type labelDTO struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	NormalizedName string    `json:"normalized_name"`
	Description    *string   `json:"description"`
	CreatedAt      time.Time `json:"created_at"`
}

type labelListOutput struct {
	Items      []labelDTO `json:"items"`
	NextCursor *string    `json:"next_cursor"`
	HasMore    bool       `json:"has_more"`
}

type issueDTO struct {
	ID                 string     `json:"id"`
	DisplayID          string     `json:"display_id"`
	SequenceNo         int64      `json:"sequence_no"`
	Type               string     `json:"type"`
	Title              string     `json:"title"`
	Description        *string    `json:"description"`
	AcceptanceCriteria *string    `json:"acceptance_criteria"`
	Status             string     `json:"status"`
	Priority           string     `json:"priority"`
	ParentIssueID      *string    `json:"parent_issue_id"`
	BlockedReason      *string    `json:"blocked_reason"`
	Version            int64      `json:"version"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	ClosedAt           *time.Time `json:"closed_at"`
	ArchivedAt         *time.Time `json:"archived_at"`
	Labels             []labelDTO `json:"labels"`
}

type commentDTO struct {
	ID                 string     `json:"id"`
	IssueID            string     `json:"issue_id"`
	Content            string     `json:"content"`
	CreatedBySessionID *string    `json:"created_by_session_id"`
	AuthorLabel        *string    `json:"author_label"`
	CreatedAt          time.Time  `json:"created_at"`
	EditedAt           *time.Time `json:"edited_at"`
}

type issueEventDTO struct {
	ID        int64           `json:"id"`
	IssueID   *string         `json:"issue_id"`
	EventType string          `json:"event_type"`
	SessionID *string         `json:"session_id"`
	AttemptID *string         `json:"attempt_id"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type activityItemDTO struct {
	EntityType  string                     `json:"entity_type"`
	EntityID    string                     `json:"entity_id"`
	IssueID     string                     `json:"issue_id"`
	OccurredAt  time.Time                  `json:"occurred_at"`
	Comment     *commentDTO                `json:"comment,omitempty"`
	Decision    *recordDecisionDecisionDTO `json:"decision,omitempty"`
	Attempt     *attemptDTO                `json:"attempt,omitempty"`
	AttemptNote *attemptNoteDTO            `json:"attempt_note,omitempty"`
	Event       *issueEventDTO             `json:"event,omitempty"`
	Artifact    *artifactDTO               `json:"artifact,omitempty"`
}

type issueActivityOutput struct {
	Items      []activityItemDTO `json:"items"`
	NextCursor *string           `json:"next_cursor"`
	HasMore    bool              `json:"has_more"`
}

type searchResultDTO struct {
	EntityType string  `json:"entity_type"`
	EntityID   string  `json:"entity_id"`
	IssueID    *string `json:"issue_id"`
	Title      string  `json:"title"`
	Snippet    string  `json:"snippet"`
	Score      float64 `json:"score"`
}

type searchOutput struct {
	Results    []searchResultDTO `json:"results"`
	NextCursor *string           `json:"next_cursor"`
	HasMore    bool              `json:"has_more"`
}

type changesOutput struct {
	Events        []issueEventDTO `json:"events"`
	LatestEventID int64           `json:"latest_event_id"`
	HasMore       bool            `json:"has_more"`
	NextEventID   int64           `json:"next_event_id"`
}

type addCommentOutput struct {
	Comment commentDTO `json:"comment"`
}

type recordDecisionDecisionDTO struct {
	ID                 string    `json:"id"`
	IssueID            *string   `json:"issue_id"`
	Title              string    `json:"title"`
	Summary            string    `json:"summary"`
	Content            string    `json:"content"`
	Status             string    `json:"status"`
	SupersedesID       *string   `json:"supersedes_id"`
	CreatedBySessionID *string   `json:"created_by_session_id"`
	CreatedAt          time.Time `json:"created_at"`
}

type recordDecisionOutput struct {
	Decision             recordDecisionDecisionDTO `json:"decision"`
	SupersededDecisionID *string                   `json:"superseded_decision_id"`
}

type decisionListOutput struct {
	Items      []recordDecisionDecisionDTO `json:"items"`
	NextCursor *string                     `json:"next_cursor"`
	HasMore    bool                        `json:"has_more"`
}

type updateIssueOutput struct {
	Issue         issueDTO `json:"issue"`
	ChangedFields []string `json:"changed_fields"`
}

type issueListItemDTO struct {
	issueDTO
	EffectiveStatus        string  `json:"effective_status"`
	UnresolvedBlockerCount int64   `json:"unresolved_blocker_count"`
	IsBlocked              bool    `json:"is_blocked"`
	IsClaimable            bool    `json:"is_claimable"`
	ActiveAttemptID        *string `json:"active_attempt_id"`
}

type attemptDTO struct {
	ID                     string     `json:"id"`
	IssueID                string     `json:"issue_id"`
	Kind                   string     `json:"kind"`
	Status                 string     `json:"status"`
	IssueVersionAtStart    int64      `json:"issue_version_at_start"`
	ContextEventIDAtStart  int64      `json:"context_event_id_at_start"`
	LeaseExpiresAt         time.Time  `json:"lease_expires_at"`
	StartedAt              time.Time  `json:"started_at"`
	LastHeartbeatAt        time.Time  `json:"last_heartbeat_at"`
	FinishedAt             *time.Time `json:"finished_at"`
	ResultSummary          *string    `json:"result_summary"`
	NextSteps              []string   `json:"next_steps"`
	Verification           []string   `json:"verification"`
	FailureReasonCode      *string    `json:"failure_reason_code"`
	InterruptionReasonCode *string    `json:"interruption_reason_code"`
	ReasonDetails          *string    `json:"reason_details"`
}

type finishAttemptOutput struct {
	Attempt       attemptDTO    `json:"attempt"`
	Issue         issueDTO      `json:"issue"`
	Warnings      []string      `json:"warnings"`
	LatestEventID int64         `json:"latest_event_id"`
	Artifacts     []artifactDTO `json:"artifacts"`
	NextActions   []string      `json:"next_actions"`
}

type emptyWorkContextDTO struct{}

type workContextIssueDTO struct {
	ID                     string  `json:"id"`
	DisplayID              string  `json:"display_id"`
	Title                  string  `json:"title"`
	Description            *string `json:"description"`
	AcceptanceCriteria     *string `json:"acceptance_criteria"`
	EffectiveStatus        string  `json:"effective_status"`
	UnresolvedBlockerCount int64   `json:"unresolved_blocker_count"`
	IsBlocked              bool    `json:"is_blocked"`
}

type workContextDecisionSummaryDTO struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type workContextAttemptSummaryDTO struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind"`
	Status        string     `json:"status"`
	FinishedAt    *time.Time `json:"finished_at"`
	ResultSummary *string    `json:"result_summary"`
	NextSteps     []string   `json:"next_steps"`
}

type workContextOutput struct {
	Issue                       workContextIssueDTO             `json:"issue"`
	Blockers                    []workContextIssueDTO           `json:"blockers"`
	Decisions                   []workContextDecisionSummaryDTO `json:"decisions"`
	PreviousAttempt             *workContextAttemptSummaryDTO   `json:"previous_attempt"`
	Checkpoint                  *attemptNoteDTO                 `json:"checkpoint"`
	Warnings                    []string                        `json:"warnings"`
	ParentEpic                  *workContextIssueDTO            `json:"parent_epic"`
	Relations                   []relationDTO                   `json:"relations"`
	RelatedIssueSummaries       []workContextIssueDTO           `json:"related_issue_summaries"`
	RecentComments              []commentDTO                    `json:"recent_comments"`
	RecentAttemptNotes          []attemptNoteDTO                `json:"recent_attempt_notes"`
	DecisionContent             []recordDecisionDecisionDTO     `json:"decision_content"`
	AttemptHistory              []attemptDTO                    `json:"attempt_history"`
	Artifacts                   []artifactDTO                   `json:"artifacts"`
	ProjectInstructions         *string                         `json:"project_instructions"`
	ChangesSincePreviousAttempt []issueEventDTO                 `json:"changes_since_previous_attempt"`
	Truncated                   bool                            `json:"truncated"`
	TruncatedSections           []string                        `json:"truncated_sections"`
	NextActions                 []string                        `json:"next_actions"`
}

type claimIssueOutput struct {
	Issue              issueListItemDTO    `json:"issue"`
	Attempt            attemptDTO          `json:"attempt"`
	LeaseToken         string              `json:"lease_token"`
	LeaseExpiresAt     time.Time           `json:"lease_expires_at"`
	MinimalWorkContext emptyWorkContextDTO `json:"minimal_work_context"`
	Warnings           []string            `json:"warnings"`
	NextActions        []string            `json:"next_actions"`
}

type renewAttemptOutput struct {
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	ServerTime     time.Time `json:"server_time"`
	NextActions    []string  `json:"next_actions"`
}

type attemptNoteDTO struct {
	ID        string    `json:"id"`
	AttemptID string    `json:"attempt_id"`
	Kind      string    `json:"kind"`
	Content   string    `json:"content"`
	NextSteps []string  `json:"next_steps"`
	Important bool      `json:"important"`
	CreatedAt time.Time `json:"created_at"`
}

type artifactDTO struct {
	ID        string          `json:"id"`
	IssueID   string          `json:"issue_id"`
	AttemptID *string         `json:"attempt_id"`
	Type      string          `json:"type"`
	URI       string          `json:"uri"`
	Title     *string         `json:"title"`
	Metadata  json.RawMessage `json:"metadata"`
	CreatedAt time.Time       `json:"created_at"`
}

type saveAttemptNoteOutput struct {
	AttemptNote attemptNoteDTO `json:"attempt_note"`
	Artifacts   []artifactDTO  `json:"artifacts"`
	NextActions []string       `json:"next_actions"`
}

type issueListOutput struct {
	Items       []issueListItemDTO `json:"items"`
	NextCursor  *string            `json:"next_cursor"`
	HasMore     bool               `json:"has_more"`
	NextActions []string           `json:"next_actions"`
}

type relationDTO struct {
	ID            string    `json:"id,omitempty"`
	SourceIssueID string    `json:"source_issue_id"`
	TargetIssueID string    `json:"target_issue_id"`
	Type          string    `json:"type"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
}

type manageIssueRelationOutput struct {
	Relation       relationDTO        `json:"relation"`
	AffectedIssues []issueListItemDTO `json:"affected_issues"`
	Changed        bool               `json:"changed"`
}

type graphEdgeDTO struct {
	SourceIssueID string `json:"source_issue_id"`
	TargetIssueID string `json:"target_issue_id"`
	Type          string `json:"type"`
}

type graphSummaryDTO struct {
	NodeCount         int `json:"node_count"`
	EdgeCount         int `json:"edge_count"`
	EntryPointCount   int `json:"entry_point_count"`
	BlockingNodeCount int `json:"blocking_node_count"`
}

type graphOutput struct {
	RootIssueID      *string            `json:"root_issue_id,omitempty"`
	Nodes            []issueListItemDTO `json:"nodes"`
	Edges            []graphEdgeDTO     `json:"edges"`
	EntryPoints      []string           `json:"entry_points"`
	BlockingNodes    []string           `json:"blocking_nodes,omitempty"`
	Summary          graphSummaryDTO    `json:"summary"`
	Warnings         []string           `json:"warnings,omitempty"`
	Truncated        bool               `json:"truncated"`
	TruncationReason *string            `json:"truncation_reason,omitempty"`
	NextActions      []string           `json:"next_actions"`
}

type planSummaryDTO struct {
	IssueCount           int `json:"issue_count"`
	RelationCount        int `json:"relation_count"`
	DecisionCount        int `json:"decision_count"`
	LabelAssignmentCount int `json:"label_assignment_count"`
}
type normalizedPlanDTO struct {
	Issues    []planIssueInput    `json:"issues"`
	Relations []planRelationInput `json:"relations"`
	Decisions []planDecisionInput `json:"decisions"`
}
type planValidationOutput struct {
	Valid          bool              `json:"valid"`
	Errors         []domain.Detail   `json:"errors"`
	Warnings       []string          `json:"warnings"`
	Summary        planSummaryDTO    `json:"summary"`
	NormalizedPlan normalizedPlanDTO `json:"normalized_plan"`
	NextActions    []string          `json:"next_actions"`
}
type createdPlanIssueDTO struct {
	Ref   string   `json:"ref,omitempty"`
	Issue issueDTO `json:"issue"`
}
type decisionDTO struct {
	ID        string    `json:"id"`
	IssueID   *string   `json:"issue_id,omitempty"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	Content   string    `json:"content"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}
type applyIssuePlanOutput struct {
	CreatedIssues    []createdPlanIssueDTO `json:"created_issues"`
	CreatedRelations []relationDTO         `json:"created_relations"`
	CreatedDecisions []decisionDTO         `json:"created_decisions"`
	LatestEventID    int64                 `json:"latest_event_id"`
	NextActions      []string              `json:"next_actions"`
}

func planValidationOutputFromDomain(value domain.PlanValidation) planValidationOutput {
	plan := issuePlanInputFromDomain(value.NormalizedPlan)
	return planValidationOutput{Valid: value.Valid, Errors: append([]domain.Detail{}, value.Errors...), Warnings: append([]string{}, value.Warnings...),
		Summary:        planSummaryDTO{IssueCount: value.Summary.IssueCount, RelationCount: value.Summary.RelationCount, DecisionCount: value.Summary.DecisionCount, LabelAssignmentCount: value.Summary.LabelAssignmentCount},
		NormalizedPlan: normalizedPlanDTO{Issues: plan.Issues, Relations: plan.Relations, Decisions: plan.Decisions}}
}
func issuePlanInputFromDomain(value domain.IssuePlan) issuePlanInput {
	result := issuePlanInput{Issues: make([]planIssueInput, len(value.Issues)), Relations: make([]planRelationInput, len(value.Relations)), Decisions: make([]planDecisionInput, len(value.Decisions))}
	for i, issue := range value.Issues {
		result.Issues[i] = planIssueInput{Ref: issue.Ref, Type: string(issue.Type), Title: issue.Title, Description: issue.Description, AcceptanceCriteria: issue.AcceptanceCriteria, Status: string(issue.Status), Priority: string(issue.Priority), ParentRef: issue.ParentRef, BlockedReason: issue.BlockedReason, Labels: issue.Labels, CreateMissingLabels: issue.CreateMissingLabels}
	}
	for i, relation := range value.Relations {
		result.Relations[i] = planRelationInput{SourceRef: relation.SourceRef, TargetRef: relation.TargetRef, Type: string(relation.Type)}
	}
	for i, decision := range value.Decisions {
		result.Decisions[i] = planDecisionInput{IssueRef: decision.IssueRef, Title: decision.Title, Summary: decision.Summary, Content: decision.Content, Status: decision.Status}
	}
	return result
}
func applyIssuePlanOutputFromPort(value ports.ApplyIssuePlanResult) applyIssuePlanOutput {
	result := applyIssuePlanOutput{CreatedIssues: make([]createdPlanIssueDTO, len(value.CreatedIssues)), CreatedRelations: make([]relationDTO, len(value.CreatedRelations)), CreatedDecisions: make([]decisionDTO, len(value.CreatedDecisions)), LatestEventID: value.LatestEventID}
	for i, issue := range value.CreatedIssues {
		result.CreatedIssues[i] = createdPlanIssueDTO{Ref: issue.Ref, Issue: issueDTOFromDomain(issue.Issue)}
	}
	for i, relation := range value.CreatedRelations {
		result.CreatedRelations[i] = relationDTOFromDomain(relation)
	}
	for i, decision := range value.CreatedDecisions {
		result.CreatedDecisions[i] = decisionDTO{ID: decision.ID, IssueID: decision.IssueID, Title: decision.Title, Summary: decision.Summary, Content: decision.Content, Status: decision.Status, CreatedAt: decision.CreatedAt}
	}
	return result
}

func projectDTOFromDomain(project domain.Project, includeInstructions bool) projectDTO {
	instructions := project.Instructions
	if !includeInstructions {
		instructions = nil
	}
	return projectDTO{
		ID: project.ID, Name: project.Name, Instructions: instructions, NextIssueNumber: project.NextIssueNumber,
		CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt,
	}
}

func labelDTOFromDomain(label domain.Label) labelDTO {
	return labelDTO{
		ID: label.ID, Name: label.Name, NormalizedName: label.NormalizedName,
		Description: label.Description, CreatedAt: label.CreatedAt,
	}
}

func issueDTOFromDomain(issue domain.Issue) issueDTO {
	labels := make([]labelDTO, len(issue.Labels))
	for i, label := range issue.Labels {
		labels[i] = labelDTOFromDomain(label)
	}
	return issueDTO{
		ID: issue.ID, DisplayID: issue.DisplayID, SequenceNo: issue.SequenceNo, Type: string(issue.Type),
		Title: issue.Title, Description: issue.Description, AcceptanceCriteria: issue.AcceptanceCriteria,
		Status: string(issue.Status), Priority: string(issue.Priority), ParentIssueID: issue.ParentID,
		BlockedReason: issue.BlockedReason, Version: issue.Version, CreatedAt: issue.CreatedAt,
		UpdatedAt: issue.UpdatedAt, ClosedAt: issue.ClosedAt, ArchivedAt: issue.ArchivedAt, Labels: labels,
	}
}

func commentDTOFromDomain(comment domain.Comment) commentDTO {
	return commentDTO{
		ID: comment.ID, IssueID: comment.IssueID, Content: comment.Content,
		CreatedBySessionID: copyString(comment.CreatedBySessionID), AuthorLabel: copyString(comment.AuthorLabel),
		CreatedAt: comment.CreatedAt, EditedAt: copyTime(comment.EditedAt),
	}
}

func recordDecisionDTOFromDomain(decision domain.Decision) recordDecisionDecisionDTO {
	return recordDecisionDecisionDTO{
		ID: decision.ID, IssueID: copyString(decision.IssueID), Title: decision.Title, Summary: decision.Summary,
		Content: decision.Content, Status: string(decision.Status), SupersedesID: copyString(decision.SupersedesID),
		CreatedBySessionID: copyString(decision.CreatedBySessionID), CreatedAt: decision.CreatedAt,
	}
}

func decisionListOutputFromDomain(list domain.DecisionList) decisionListOutput {
	items := make([]recordDecisionDecisionDTO, len(list.Items))
	for index, item := range list.Items {
		items[index] = recordDecisionDTOFromDomain(item)
	}
	return decisionListOutput{Items: items, NextCursor: copyString(list.NextCursor), HasMore: list.HasMore}
}

func relationDTOFromDomain(relation domain.IssueRelation) relationDTO {
	return relationDTO{
		ID: relation.ID, SourceIssueID: relation.SourceIssueID, TargetIssueID: relation.TargetIssueID,
		Type: string(relation.Type), CreatedAt: relation.CreatedAt,
	}
}

func attemptDTOFromDomain(attempt domain.WorkAttempt) attemptDTO {
	nextSteps := append([]string{}, attempt.NextSteps...)
	verification := append([]string{}, attempt.Verification...)
	finishedAt := copyTime(attempt.FinishedAt)
	resultSummary := copyString(attempt.ResultSummary)
	reasonDetails := copyString(attempt.ReasonDetails)
	return attemptDTO{ID: attempt.ID, IssueID: attempt.IssueID, Kind: string(attempt.Kind), Status: string(attempt.Status),
		IssueVersionAtStart: attempt.IssueVersionAtStart, ContextEventIDAtStart: attempt.ContextEventIDAtStart,
		LeaseExpiresAt: attempt.LeaseExpiresAt, StartedAt: attempt.StartedAt, LastHeartbeatAt: attempt.LastHeartbeatAt,
		FinishedAt: finishedAt, ResultSummary: resultSummary, NextSteps: nextSteps, Verification: verification,
		FailureReasonCode: stringPointer(attempt.FailureReasonCode), InterruptionReasonCode: stringPointer(attempt.InterruptionReasonCode),
		ReasonDetails: reasonDetails}
}

func stringPointer[T ~string](value *T) *string {
	if value == nil {
		return nil
	}
	result := string(*value)
	return &result
}

func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func attemptNoteDTOFromDomain(note domain.AttemptNote) attemptNoteDTO {
	return attemptNoteDTO{
		ID: note.ID, AttemptID: note.AttemptID, Kind: string(note.Kind), Content: note.Content,
		NextSteps: append([]string(nil), note.NextSteps...), Important: note.Important, CreatedAt: note.CreatedAt,
	}
}

func artifactDTOFromDomain(artifact domain.Artifact) artifactDTO {
	return artifactDTO{
		ID: artifact.ID, IssueID: artifact.IssueID, AttemptID: copyString(artifact.AttemptID),
		Type: string(artifact.Type), URI: artifact.URI, Title: copyString(artifact.Title),
		Metadata: append(json.RawMessage(nil), artifact.Metadata...), CreatedAt: artifact.CreatedAt,
	}
}

func getIssueActivityInputToDomain(input getIssueActivityInput) domain.GetIssueActivityInput {
	types := make([]domain.ActivityCategory, len(input.Types))
	for index, value := range input.Types {
		types[index] = domain.ActivityCategory(value)
	}
	cursor := ""
	if input.Cursor != nil {
		cursor = *input.Cursor
	}
	return domain.GetIssueActivityInput{
		IssueID: input.IssueID,
		Types:   types,
		Limit:   input.Limit,
		Cursor:  cursor,
		Order:   domain.ActivityOrder(input.Order),
	}
}

func issueEventDTOFromDomain(event domain.IssueEvent) issueEventDTO {
	return issueEventDTO{
		ID:        event.ID,
		IssueID:   copyString(event.IssueID),
		EventType: event.EventType,
		SessionID: copyString(event.SessionID),
		AttemptID: copyString(event.AttemptID),
		Payload:   append(json.RawMessage(nil), event.Payload...),
		CreatedAt: event.CreatedAt,
	}
}

func searchOutputFromDomain(page domain.SearchPage) searchOutput {
	results := make([]searchResultDTO, len(page.Results))
	for index, result := range page.Results {
		results[index] = searchResultDTO{
			EntityType: string(result.EntityType), EntityID: result.EntityID, IssueID: copyString(result.IssueID),
			Title: result.Title, Snippet: result.Snippet, Score: result.Score,
		}
	}
	return searchOutput{Results: results, NextCursor: copyString(page.NextCursor), HasMore: page.HasMore}
}

func changesOutputFromDomain(page domain.ChangesPage) changesOutput {
	events := make([]issueEventDTO, len(page.Events))
	for index, event := range page.Events {
		events[index] = issueEventDTOFromDomain(event)
	}
	return changesOutput{
		Events: events, LatestEventID: page.LatestEventID, HasMore: page.HasMore, NextEventID: page.NextEventID,
	}
}

func workContextOutputFromDomain(value domain.WorkContext) workContextOutput {
	result := workContextOutput{
		Issue:                       workContextIssueDTOFromDomain(value.Issue),
		Blockers:                    make([]workContextIssueDTO, len(value.Blockers)),
		Decisions:                   make([]workContextDecisionSummaryDTO, len(value.Decisions)),
		Warnings:                    make([]string, len(value.Warnings)),
		ParentEpic:                  workContextIssueDTOFromDomainPointer(value.ParentEpic),
		Relations:                   make([]relationDTO, len(value.Relations)),
		RelatedIssueSummaries:       make([]workContextIssueDTO, len(value.RelatedIssueSummaries)),
		RecentComments:              make([]commentDTO, len(value.RecentComments)),
		RecentAttemptNotes:          make([]attemptNoteDTO, len(value.RecentAttemptNotes)),
		DecisionContent:             make([]recordDecisionDecisionDTO, len(value.DecisionContent)),
		AttemptHistory:              make([]attemptDTO, len(value.AttemptHistory)),
		Artifacts:                   make([]artifactDTO, len(value.Artifacts)),
		ChangesSincePreviousAttempt: make([]issueEventDTO, len(value.ChangesSincePreviousAttempt)),
		Truncated:                   value.Truncated,
		TruncatedSections:           make([]string, len(value.TruncatedSections)),
	}
	for index, blocker := range value.Blockers {
		result.Blockers[index] = workContextIssueDTOFromDomain(blocker)
	}
	for index, decision := range value.Decisions {
		result.Decisions[index] = workContextDecisionSummaryDTOFromDomain(decision)
	}
	for index, warning := range value.Warnings {
		result.Warnings[index] = warning
	}
	for index, relation := range value.Relations {
		result.Relations[index] = relationDTOFromDomain(relation)
	}
	for index, issue := range value.RelatedIssueSummaries {
		result.RelatedIssueSummaries[index] = workContextIssueDTOFromDomain(issue)
	}
	for index, comment := range value.RecentComments {
		result.RecentComments[index] = commentDTOFromDomain(comment)
	}
	for index, note := range value.RecentAttemptNotes {
		noteDTO := attemptNoteDTOFromDomain(note)
		result.RecentAttemptNotes[index] = noteDTO
	}
	for index, decision := range value.DecisionContent {
		result.DecisionContent[index] = recordDecisionDTOFromDomain(decision)
	}
	for index, attempt := range value.AttemptHistory {
		result.AttemptHistory[index] = attemptDTOFromDomain(attempt)
	}
	for index, artifact := range value.Artifacts {
		result.Artifacts[index] = artifactDTOFromDomain(artifact)
	}
	for index, event := range value.ChangesSincePreviousAttempt {
		result.ChangesSincePreviousAttempt[index] = issueEventDTOFromDomain(event)
	}
	for index, include := range value.TruncatedSections {
		result.TruncatedSections[index] = string(include)
	}
	if value.PreviousAttempt != nil {
		result.PreviousAttempt = workContextAttemptSummaryDTOFromDomain(value.PreviousAttempt)
	}
	if value.Checkpoint != nil {
		checkpoint := attemptNoteDTOFromDomain(*value.Checkpoint)
		result.Checkpoint = &checkpoint
	}
	result.ProjectInstructions = copyString(value.ProjectInstructions)
	return result
}

func workContextIssueDTOFromDomain(value domain.WorkContextIssue) workContextIssueDTO {
	return workContextIssueDTO{
		ID:                     value.ID,
		DisplayID:              value.DisplayID,
		Title:                  value.Title,
		Description:            copyString(value.Description),
		AcceptanceCriteria:     copyString(value.AcceptanceCriteria),
		EffectiveStatus:        string(value.EffectiveStatus),
		UnresolvedBlockerCount: value.UnresolvedBlockerCount,
		IsBlocked:              value.IsBlocked,
	}
}

func workContextIssueDTOFromDomainPointer(value *domain.WorkContextIssue) *workContextIssueDTO {
	if value == nil {
		return nil
	}
	issue := workContextIssueDTOFromDomain(*value)
	return &issue
}

func workContextDecisionSummaryDTOFromDomain(value domain.WorkContextDecisionSummary) workContextDecisionSummaryDTO {
	return workContextDecisionSummaryDTO{
		ID:        value.ID,
		Title:     value.Title,
		Summary:   value.Summary,
		Status:    string(value.Status),
		CreatedAt: value.CreatedAt,
	}
}

func workContextAttemptSummaryDTOFromDomain(value *domain.WorkContextAttemptSummary) *workContextAttemptSummaryDTO {
	if value == nil {
		return nil
	}
	result := workContextAttemptSummaryDTO{
		ID:            value.ID,
		Kind:          string(value.Kind),
		Status:        string(value.Status),
		FinishedAt:    copyTime(value.FinishedAt),
		ResultSummary: copyString(value.ResultSummary),
		NextSteps:     append([]string(nil), value.NextSteps...),
	}
	return &result
}

func activityItemDTOFromDomain(item domain.ActivityItem) activityItemDTO {
	result := activityItemDTO{
		EntityType: string(item.EntityType),
		EntityID:   item.EntityID,
		IssueID:    item.IssueID,
		OccurredAt: item.OccurredAt,
	}
	if item.Comment != nil {
		comment := commentDTOFromDomain(*item.Comment)
		result.Comment = &comment
	} else if item.Decision != nil {
		decision := recordDecisionDTOFromDomain(*item.Decision)
		result.Decision = &decision
	} else if item.Attempt != nil {
		attempt := attemptDTOFromDomain(*item.Attempt)
		result.Attempt = &attempt
	} else if item.AttemptNote != nil {
		note := attemptNoteDTOFromDomain(*item.AttemptNote)
		result.AttemptNote = &note
	} else if item.Event != nil {
		event := issueEventDTOFromDomain(*item.Event)
		result.Event = &event
	} else if item.Artifact != nil {
		artifact := artifactDTOFromDomain(*item.Artifact)
		result.Artifact = &artifact
	}
	return result
}

func issueActivityOutputFromDomain(activity domain.IssueActivity) issueActivityOutput {
	items := make([]activityItemDTO, len(activity.Items))
	for index, item := range activity.Items {
		items[index] = activityItemDTOFromDomain(item)
	}
	return issueActivityOutput{Items: items, NextCursor: copyString(activity.NextCursor), HasMore: activity.HasMore}
}

func graphOutputFromDomain(graph domain.GraphResult) graphOutput {
	nodes := make([]issueListItemDTO, len(graph.Nodes))
	for index, node := range graph.Nodes {
		nodes[index] = issueListItemDTO{
			issueDTO: issueDTOFromDomain(node.Issue), EffectiveStatus: string(node.EffectiveStatus),
			UnresolvedBlockerCount: node.UnresolvedBlockerCount, IsBlocked: node.IsBlocked, IsClaimable: node.IsClaimable,
			ActiveAttemptID: node.ActiveAttemptID,
		}
	}
	edges := make([]graphEdgeDTO, len(graph.Edges))
	for index, edge := range graph.Edges {
		edges[index] = graphEdgeDTO{SourceIssueID: edge.SourceIssueID, TargetIssueID: edge.TargetIssueID, Type: edge.Type}
	}
	return graphOutput{
		RootIssueID: graph.RootIssueID, Nodes: nodes, Edges: edges,
		EntryPoints: append([]string{}, graph.EntryPoints...), BlockingNodes: append([]string{}, graph.BlockingNodes...),
		Summary: graphSummaryDTO{NodeCount: graph.Summary.NodeCount, EdgeCount: graph.Summary.EdgeCount,
			EntryPointCount: graph.Summary.EntryPointCount, BlockingNodeCount: graph.Summary.BlockingNodeCount},
		Warnings: append([]string{}, graph.Warnings...), Truncated: graph.Truncated, TruncationReason: graph.TruncationReason,
	}
}
