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
	WorkContextIncludeResourceReservations        WorkContextInclude = "resource_reservations"
	WorkContextIncludeReservationConflicts        WorkContextInclude = "reservation_conflicts"
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
	WorkContextIncludeResourceReservations,
	WorkContextIncludeReservationConflicts,
}

const (
	MaxWorkContextIncludes = 12

	DefaultWorkContextRelatedIssueLimit        = 20
	DefaultWorkContextRecentCommentLimit       = 10
	DefaultWorkContextRecentAttemptNoteLimit   = 10
	DefaultWorkContextDecisionContentLimit     = 10
	DefaultWorkContextAttemptHistoryLimit      = 10
	DefaultWorkContextArtifactLimit            = 20
	DefaultWorkContextChangesLimit             = 20
	DefaultWorkContextResourceReservationLimit = 10
	DefaultWorkContextReservationConflictLimit = 10
	MaxWorkContextSectionLimit                 = 20
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
		WorkContextIncludeChangesSincePreviousAttempt,
		WorkContextIncludeResourceReservations,
		WorkContextIncludeReservationConflicts:
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
		WorkContextIncludeChangesSincePreviousAttempt,
		WorkContextIncludeResourceReservations,
		WorkContextIncludeReservationConflicts:
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
	case WorkContextIncludeResourceReservations:
		return DefaultWorkContextResourceReservationLimit
	case WorkContextIncludeReservationConflicts:
		return DefaultWorkContextReservationConflictLimit
	default:
		return 0
	}
}

// GetWorkContextInput is the normalized contract for a work-context request.
//
// DesiredResources is an optional, caller-supplied set of resources to
// diagnose against the project's currently active reservations -- not
// resources the caller is acquiring. Per ISSUE-176's "reservations do not
// change issue dependency state" decision, a conflict can only be diagnosed
// once a caller supplies a concrete desired set; there is no issue-level
// intended-resource state to infer one from. When non-empty, it drives the
// default-context conflict warning and, with reservation_conflicts
// requested, the bounded conflict rows -- independent of whether
// reservation_conflicts is itself requested.
type GetWorkContextInput struct {
	IssueID          string
	Include          []WorkContextInclude
	Limits           map[WorkContextInclude]int
	DesiredResources []Resource
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

	var desiredResources []Resource
	if len(input.DesiredResources) > 0 {
		if _, err := PrepareReservationRequest(input.DesiredResources); err != nil {
			return GetWorkContextInput{}, relabelResourcesField(err, "desired_resources")
		}
		desiredResources = append([]Resource(nil), input.DesiredResources...)
	}

	return GetWorkContextInput{
		IssueID:          identifier.Value,
		Include:          normalizedInclude,
		Limits:           normalizedLimits,
		DesiredResources: desiredResources,
	}, nil
}

// relabelResourcesField rewrites a PrepareReservationRequest validation
// error's top-level "resources" detail field to fieldName. Per-resource
// errors (bad kind/path/namespace/name) already carry their own specific
// field name from Normalize and are left untouched; only the batch-level
// REQUIRED/MAX_ITEMS/INTERNAL_OVERLAP details say "resources" literally --
// correct for reserve_resources' actual "resources" input field, but wrong
// for get_work_context, whose caller-supplied field is "desired_resources".
func relabelResourcesField(err error, fieldName string) error {
	domainErr, ok := err.(*Error)
	if !ok {
		return err
	}
	details := make([]Detail, len(domainErr.Details))
	for index, detail := range domainErr.Details {
		if detail.Field == "resources" {
			detail.Field = fieldName
		}
		details[index] = detail
	}
	return NewError(domainErr.Code, domainErr.Message, domainErr.Retryable, details...)
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

// WorkContextReview is the compact review projection used by default work context.
type WorkContextReview struct {
	ID                 string
	Status             ReviewRequestStatus
	TargetIssueVersion int64
	TargetEventID      int64
	ArtifactIDs        []string
	Outcome            *ReviewOutcomeRecord
	Reason             *string
	FollowUpID         *string
	Claimable          bool
	CreatedAt          time.Time
	ResolvedAt         *time.Time
}

// ReservationConflict pairs one desired resource (identified by its index
// into the caller-supplied DesiredResources) with one currently active
// reservation, elsewhere in the project, that it overlaps -- the same
// Overlaps check acquireReservationsForAttempt runs at claim/reserve time,
// run here as a read-only diagnostic. IssueDisplayID/IssueTitle/SessionLabel
// identify the conflicting reservation's owner without exposing its lease
// token.
type ReservationConflict struct {
	DesiredIndex        int
	DesiredKind         ResourceKind
	DesiredDisplayValue string
	Existing            Reservation
	IssueDisplayID      string
	IssueTitle          string
	SessionLabel        *string
	LeaseExpiresAt      time.Time
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
	Reviews                     []WorkContextReview
	ProjectInstructions         *string
	ChangesSincePreviousAttempt []IssueEvent

	// ActiveReservationCount is the issue's active attempt's currently held
	// active reservation count -- always populated, independent of whether
	// resource_reservations is requested (ISSUE-181's default-context
	// "counts and a warning" behavior).
	ActiveReservationCount int64
	// ResourceReservations is the issue's own reservations, active and
	// released, newest first; populated only when resource_reservations is
	// requested.
	ResourceReservations []Reservation
	// ConflictCount is how many DesiredResources/ReservationConflicts
	// entries were found; always populated (0 when DesiredResources is
	// empty), independent of whether reservation_conflicts is requested.
	ConflictCount int64
	// ReservationConflicts is bounded, populated only when
	// reservation_conflicts is requested and DesiredResources is non-empty.
	ReservationConflicts []ReservationConflict

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
		Reviews:                     []WorkContextReview{},
		ChangesSincePreviousAttempt: []IssueEvent{},
		ResourceReservations:        []Reservation{},
		ReservationConflicts:        []ReservationConflict{},
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
	result.Reviews = cloneWorkContextReviews(value.Reviews)
	result.ProjectInstructions = copyOptionalString(value.ProjectInstructions)
	result.ChangesSincePreviousAttempt = cloneIssueEvents(value.ChangesSincePreviousAttempt)
	result.ResourceReservations = cloneReservations(value.ResourceReservations)
	result.ReservationConflicts = cloneReservationConflicts(value.ReservationConflicts)
	result.TruncatedSections = cloneWorkContextIncludes(value.TruncatedSections)
	return result
}

func cloneReservations(values []Reservation) []Reservation {
	if values == nil {
		return nil
	}
	result := make([]Reservation, len(values))
	for index, value := range values {
		result[index] = CloneReservation(value)
	}
	return result
}

func cloneReservationConflicts(values []ReservationConflict) []ReservationConflict {
	if values == nil {
		return nil
	}
	result := make([]ReservationConflict, len(values))
	for index, value := range values {
		result[index] = cloneReservationConflict(value)
	}
	return result
}

func cloneReservationConflict(value ReservationConflict) ReservationConflict {
	result := value
	result.Existing = CloneReservation(value.Existing)
	result.SessionLabel = cloneOptionalString(value.SessionLabel)
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

func cloneWorkContextReviews(values []WorkContextReview) []WorkContextReview {
	if values == nil {
		return nil
	}
	result := make([]WorkContextReview, len(values))
	for index, value := range values {
		result[index] = cloneWorkContextReview(value)
	}
	return result
}

func cloneWorkContextReview(value WorkContextReview) WorkContextReview {
	result := value
	result.ArtifactIDs = cloneStringSlice(value.ArtifactIDs)
	result.Outcome = cloneReviewOutcomeRecord(value.Outcome)
	result.Reason = cloneOptionalString(value.Reason)
	result.FollowUpID = cloneOptionalString(value.FollowUpID)
	result.ResolvedAt = cloneTimePointer(value.ResolvedAt)
	return result
}

func cloneReviewOutcomeRecord(value *ReviewOutcomeRecord) *ReviewOutcomeRecord {
	if value == nil {
		return nil
	}
	result := *value
	result.Reason = cloneOptionalString(value.Reason)
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
	copy(result, values)
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
