package domain

import (
	"sort"
	"strings"
)

// IssuePlan is the bounded, transport-independent request used to validate and
// apply a set of new issues, relations, and decisions.
type IssuePlan struct {
	Issues    []PlannedIssue    `json:"issues"`
	Relations []PlannedRelation `json:"relations"`
	Decisions []PlannedDecision `json:"decisions"`
}

type PlannedIssue struct {
	Ref                 string   `json:"ref,omitempty"`
	Type                Type     `json:"type"`
	Title               string   `json:"title"`
	Description         *string  `json:"description,omitempty"`
	AcceptanceCriteria  *string  `json:"acceptance_criteria,omitempty"`
	Status              Status   `json:"status,omitempty"`
	Priority            Priority `json:"priority,omitempty"`
	ParentRef           *string  `json:"parent_ref,omitempty"`
	BlockedReason       *string  `json:"blocked_reason,omitempty"`
	Labels              []string `json:"labels,omitempty"`
	CreateMissingLabels bool     `json:"create_missing_labels,omitempty"`
}

type PlannedRelation struct {
	SourceRef string       `json:"source_ref"`
	TargetRef string       `json:"target_ref"`
	Type      RelationType `json:"type"`
}

type PlannedDecision struct {
	IssueRef *string `json:"issue_ref,omitempty"`
	Title    string  `json:"title"`
	Summary  string  `json:"summary"`
	Content  string  `json:"content"`
	Status   string  `json:"status,omitempty"`
}

type PlanSummary struct {
	IssueCount           int `json:"issue_count"`
	RelationCount        int `json:"relation_count"`
	DecisionCount        int `json:"decision_count"`
	LabelAssignmentCount int `json:"label_assignment_count"`
}

// PlanValidation is the deterministic result returned from plan validation.
type PlanValidation struct {
	Valid          bool        `json:"valid"`
	Errors         []Detail    `json:"errors"`
	Warnings       []string    `json:"warnings"`
	Summary        PlanSummary `json:"summary"`
	NormalizedPlan IssuePlan   `json:"normalized_plan"`
}

// Normalize validates all request-local plan rules. References which point to
// existing issues are resolved by the planning repository.
func NormalizeIssuePlan(input IssuePlan) PlanValidation {
	result := PlanValidation{Warnings: []string{}, Errors: []Detail{}, NormalizedPlan: IssuePlan{
		Issues:    append([]PlannedIssue(nil), input.Issues...),
		Relations: append([]PlannedRelation(nil), input.Relations...),
		Decisions: append([]PlannedDecision(nil), input.Decisions...),
	}}
	result.Summary = PlanSummary{IssueCount: len(input.Issues), RelationCount: len(input.Relations), DecisionCount: len(input.Decisions)}
	if len(input.Issues) > MaxBatchIssues {
		result.Errors = append(result.Errors, planDetail(nil, "issues", "MAX_ITEMS", "maximum 50"))
	}
	if len(input.Relations) > MaxRelationsPerOperation {
		result.Errors = append(result.Errors, planDetail(nil, "relations", "MAX_ITEMS", "maximum 100"))
	}
	if len(input.Decisions) > MaxBatchDecisions {
		result.Errors = append(result.Errors, planDetail(nil, "decisions", "MAX_ITEMS", "maximum 20"))
	}

	refs := make(map[string]int)
	for i := range result.NormalizedPlan.Issues {
		issue := &result.NormalizedPlan.Issues[i]
		index := i
		result.Summary.LabelAssignmentCount += len(issue.Labels)
		if issue.Ref != "" {
			if err := ValidateText("ref", issue.Ref, MaxLocalRefRunes); err != nil {
				result.Errors = append(result.Errors, detailForPlanError(index, "issues["+itoa(i)+"]", err))
			} else if strings.TrimSpace(issue.Ref) == "" {
				result.Errors = append(result.Errors, planDetail(&index, "issues["+itoa(i)+"].ref", "REQUIRED", "must not be blank"))
			} else if _, err := ParseIssueIdentifier(issue.Ref); err == nil {
				result.Errors = append(result.Errors, planDetail(&index, "issues["+itoa(i)+"].ref", "RESERVED_REF", "must not be an issue identifier"))
			} else if previous, found := refs[issue.Ref]; found {
				_ = previous
				result.Errors = append(result.Errors, planDetail(&index, "issues["+itoa(i)+"].ref", "DUPLICATE_REF", "must be unique"))
			} else {
				refs[issue.Ref] = i
			}
		}
		normalized, err := (CreateIssueInput{
			Type: issue.Type, Title: issue.Title, Description: issue.Description, AcceptanceCriteria: issue.AcceptanceCriteria,
			Status: issue.Status, Priority: issue.Priority, BlockedReason: issue.BlockedReason, Labels: issue.Labels,
			CreateMissingLabels: issue.CreateMissingLabels,
		}).Validate()
		if err != nil {
			result.Errors = append(result.Errors, detailForPlanError(index, "issues["+itoa(i)+"]", err))
			continue
		}
		issue.Type, issue.Title, issue.Description, issue.AcceptanceCriteria = normalized.Type, normalized.Title, normalized.Description, normalized.AcceptanceCriteria
		issue.Status, issue.Priority, issue.BlockedReason, issue.Labels = normalized.Status, normalized.Priority, normalized.BlockedReason, normalized.Labels
		if issue.ParentRef != nil {
			if err := ValidateText("parent_ref", *issue.ParentRef, MaxLocalRefRunes); err != nil {
				result.Errors = append(result.Errors, detailForPlanError(index, "issues["+itoa(i)+"]", err))
			}
		}
	}
	if result.Summary.LabelAssignmentCount > MaxBatchLabelAssignments {
		result.Errors = append(result.Errors, planDetail(nil, "issues.labels", "MAX_ITEMS", "maximum 50 label assignments"))
	}
	for i := range result.NormalizedPlan.Relations {
		relation := &result.NormalizedPlan.Relations[i]
		index := i
		for field, value := range map[string]string{"source_ref": relation.SourceRef, "target_ref": relation.TargetRef} {
			if err := ValidateText(field, value, MaxLocalRefRunes); err != nil {
				result.Errors = append(result.Errors, detailForPlanError(index, "relations["+itoa(i)+"]", err))
			} else if strings.TrimSpace(value) == "" {
				result.Errors = append(result.Errors, planDetail(&index, "relations["+itoa(i)+"]."+field, "REQUIRED", "must not be blank"))
			}
		}
		if !relation.Type.Valid() {
			result.Errors = append(result.Errors, planDetail(&index, "relations["+itoa(i)+"].type", "INVALID_ENUM", string(relation.Type)))
		}
		if relation.SourceRef == relation.TargetRef && relation.SourceRef != "" {
			result.Errors = append(result.Errors, planDetail(&index, "relations["+itoa(i)+"].target_ref", "SELF_RELATION", "endpoints must differ"))
		}
	}
	for i := range result.NormalizedPlan.Decisions {
		decision := &result.NormalizedPlan.Decisions[i]
		index := i
		for field, value := range map[string]struct {
			value   string
			maximum int
		}{
			"title": {decision.Title, MaxTitleRunes}, "summary": {decision.Summary, MaxDecisionSummaryRunes}, "content": {decision.Content, MaxDecisionContentRunes},
		} {
			if err := ValidateText(field, value.value, value.maximum); err != nil {
				result.Errors = append(result.Errors, detailForPlanError(index, "decisions["+itoa(i)+"]", err))
			} else if field != "content" && strings.TrimSpace(value.value) == "" {
				result.Errors = append(result.Errors, planDetail(&index, "decisions["+itoa(i)+"]."+field, "REQUIRED", "must not be blank"))
			}
		}
		if decision.Status == "" {
			decision.Status = "active"
		}
		if decision.Status != "active" && decision.Status != "superseded" && decision.Status != "rejected" {
			result.Errors = append(result.Errors, planDetail(&index, "decisions["+itoa(i)+"].status", "INVALID_ENUM", decision.Status))
		}
		if decision.IssueRef != nil {
			if err := ValidateText("issue_ref", *decision.IssueRef, MaxLocalRefRunes); err != nil {
				result.Errors = append(result.Errors, detailForPlanError(index, "decisions["+itoa(i)+"]", err))
			}
		}
	}
	SortDetails(result.Errors)
	result.Valid = len(result.Errors) == 0
	return result
}

func planDetail(index *int, field, code, message string) Detail {
	return Detail{EntityIndex: index, Field: field, Code: code, Message: message}
}

func detailForPlanError(index int, prefix string, err error) Detail {
	if domainErr, ok := err.(*Error); ok && len(domainErr.Details) != 0 {
		detail := domainErr.Details[0]
		detail.EntityIndex = &index
		if prefix != "" {
			if detail.Field != "" {
				detail.Field = prefix + "." + detail.Field
			} else {
				detail.Field = prefix
			}
		}
		return detail
	}
	return planDetail(&index, prefix, "INVALID", "invalid value")
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	result := ""
	for value > 0 {
		result = string(rune('0'+value%10)) + result
		value /= 10
	}
	return result
}

// MergePlanErrors returns one sorted validation result without changing a
// successful normalized representation.
func MergePlanErrors(validation PlanValidation, details []Detail) PlanValidation {
	validation.Errors = append(validation.Errors, details...)
	sort.SliceStable(validation.Errors, func(i, j int) bool {
		left, right := validation.Errors[i], validation.Errors[j]
		if compare := compareEntityIndex(left.EntityIndex, right.EntityIndex); compare != 0 {
			return compare < 0
		}
		if left.Field != right.Field {
			return left.Field < right.Field
		}
		return left.Code < right.Code
	})
	validation.Valid = len(validation.Errors) == 0
	return validation
}
