package domain

import (
	"fmt"
	"slices"
	"time"
)

// WorkContextInclude identifies an optional work context section.
type WorkContextInclude string

const (
	WorkContextIncludeParentEpic                  WorkContextInclude = "parent_epic"
	WorkContextIncludeRelations                   WorkContextInclude = "relations"
	WorkContextIncludeRelatedIssueSummaries       WorkContextInclude = "related_issue_summaries"
	WorkContextIncludeRecentComments              WorkContextInclude = "recent_comments"
	WorkContextIncludeRecentAttemptNotes          WorkContextInclude = "recent_attempt_notes"
	WorkContextIncludeDecisionContent             WorkContextInclude = "decision_content"
	WorkContextIncludeAttemptHistory              WorkContextInclude = "attempt_history"
	WorkContextIncludeArtifacts                   WorkContextInclude = "artifacts"
	WorkContextIncludeProjectInstructions         WorkContextInclude = "project_instructions"
	WorkContextIncludeChangesSincePreviousAttempt WorkContextInclude = "changes_since_previous_attempt"
)

// AllWorkContextIncludes is the canonical ordering used by the domain contract.
var AllWorkContextIncludes = []WorkContextInclude{
	WorkContextIncludeParentEpic,
	WorkContextIncludeRelations,
	WorkContextIncludeRelatedIssueSummaries,
	WorkContextIncludeRecentComments,
	WorkContextIncludeRecentAttemptNotes,
	WorkContextIncludeDecisionContent,
	WorkContextIncludeAttemptHistory,
	WorkContextIncludeArtifacts,
	WorkContextIncludeProjectInstructions,
	WorkContextIncludeChangesSincePreviousAttempt,
}

const (
	MaxWorkContextIncludes = 10

	DefaultWorkContextRelatedIssueLimit      = 20
	DefaultWorkContextRecentCommentLimit     = 10
	DefaultWorkContextRecentAttemptNoteLimit = 10
	DefaultWorkContextDecisionContentLimit   = 10
	DefaultWorkContextAttemptHistoryLimit    = 10
	DefaultWorkContextArtifactLimit          = 20
	DefaultWorkContextChangesLimit           = 20
	MaxWorkContextSectionLimit               = 20
)

func (value WorkContextInclude) Valid() bool {
	switch value {
	case WorkContextIncludeParentEpic,
		WorkContextIncludeRelations,
		WorkContextIncludeRelatedIssueSummaries,
		WorkContextIncludeRecentComments,
		WorkContextIncludeRecentAttemptNotes,
		WorkContextIncludeDecisionContent,
		WorkContextIncludeAttemptHistory,
		WorkContextIncludeArtifacts,
		WorkContextIncludeProjectInstructions,
		WorkContextIncludeChangesSincePreviousAttempt:
		return true
	default:
		return false
	}
}

func (value WorkContextInclude) isListSection() bool {
	switch value {
	case WorkContextIncludeRelatedIssueSummaries,
		WorkContextIncludeRecentComments,
		WorkContextIncludeRecentAttemptNotes,
		WorkContextIncludeDecisionContent,
		WorkContextIncludeAttemptHistory,
		WorkContextIncludeArtifacts,
		WorkContextIncludeChangesSincePreviousAttempt:
		return true
	default:
		return false
	}
}

func (value WorkContextInclude) defaultLimit() int {
	switch value {
	case WorkContextIncludeRelatedIssueSummaries:
		return DefaultWorkContextRelatedIssueLimit
	case WorkContextIncludeRecentComments:
		return DefaultWorkContextRecentCommentLimit
	case WorkContextIncludeRecentAttemptNotes:
		return DefaultWorkContextRecentAttemptNoteLimit
	case WorkContextIncludeDecisionContent:
		return DefaultWorkContextDecisionContentLimit
	case WorkContextIncludeAttemptHistory:
		return DefaultWorkContextAttemptHistoryLimit
	case WorkContextIncludeArtifacts:
		return DefaultWorkContextArtifactLimit
	case WorkContextIncludeChangesSincePreviousAttempt:
		return DefaultWorkContextChangesLimit
	default:
		return 0
	}
}

// GetWorkContextInput is the normalized contract for a work-context request.
type GetWorkContextInput struct {
	IssueID string
	Include []WorkContextInclude
	Limits  map[WorkContextInclude]int
}

// Validate checks and normalizes a work-context request.
func (input GetWorkContextInput) Validate() (GetWorkContextInput, error) {
	identifier, err := ParseIssueIdentifier(input.IssueID)
	if err != nil {
		return GetWorkContextInput{}, err
	}

	if len(input.Include) > MaxWorkContextIncludes {
		return GetWorkContextInput{}, validationError("include", "OUT_OF_RANGE", fmt.Sprintf("must contain at most %d values", MaxWorkContextIncludes))
	}

	normalizedInclude := make([]WorkContextInclude, 0, len(input.Include))
	seenIncludes := make(map[WorkContextInclude]struct{}, len(input.Include))
	for _, value := range input.Include {
		if !value.Valid() {
			return GetWorkContextInput{}, invalidEnum("include", string(value))
		}
		if _, exists := seenIncludes[value]; exists {
			return GetWorkContextInput{}, validationError("include", "DUPLICATE", fmt.Sprintf("duplicate include %q", value))
		}
		seenIncludes[value] = struct{}{}
		normalizedInclude = append(normalizedInclude, value)
	}

	normalizedLimits := make(map[WorkContextInclude]int, len(input.Limits))
	requestedIncludes := make(map[WorkContextInclude]struct{}, len(normalizedInclude))
	for _, include := range normalizedInclude {
		requestedIncludes[include] = struct{}{}
	}

	for key, limit := range input.Limits {
		if !key.Valid() {
			return GetWorkContextInput{}, validationError("limits."+string(key), "INVALID_SHAPE", "must be a supported include")
		}
		if _, exists := requestedIncludes[key]; !exists {
			return GetWorkContextInput{}, validationError("limits."+string(key), "INVALID_SHAPE", "must be requested")
		}
		if !key.isListSection() {
			return GetWorkContextInput{}, validationError("limits."+string(key), "INVALID_SHAPE", "must be a list section")
		}
		if limit < 1 || limit > MaxWorkContextSectionLimit {
			return GetWorkContextInput{}, validationError("limits."+string(key), "OUT_OF_RANGE", fmt.Sprintf("must be between 1 and %d", MaxWorkContextSectionLimit))
		}
		normalizedLimits[key] = limit
	}

	for _, include := range normalizedInclude {
		if include.isListSection() {
			if _, exists := normalizedLimits[include]; !exists {
				normalizedLimits[include] = include.defaultLimit()
			}
		}
	}

	return GetWorkContextInput{
		IssueID: identifier.Value,
		Include: normalizedInclude,
		Limits:  normalizedLimits,
	}, nil
}

// WorkContextIssue is the compact issue projection used by default work context.
type WorkContextIssue struct {
	ID                     string
	DisplayID              string
	Title                  string
	Description            *string
	AcceptanceCriteria     *string
	EffectiveStatus        EffectiveStatus
	UnresolvedBlockerCount int64
	IsBlocked              bool
}

// WorkContextDecisionSummary is the compact issue decision projection used by default work context.
type WorkContextDecisionSummary struct {
	ID        string
	Title     string
	Summary   string
	Status    DecisionStatus
	CreatedAt time.Time
}

// WorkContextAttemptSummary is the compact attempt projection used by default work context.
type WorkContextAttemptSummary struct {
	ID            string
	Kind          AttemptKind
	Status        AttemptStatus
	FinishedAt    *time.Time
	ResultSummary *string
	NextSteps     []string
}

// WorkContext is the compact work-context domain contract.
type WorkContext struct {
	Issue           WorkContextIssue
	Blockers        []WorkContextIssue
	Decisions       []WorkContextDecisionSummary
	PreviousAttempt *WorkContextAttemptSummary
	Checkpoint      *AttemptNote
	Warnings        []string

	ParentEpic                  *WorkContextIssue
	Relations                   []IssueRelation
	RelatedIssueSummaries       []WorkContextIssue
	RecentComments              []Comment
	RecentAttemptNotes          []AttemptNote
	DecisionContent             []Decision
	AttemptHistory              []WorkAttempt
	Artifacts                   []Artifact
	ProjectInstructions         *string
	ChangesSincePreviousAttempt []IssueEvent

	Truncated         bool
	TruncatedSections []WorkContextInclude
}

// NewEmptyWorkContext returns a work context with every list field initialized to an empty nonnil slice.
func NewEmptyWorkContext() WorkContext {
	return WorkContext{
		Blockers:                    []WorkContextIssue{},
		Decisions:                   []WorkContextDecisionSummary{},
		Warnings:                    []string{},
		ParentEpic:                  nil,
		Relations:                   []IssueRelation{},
		RelatedIssueSummaries:       []WorkContextIssue{},
		RecentComments:              []Comment{},
		RecentAttemptNotes:          []AttemptNote{},
		DecisionContent:             []Decision{},
		AttemptHistory:              []WorkAttempt{},
		Artifacts:                   []Artifact{},
		ChangesSincePreviousAttempt: []IssueEvent{},
		TruncatedSections:           []WorkContextInclude{},
	}
}

// CloneWorkContext produces a deep copy of a work context.
func CloneWorkContext(value WorkContext) WorkContext {
	result := value
	result.Issue = cloneWorkContextIssue(value.Issue)
	result.Blockers = cloneWorkContextIssues(value.Blockers)
	result.Decisions = cloneWorkContextDecisionSummaries(value.Decisions)
	result.PreviousAttempt = cloneWorkContextAttemptSummary(value.PreviousAttempt)
	result.Checkpoint = cloneAttemptNote(value.Checkpoint)
	result.Warnings = cloneStringSlice(value.Warnings)
	result.ParentEpic = cloneWorkContextIssuePointer(value.ParentEpic)
	result.Relations = cloneIssueRelations(value.Relations)
	result.RelatedIssueSummaries = cloneWorkContextIssues(value.RelatedIssueSummaries)
	result.RecentComments = cloneComments(value.RecentComments)
	result.RecentAttemptNotes = cloneAttemptNotes(value.RecentAttemptNotes)
	result.DecisionContent = cloneDecisions(value.DecisionContent)
	result.AttemptHistory = cloneWorkAttempts(value.AttemptHistory)
	result.Artifacts = CloneArtifacts(value.Artifacts)
	result.ProjectInstructions = copyOptionalString(value.ProjectInstructions)
	result.ChangesSincePreviousAttempt = cloneIssueEvents(value.ChangesSincePreviousAttempt)
	result.TruncatedSections = cloneWorkContextIncludes(value.TruncatedSections)
	return result
}

func cloneWorkContextIssue(value WorkContextIssue) WorkContextIssue {
	result := value
	result.Description = cloneOptionalString(value.Description)
	result.AcceptanceCriteria = cloneOptionalString(value.AcceptanceCriteria)
	return result
}

func cloneWorkContextIssuePointer(value *WorkContextIssue) *WorkContextIssue {
	if value == nil {
		return nil
	}
	clone := cloneWorkContextIssue(*value)
	return &clone
}

func cloneWorkContextIssues(values []WorkContextIssue) []WorkContextIssue {
	if values == nil {
		return nil
	}
	result := make([]WorkContextIssue, len(values))
	for index, value := range values {
		result[index] = cloneWorkContextIssue(value)
	}
	return result
}

func cloneWorkContextDecisionSummaries(values []WorkContextDecisionSummary) []WorkContextDecisionSummary {
	if values == nil {
		return nil
	}
	result := make([]WorkContextDecisionSummary, len(values))
	for index, value := range values {
		result[index] = cloneWorkContextDecisionSummary(value)
	}
	return result
}

func cloneWorkContextDecisionSummary(value WorkContextDecisionSummary) WorkContextDecisionSummary {
	return value
}

func cloneWorkContextAttemptSummary(value *WorkContextAttemptSummary) *WorkContextAttemptSummary {
	if value == nil {
		return nil
	}
	result := *value
	result.FinishedAt = cloneTimePointer(value.FinishedAt)
	result.ResultSummary = cloneOptionalString(value.ResultSummary)
	result.NextSteps = cloneStringSlice(value.NextSteps)
	return &result
}

func cloneAttemptNote(value *AttemptNote) *AttemptNote {
	if value == nil {
		return nil
	}
	result := *value
	result.NextSteps = cloneStringSlice(value.NextSteps)
	return &result
}

func cloneStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	return slices.Clone(values)
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneIssueRelations(values []IssueRelation) []IssueRelation {
	if values == nil {
		return nil
	}
	result := make([]IssueRelation, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func cloneComments(values []Comment) []Comment {
	if values == nil {
		return nil
	}
	result := make([]Comment, len(values))
	for index, value := range values {
		result[index] = CloneComment(value)
	}
	return result
}

func cloneAttemptNotes(values []AttemptNote) []AttemptNote {
	if values == nil {
		return nil
	}
	result := make([]AttemptNote, len(values))
	for index, value := range values {
		result[index] = *cloneAttemptNote(&value)
	}
	return result
}

func cloneDecisions(values []Decision) []Decision {
	if values == nil {
		return nil
	}
	result := make([]Decision, len(values))
	for index, value := range values {
		result[index] = CloneDecision(value)
	}
	return result
}

func cloneWorkAttempts(values []WorkAttempt) []WorkAttempt {
	if values == nil {
		return nil
	}
	result := make([]WorkAttempt, len(values))
	for index, value := range values {
		result[index] = cloneWorkAttempt(value)
	}
	return result
}

func cloneWorkAttempt(value WorkAttempt) WorkAttempt {
	result := value
	result.SessionID = cloneOptionalString(value.SessionID)
	result.AgentLabel = cloneOptionalString(value.AgentLabel)
	result.FinishedAt = cloneTimePointer(value.FinishedAt)
	result.ResultSummary = cloneOptionalString(value.ResultSummary)
	result.NextSteps = cloneStringSlice(value.NextSteps)
	result.Verification = cloneStringSlice(value.Verification)
	result.FailureReasonCode = cloneFailureReasonCode(value.FailureReasonCode)
	result.InterruptionReasonCode = cloneInterruptionReasonCode(value.InterruptionReasonCode)
	result.ReasonDetails = cloneOptionalString(value.ReasonDetails)
	return result
}

func cloneFailureReasonCode(value *FailureReasonCode) *FailureReasonCode {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInterruptionReasonCode(value *InterruptionReasonCode) *InterruptionReasonCode {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneIssueEvents(values []IssueEvent) []IssueEvent {
	if values == nil {
		return nil
	}
	result := make([]IssueEvent, len(values))
	for index, value := range values {
		result[index] = CloneIssueEvent(value)
	}
	return result
}

func cloneWorkContextIncludes(values []WorkContextInclude) []WorkContextInclude {
	if values == nil {
		return nil
	}
	return slices.Clone(values)
}
