package cli

import (
	"strings"
	"testing"
	"time"

	"rhizome-mcp/internal/domain"
)

func boardResultWithGates() domain.BoardResult {
	fingerprint := "fp-1234"
	return domain.BoardResult{
		GeneratedAt: time.Date(2026, 8, 25, 11, 0, 0, 0, time.UTC),
		ActiveAttempts: []domain.ActiveAttemptSummary{
			{AttemptID: "attempt-gated", IssueID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", IssueDisplayID: "ISSUE-300", IssueTitle: "Gated work", Kind: domain.AttemptKindWork, StartedAt: time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC), LeaseExpiresAt: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)},
		},
		AttemptGates: []domain.AttemptGateProgress{{
			AttemptID:      "attempt-gated",
			IssueID:        "01ARZ3NDEKTSV4RRFFQ69G5FAV",
			IssueDisplayID: "ISSUE-300",
			Gates: domain.WorkContextGateSummary{
				Point:               domain.EnforcementPointCompleteWorkToDone,
				SnapshotFingerprint: &fingerprint,
				RequirementCount:    2,
				SatisfiedCount:      1,
				Unmet: []domain.WorkContextUnmetRequirement{{
					PolicyID:       "policy-1",
					RequirementKey: "impl-<script>alert(9)</script>",
					Reason:         "attempt evidence \"impl\" missing",
				}},
				NextActions: []string{"submit_gate_evidence for key \"impl\" on the active attempt"},
			},
		}},
	}
}

// TestBoardHTMLRendersAttemptGateProgress pins ISSUE-175 AC2 on both board
// pages: gate progress appears as text on the owning attempt's row, unmet
// requirement keys are listed, and requirement text is HTML-escaped.
func TestBoardHTMLRendersAttemptGateProgress(t *testing.T) {
	result := boardResultWithGates()

	staticHTML, err := renderBoardHTML(result)
	if err != nil {
		t.Fatalf("renderBoardHTML() error = %v", err)
	}
	servedHTML, err := renderServedBoardHTML(result)
	if err != nil {
		t.Fatalf("renderServedBoardHTML() error = %v", err)
	}
	for name, html := range map[string]string{"static": staticHTML, "served": servedHTML} {
		if !strings.Contains(html, "<th>Gates</th>") {
			t.Fatalf("%s board is missing the Gates column header:\n%s", name, html)
		}
		if !strings.Contains(html, "1/2 satisfied") {
			t.Fatalf("%s board is missing the gate progress text:\n%s", name, html)
		}
		if !strings.Contains(html, "impl-&lt;script&gt;alert(9)&lt;/script&gt;: attempt evidence &#34;impl&#34; missing") {
			t.Fatalf("%s board is missing the escaped unmet requirement line:\n%s", name, html)
		}
		if strings.Contains(html, "<script>alert(9)</script>") {
			t.Fatalf("%s board rendered raw requirement HTML:\n%s", name, html)
		}
	}
}

// TestIssueDetailHTMLRendersGateSection pins the issue page's workflow-gate
// section: the evaluated point and snapshot source in the status line, one
// row per unmet requirement with reason and next action, escaped text, and
// header cells scoped for screen readers.
func TestIssueDetailHTMLRendersGateSection(t *testing.T) {
	fingerprint := "fp-abcd"
	detail := domain.IssueDetail{
		Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", DisplayID: "ISSUE-300", Title: "Gated work", Status: domain.StatusReady, Type: domain.TypeTask, Priority: domain.PriorityMedium},
		Gates: domain.WorkContextGateSummary{
			Point:               domain.EnforcementPointCompleteWorkToDone,
			SnapshotFingerprint: &fingerprint,
			RequirementCount:    2,
			SatisfiedCount:      1,
			Unmet: []domain.WorkContextUnmetRequirement{{
				PolicyID:       "policy-1",
				RequirementKey: "security-review",
				Reason:         "review approval for purpose \"security\" <b>missing</b>",
			}},
			NextActions: []string{"obtain an approved review covering purpose \"security\""},
		},
	}
	html, err := renderIssueDetailHTML(detail)
	if err != nil {
		t.Fatalf("renderIssueDetailHTML() error = %v", err)
	}
	for _, want := range []string{
		"<h2>Workflow gates</h2>",
		"Evaluated at complete_work_to_done against the active attempt&#39;s frozen snapshot (fingerprint fp-abcd): 1 of 2 requirements satisfied.",
		"<th scope=\"col\">Requirement</th>",
		"<th scope=\"col\">Reason</th>",
		"<th scope=\"col\">Next action</th>",
		"<td>security-review</td>",
		"review approval for purpose &#34;security&#34; &lt;b&gt;missing&lt;/b&gt;",
		"obtain an approved review covering purpose &#34;security&#34;",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("issue detail missing %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, "<b>missing</b>") {
		t.Fatalf("issue detail rendered raw requirement HTML:\n%s", html)
	}

	detail.Gates = domain.WorkContextGateSummary{Point: domain.EnforcementPointClaimWork}
	html, err = renderIssueDetailHTML(detail)
	if err != nil {
		t.Fatalf("renderIssueDetailHTML(no gates) error = %v", err)
	}
	if !strings.Contains(html, "No workflow gate requirements apply to this issue.") {
		t.Fatalf("issue detail missing the no-policy compatibility line:\n%s", html)
	}

	detail.Gates = domain.WorkContextGateSummary{Point: domain.EnforcementPointClaimWork, RequirementCount: 2, SatisfiedCount: 2}
	html, err = renderIssueDetailHTML(detail)
	if err != nil {
		t.Fatalf("renderIssueDetailHTML(satisfied) error = %v", err)
	}
	if !strings.Contains(html, "All gate requirements are satisfied.") {
		t.Fatalf("issue detail missing the all-satisfied line:\n%s", html)
	}
	if !strings.Contains(html, "against live policies: 2 of 2 requirements satisfied.") {
		t.Fatalf("issue detail missing the live-policy status line:\n%s", html)
	}
}

// TestBoardResponseIncludesAttemptGates pins the stable JSON projection:
// attempt_gates rows join to active_attempts by attempt_id and carry the
// point, fingerprint, counts, and unmet identities.
func TestBoardResponseIncludesAttemptGates(t *testing.T) {
	response := boardResponseFromDomain(boardResultWithGates())
	if len(response.AttemptGates) != 1 {
		t.Fatalf("AttemptGates = %#v, want 1 row", response.AttemptGates)
	}
	row := response.AttemptGates[0]
	if row.AttemptID != "attempt-gated" || row.IssueID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" || row.IssueDisplayID != "ISSUE-300" {
		t.Fatalf("row identity = %#v", row)
	}
	if row.EnforcementPoint != "complete_work_to_done" || row.SnapshotFingerprint == nil || *row.SnapshotFingerprint != "fp-1234" {
		t.Fatalf("row evaluation source = %#v", row)
	}
	if row.RequirementCount != 2 || row.SatisfiedCount != 1 {
		t.Fatalf("row counts = %#v", row)
	}
	if len(row.Unmet) != 1 || row.Unmet[0].PolicyID != "policy-1" || row.Unmet[0].RequirementKey != "impl-<script>alert(9)</script>" || row.Unmet[0].Reason == "" {
		t.Fatalf("row unmet = %#v", row.Unmet)
	}
}

// TestSemanticBoardETagReflectsGateChanges: satisfying a gate requirement
// must change the board's semantic ETag, so a polling client refreshes.
func TestSemanticBoardETagReflectsGateChanges(t *testing.T) {
	before := boardResultWithGates()
	after := boardResultWithGates()
	after.AttemptGates[0].Gates.SatisfiedCount = 2
	after.AttemptGates[0].Gates.Unmet = nil

	if semanticBoardETag(before) == semanticBoardETag(after) {
		t.Fatal("board ETag unchanged after gate progress changed")
	}
}
