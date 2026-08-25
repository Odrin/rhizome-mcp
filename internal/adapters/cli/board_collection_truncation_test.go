package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"rhizome-mcp/internal/domain"
)

// boardResultWithTruncation returns an otherwise-empty board whose four
// bounded collections carry the given truncation flags. Every collection is
// non-nil so the table and both templates render their real branches.
func boardResultWithTruncation(truncation domain.BoardTruncation) domain.BoardResult {
	return domain.BoardResult{
		GeneratedAt:        time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC),
		StatusCounts:       []domain.EffectiveStatusCount{{EffectiveStatus: domain.EffectiveStatusReady, Count: 1}},
		ActiveAttempts:     []domain.ActiveAttemptSummary{},
		AttemptGates:       []domain.AttemptGateProgress{},
		ActiveReservations: []domain.Reservation{},
		BlockedIssues:      []domain.IssueProjection{},
		ReviewRequests:     []domain.ReviewRequest{},
		PlanningGraph: domain.GraphResult{
			Nodes: []domain.IssueProjection{}, Edges: []domain.GraphEdge{},
			EntryPoints: []string{}, BlockingNodes: []string{}, Warnings: []string{},
		},
		Truncation: truncation,
	}
}

// TestBoardTableReportsEachCollectionTruncation asserts writeBoardTable emits
// the docs/06 section 7 marker once per cut collection and stays silent
// otherwise, so a board consumer reading the table can never mistake a cut
// list for a complete one.
func TestBoardTableReportsEachCollectionTruncation(t *testing.T) {
	marker := "truncated\ttrue\t(first 100 shown)"

	tests := []struct {
		name       string
		truncation domain.BoardTruncation
		wantCount  int
	}{
		{name: "none", truncation: domain.BoardTruncation{}, wantCount: 0},
		{name: "blocked issues", truncation: domain.BoardTruncation{BlockedIssues: true}, wantCount: 1},
		{name: "active attempts", truncation: domain.BoardTruncation{ActiveAttempts: true}, wantCount: 1},
		{name: "active reservations", truncation: domain.BoardTruncation{ActiveReservations: true}, wantCount: 1},
		{name: "review requests", truncation: domain.BoardTruncation{ReviewRequests: true}, wantCount: 1},
		{name: "all four", truncation: domain.BoardTruncation{
			BlockedIssues: true, ActiveAttempts: true, ActiveReservations: true, ReviewRequests: true,
		}, wantCount: 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			cli := New(Services{}, &stdout, nil, nil, nil)
			if err := cli.writeBoardTable(boardResultWithTruncation(tt.truncation)); err != nil {
				t.Fatalf("writeBoardTable() error = %v", err)
			}
			if got := strings.Count(stdout.String(), marker); got != tt.wantCount {
				t.Fatalf("marker count = %d, want %d; table output = %q", got, tt.wantCount, stdout.String())
			}
		})
	}
}

// TestBoardTableTruncationMarkerFollowsItsOwnSection pins each marker to the
// section it describes: the flags are per collection, so a marker printed
// under the wrong heading would misreport which list was cut.
func TestBoardTableTruncationMarkerFollowsItsOwnSection(t *testing.T) {
	var stdout bytes.Buffer
	cli := New(Services{}, &stdout, nil, nil, nil)
	if err := cli.writeBoardTable(boardResultWithTruncation(domain.BoardTruncation{BlockedIssues: true})); err != nil {
		t.Fatalf("writeBoardTable() error = %v", err)
	}
	output := stdout.String()
	blockedAt := strings.Index(output, "\nblocked_issues\n")
	reviewAt := strings.Index(output, "\nreview_requests\n")
	markerAt := strings.Index(output, "truncated\ttrue\t(first 100 shown)")
	if blockedAt < 0 || reviewAt < 0 || markerAt < 0 {
		t.Fatalf("table output = %q", output)
	}
	if markerAt < blockedAt || markerAt > reviewAt {
		t.Fatalf("blocked-issues marker at %d, want between %d and %d; output = %q", markerAt, blockedAt, reviewAt, output)
	}
}

// TestBoardHTMLReportsEachCollectionTruncation asserts both the offline
// snapshot and the served page name the cut collection. The two notes in the
// attempts section describe different collections, so identical wording would
// leave a reader unable to tell which list was truncated.
func TestBoardHTMLReportsEachCollectionTruncation(t *testing.T) {
	notes := map[string]struct {
		truncation domain.BoardTruncation
		note       string
	}{
		"active attempts":     {domain.BoardTruncation{ActiveAttempts: true}, "Showing the first 100 active attempts; more exist."},
		"active reservations": {domain.BoardTruncation{ActiveReservations: true}, "Showing the first 100 resource reservations; more exist."},
		"blocked issues":      {domain.BoardTruncation{BlockedIssues: true}, "Showing the first 100 blocked issues; more exist."},
		"review requests":     {domain.BoardTruncation{ReviewRequests: true}, "Showing the first 100 open review requests; more exist."},
	}

	renderers := map[string]func(domain.BoardResult) (string, error){
		"static": renderBoardHTML,
		"served": renderServedBoardHTML,
	}

	for rendererName, render := range renderers {
		for collection, expectation := range notes {
			t.Run(rendererName+"/"+collection, func(t *testing.T) {
				page, err := render(boardResultWithTruncation(expectation.truncation))
				if err != nil {
					t.Fatalf("render error = %v", err)
				}
				if !strings.Contains(page, expectation.note) {
					t.Fatalf("page is missing %q", expectation.note)
				}
				for otherCollection, other := range notes {
					if otherCollection == collection {
						continue
					}
					if strings.Contains(page, other.note) {
						t.Fatalf("page names %q too, but only %s was truncated", other.note, collection)
					}
				}
			})
		}

		t.Run(rendererName+"/none", func(t *testing.T) {
			page, err := render(boardResultWithTruncation(domain.BoardTruncation{}))
			if err != nil {
				t.Fatalf("render error = %v", err)
			}
			for _, expectation := range notes {
				if strings.Contains(page, expectation.note) {
					t.Fatalf("untruncated page names %q", expectation.note)
				}
			}
		})
	}
}
