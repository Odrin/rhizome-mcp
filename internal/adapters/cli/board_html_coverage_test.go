package cli

import (
	"strings"
	"testing"
	"time"

	"rhizome-mcp/internal/domain"
)

func TestRenderBoardHTMLCoverage(t *testing.T) {
	fixedAt := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	result := domain.BoardResult{
		GeneratedAt: fixedAt,
		StatusCounts: []domain.EffectiveStatusCount{
			{EffectiveStatus: domain.EffectiveStatusOpen, Count: 2},
			{EffectiveStatus: domain.EffectiveStatusReady, Count: 3},
			{EffectiveStatus: domain.EffectiveStatusBlocked, Count: 1},
			{EffectiveStatus: domain.EffectiveStatusReview, Count: 4},
			{EffectiveStatus: domain.EffectiveStatusDone, Count: 7},
			{EffectiveStatus: domain.EffectiveStatusCancelled, Count: 1},
			{EffectiveStatus: domain.EffectiveStatusInProgress, Count: 2},
		},
		ActiveAttempts: []domain.ActiveAttemptSummary{
			{AttemptID: "attempt-work", IssueID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", IssueDisplayID: "ISSUE-100", IssueTitle: "Implement export", Kind: domain.AttemptKindWork, SessionLabel: strPtr("session-a"), StartedAt: fixedAt, LeaseExpiresAt: fixedAt.Add(15 * time.Minute)},
			{AttemptID: "attempt-review", IssueID: "01ARZ3NDEKTSV4RRFFQ69G5FAV2", IssueDisplayID: "ISSUE-101", IssueTitle: "Review export", Kind: domain.AttemptKindReview, StartedAt: fixedAt.Add(2 * time.Minute), LeaseExpiresAt: fixedAt.Add(20 * time.Minute)},
		},
		ActiveReservations: []domain.Reservation{
			{ID: "reservation-1", IssueID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", AttemptID: "attempt-work", Kind: domain.ResourceKindFile, DisplayValue: "src/<script>alert(4)</script>.go", Status: domain.ReservationStatusActive},
		},
		BlockedIssues: []domain.IssueProjection{{
			Issue:           domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV3", DisplayID: "ISSUE-120", Title: "Blocked by dependency", Status: domain.StatusBlocked, BlockedReason: strPtr("Waiting on umbrella issue")},
			EffectiveStatus: domain.EffectiveStatusBlocked,
		}},
		ReviewRequests: []domain.ReviewRequest{{
			ID:                 "review-1",
			IssueID:            "01ARZ3NDEKTSV4RRFFQ69G5FAV4",
			Status:             domain.ReviewRequestStatusOpen,
			TargetIssueVersion: 4,
			CreatedAt:          fixedAt.Add(5 * time.Minute),
		}},
		PlanningGraph: domain.GraphResult{
			Nodes: []domain.IssueProjection{
				{Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV5", DisplayID: "ISSUE-200", Title: "Root", Status: domain.StatusReady}, EffectiveStatus: domain.EffectiveStatusReady},
				{Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV6", DisplayID: "ISSUE-201", Title: "Blocked child", Status: domain.StatusBlocked}, EffectiveStatus: domain.EffectiveStatusBlocked},
				{Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV7", DisplayID: "ISSUE-202", Title: "Review child", Status: domain.StatusReview}, EffectiveStatus: domain.EffectiveStatusReview},
				{Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV8", Title: "Done child", Status: domain.StatusDone}, EffectiveStatus: domain.EffectiveStatusDone},
				{Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV9", DisplayID: "ISSUE-204", Title: "In progress child", Status: domain.StatusReady}, EffectiveStatus: domain.EffectiveStatusInProgress},
				{Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV10", DisplayID: "ISSUE-205", Title: "Open child", Status: domain.StatusOpen}, EffectiveStatus: domain.EffectiveStatusOpen},
			},
			Edges: []domain.GraphEdge{
				{SourceIssueID: "01ARZ3NDEKTSV4RRFFQ69G5FAV5", TargetIssueID: "01ARZ3NDEKTSV4RRFFQ69G5FAV6", Type: "blocks"},
				{SourceIssueID: "01ARZ3NDEKTSV4RRFFQ69G5FAV5", TargetIssueID: "01ARZ3NDEKTSV4RRFFQ69G5FAV7", Type: "contains"},
			},
			EntryPoints:      []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV5"},
			BlockingNodes:    []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV6"},
			Summary:          domain.GraphSummary{NodeCount: 6, EdgeCount: 2, EntryPointCount: 1, BlockingNodeCount: 1},
			Truncated:        true,
			TruncationReason: strPtr("too many nodes"),
		},
	}

	t.Run("static board", func(t *testing.T) {
		html, err := renderBoardHTML(result)
		if err != nil {
			t.Fatalf("renderBoardHTML: %v", err)
		}
		for _, want := range []string{"Rhizome status board", "Status counts", "ISSUE-120", "Waiting on umbrella issue", "attempt-work", "file: src/&lt;script&gt;alert(4)&lt;/script&gt;.go", "1 active resource reservation,"} {
			if !strings.Contains(html, want) {
				t.Fatalf("static board missing %q:\n%s", want, html)
			}
		}
		if strings.Contains(html, "/issues/") {
			t.Fatalf("static board unexpectedly contained issue route: %s", html)
		}
		if strings.Contains(html, "data-board-search-form") || strings.Contains(html, "boardLiveRefreshScript") {
			t.Fatalf("static board unexpectedly included served-only UI/scripts: %s", html)
		}
		if strings.Contains(html, "<script>alert(4)</script>") {
			t.Fatalf("static board rendered raw script-like reservation display value: %s", html)
		}
		reviewRow := tableRowFor(t, html, "attempt-review")
		if strings.Count(reviewRow, "—") != 2 {
			t.Fatalf("attempt-review row should show the empty placeholder for both session label and reservations, got: %s", reviewRow)
		}
		if strings.Contains(reviewRow, "<li>") {
			t.Fatalf("attempt-review has no reservations and should render no <li>: %s", reviewRow)
		}
		workRow := tableRowFor(t, html, "attempt-work")
		if !strings.Contains(workRow, "<li>file: src/&lt;script&gt;alert(4)&lt;/script&gt;.go</li>") {
			t.Fatalf("attempt-work row missing its grouped reservation: %s", workRow)
		}
	})

	t.Run("served board", func(t *testing.T) {
		html, err := renderServedBoardHTML(result)
		if err != nil {
			t.Fatalf("renderServedBoardHTML: %v", err)
		}
		for _, want := range []string{"data-board-main", "Search", "data-board-search-form", "board-search-query", "ISSUE-100", "ISSUE-120", "ISSUE-200", "ISSUE-201", "(truncated)", "href=\"/issues/ISSUE-200\"", "href=\"/issues/ISSUE-201\"", "href=\"/issues/ISSUE-100\"", "1 active resource reservation,"} {
			if !strings.Contains(html, want) {
				t.Fatalf("served board missing %q:\n%s", want, html)
			}
		}
		for _, want := range []string{"<rect x=", "fill=\"#fecaca\"", "fill=\"#fde68a\"", "fill=\"#bbf7d0\"", "fill=\"#bfdbfe\"", "fill=\"#e2e8f0\""} {
			if !strings.Contains(html, want) {
				t.Fatalf("served board SVG missing %q:\n%s", want, html)
			}
		}
		if !strings.Contains(html, `<a href="/issues/ISSUE-100"`) {
			t.Fatalf("served board failed to link active attempt issue display ID: %s", html)
		}
		if !strings.Contains(html, `<a href="/issues/ISSUE-120"`) {
			t.Fatalf("served board failed to link blocked issue display ID: %s", html)
		}
		if !strings.Contains(html, `aria-label="ISSUE-200"`) {
			t.Fatalf("served board SVG anchor missing aria-label: %s", html)
		}
		if !strings.Contains(html, `href="/issues/ISSUE-101"`) {
			t.Fatalf("served board review request link missing fallback display ID: %s", html)
		}
		if strings.Contains(html, "<script>alert(4)</script>") {
			t.Fatalf("served board rendered raw script-like reservation display value: %s", html)
		}
		workRow := tableRowFor(t, html, "attempt-work")
		if !strings.Contains(workRow, "<li>file: src/&lt;script&gt;alert(4)&lt;/script&gt;.go</li>") {
			t.Fatalf("served board attempt-work row missing its grouped reservation: %s", workRow)
		}
		reviewRow := tableRowFor(t, html, "attempt-review")
		if strings.Contains(reviewRow, "<li>") {
			t.Fatalf("served board attempt-review has no reservations and should render no <li>: %s", reviewRow)
		}
	})
}

func reservationReleaseReasonPtr(value domain.ReservationReleaseReason) *domain.ReservationReleaseReason {
	return &value
}

// tableRowFor returns the <tr>...</tr> segment containing marker, for
// asserting on one board table row's full content without over-matching
// against the rest of the page.
func tableRowFor(t *testing.T, html, marker string) string {
	t.Helper()
	index := strings.Index(html, marker)
	if index < 0 {
		t.Fatalf("marker %q not found in html", marker)
	}
	rowStart := strings.LastIndex(html[:index], "<tr>")
	if rowStart < 0 {
		t.Fatalf("no <tr> before marker %q", marker)
	}
	rowEnd := strings.Index(html[index:], "</tr>")
	if rowEnd < 0 {
		t.Fatalf("no </tr> after marker %q", marker)
	}
	return html[rowStart : index+rowEnd+len("</tr>")]
}

func TestRenderIssueDetailHTMLCoverage(t *testing.T) {
	issueID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	rootID := "01ARZ3NDEKTSV4RRFFQ69G5FAV2"
	rootGraphID := "01ARZ3NDEKTSV4RRFFQ69G5FAV3"
	fixedAt := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	archivedAt := fixedAt.Add(2 * time.Hour)
	richDetail := domain.IssueDetail{
		Issue: domain.Issue{
			ID:                 issueID,
			DisplayID:          "ISSUE-300",
			Title:              "Export and review",
			Status:             domain.StatusBlocked,
			Priority:           domain.PriorityHigh,
			Type:               domain.TypeTask,
			Version:            7,
			CreatedAt:          fixedAt,
			UpdatedAt:          fixedAt.Add(30 * time.Minute),
			ArchivedAt:         &archivedAt,
			Description:        strPtr("Need <script>alert(1)</script> and details"),
			AcceptanceCriteria: strPtr("Approval <b>required</b>"),
			BlockedReason:      strPtr("Blocked by <script>alert(2)</script>"),
			Labels:             []domain.Label{{Name: "alpha"}, {Name: "beta"}},
		},
		RootIssueProjection: &domain.IssueProjection{Issue: domain.Issue{ID: rootID, DisplayID: "ISSUE-301", Title: "Parent issue"}},
		Graph:               domain.GraphResult{Nodes: []domain.IssueProjection{{Issue: domain.Issue{ID: rootGraphID, DisplayID: "ISSUE-400", Title: "Related issue"}, EffectiveStatus: domain.EffectiveStatusReady}}, Edges: []domain.GraphEdge{{SourceIssueID: rootGraphID, TargetIssueID: rootID, Type: "blocks"}}},
		LatestAttempt:       &domain.WorkAttempt{ID: "attempt-777", IssueID: issueID, Kind: domain.AttemptKindWork, Status: domain.AttemptStatusCompleted, StartedAt: fixedAt.Add(10 * time.Minute), FinishedAt: &archivedAt},
		OpenReview:          &domain.ReviewRequest{ID: "review-open", IssueID: issueID, Status: domain.ReviewRequestStatusOpen},
		LatestDecision:      &domain.Decision{ID: "decision-1", IssueID: &issueID, Title: "Decision title", Summary: "Decision summary", Status: domain.DecisionStatusActive, CreatedAt: fixedAt.Add(45 * time.Minute)},
		Activity: domain.IssueActivity{HasMore: true, Items: []domain.ActivityItem{
			{EntityType: domain.ActivityEntityTypeComment, EntityID: "01ARZ3NDEKTSV4RRFFQ69G5FAV11", IssueID: issueID, OccurredAt: fixedAt.Add(1 * time.Minute), Comment: &domain.Comment{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV11", IssueID: issueID, Content: "Comment <script>alert(3)</script>", CreatedAt: fixedAt.Add(1 * time.Minute)}},
			{EntityType: domain.ActivityEntityTypeDecision, EntityID: "01ARZ3NDEKTSV4RRFFQ69G5FAV12", IssueID: issueID, OccurredAt: fixedAt.Add(2 * time.Minute), Decision: &domain.Decision{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV12", IssueID: &issueID, Title: "Decision title", Summary: "Decision summary", Status: domain.DecisionStatusActive, CreatedAt: fixedAt.Add(2 * time.Minute)}},
			{EntityType: domain.ActivityEntityTypeAttempt, EntityID: "01ARZ3NDEKTSV4RRFFQ69G5FAV13", IssueID: issueID, OccurredAt: fixedAt.Add(3 * time.Minute), Attempt: &domain.WorkAttempt{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV13", IssueID: issueID, Kind: domain.AttemptKindWork, Status: domain.AttemptStatusCompleted, ResultSummary: strPtr("Work done <b>today</b>"), StartedAt: fixedAt.Add(3 * time.Minute)}},
			{EntityType: domain.ActivityEntityTypeReview, EntityID: "01ARZ3NDEKTSV4RRFFQ69G5FAV14", IssueID: issueID, OccurredAt: fixedAt.Add(4 * time.Minute), Review: &domain.ReviewRequest{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV14", IssueID: issueID, Status: domain.ReviewRequestStatusApproved}},
		}},
		Reservations: []domain.ReservationSummary{
			{ID: "reservation-active", IssueID: issueID, AttemptID: "attempt-777", Kind: domain.ResourceKindFile, DisplayValue: "current.go", Status: domain.ReservationStatusActive, CreatedAt: fixedAt.Add(5 * time.Minute)},
			{ID: "reservation-released", IssueID: issueID, AttemptID: "attempt-777", Kind: domain.ResourceKindGlob, DisplayValue: "old/<script>alert(5)</script>/**", Status: domain.ReservationStatusReleased, CreatedAt: fixedAt.Add(6 * time.Minute), ReleasedAt: &archivedAt, ReleaseReason: reservationReleaseReasonPtr(domain.ReservationReleaseReasonExplicit)},
		},
		HasMoreReservations: true,
	}

	sparseDetail := domain.IssueDetail{Issue: domain.Issue{ID: issueID, DisplayID: "ISSUE-301", Title: "Sparse detail", Status: domain.StatusOpen, Priority: domain.PriorityMedium, Type: domain.TypeBug, Version: 1, CreatedAt: fixedAt, UpdatedAt: fixedAt.Add(1 * time.Minute)}}

	t.Run("rich detail", func(t *testing.T) {
		html, err := renderIssueDetailHTML(richDetail)
		if err != nil {
			t.Fatalf("renderIssueDetailHTML: %v", err)
		}
		for _, want := range []string{"ISSUE-300", "Archived:", "alpha, beta", "Need &lt;script&gt;alert(1)&lt;/script&gt;", "Approval &lt;b&gt;required&lt;/b&gt;", "Blocked by &lt;script&gt;alert(2)&lt;/script&gt;", "Root issue", "href=\"/issues/ISSUE-301\"", "Latest attempt", "attempt-777", "Open review", "review-open", "Latest decision", "Decision summary", "Related graph", "aria-label=\"Planning graph\"", "Additional activity is available.", "Comment &lt;script&gt;alert(3)&lt;/script&gt;", "Work done &lt;b&gt;today&lt;/b&gt;", "current.go", "old/&lt;script&gt;alert(5)&lt;/script&gt;/**", "active", "released", "Additional reservations are available."} {
			if !strings.Contains(html, want) {
				t.Fatalf("rich detail missing %q:\n%s", want, html)
			}
		}
		if strings.Contains(html, "<script>alert(1)</script>") || strings.Contains(html, "Blocked by <script>") || strings.Contains(html, "<script>alert(5)</script>") {
			t.Fatalf("rich detail rendered raw script-like content: %s", html)
		}
		releasedRow := tableRowFor(t, html, "old/&lt;script&gt;alert(5)&lt;/script&gt;/**")
		if !strings.Contains(releasedRow, fixedAt.Add(2*time.Hour).Format(time.RFC3339)) {
			t.Fatalf("released reservation row missing its released-at timestamp: %s", releasedRow)
		}
		activeRow := tableRowFor(t, html, "current.go")
		if strings.Count(activeRow, "—") != 1 {
			t.Fatalf("active reservation row should show the empty placeholder for released-at only: %s", activeRow)
		}
	})

	t.Run("self root issue is omitted", func(t *testing.T) {
		selfRootDetail := sparseDetail
		selfRootDetail.RootIssueProjection = &domain.IssueProjection{Issue: domain.Issue{ID: issueID, DisplayID: "ISSUE-301"}}
		html, err := renderIssueDetailHTML(selfRootDetail)
		if err != nil {
			t.Fatalf("renderIssueDetailHTML: %v", err)
		}
		if strings.Contains(html, "<h2>Root issue</h2>") {
			t.Fatalf("self root section should be omitted: %s", html)
		}
		if strings.Contains(html, "href=\"/issues/ISSUE-301\"") {
			t.Fatalf("self root link should be omitted: %s", html)
		}
	})

	t.Run("sparse detail", func(t *testing.T) {
		html, err := renderIssueDetailHTML(sparseDetail)
		if err != nil {
			t.Fatalf("renderIssueDetailHTML: %v", err)
		}
		for _, want := range []string{"ISSUE-301", "Not archived.", "No labels assigned.", "No description provided.", "No acceptance criteria provided.", "No blocked reason provided.", "No activity recorded yet.", "No current or historical reservations.", "Rhizome issue detail"} {
			if !strings.Contains(html, want) {
				t.Fatalf("sparse detail missing %q:\n%s", want, html)
			}
		}
		if strings.Contains(html, "Latest attempt") || strings.Contains(html, "Related graph") {
			t.Fatalf("sparse detail unexpectedly rendered detail sections: %s", html)
		}
	})
}
