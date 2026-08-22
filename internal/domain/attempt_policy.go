package domain

import (
	"sort"
	"time"
)

// See ISSUE-186: these are the attempt-lifecycle decision rules docs/02 and
// docs/05 treat as domain invariants, lifted out of the SQLite adapter
// (where several existed in divergent copies) into pure, independently
// testable functions. Callers still own every query (blocker counts, active
// attempt checks, event lookups); only the decision itself moved here.

// EvaluateClaim decides whether an issue can be claimed for a new attempt
// and, if so, which attempt kind the claim starts. It runs the same checks
// in the same order the SQLite adapter's ClaimIssue always has: archived
// issues and non-executable types are rejected outright, then unresolved
// blockers, then the issue's status (ready starts a work attempt, review
// starts a review attempt, anything else is not claimable), and finally
// whether an active attempt already exists.
func EvaluateClaim(issue Issue, unresolvedBlockers int64, hasActiveAttempt bool) (AttemptKind, error) {
	if issue.ArchivedAt != nil {
		return "", NewError(CodeIssueArchived, "issue is archived", false)
	}
	if issue.Type != TypeTask && issue.Type != TypeBug {
		return "", NewError(CodeInvalidArgument, "issue type is not executable", false,
			Detail{Field: "issue_id", Code: "NOT_EXECUTABLE"})
	}
	if unresolvedBlockers > 0 {
		return "", NewError(CodeInvalidArgument, "issue has unresolved blockers", false,
			Detail{Field: "issue_id", Code: "BLOCKED"})
	}
	var kind AttemptKind
	switch issue.Status {
	case StatusReady:
		kind = AttemptKindWork
	case StatusReview:
		kind = AttemptKindReview
	default:
		return "", NewError(CodeInvalidArgument, "issue is not claimable", false,
			Detail{Field: "issue_id", Code: "NOT_CLAIMABLE"})
	}
	if hasActiveAttempt {
		return "", NewError(CodeActiveAttemptExists, "issue has an active work attempt", false)
	}
	return kind, nil
}

// FinishTargetStatus derives the issue status a completed attempt moves to.
// A non-completed outcome (failed, interrupted) never moves the issue, so
// current is returned unchanged. A completed work attempt uses
// input.TargetIssueStatus verbatim; a completed review attempt maps
// input.ReviewOutcome through the fixed table pinned to docs/02:
// approved -> done, changes_requested -> ready, blocked -> blocked.
//
// FinishTargetStatus assumes input has already passed
// ValidateFinishAttemptForKind, which guarantees TargetIssueStatus is set
// for a completed work attempt and ReviewOutcome is set (to a valid value)
// for a completed review attempt; it does not itself validate the
// transition -- callers still run the result through ApplyFinishTransition.
func FinishTargetStatus(kind AttemptKind, input FinishAttemptInput, current Status) (Status, error) {
	if input.Outcome != AttemptOutcomeCompleted {
		return current, nil
	}
	if kind == AttemptKindWork {
		if input.TargetIssueStatus == nil {
			return "", NewError(CodeInvalidArgument, "target_issue_status is required for work completion", false,
				Detail{Field: "target_issue_status", Code: "REQUIRED"})
		}
		return *input.TargetIssueStatus, nil
	}
	if input.ReviewOutcome == nil {
		return "", NewError(CodeInvalidArgument, "review_outcome is required for review completion", false,
			Detail{Field: "review_outcome", Code: "REQUIRED"})
	}
	switch *input.ReviewOutcome {
	case ReviewOutcomeApproved:
		return StatusDone, nil
	case ReviewOutcomeChangesRequested:
		return StatusReady, nil
	case ReviewOutcomeBlocked:
		return StatusBlocked, nil
	default:
		return "", invalidEnum("review_outcome", string(*input.ReviewOutcome))
	}
}

// NextClosedAt derives an issue's closed_at value for a status transition
// from -> to: entering a terminal status (done, cancelled) stamps closed_at
// with now, leaving a terminal status clears it, and any other transition
// (including a terminal-to-terminal or non-terminal-to-non-terminal move)
// leaves the current value unchanged.
func NextClosedAt(from, to Status, now time.Time, current *time.Time) *time.Time {
	switch {
	case !from.Terminal() && to.Terminal():
		stamped := now
		return &stamped
	case from.Terminal() && !to.Terminal():
		return nil
	default:
		return current
	}
}

// changeClassificationRequired and changeClassificationWarning list the
// issue-event changed_fields entries FinishAttempt treats as requiring
// explicit acknowledgement versus merely warning about, per docs/02 §16.
var (
	changeClassificationRequired = map[string]bool{
		"description":         true,
		"acceptance_criteria": true,
		"status":              true,
		"blocked_reason":      true,
	}
	changeClassificationWarning = map[string]bool{
		"title":     true,
		"priority":  true,
		"labels":    true,
		"parent_id": true,
		"type":      true,
	}
)

// ClassifyIssueChanges partitions a set of issue-event changed_fields names
// (already deduplicated across events by the caller, or not -- duplicates
// are collapsed here too) into warning fields (reported as
// "ISSUE_CHANGED:<field>") and required fields (fields that must be
// explicitly acknowledged before FinishAttempt can complete the attempt).
// Both outputs are sorted for determinism. Fields not in either
// classification are ignored.
func ClassifyIssueChanges(changedFields []string) (warnings, required []string) {
	warningSet, requiredSet := map[string]bool{}, map[string]bool{}
	for _, field := range changedFields {
		if changeClassificationRequired[field] {
			requiredSet[field] = true
		} else if changeClassificationWarning[field] {
			warningSet[field] = true
		}
	}
	warnings = make([]string, 0, len(warningSet))
	for field := range warningSet {
		warnings = append(warnings, "ISSUE_CHANGED:"+field)
	}
	sort.Strings(warnings)
	required = make([]string, 0, len(requiredSet))
	for field := range requiredSet {
		required = append(required, field)
	}
	sort.Strings(required)
	return warnings, required
}

// BlocksPathExists reports whether a directed path leads from start to
// sought, using neighbors to look up a node's direct successors. It runs a
// breadth-first search and is the single shared reachability primitive
// behind every blocks-cycle check in the codebase: adding a new blocks edge
// is rejected when a path already runs the other way, and a batch of
// relations is acyclic exactly when no node in it can reach itself.
// neighbors is called at most once per distinct node reached.
func BlocksPathExists(start, sought string, neighbors func(node string) []string) bool {
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range neighbors(current) {
			if next == sought {
				return true
			}
			if seen[next] {
				continue
			}
			seen[next] = true
			queue = append(queue, next)
		}
	}
	return false
}
