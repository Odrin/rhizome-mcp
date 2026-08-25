package domain_test

import (
	"errors"
	"testing"

	"rhizome-mcp/internal/domain"
)

func epicPatchFixture(issueType domain.Type, status domain.Status) domain.Issue {
	return domain.Issue{
		ID:        "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		DisplayID: "ISSUE-1",
		Type:      issueType,
		Title:     "fixture",
		Status:    status,
		Priority:  domain.PriorityMedium,
		Version:   1,
	}
}

func statusPatch(target domain.Status) domain.IssuePatch {
	patch := domain.IssuePatch{}
	patch.Status.Set = true
	patch.Status.Value = target
	return patch
}

// TestApplyIssuePatchClosesNonExecutableTypesDirectly is the ISSUE-224
// regression. Two individually correct rules combined to make an epic
// unclosable: the patch path refused a direct move to done for every type, and
// EvaluateClaim refused to claim anything that is not a task or bug, so neither
// route to a terminal status existed.
func TestApplyIssuePatchClosesNonExecutableTypesDirectly(t *testing.T) {
	for _, from := range []domain.Status{domain.StatusOpen, domain.StatusReady} {
		t.Run("epic from "+string(from), func(t *testing.T) {
			current := epicPatchFixture(domain.TypeEpic, from)
			updated, changed, err := domain.ApplyIssuePatch(current, statusPatch(domain.StatusDone))
			if err != nil {
				t.Fatalf("ApplyIssuePatch(epic %s -> done) error = %v, want success", from, err)
			}
			if updated.Status != domain.StatusDone {
				t.Fatalf("status = %q, want done", updated.Status)
			}
			if !containsID(changed, "status") {
				t.Fatalf("changed fields = %v, want status among them", changed)
			}
		})
	}

	// ISSUE-176 was parked in ready, which for a non-executable type can never
	// be claimed out of. That exact state must be closable.
	t.Run("a ready epic is not a trap", func(t *testing.T) {
		current := epicPatchFixture(domain.TypeEpic, domain.StatusReady)
		if _, _, err := domain.ApplyIssuePatch(current, statusPatch(domain.StatusDone)); err != nil {
			t.Fatalf("a ready epic must be closable, got %v", err)
		}
	})
}

// TestApplyIssuePatchStillGuardsExecutableTypes pins the half of the contract
// that must NOT change: for task and bug, review and done stay reachable only
// through claim_issue/finish_attempt (docs/02 section 17.1, locked by
// ISSUE-172).
func TestApplyIssuePatchStillGuardsExecutableTypes(t *testing.T) {
	for _, issueType := range []domain.Type{domain.TypeTask, domain.TypeBug} {
		for _, target := range []domain.Status{domain.StatusReview, domain.StatusDone} {
			t.Run(string(issueType)+" -> "+string(target), func(t *testing.T) {
				current := epicPatchFixture(issueType, domain.StatusReady)
				_, _, err := domain.ApplyIssuePatch(current, statusPatch(target))
				if err == nil {
					t.Fatalf("ApplyIssuePatch(%s ready -> %s) succeeded; the gated-status guard must still reject it", issueType, target)
				}
				var domainErr *domain.Error
				if !errors.As(err, &domainErr) {
					t.Fatalf("error = %T, want a *domain.Error", err)
				}
				if domainErr.Code != domain.CodeInvalidTransition {
					t.Fatalf("code = %q, want %q", domainErr.Code, domain.CodeInvalidTransition)
				}
			})
		}
	}
}

// TestApplyIssuePatchKeepsReviewForbiddenForEveryType: review means "inspect
// this attempt's result". An epic has no attempt to inspect, so widening the
// guard must not have opened review as a side effect.
func TestApplyIssuePatchKeepsReviewForbiddenForEveryType(t *testing.T) {
	for _, issueType := range domain.AllIssueTypes {
		t.Run(string(issueType), func(t *testing.T) {
			current := epicPatchFixture(issueType, domain.StatusReady)
			if _, _, err := domain.ApplyIssuePatch(current, statusPatch(domain.StatusReview)); err == nil {
				t.Fatalf("a direct patch to review must be rejected for %s", issueType)
			}
		})
	}
}

// TestApplyPatchStatusTransitionScopesTheOpenToDoneAllowance keeps the
// allowance narrow: it exists for non-executable types only, and it does not
// leak into CanTransition, which finish_attempt and other direct writes share.
func TestApplyPatchStatusTransitionScopesTheOpenToDoneAllowance(t *testing.T) {
	if _, err := domain.ApplyPatchStatusTransition(domain.TypeEpic, domain.StatusOpen, domain.StatusDone, ""); err != nil {
		t.Fatalf("epic open -> done must be allowed in the patch path, got %v", err)
	}
	if _, err := domain.ApplyPatchStatusTransition(domain.TypeTask, domain.StatusOpen, domain.StatusDone, ""); err == nil {
		t.Fatal("task open -> done must stay invalid")
	}
	if domain.CanTransition(domain.StatusOpen, domain.StatusDone) {
		t.Fatal("CanTransition(open, done) must stay false; the allowance belongs to the patch path only")
	}
}

// TestTypeExecutable pins the one definition of executable that both the patch
// guard and EvaluateClaim now share.
func TestTypeExecutable(t *testing.T) {
	if !domain.TypeTask.Executable() || !domain.TypeBug.Executable() {
		t.Fatal("task and bug must be executable")
	}
	if domain.TypeEpic.Executable() {
		t.Fatal("epic must not be executable")
	}
}
