package domain

import (
	"bytes"
	"encoding/json"
)

// LogicalProjectDocument is the logical project interchange document, versions
// 1 and 2. Version 1's fields are frozen -- see docs/07 §7 for the v2
// compatibility policy. Version 2 adds the review workflow entities
// (ReviewTargets/ReviewRequests/ReviewOutcomes/ReviewEvents) and a reserved
// top-level Extensions map that future work (gates, reservations) adds
// namespaced sections to instead of each independently claiming "version 2"
// or forcing a version bump; see ISSUE-215. All version-2 fields are
// optional: a version-1 document simply has none of them, and an absent v2
// array on import means empty, not "unset."
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

	// Version 2 fields. Omitted (nil/absent) in a version 1 document.
	ReviewTargets  []LogicalReviewTarget      `json:"review_targets,omitempty"`
	ReviewRequests []LogicalReviewRequest     `json:"review_requests,omitempty"`
	ReviewOutcomes []LogicalReviewOutcome     `json:"review_outcomes,omitempty"`
	ReviewEvents   []LogicalReviewEvent       `json:"review_events,omitempty"`
	Extensions     map[string]json.RawMessage `json:"extensions,omitempty"`
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

	// Source is the event's origin in the unified event log, either
	// LogicalEventSourceIssue or LogicalEventSourceReview. Version 2 only:
	// a version 1 document omits it and imports as "issue", because v1
	// predates the unified log (migration 008) and cannot express the
	// distinction. Carrying it is what lets a project that used reviews
	// round-trip without silently reclassifying its review lifecycle as
	// ordinary issue activity -- review staleness reads this column, so
	// losing it changes behaviour after a restore (ISSUE-215).
	Source string `json:"source,omitempty"`
}

// Event origins for LogicalEvent.Source, mirroring the issue_events.source
// column introduced by migration 008.
const (
	LogicalEventSourceIssue  = "issue"
	LogicalEventSourceReview = "review"
)

// LogicalReviewTarget is the exported immutable review-target snapshot
// (version 2). Mirrors domain.ReviewTarget.
//
// Purposes is the purpose set the target covers (docs/02 §17.5). It is
// omitted when it equals the compatibility default [implementation], so a
// project that never named a purpose exports exactly the document it
// exported before ISSUE-175 -- and a document that does carry a non-default
// purpose set fails loudly in an older build (whose frozen key table
// rejects the key) instead of silently importing with a security-review
// scope downgraded to the default.
type LogicalReviewTarget struct {
	ID            string   `json:"id"`
	IssueID       string   `json:"issue_id"`
	IssueVersion  int64    `json:"issue_version"`
	LatestEventID int64    `json:"latest_event_id"`
	ArtifactIDs   []string `json:"artifact_ids"`
	Purposes      []string `json:"purposes,omitempty"`
	CreatedAt     string   `json:"created_at"`
}

// LogicalReviewRequest is the exported review request record (version 2).
// Mirrors domain.ReviewRequest. Only non-active requests are exported --
// ActiveAttemptID is always absent, the same "cannot export a claim in
// progress" rule already applied to work_attempts (see LogicalAttempt) --
// so a claimed request round-trips as its pre-claim status would require
// (in practice: open, since claimed always implies an active attempt).
type LogicalReviewRequest struct {
	ID                 string   `json:"id"`
	TargetID           string   `json:"target_id"`
	IssueID            string   `json:"issue_id"`
	TargetIssueVersion int64    `json:"target_issue_version"`
	TargetEventID      int64    `json:"target_event_id"`
	ArtifactIDs        []string `json:"artifact_ids"`
	// Purposes follows LogicalReviewTarget.Purposes: omitted when it equals
	// the [implementation] compatibility default.
	Purposes     []string `json:"purposes,omitempty"`
	Status       string   `json:"status"`
	SupersedesID *string  `json:"supersedes_id"`
	CreatedAt    string   `json:"created_at"`
	ResolvedAt   *string  `json:"resolved_at"`
}

// LogicalReviewOutcome is the exported review resolution record (version 2).
// Mirrors domain.ReviewOutcomeRecord.
type LogicalReviewOutcome struct {
	ID        string  `json:"id"`
	RequestID string  `json:"request_id"`
	AttemptID string  `json:"attempt_id"`
	Outcome   string  `json:"outcome"`
	Reason    *string `json:"reason"`
	CreatedAt string  `json:"created_at"`
}

// LogicalReviewEvent is the exported review-workflow event record (version
// 2). There is no standalone review_events table to mirror -- migration 008
// folded it into issue_events (source='review') -- so this is derived from
// that unified log's review-sourced rows, with RequestID/TargetID pulled
// back out of the payload every review event already carries them in (see
// payloadForReviewEvent in internal/adapters/sqlite/reviews.go). SourceID
// plays the same role LogicalEvent.SourceID does for issue_events (a stable
// reference to the original row, not reused as a destination ID on import).
//
// ReviewEvents is export-only: every review-sourced row it contains is
// already present in the document's Events too (LogicalEvent's export query
// does not filter by source), so on import only Events is replayed --
// re-inserting ReviewEvents as well would duplicate those rows. ReviewEvents
// exists as a typed, review-scoped convenience projection for tools and
// humans reading the interchange document directly; its referential
// integrity is still checked at parse time like every other v2 entity.
type LogicalReviewEvent struct {
	SourceID  int64           `json:"source_id"`
	RequestID string          `json:"request_id"`
	TargetID  string          `json:"target_id"`
	AttemptID *string         `json:"attempt_id"`
	EventType string          `json:"event_type"`
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

// Logical interchange extension namespaces. Extensions is the version-2
// escape hatch ISSUE-215 reserved so a feature can add its own interchange
// section without redefining v2 for everyone or forcing a version bump.
// Each namespace carries its own independent version, so reservations can
// evolve their payload without touching the document version or any other
// namespace.
const (
	// LogicalReservationsExtensionKey is the Extensions key resource
	// reservations are carried under.
	LogicalReservationsExtensionKey = "reservations"
	// LogicalReservationsExtensionVersion is the current version of the
	// reservations namespace payload.
	LogicalReservationsExtensionVersion = 1
)

// LogicalReservationsExtension is the payload stored under
// Extensions["reservations"]. Version is the namespace's own version, not
// the document's.
type LogicalReservationsExtension struct {
	Version int                  `json:"version"`
	Records []LogicalReservation `json:"records"`
}

// LogicalReservation is one exported resource reservation. Only released
// reservations are exported and only released reservations import: an
// active reservation is owned by an active attempt, and active attempts do
// not cross the interchange boundary (see LogicalAttempt and
// readLogicalAttempts' status filter). ComparisonValue and NormalizedJSON
// are carried so an import is a faithful insert rather than a
// re-normalization under whatever rules happen to be current; they are
// inert for a released row (the active-identity unique index is partial on
// status = 'active'), and they are deliberately absent from the activity
// and search projections, which expose display values only.
type LogicalReservation struct {
	ID              string          `json:"id"`
	IssueID         string          `json:"issue_id"`
	AttemptID       string          `json:"attempt_id"`
	Kind            string          `json:"kind"`
	DisplayValue    string          `json:"display_value"`
	ComparisonValue string          `json:"comparison_value"`
	NormalizedJSON  json.RawMessage `json:"normalized_json"`
	Status          string          `json:"status"`
	CreatedAt       string          `json:"created_at"`
	ReleasedAt      string          `json:"released_at"`
	ReleaseReason   string          `json:"release_reason"`
}

// Workflow-gate interchange (ISSUE-175 AC3). Gates ride in the version-2
// extensions map under their own namespace, following the reservations
// precedent -- no document version bump (docs/07 §7).
const (
	// LogicalGatesExtensionKey is the Extensions key workflow-gate state is
	// carried under.
	LogicalGatesExtensionKey = "gates"
	// LogicalGatesExtensionVersion is the current version of the gates
	// namespace payload.
	LogicalGatesExtensionVersion = 1
)

// LogicalGatesExtension is the payload stored under Extensions["gates"]:
// the durable workflow-gate state -- policies with their audit trail,
// frozen requirement snapshots, attempt evidence with its audit trail, and
// purpose-scoped review approvals. Version is the namespace's own version,
// not the document's.
type LogicalGatesExtension struct {
	Version               int                               `json:"version"`
	Policies              []LogicalWorkflowPolicy           `json:"policies"`
	PolicyEvents          []LogicalWorkflowPolicyEvent      `json:"policy_events"`
	AttemptSnapshots      []LogicalAttemptGateSnapshot      `json:"attempt_snapshots"`
	ReviewTargetSnapshots []LogicalReviewTargetGateSnapshot `json:"review_target_snapshots"`
	Evidence              []LogicalGateEvidence             `json:"evidence"`
	EvidenceEvents        []LogicalGateEvidenceEvent        `json:"evidence_events"`
	ReviewApprovals       []LogicalReviewApproval           `json:"review_approvals"`
}

// LogicalWorkflowPolicy is one exported workflow policy. Selector and
// requirements are carried as their stored JSON, so an import is a faithful
// insert rather than a re-normalization under whatever rules happen to be
// current -- the same rule LogicalReservation applies to NormalizedJSON.
type LogicalWorkflowPolicy struct {
	ID               string          `json:"id"`
	SelectorJSON     json.RawMessage `json:"selector_json"`
	RequirementsJSON json.RawMessage `json:"requirements_json"`
	Status           string          `json:"status"`
	Version          int64           `json:"version"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
}

// LogicalWorkflowPolicyEvent is one exported policy audit event. SourceID
// plays the same role LogicalEvent.SourceID does (a stable reference to the
// original row, not reused as a destination ID); SessionID imports as NULL
// like every other session reference, since agent sessions do not cross the
// interchange boundary.
type LogicalWorkflowPolicyEvent struct {
	SourceID     int64           `json:"source_id"`
	PolicyID     string          `json:"policy_id"`
	EventType    string          `json:"event_type"`
	SessionID    *string         `json:"session_id"`
	PriorVersion *int64          `json:"prior_version"`
	NewVersion   int64           `json:"new_version"`
	Payload      json.RawMessage `json:"payload"`
	CreatedAt    string          `json:"created_at"`
}

// LogicalAttemptGateSnapshot is one exported claim-time requirement
// snapshot (docs/02 §17.6). The requirement and source-policy blobs are
// carried verbatim: the fingerprint is SHA-256 over the canonical snapshot
// payload, so rewriting embedded policy identities to destination IDs would
// falsify it. Embedded policy IDs are therefore frozen audit identities
// naming the source document's policies, exactly as issue-event payloads
// already keep their source-document references.
type LogicalAttemptGateSnapshot struct {
	AttemptID          string          `json:"attempt_id"`
	RequirementsJSON   json.RawMessage `json:"requirements_json"`
	SourcePoliciesJSON json.RawMessage `json:"source_policies_json"`
	Fingerprint        string          `json:"fingerprint"`
	IssueVersion       int64           `json:"issue_version"`
	CreatedAt          string          `json:"created_at"`
}

// LogicalReviewTargetGateSnapshot is one exported review-target requirement
// snapshot; see LogicalAttemptGateSnapshot for the verbatim-blob rule.
type LogicalReviewTargetGateSnapshot struct {
	TargetID           string          `json:"target_id"`
	RequirementsJSON   json.RawMessage `json:"requirements_json"`
	SourcePoliciesJSON json.RawMessage `json:"source_policies_json"`
	Fingerprint        string          `json:"fingerprint"`
	IssueVersion       int64           `json:"issue_version"`
	CreatedAt          string          `json:"created_at"`
}

// LogicalGateEvidence is one exported attempt evidence record. Only
// evidence owned by an exported (non-active) attempt crosses the boundary,
// the same rule reservations follow. ArtifactIDs are carried as the
// source-document identifiers, matching how review targets and requests
// carry theirs.
type LogicalGateEvidence struct {
	ID          string   `json:"id"`
	AttemptID   string   `json:"attempt_id"`
	IssueID     string   `json:"issue_id"`
	Key         string   `json:"key"`
	Result      string   `json:"result"`
	Summary     string   `json:"summary"`
	Details     *string  `json:"details"`
	ArtifactIDs []string `json:"artifact_ids"`
	Version     int64    `json:"version"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

// LogicalGateEvidenceEvent is one exported evidence audit event; see
// LogicalWorkflowPolicyEvent for the SourceID rule.
type LogicalGateEvidenceEvent struct {
	SourceID   int64           `json:"source_id"`
	EvidenceID string          `json:"evidence_id"`
	AttemptID  string          `json:"attempt_id"`
	IssueID    string          `json:"issue_id"`
	Key        string          `json:"key"`
	EventType  string          `json:"event_type"`
	Version    int64           `json:"version"`
	Payload    json.RawMessage `json:"payload"`
	CreatedAt  string          `json:"created_at"`
}

// LogicalReviewApproval is one exported immutable purpose-scoped review
// approval (docs/02 §17.5). Round-tripping these is what keeps a satisfied
// review_approval requirement satisfied after a restore.
type LogicalReviewApproval struct {
	ID                 string `json:"id"`
	IssueID            string `json:"issue_id"`
	TargetID           string `json:"target_id"`
	RequestID          string `json:"request_id"`
	AttemptID          string `json:"attempt_id"`
	Purpose            string `json:"purpose"`
	TargetIssueVersion int64  `json:"target_issue_version"`
	TargetEventID      int64  `json:"target_event_id"`
	Version            int64  `json:"version"`
	CreatedAt          string `json:"created_at"`
}

// DecodeGatesExtension returns the workflow-gate state carried in
// Extensions["gates"], or the zero extension when the namespace is absent.
// Strict like DecodeReservationsExtension: unknown keys and an unsupported
// namespace version fail loudly instead of importing with silently dropped
// gate state.
func (document LogicalProjectDocument) DecodeGatesExtension() (LogicalGatesExtension, error) {
	raw, present := document.Extensions[LogicalGatesExtensionKey]
	if !present || len(bytes.TrimSpace(raw)) == 0 {
		return LogicalGatesExtension{}, nil
	}
	path := "$.extensions." + LogicalGatesExtensionKey
	var extension LogicalGatesExtension
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&extension); err != nil {
		return LogicalGatesExtension{}, decodeError(err, path)
	}
	if extension.Version != LogicalGatesExtensionVersion {
		return LogicalGatesExtension{}, unsupportedFormatVersionError(path + ".version")
	}
	return extension, nil
}

// IsEmpty reports whether the extension carries no records at all, so the
// exporter can skip emitting the namespace and a gate-free project exports
// exactly the document it exported before.
func (extension LogicalGatesExtension) IsEmpty() bool {
	return len(extension.Policies) == 0 && len(extension.PolicyEvents) == 0 &&
		len(extension.AttemptSnapshots) == 0 && len(extension.ReviewTargetSnapshots) == 0 &&
		len(extension.Evidence) == 0 && len(extension.EvidenceEvents) == 0 &&
		len(extension.ReviewApprovals) == 0
}

// DecodeReservationsExtension returns the reservations carried in
// Extensions["reservations"], or nil when the namespace is absent. It is
// strict: unknown keys and a namespace version this build does not
// understand are errors, so a document written by a newer build fails
// loudly instead of importing with silently dropped reservations.
func (document LogicalProjectDocument) DecodeReservationsExtension() ([]LogicalReservation, error) {
	raw, present := document.Extensions[LogicalReservationsExtensionKey]
	if !present || len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	path := "$.extensions." + LogicalReservationsExtensionKey
	var extension LogicalReservationsExtension
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&extension); err != nil {
		return nil, decodeError(err, path)
	}
	if extension.Version != LogicalReservationsExtensionVersion {
		return nil, unsupportedFormatVersionError(path + ".version")
	}
	return extension.Records, nil
}
