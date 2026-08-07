package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

func TestWriteTableWriters(t *testing.T) {
	t.Run("project info", func(t *testing.T) {
		var stdout bytes.Buffer
		cli := New(Services{}, &stdout, nil, nil, nil)
		project := domain.Project{ID: "proj-1", Name: stringPtr("Northwind"), NextIssueNumber: 7, SchemaVersion: 9, LatestEventID: 42}
		if err := cli.writeProjectInfoTable(project, "v1.2.3"); err != nil {
			t.Fatalf("writeProjectInfoTable: %v", err)
		}
		got := stdout.String()
		for _, want := range []string{"id\tproj-1", "name\tNorthwind", "next_issue_number\t7", "schema_version\t9", "app_version\tv1.2.3", "latest_event_id\t42"} {
			if !strings.Contains(got, want) {
				t.Fatalf("output %q does not contain %q", got, want)
			}
		}
	})

	t.Run("issue list and show", func(t *testing.T) {
		var stdout bytes.Buffer
		cli := New(Services{}, &stdout, nil, nil, nil)
		issue := domain.Issue{DisplayID: "ISSUE-100", Type: domain.TypeBug, Status: domain.StatusBlocked, Priority: domain.PriorityCritical, Title: "Need\tvalue\nnow"}
		if err := cli.writeIssueListTable(domain.IssueList{Items: []domain.IssueProjection{{Issue: issue}}}); err != nil {
			t.Fatalf("writeIssueListTable: %v", err)
		}
		got := stdout.String()
		if !strings.Contains(got, "display_id\ttype\tstatus\tpriority\ttitle") {
			t.Fatalf("missing header in issue list output: %q", got)
		}
		if !strings.Contains(got, "ISSUE-100\tbug\tblocked\tcritical\tNeed value now") {
			t.Fatalf("unexpected issue list output: %q", got)
		}
		stdout.Reset()
		if err := cli.writeIssueTable(issue); err != nil {
			t.Fatalf("writeIssueTable: %v", err)
		}
		got = stdout.String()
		if !strings.Contains(got, "display_id\tISSUE-100") || !strings.Contains(got, "title\tNeed value now") {
			t.Fatalf("unexpected issue table output: %q", got)
		}
	})

	t.Run("search", func(t *testing.T) {
		var stdout bytes.Buffer
		cli := New(Services{}, &stdout, nil, nil, nil)
		issueID := "ISSUE-7"
		page := domain.SearchPage{Results: []domain.SearchResult{{EntityType: domain.SearchEntityTypeIssue, EntityID: "evt-1", IssueID: &issueID, Title: "Alpha\tBeta\nGamma"}}}
		if err := cli.writeSearchTable(page); err != nil {
			t.Fatalf("writeSearchTable: %v", err)
		}
		got := stdout.String()
		if !strings.Contains(got, "entity_type\tentity_id\tissue_id\ttitle") {
			t.Fatalf("missing search header: %q", got)
		}
		if !strings.Contains(got, "issue\tevt-1\tISSUE-7\tAlpha Beta Gamma") {
			t.Fatalf("unexpected search output: %q", got)
		}
	})

	t.Run("graph", func(t *testing.T) {
		var stdout bytes.Buffer
		cli := New(Services{}, &stdout, nil, nil, nil)
		result := domain.GraphResult{Nodes: []domain.IssueProjection{{Issue: domain.Issue{ID: "node-1", DisplayID: "ISSUE-1", Title: "Need\tline\nwrap", Status: domain.StatusOpen}}}, Edges: []domain.GraphEdge{{SourceIssueID: "node-1", TargetIssueID: "node-1", Type: "blocks"}}}
		if err := cli.writeGraphTable(result); err != nil {
			t.Fatalf("writeGraphTable: %v", err)
		}
		got := stdout.String()
		if !strings.Contains(got, "node\tstate\ttitle") || !strings.Contains(got, "edges") {
			t.Fatalf("missing graph header/edges section: %q", got)
		}
		if !strings.Contains(got, "ISSUE-1\topen\tNeed line wrap") {
			t.Fatalf("unexpected graph output: %q", got)
		}
	})

	t.Run("board", func(t *testing.T) {
		var stdout bytes.Buffer
		cli := New(Services{}, &stdout, nil, nil, nil)
		generatedAt := time.Date(2026, 8, 7, 12, 34, 56, 0, time.UTC)
		leaseExpiresAt := generatedAt.Add(15 * time.Minute)
		label := "agent\talpha\nline"
		blockedReason := "needs\tclarity\nnow"
		result := domain.BoardResult{
			GeneratedAt:    generatedAt,
			StatusCounts:   []domain.EffectiveStatusCount{{EffectiveStatus: domain.EffectiveStatusBlocked, Count: 2}},
			ActiveAttempts: []domain.ActiveAttemptSummary{{AttemptID: "att-1", IssueDisplayID: "ISSUE-100", Kind: domain.AttemptKindWork, SessionLabel: &label, LeaseExpiresAt: leaseExpiresAt}},
			BlockedIssues:  []domain.IssueProjection{{Issue: domain.Issue{DisplayID: "ISSUE-101", Title: "Blocked title", BlockedReason: &blockedReason}}},
			ReviewRequests: []domain.ReviewRequest{{ID: "rev-1", IssueID: "ISSUE-101", Status: "open", CreatedAt: generatedAt.Add(1 * time.Minute)}},
			PlanningGraph:  domain.GraphResult{Summary: domain.GraphSummary{NodeCount: 3, EdgeCount: 2, EntryPointCount: 1, BlockingNodeCount: 1}},
		}
		if err := cli.writeBoardTable(result); err != nil {
			t.Fatalf("writeBoardTable: %v", err)
		}
		got := stdout.String()
		for _, want := range []string{"generated_at\t2026-08-07T12:34:56Z", "effective_status\tcount", "att-1\tISSUE-100\twork\tagent alpha line", "ISSUE-101\tBlocked title\tneeds clarity now", "rev-1\tISSUE-101\topen\t2026-08-07T12:35:56Z", "nodes\t3", "entry_points\t1"} {
			if !strings.Contains(got, want) {
				t.Fatalf("output %q does not contain %q", got, want)
			}
		}
	})

	t.Run("maintenance release attempt", func(t *testing.T) {
		var stdout bytes.Buffer
		cli := New(Services{}, &stdout, nil, nil, nil)
		finishedAt := time.Date(2026, 8, 7, 12, 45, 0, 0, time.UTC)
		interruption := domain.InterruptionReasonHandoff
		result := ports.ForceReleaseAttemptResult{Attempt: domain.WorkAttempt{ID: "att-2", Status: domain.AttemptStatusInterrupted, InterruptionReasonCode: &interruption, FinishedAt: &finishedAt}, LatestEventID: 18}
		if err := cli.writeMaintenanceReleaseAttemptTable(result); err != nil {
			t.Fatalf("writeMaintenanceReleaseAttemptTable: %v", err)
		}
		got := stdout.String()
		for _, want := range []string{"attempt_id\tstatus\tinterruption_reason\tfinished_at\tlatest_event_id", "att-2\tinterrupted\thandoff\t2026-08-07T12:45:00Z\t18"} {
			if !strings.Contains(got, want) {
				t.Fatalf("output %q does not contain %q", got, want)
			}
		}
	})

	t.Run("doctor", func(t *testing.T) {
		var stdout bytes.Buffer
		cli := New(Services{}, &stdout, nil, nil, nil)
		report := DoctorReport{Full: true, AppVersion: "v2.0.0", Checks: []DoctorCheck{{Check: "db", Healthy: true, Message: "ok"}, {Check: "config", Healthy: false, Message: "bad"}}}
		if err := cli.writeDoctorTable(report); err != nil {
			t.Fatalf("writeDoctorTable: %v", err)
		}
		got := stdout.String()
		if !strings.Contains(got, "mode") || !strings.Contains(got, "overall_health") || !strings.Contains(got, "app_version") {
			t.Fatalf("missing doctor header sections: %q", got)
		}
		if !strings.Contains(got, ansiGreen) || !strings.Contains(got, ansiRed) {
			t.Fatalf("expected colored doctor statuses in %q", got)
		}
	})

	t.Run("backup", func(t *testing.T) {
		var stdout bytes.Buffer
		cli := New(Services{}, &stdout, nil, nil, nil)
		if err := cli.writeBackupTable(BackupReport{OutputPath: "/tmp/backup.db", SchemaVersion: 3}); err != nil {
			t.Fatalf("writeBackupTable: %v", err)
		}
		got := stdout.String()
		for _, want := range []string{"output\t/tmp/backup.db", "schema_version\t3", "validated\ttrue"} {
			if !strings.Contains(got, want) {
				t.Fatalf("output %q does not contain %q", got, want)
			}
		}
	})
}

func TestRunCommandFlowsAndParsing(t *testing.T) {
	t.Run("project info table and json", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			args     []string
			wantText string
		}{
			{name: "table", args: []string{"project", "info"}, wantText: "name\tNorthwind"},
			{name: "json", args: []string{"project", "info", "--format", "json"}, wantText: `"app_version": "v1.2.3"`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				stub := &stubProjectService{project: domain.Project{ID: "proj-1", Name: stringPtr("Northwind")}}
				cli := New(Services{ProjectService: stub}, &stdout, &stderr, nil, nil)
				cli.SetAppVersion("v1.2.3")
				if err := cli.Run(context.Background(), tc.args); err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if stub.calls != 1 {
					t.Fatalf("expected one project service call, got %d", stub.calls)
				}
				if !strings.Contains(stdout.String(), tc.wantText) {
					t.Fatalf("output %q does not contain %q", stdout.String(), tc.wantText)
				}
				if stderr.Len() != 0 {
					t.Fatalf("expected no stderr output, got %q", stderr.String())
				}
			})
		}
	})

	t.Run("issue list and show forward parsed values", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		stub := &stubIssueService{}
		cli := New(Services{IssueService: stub}, &stdout, &stderr, nil, nil)
		if err := cli.Run(context.Background(), []string{"issue", "list", "--format", "table", "--limit", "7", "--cursor", "abc", "--type", "task", "--status", "blocked", "--effective-status", "review", "--priority", "critical", "--include-archived"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stub.listCalls != 1 {
			t.Fatalf("expected one list call, got %d", stub.listCalls)
		}
		if stub.listInput.Limit != 7 || stub.listInput.Cursor != "abc" || !containsType(stub.listInput.Types, domain.TypeTask) || !containsStatus(stub.listInput.Statuses, domain.StatusBlocked) || !containsEffectiveStatus(stub.listInput.EffectiveStatuses, domain.EffectiveStatusReview) || !containsPriority(stub.listInput.Priorities, domain.PriorityCritical) || !stub.listInput.IncludeArchived {
			t.Fatalf("unexpected list input: %+v", stub.listInput)
		}
		if !strings.Contains(stdout.String(), "display_id\ttype\tstatus\tpriority\ttitle") {
			t.Fatalf("missing table header: %q", stdout.String())
		}
		stdout.Reset()
		stub.showIssue = domain.Issue{DisplayID: "ISSUE-42", Title: "Shown"}
		if err := cli.Run(context.Background(), []string{"issue", "show", "ISSUE-42", "--format", "json"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stub.showCalls != 1 || stub.showID != "ISSUE-42" {
			t.Fatalf("expected show service call for ISSUE-42, got calls=%d id=%q", stub.showCalls, stub.showID)
		}
		if !strings.Contains(stdout.String(), `"display_id": "ISSUE-42"`) {
			t.Fatalf("expected JSON output, got %q", stdout.String())
		}
	})

	t.Run("search forwards parsed input", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		stub := &stubSearchService{page: domain.SearchPage{Results: []domain.SearchResult{{EntityType: domain.SearchEntityTypeIssue, Title: "hit"}}}}
		cli := New(Services{SearchService: stub}, &stdout, &stderr, nil, nil)
		if err := cli.Run(context.Background(), []string{"search", "needle", "--format", "table", "--limit", "3", "--cursor", "cur", "--entity-type", "issue", "--issue", "ISSUE-4", "--epic", "ISSUE-5", "--status", "open", "--label", "alpha", "--include-archived", "--snippet-length", "11"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stub.calls != 1 {
			t.Fatalf("expected one search service call, got %d", stub.calls)
		}
		if stub.input.Query != "needle" || stub.input.Limit != 3 || stub.input.Cursor != "cur" || stub.input.IncludeArchived != true || stub.input.SnippetLength != 11 || stub.input.IssueID == nil || *stub.input.IssueID != "ISSUE-4" || stub.input.EpicID == nil || *stub.input.EpicID != "ISSUE-5" || !containsSearchEntityType(stub.input.EntityTypes, domain.SearchEntityTypeIssue) || !containsString(stub.input.Labels, "alpha") {
			t.Fatalf("unexpected search input: %+v", stub.input)
		}
		if !strings.Contains(stdout.String(), "entity_type\tentity_id\tissue_id\ttitle") {
			t.Fatalf("missing search table output: %q", stdout.String())
		}
	})

	t.Run("graph forwards parsed input", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		stub := &stubGraphService{graph: domain.GraphResult{Nodes: []domain.IssueProjection{{Issue: domain.Issue{ID: "node-1", DisplayID: "ISSUE-1"}}}}}
		cli := New(Services{GraphService: stub}, &stdout, &stderr, nil, nil)
		if err := cli.Run(context.Background(), []string{"graph", "ISSUE-1", "--format", "table", "--depth", "2", "--max-nodes", "10", "--direction", "outgoing", "--relation-type", "blocks", "--include-hierarchy", "--include-terminal"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stub.calls != 1 {
			t.Fatalf("expected one graph service call, got %d", stub.calls)
		}
		if stub.input.RootIssueID != "ISSUE-1" || stub.input.Direction != domain.GraphDirectionOutgoing || stub.input.Depth == nil || *stub.input.Depth != 2 || stub.input.MaxNodes == nil || *stub.input.MaxNodes != 10 || stub.input.IncludeHierarchy == nil || !*stub.input.IncludeHierarchy || stub.input.IncludeTerminal == nil || !*stub.input.IncludeTerminal || !containsRelationType(stub.input.RelationTypes, domain.RelationTypeBlocks) {
			t.Fatalf("unexpected graph input: %+v", stub.input)
		}
		if !strings.Contains(stdout.String(), "node\tstate\ttitle") {
			t.Fatalf("missing graph table output: %q", stdout.String())
		}
	})

	t.Run("board uses json and table output", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			args []string
			want string
		}{
			{name: "table", args: []string{"board", "--format", "table"}, want: "generated_at"},
			{name: "json", args: []string{"board", "--format", "json"}, want: `"generated_at"`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				stub := &stubBoardService{board: domain.BoardResult{GeneratedAt: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)}}
				cli := New(Services{BoardService: stub}, &stdout, &stderr, nil, nil)
				if err := cli.Run(context.Background(), tc.args); err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if stub.calls != 1 {
					t.Fatalf("expected one board service call, got %d", stub.calls)
				}
				if !strings.Contains(stdout.String(), tc.want) {
					t.Fatalf("output %q does not contain %q", stdout.String(), tc.want)
				}
			})
		}
	})

	t.Run("maintenance release attempt and rebuild", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		stub := &stubMaintenanceService{releaseResult: ports.ForceReleaseAttemptResult{Attempt: domain.WorkAttempt{ID: "att-9"}, LatestEventID: 12}}
		cli := New(Services{MaintenanceService: stub}, &stdout, &stderr, nil, nil)
		if err := cli.Run(context.Background(), []string{"maintenance", "release-attempt", "ATT-9", "--format", "json"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !stub.calledRelease || stub.releaseID != "ATT-9" {
			t.Fatalf("expected release attempt to be invoked for ATT-9, got called=%v id=%q", stub.calledRelease, stub.releaseID)
		}
		var payload struct {
			LatestEventID int64 `json:"latest_event_id"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal maintenance JSON: %v", err)
		}
		if payload.LatestEventID != 12 {
			t.Fatalf("unexpected latest event id: %d", payload.LatestEventID)
		}
		stdout.Reset()
		if err := cli.Run(context.Background(), []string{"maintenance", "rebuild-search-index", "--format", "table"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !stub.calledRebuild {
			t.Fatalf("expected rebuild to be invoked")
		}
		if !strings.Contains(stdout.String(), "search index rebuilt") {
			t.Fatalf("missing rebuild output: %q", stdout.String())
		}
	})
}

func TestRunValidationErrorsBeforeServiceCalls(t *testing.T) {
	t.Run("unsupported format", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		stub := &stubProjectService{}
		cli := New(Services{ProjectService: stub}, &stdout, &stderr, nil, nil)
		err := cli.Run(context.Background(), []string{"project", "info", "--format", "markdown"})
		if err == nil || !strings.Contains(err.Error(), "unsupported format") {
			t.Fatalf("expected unsupported format error, got %v", err)
		}
		if stub.calls != 0 {
			t.Fatalf("expected no service call, got %d", stub.calls)
		}
	})

	t.Run("extra positionals", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		stub := &stubIssueService{}
		cli := New(Services{IssueService: stub}, &stdout, &stderr, nil, nil)
		err := cli.Run(context.Background(), []string{"issue", "show", "ISSUE-1", "extra"})
		if err == nil || !strings.Contains(err.Error(), "usage error") {
			t.Fatalf("expected usage error, got %v", err)
		}
		if stub.showCalls != 0 || stub.listCalls != 0 {
			t.Fatalf("expected no service call, got show=%d list=%d", stub.showCalls, stub.listCalls)
		}
	})

	t.Run("missing positional for search", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		stub := &stubSearchService{}
		cli := New(Services{SearchService: stub}, &stdout, &stderr, nil, nil)
		err := cli.Run(context.Background(), []string{"search"})
		if err == nil || !strings.Contains(err.Error(), "usage error") {
			t.Fatalf("expected usage error, got %v", err)
		}
		if stub.calls != 0 {
			t.Fatalf("expected no service call, got %d", stub.calls)
		}
	})
}

func stringPtr(value string) *string { return &value }

func containsType(values []domain.Type, want domain.Type) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsStatus(values []domain.Status, want domain.Status) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsEffectiveStatus(values []domain.EffectiveStatus, want domain.EffectiveStatus) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsPriority(values []domain.Priority, want domain.Priority) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsSearchEntityType(values []domain.SearchEntityType, want domain.SearchEntityType) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsRelationType(values []domain.RelationType, want domain.RelationType) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
