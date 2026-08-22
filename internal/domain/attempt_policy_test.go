package domain

import (
	"errors"
	"testing"
	"time"
)

func TestEvaluateClaimMatrix(t *testing.T) {
	someTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		issueType  Type
		status     Status
		archived   bool
		blockers   int64
		hasActive  bool
		wantKind   AttemptKind
		wantCode   string
		wantDetail string
	}{
		{name: "ready task claims work", issueType: TypeTask, status: StatusReady, wantKind: AttemptKindWork},
		{name: "ready bug claims work", issueType: TypeBug, status: StatusReady, wantKind: AttemptKindWork},
		{name: "review task claims review", issueType: TypeTask, status: StatusReview, wantKind: AttemptKindReview},
		{name: "archived issue rejected regardless of status", issueType: TypeTask, status: StatusReady, archived: true, wantCode: CodeIssueArchived},
		{name: "epic is not executable", issueType: TypeEpic, status: StatusReady, wantCode: CodeInvalidArgument, wantDetail: "NOT_EXECUTABLE"},
		{name: "unresolved blockers reject ready", issueType: TypeTask, status: StatusReady, blockers: 1, wantCode: CodeInvalidArgument, wantDetail: "BLOCKED"},
		{name: "unresolved blockers reject review", issueType: TypeBug, status: StatusReview, blockers: 2, wantCode: CodeInvalidArgument, wantDetail: "BLOCKED"},
		{name: "open status is not claimable", issueType: TypeTask, status: StatusOpen, wantCode: CodeInvalidArgument, wantDetail: "NOT_CLAIMABLE"},
		{name: "blocked status is not claimable", issueType: TypeTask, status: StatusBlocked, wantCode: CodeInvalidArgument, wantDetail: "NOT_CLAIMABLE"},
		{name: "done status is not claimable", issueType: TypeTask, status: StatusDone, wantCode: CodeInvalidArgument, wantDetail: "NOT_CLAIMABLE"},
		{name: "cancelled status is not claimable", issueType: TypeTask, status: StatusCancelled, wantCode: CodeInvalidArgument, wantDetail: "NOT_CLAIMABLE"},
		{name: "active attempt blocks a second claim on ready", issueType: TypeTask, status: StatusReady, hasActive: true, wantCode: CodeActiveAttemptExists},
		{name: "active attempt blocks a second claim on review", issueType: TypeTask, status: StatusReview, hasActive: true, wantCode: CodeActiveAttemptExists},
		{name: "archived beats blocked-status not-claimable", issueType: TypeTask, status: StatusBlocked, archived: true, wantCode: CodeIssueArchived},
		{name: "epic beats unresolved blockers", issueType: TypeEpic, status: StatusReady, blockers: 5, wantCode: CodeInvalidArgument, wantDetail: "NOT_EXECUTABLE"},
		{name: "blockers beat not-claimable status", issueType: TypeTask, status: StatusOpen, blockers: 1, wantCode: CodeInvalidArgument, wantDetail: "BLOCKED"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			issue := Issue{Type: testCase.issueType, Status: testCase.status}
			if testCase.archived {
				issue.ArchivedAt = &someTime
			}
			kind, err := EvaluateClaim(issue, testCase.blockers, testCase.hasActive)
			if testCase.wantCode == "" {
				if err != nil {
					t.Fatalf("EvaluateClaim() unexpected error: %v", err)
				}
				if kind != testCase.wantKind {
					t.Fatalf("EvaluateClaim() kind = %q, want %q", kind, testCase.wantKind)
				}
				return
			}
			if err == nil {
				t.Fatalf("EvaluateClaim() expected error with code %q, got nil (kind=%q)", testCase.wantCode, kind)
			}
			var domainErr *Error
			if !errors.As(err, &domainErr) {
				t.Fatalf("EvaluateClaim() error is not a domain.Error: %v", err)
			}
			if domainErr.Code != testCase.wantCode {
				t.Fatalf("EvaluateClaim() code = %q, want %q", domainErr.Code, testCase.wantCode)
			}
			if testCase.wantDetail != "" {
				found := false
				for _, detail := range domainErr.Details {
					if detail.Code == testCase.wantDetail {
						found = true
					}
				}
				if !found {
					t.Fatalf("EvaluateClaim() details = %+v, want a detail code %q", domainErr.Details, testCase.wantDetail)
				}
			}
		})
	}
}

// TestFinishTargetStatusReviewOutcomeTable pins the review-outcome mapping
// to docs/02-domain-model.md §5.4 (approved -> done, changes_requested ->
// ready, blocked -> blocked).
func TestFinishTargetStatusReviewOutcomeTable(t *testing.T) {
	cases := []struct {
		outcome ReviewOutcome
		want    Status
	}{
		{ReviewOutcomeApproved, StatusDone},
		{ReviewOutcomeChangesRequested, StatusReady},
		{ReviewOutcomeBlocked, StatusBlocked},
	}
	for _, testCase := range cases {
		t.Run(string(testCase.outcome), func(t *testing.T) {
			input := FinishAttemptInput{Outcome: AttemptOutcomeCompleted, ReviewOutcome: &testCase.outcome}
			got, err := FinishTargetStatus(AttemptKindReview, input, StatusReview)
			if err != nil {
				t.Fatalf("FinishTargetStatus() unexpected error: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("FinishTargetStatus() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestFinishTargetStatusWorkUsesTargetVerbatim(t *testing.T) {
	target := StatusDone
	input := FinishAttemptInput{Outcome: AttemptOutcomeCompleted, TargetIssueStatus: &target}
	got, err := FinishTargetStatus(AttemptKindWork, input, StatusReady)
	if err != nil {
		t.Fatalf("FinishTargetStatus() unexpected error: %v", err)
	}
	if got != StatusDone {
		t.Fatalf("FinishTargetStatus() = %q, want %q", got, StatusDone)
	}
}

func TestFinishTargetStatusNonCompletedOutcomeLeavesIssueUnchanged(t *testing.T) {
	for _, outcome := range []AttemptOutcome{AttemptOutcomeFailed, AttemptOutcomeInterrupted} {
		input := FinishAttemptInput{Outcome: outcome}
		got, err := FinishTargetStatus(AttemptKindWork, input, StatusReview)
		if err != nil {
			t.Fatalf("FinishTargetStatus(%s) unexpected error: %v", outcome, err)
		}
		if got != StatusReview {
			t.Fatalf("FinishTargetStatus(%s) = %q, want unchanged %q", outcome, got, StatusReview)
		}
	}
}

func TestNextClosedAt(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	earlier := now.Add(-time.Hour)

	t.Run("entering terminal stamps now", func(t *testing.T) {
		got := NextClosedAt(StatusReady, StatusDone, now, nil)
		if got == nil || !got.Equal(now) {
			t.Fatalf("NextClosedAt() = %v, want %v", got, now)
		}
	})
	t.Run("leaving terminal clears", func(t *testing.T) {
		got := NextClosedAt(StatusDone, StatusReady, now, &earlier)
		if got != nil {
			t.Fatalf("NextClosedAt() = %v, want nil", got)
		}
	})
	t.Run("non-terminal to non-terminal leaves current unchanged", func(t *testing.T) {
		got := NextClosedAt(StatusReady, StatusBlocked, now, nil)
		if got != nil {
			t.Fatalf("NextClosedAt() = %v, want nil", got)
		}
	})
	t.Run("terminal to terminal is unreachable but leaves current unchanged", func(t *testing.T) {
		got := NextClosedAt(StatusDone, StatusCancelled, now, &earlier)
		if got == nil || !got.Equal(earlier) {
			t.Fatalf("NextClosedAt() = %v, want %v", got, earlier)
		}
	})
}

// TestClassifyIssueChangesFieldLists pins the two field lists to
// docs/02-domain-model.md §16.
func TestClassifyIssueChangesFieldLists(t *testing.T) {
	warningFields := []string{"title", "priority", "labels", "parent_id", "type"}
	requiredFields := []string{"description", "acceptance_criteria", "status", "blocked_reason"}

	warnings, required := ClassifyIssueChanges(append(append([]string{}, warningFields...), requiredFields...))

	wantWarnings := []string{"ISSUE_CHANGED:labels", "ISSUE_CHANGED:parent_id", "ISSUE_CHANGED:priority", "ISSUE_CHANGED:title", "ISSUE_CHANGED:type"}
	if !equalStrings(warnings, wantWarnings) {
		t.Fatalf("ClassifyIssueChanges() warnings = %v, want %v", warnings, wantWarnings)
	}
	wantRequired := []string{"acceptance_criteria", "blocked_reason", "description", "status"}
	if !equalStrings(required, wantRequired) {
		t.Fatalf("ClassifyIssueChanges() required = %v, want %v", required, wantRequired)
	}
}

func TestClassifyIssueChangesIgnoresUnknownAndDeduplicates(t *testing.T) {
	warnings, required := ClassifyIssueChanges([]string{"title", "title", "unknown_field", "status", "status"})
	if !equalStrings(warnings, []string{"ISSUE_CHANGED:title"}) {
		t.Fatalf("ClassifyIssueChanges() warnings = %v", warnings)
	}
	if !equalStrings(required, []string{"status"}) {
		t.Fatalf("ClassifyIssueChanges() required = %v", required)
	}
}

func TestClassifyIssueChangesEmptyInput(t *testing.T) {
	warnings, required := ClassifyIssueChanges(nil)
	if len(warnings) != 0 || len(required) != 0 {
		t.Fatalf("ClassifyIssueChanges(nil) = (%v, %v), want empty", warnings, required)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBlocksPathExists(t *testing.T) {
	// a -> b -> c, d is disconnected.
	adjacency := map[string][]string{
		"a": {"b"},
		"b": {"c"},
	}
	neighbors := func(node string) []string { return adjacency[node] }

	if !BlocksPathExists("a", "c", neighbors) {
		t.Fatal("BlocksPathExists(a, c) = false, want true (transitive path)")
	}
	if !BlocksPathExists("a", "b", neighbors) {
		t.Fatal("BlocksPathExists(a, b) = false, want true (direct edge)")
	}
	if BlocksPathExists("c", "a", neighbors) {
		t.Fatal("BlocksPathExists(c, a) = true, want false (wrong direction)")
	}
	if BlocksPathExists("a", "d", neighbors) {
		t.Fatal("BlocksPathExists(a, d) = true, want false (disconnected)")
	}
	if BlocksPathExists("d", "a", neighbors) {
		t.Fatal("BlocksPathExists(d, a) = true, want false (disconnected, no outgoing edges)")
	}
}

func TestBlocksPathExistsSelfReachabilityDetectsCycle(t *testing.T) {
	// a -> b -> c -> a is a cycle; d -> e is not.
	cyclic := map[string][]string{"a": {"b"}, "b": {"c"}, "c": {"a"}}
	acyclic := map[string][]string{"d": {"e"}}

	if !BlocksPathExists("a", "a", func(node string) []string { return cyclic[node] }) {
		t.Fatal("BlocksPathExists(a, a) over a cyclic graph = false, want true")
	}
	if BlocksPathExists("d", "d", func(node string) []string { return acyclic[node] }) {
		t.Fatal("BlocksPathExists(d, d) over an acyclic graph = true, want false")
	}
}
