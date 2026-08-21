package domain

import (
	"slices"
	"sort"
	"strings"
	"time"
)

// See docs/02 §17 for the full workflow policy and gate evaluation contract
// this file, workflow_gate.go, and workflow_gate_fuzz_test.go implement.

const (
	// MaxPolicyRequirements bounds the requirement count on one policy.
	MaxPolicyRequirements = 50
	// MaxPolicyLabelsAll bounds the labels_all entry count on one selector.
	MaxPolicyLabelsAll = 20
	// MaxPolicyKeyRunes bounds a requirement key, evidence_key, or purpose.
	MaxPolicyKeyRunes = 128
)

// PolicyStatus identifies whether a workflow policy currently contributes
// requirements.
type PolicyStatus string

const (
	PolicyStatusActive   PolicyStatus = "active"
	PolicyStatusArchived PolicyStatus = "archived"
)

// Valid reports whether status is a known policy status.
func (status PolicyStatus) Valid() bool {
	switch status {
	case PolicyStatusActive, PolicyStatusArchived:
		return true
	default:
		return false
	}
}

// RequirementKind identifies the closed set of requirement payload shapes a
// policy requirement can carry.
type RequirementKind string

const (
	RequirementKindIssueFieldNonblank RequirementKind = "issue_field_nonblank"
	RequirementKindAttemptEvidence    RequirementKind = "attempt_evidence"
	RequirementKindReviewApproval     RequirementKind = "review_approval"
)

// Valid reports whether kind is one of the three supported requirement kinds.
func (kind RequirementKind) Valid() bool {
	switch kind {
	case RequirementKindIssueFieldNonblank, RequirementKindAttemptEvidence, RequirementKindReviewApproval:
		return true
	default:
		return false
	}
}

// appliesAt reports whether kind is evaluated at the given enforcement point.
// Applicability is fixed by kind (docs/02 §17.4); it is not independently
// configurable per policy or per requirement.
func (kind RequirementKind) appliesAt(point EnforcementPoint) bool {
	switch kind {
	case RequirementKindIssueFieldNonblank:
		return point == EnforcementPointClaimWork || point == EnforcementPointCompleteWorkToReview || point == EnforcementPointCompleteWorkToDone
	case RequirementKindAttemptEvidence:
		return point == EnforcementPointCompleteWorkToReview || point == EnforcementPointCompleteWorkToDone
	case RequirementKindReviewApproval:
		return point == EnforcementPointCompleteWorkToDone || point == EnforcementPointApproveReview
	default:
		return false
	}
}

// PolicySelector determines which executable issues a policy's requirements
// apply to. An empty IssueTypes matches every executable type (task, bug);
// epic is never a valid selector member since epics are never executable
// targets. Every LabelsAll value must be present on the issue
// (case-insensitive, matching label-name normalization, §10); an empty
// LabelsAll applies no label constraint. There is no labels_any, override,
// priority, or exclusion.
type PolicySelector struct {
	IssueTypes []Type
	LabelsAll  []string
}

// Matches reports whether the selector matches an issue of issueType
// carrying issueLabels (caller-spelling display names; matched
// case-insensitively via ASCII fold, consistent with label storage).
func (selector PolicySelector) Matches(issueType Type, issueLabels []string) bool {
	if issueType == TypeEpic {
		return false
	}
	if len(selector.IssueTypes) > 0 && !slices.Contains(selector.IssueTypes, issueType) {
		return false
	}
	if len(selector.LabelsAll) == 0 {
		return true
	}
	folded := make(map[string]struct{}, len(issueLabels))
	for _, label := range issueLabels {
		folded[asciiLower(strings.TrimSpace(label))] = struct{}{}
	}
	for _, required := range selector.LabelsAll {
		if _, ok := folded[asciiLower(strings.TrimSpace(required))]; !ok {
			return false
		}
	}
	return true
}

// PolicySelectorInput is caller-supplied selector input before validation.
type PolicySelectorInput struct {
	IssueTypes []Type
	LabelsAll  []string
}

// Validate validates and normalizes a selector: issue types must be
// executable (task or bug, never epic), with no duplicates; labels_all
// entries are validated, ASCII-folded for comparison, deduplicated by fold,
// and bounded at MaxPolicyLabelsAll.
func (input PolicySelectorInput) Validate() (PolicySelector, error) {
	types, err := CopyBounded("selector.issue_types", input.IssueTypes, len(executableTypes))
	if err != nil {
		return PolicySelector{}, err
	}
	seenTypes := make(map[Type]struct{}, len(types))
	for _, issueType := range types {
		if issueType == TypeEpic || !issueType.Valid() {
			return PolicySelector{}, validationError("selector.issue_types", "UNSUPPORTED_TYPE", "must be task or bug, never epic")
		}
		if _, exists := seenTypes[issueType]; exists {
			return PolicySelector{}, validationError("selector.issue_types", "DUPLICATE", "must not contain duplicate values")
		}
		seenTypes[issueType] = struct{}{}
	}

	rawLabels, err := CopyBounded("selector.labels_all", input.LabelsAll, MaxPolicyLabelsAll)
	if err != nil {
		return PolicySelector{}, err
	}
	labels := make([]string, 0, len(rawLabels))
	seenLabels := make(map[string]struct{}, len(rawLabels))
	for _, name := range rawLabels {
		display, folded, err := NormalizeLabelName(name)
		if err != nil {
			return PolicySelector{}, err
		}
		if _, exists := seenLabels[folded]; exists {
			return PolicySelector{}, validationError("selector.labels_all", "DUPLICATE", "must not contain duplicate names")
		}
		seenLabels[folded] = struct{}{}
		labels = append(labels, display)
	}
	sort.Strings(labels)

	return PolicySelector{IssueTypes: types, LabelsAll: labels}, nil
}

var executableTypes = []Type{TypeTask, TypeBug}

// PolicyRequirementInput is caller-supplied requirement input before
// validation and before a policy ID is assigned.
type PolicyRequirementInput struct {
	Key         string
	Kind        RequirementKind
	Field       string
	EvidenceKey string
	Purpose     string
}

// PolicyRequirement is one validated, policy-owned requirement. PolicyID is
// the owning policy's ULID, assigned when the requirement is persisted.
type PolicyRequirement struct {
	PolicyID    string
	Key         string
	Kind        RequirementKind
	Field       string
	EvidenceKey string
	Purpose     string
}

func (input PolicyRequirementInput) validate() (PolicyRequirement, error) {
	if err := ValidateText("key", input.Key, MaxPolicyKeyRunes); err != nil {
		return PolicyRequirement{}, err
	}
	key := strings.TrimSpace(input.Key)
	if key == "" {
		return PolicyRequirement{}, validationError("key", "REQUIRED", "must not be blank")
	}
	if !input.Kind.Valid() {
		return PolicyRequirement{}, invalidEnum("kind", string(input.Kind))
	}

	result := PolicyRequirement{Key: key, Kind: input.Kind}
	switch input.Kind {
	case RequirementKindIssueFieldNonblank:
		if input.Field != "acceptance_criteria" {
			return PolicyRequirement{}, validationError("field", "UNSUPPORTED_FIELD", "only 'acceptance_criteria' is supported")
		}
		if input.EvidenceKey != "" || input.Purpose != "" {
			return PolicyRequirement{}, validationError("kind", "UNEXPECTED_FIELD", "issue_field_nonblank must not set evidence_key or purpose")
		}
		result.Field = input.Field
	case RequirementKindAttemptEvidence:
		if input.Field != "" || input.Purpose != "" {
			return PolicyRequirement{}, validationError("kind", "UNEXPECTED_FIELD", "attempt_evidence must not set field or purpose")
		}
		if err := ValidateText("evidence_key", input.EvidenceKey, MaxPolicyKeyRunes); err != nil {
			return PolicyRequirement{}, err
		}
		evidenceKey := strings.TrimSpace(input.EvidenceKey)
		if evidenceKey == "" {
			return PolicyRequirement{}, validationError("evidence_key", "REQUIRED", "must not be blank")
		}
		result.EvidenceKey = evidenceKey
	case RequirementKindReviewApproval:
		if input.Field != "" || input.EvidenceKey != "" {
			return PolicyRequirement{}, validationError("kind", "UNEXPECTED_FIELD", "review_approval must not set field or evidence_key")
		}
		if err := ValidateText("purpose", input.Purpose, MaxPolicyKeyRunes); err != nil {
			return PolicyRequirement{}, err
		}
		purpose := strings.TrimSpace(input.Purpose)
		if purpose == "" {
			return PolicyRequirement{}, validationError("purpose", "REQUIRED", "must not be blank")
		}
		result.Purpose = purpose
	}
	return result, nil
}

// WorkflowPolicyInput is caller-supplied policy input before validation and
// before an ID is assigned.
type WorkflowPolicyInput struct {
	Selector     PolicySelectorInput
	Requirements []PolicyRequirementInput
}

// ValidatedWorkflowPolicyInput is a WorkflowPolicyInput that has passed
// Validate: a normalized selector plus policy-ID-less requirements, ready to
// be assigned an ID and persisted.
type ValidatedWorkflowPolicyInput struct {
	Selector     PolicySelector
	Requirements []PolicyRequirementInput
}

// Validate validates a policy's selector and every requirement, rejecting
// unsupported fields or kinds, duplicate keys, epic selectors, bad labels,
// invalid purposes, and limit violations with stable details. It does not
// assign a policy ID or requirement PolicyID: those are assigned by the
// caller (the persistence layer) once validation succeeds.
func (input WorkflowPolicyInput) Validate() (ValidatedWorkflowPolicyInput, error) {
	selector, err := input.Selector.Validate()
	if err != nil {
		return ValidatedWorkflowPolicyInput{}, err
	}
	requirements, err := CopyBounded("requirements", input.Requirements, MaxPolicyRequirements)
	if err != nil {
		return ValidatedWorkflowPolicyInput{}, err
	}
	seenKeys := make(map[string]struct{}, len(requirements))
	validated := make([]PolicyRequirementInput, 0, len(requirements))
	for _, requirement := range requirements {
		normalized, err := requirement.validate()
		if err != nil {
			return ValidatedWorkflowPolicyInput{}, err
		}
		if _, exists := seenKeys[normalized.Key]; exists {
			return ValidatedWorkflowPolicyInput{}, validationError("requirements", "DUPLICATE_KEY", "must not contain duplicate keys")
		}
		seenKeys[normalized.Key] = struct{}{}
		validated = append(validated, PolicyRequirementInput{
			Key: normalized.Key, Kind: normalized.Kind, Field: normalized.Field,
			EvidenceKey: normalized.EvidenceKey, Purpose: normalized.Purpose,
		})
	}
	return ValidatedWorkflowPolicyInput{Selector: selector, Requirements: validated}, nil
}

// WorkflowPolicy is one persisted, ID-bearing policy: a matching selector
// plus its ordered, owned requirements.
type WorkflowPolicy struct {
	ID           string
	Selector     PolicySelector
	Status       PolicyStatus
	Version      int64
	Requirements []PolicyRequirement
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CloneWorkflowPolicy returns a policy with no shared slice data.
func CloneWorkflowPolicy(policy WorkflowPolicy) WorkflowPolicy {
	policy.Selector.IssueTypes = slices.Clone(policy.Selector.IssueTypes)
	policy.Selector.LabelsAll = slices.Clone(policy.Selector.LabelsAll)
	policy.Requirements = slices.Clone(policy.Requirements)
	return policy
}

// MatchWorkflowPolicies computes the effective requirement set for an issue
// per docs/02 §17.3: every active policy whose selector matches contributes
// its requirements; the result is the union of all matching policies'
// requirements, ordered by policy ID then key, and deduplicated only when
// both policy ID and key are identical. It performs no storage reads;
// policies must already be materialized by the caller.
func MatchWorkflowPolicies(policies []WorkflowPolicy, issueType Type, issueLabels []string) []PolicyRequirement {
	matched := make([]PolicyRequirement, 0)
	for _, policy := range policies {
		if policy.Status != PolicyStatusActive {
			continue
		}
		if !policy.Selector.Matches(issueType, issueLabels) {
			continue
		}
		for _, requirement := range policy.Requirements {
			matched = append(matched, requirement)
		}
	}
	slices.SortFunc(matched, func(a, b PolicyRequirement) int {
		if a.PolicyID != b.PolicyID {
			if a.PolicyID < b.PolicyID {
				return -1
			}
			return 1
		}
		if a.Key != b.Key {
			if a.Key < b.Key {
				return -1
			}
			return 1
		}
		return 0
	})
	deduped := matched[:0:0]
	for index, requirement := range matched {
		if index > 0 && requirement.PolicyID == matched[index-1].PolicyID && requirement.Key == matched[index-1].Key {
			continue
		}
		deduped = append(deduped, requirement)
	}
	return deduped
}
