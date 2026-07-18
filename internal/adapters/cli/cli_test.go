package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
)

type stubProjectService struct {
	project domain.Project
	err     error
	calls   int
}

func (s *stubProjectService) GetProject(context.Context) (domain.Project, error) {
	s.calls++
	return s.project, s.err
}

func (s *stubProjectService) ExportLogicalProject(context.Context) ([]byte, error) {
	s.calls++
	return []byte(`{
  "format": "rhizome-logical-project",
  "version": 1,
  "project": {},
  "issues": [],
  "labels": [],
  "issue_labels": [],
  "relations": [],
  "comments": [],
  "decisions": [],
  "attempts": [],
  "attempt_notes": [],
  "artifacts": [],
  "events": []
}`), s.err
}

func (s *stubProjectService) ValidateLogicalProjectImport(context.Context, []byte) (domain.LogicalProjectImportDryRun, error) {
	s.calls++
	return domain.LogicalProjectImportDryRun{}, s.err
}

func (s *stubProjectService) ApplyLogicalProjectImport(context.Context, []byte) (domain.LogicalProjectImportApplyResult, error) {
	s.calls++
	return domain.LogicalProjectImportApplyResult{}, s.err
}

type stubIssueService struct {
	listInput domain.ListIssuesInput
	listPage  domain.IssueList
	showID    string
	showIssue domain.Issue
	listErr   error
	showErr   error
	listCalls int
	showCalls int
}

func (s *stubIssueService) ListIssues(ctx context.Context, input domain.ListIssuesInput) (domain.IssueList, error) {
	s.listCalls++
	s.listInput = input
	return s.listPage, s.listErr
}

func (s *stubIssueService) GetIssue(ctx context.Context, identifier string) (domain.Issue, error) {
	s.showCalls++
	s.showID = identifier
	return s.showIssue, s.showErr
}

type stubSearchService struct {
	input domain.SearchInput
	page  domain.SearchPage
	err   error
	calls int
}

func (s *stubSearchService) Search(ctx context.Context, input domain.SearchInput) (domain.SearchPage, error) {
	s.calls++
	s.input = input
	return s.page, s.err
}

type stubGraphService struct {
	input domain.GetIssueGraphInput
	graph domain.GraphResult
	err   error
	calls int
}

func (s *stubGraphService) GetIssueGraph(ctx context.Context, input domain.GetIssueGraphInput) (domain.GraphResult, error) {
	s.calls++
	s.input = input
	return s.graph, s.err
}

type stubMaintenanceService struct {
	releaseResult ports.ForceReleaseAttemptResult
	releaseErr    error
	rebuildErr    error
	calledRelease bool
	calledRebuild bool
	releaseID     string
}

func (s *stubMaintenanceService) ForceReleaseAttempt(ctx context.Context, attemptID string) (ports.ForceReleaseAttemptResult, error) {
	s.calledRelease = true
	s.releaseID = attemptID
	return s.releaseResult, s.releaseErr
}

func (s *stubMaintenanceService) RebuildSearchIndex(ctx context.Context) error {
	s.calledRebuild = true
	return s.rebuildErr
}

func TestRunUsageAndErrors(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantUsage   bool
		wantErrText string
	}{
		{name: "no args", args: nil, wantUsage: true},
		{name: "unknown command", args: []string{"unknown"}, wantUsage: true},
		{name: "invalid format", args: []string{"project", "info", "--format", "markdown"}, wantUsage: false, wantErrText: "unsupported format"},
		{name: "missing issue show arg", args: []string{"issue", "show"}, wantUsage: false, wantErrText: "usage error"},
		{name: "duplicate search issue filter", args: []string{"search", "query", "--issue", "ISSUE-1", "--issue", "ISSUE-2"}, wantUsage: false, wantErrText: "may only be specified once"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			services := Services{
				ProjectService: &stubProjectService{project: domain.Project{}},
				IssueService:   &stubIssueService{},
				SearchService:  &stubSearchService{},
			}
			cli := New(services, &stdout, &stderr, nil, nil)
			err := cli.Run(context.Background(), tt.args)
			if err == nil {
				t.Fatalf("expected error")
			}
			if tt.wantUsage && !strings.Contains(stderr.String(), "rhizome-mcp [--data-root PATH] project info") {
				t.Fatalf("expected usage text in stderr, got %q", stderr.String())
			}
			if tt.wantErrText != "" && !strings.Contains(err.Error(), tt.wantErrText) {
				t.Fatalf("expected error to contain %q, got %q", tt.wantErrText, err.Error())
			}
		})
	}
}

func TestServeCommandParsesHTTPAddress(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var captured string
	cli := New(Services{}, &stdout, &stderr, nil, func(_ context.Context, httpAddress string) error {
		captured = httpAddress
		return nil
	})
	if err := cli.Run(context.Background(), []string{"serve", "--http-address", "127.0.0.1:0"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured != "127.0.0.1:0" {
		t.Fatalf("captured HTTP address = %q, want %q", captured, "127.0.0.1:0")
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr output, got %q", stderr.String())
	}
}

func TestRunProjectImportApply(t *testing.T) {
	var stdout, stderr bytes.Buffer
	services := Services{ProjectService: &stubProjectService{}}
	cli := New(services, &stdout, &stderr, nil, nil)
	if err := cli.Run(context.Background(), []string{"project", "import", "--input", "-", "--apply"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr output, got %q", stderr.String())
	}
	if stdout.Len() == 0 {
		t.Fatal("expected JSON output")
	}
}

func TestRunProjectExport(t *testing.T) {
	t.Run("stdout", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		cli := New(Services{ProjectService: &stubProjectService{}}, &stdout, &stderr, nil, nil)
		if err := cli.Run(context.Background(), []string{"project", "export", "--output", "-"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		output := stdout.String()
		if !strings.Contains(output, "\"format\": \"rhizome-logical-project\"") || !strings.HasSuffix(output, "\n") {
			t.Fatalf("expected newline-terminated JSON stdout, got %q", output)
		}
		if stderr.Len() != 0 {
			t.Fatalf("expected no stderr output, got %q", stderr.String())
		}
	})

	t.Run("file overwrite", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "export.json")
		if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
			t.Fatalf("write existing file: %v", err)
		}
		var stdout, stderr bytes.Buffer
		cli := New(Services{ProjectService: &stubProjectService{}}, &stdout, &stderr, nil, nil)
		if err := cli.Run(context.Background(), []string{"project", "export", "--output", path, "--overwrite"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stdout.Len() != 0 {
			t.Fatalf("expected no stdout output for file export, got %q", stdout.String())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read exported file: %v", err)
		}
		if !strings.Contains(string(data), "\"format\": \"rhizome-logical-project\"") || !strings.HasSuffix(string(data), "\n") {
			t.Fatalf("expected exported file JSON, got %q", string(data))
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat exported file: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o, want 0600", info.Mode().Perm())
		}
	})

	t.Run("refuse overwrite", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "export.json")
		if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
			t.Fatalf("write existing file: %v", err)
		}
		var stdout, stderr bytes.Buffer
		cli := New(Services{ProjectService: &stubProjectService{}}, &stdout, &stderr, nil, nil)
		err := cli.Run(context.Background(), []string{"project", "export", "--output", path})
		if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
			t.Fatalf("expected overwrite refusal, got %v", err)
		}
		if stdout.Len() != 0 {
			t.Fatalf("expected no stdout output, got %q", stdout.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("expected no stderr output, got %q", stderr.String())
		}
	})
}

func TestRunBackup(t *testing.T) {
	t.Run("requires output", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		cli := New(Services{}, &stdout, &stderr, nil, nil)
		called := false
		cli.SetBackupHandler(func(context.Context, string) (BackupReport, error) {
			called = true
			return BackupReport{}, nil
		})
		err := cli.Run(context.Background(), []string{"backup"})
		if err == nil || err.Error() != "output is required" {
			t.Fatalf("expected output is required error, got %v", err)
		}
		if called {
			t.Fatal("backup handler should not be called")
		}
		if stdout.Len() != 0 {
			t.Fatalf("expected no stdout output, got %q", stdout.String())
		}
	})

	t.Run("table output", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		cli := New(Services{}, &stdout, &stderr, nil, nil)
		cli.SetBackupHandler(func(context.Context, string) (BackupReport, error) {
			return BackupReport{OutputPath: "/tmp/backup.db", SchemaVersion: 3}, nil
		})
		if err := cli.Run(context.Background(), []string{"backup", "--output", "/tmp/backup.db"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		output := stdout.String()
		for _, token := range []string{"output", "/tmp/backup.db", "schema_version", "3", "validated", "true"} {
			if !strings.Contains(output, token) {
				t.Fatalf("expected output to contain %q, got %q", token, output)
			}
		}
	})

	t.Run("json output", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		cli := New(Services{}, &stdout, &stderr, nil, nil)
		cli.SetBackupHandler(func(context.Context, string) (BackupReport, error) {
			return BackupReport{OutputPath: "/tmp/backup.db", SchemaVersion: 3}, nil
		})
		if err := cli.Run(context.Background(), []string{"backup", "--output", "/tmp/backup.db", "--format", "json"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		output := stdout.String()
		for _, token := range []string{"\"output\"", "\"schema_version\"", "\"validated\"", "true"} {
			if !strings.Contains(output, token) {
				t.Fatalf("expected output to contain %q, got %q", token, output)
			}
		}
	})

	t.Run("handler error no stdout", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		cli := New(Services{}, &stdout, &stderr, nil, nil)
		cli.SetBackupHandler(func(context.Context, string) (BackupReport, error) {
			return BackupReport{}, errors.New("boom")
		})
		err := cli.Run(context.Background(), []string{"backup", "--output", "/tmp/backup.db"})
		if err == nil || err.Error() != "boom" {
			t.Fatalf("expected boom error, got %v", err)
		}
		if stdout.Len() != 0 {
			t.Fatalf("expected no successful output, got %q", stdout.String())
		}
	})
}

func TestRunDoctor(t *testing.T) {
	t.Run("json output", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		cli := New(Services{}, &stdout, &stderr, nil, nil)
		full := false
		cli.SetDoctorHandler(func(_ context.Context, receivedFull bool) (DoctorReport, error) {
			full = receivedFull
			return DoctorReport{Full: receivedFull, Checks: []DoctorCheck{{Check: "ping", Healthy: true, Message: "ok"}}}, nil
		})
		if err := cli.Run(context.Background(), []string{"doctor", "--full", "--format", "json"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !full {
			t.Fatal("expected --full to be passed to the doctor handler")
		}
		output := stdout.String()
		for _, token := range []string{"\"full\"", "\"healthy\"", "\"checks\"", "\"ping\""} {
			if !strings.Contains(output, token) {
				t.Fatalf("expected output to contain %q, got %q", token, output)
			}
		}
	})

	t.Run("unhealthy output before error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		cli := New(Services{}, &stdout, &stderr, nil, nil)
		cli.SetDoctorHandler(func(context.Context, bool) (DoctorReport, error) {
			return DoctorReport{Full: false, Checks: []DoctorCheck{{Check: "ping", Healthy: false, Message: "failed"}}}, errors.New("boom")
		})

		t.Run("unhealthy report without handler error", func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cli := New(Services{}, &stdout, &stderr, nil, nil)
			cli.SetDoctorHandler(func(context.Context, bool) (DoctorReport, error) {
				return DoctorReport{Checks: []DoctorCheck{{Check: "ping", Healthy: false, Message: "failed"}}}, nil
			})
			err := cli.Run(context.Background(), []string{"doctor", "--format", "json"})
			if err == nil || err.Error() != "doctor found failed checks" {
				t.Fatalf("expected doctor found failed checks error, got %v", err)
			}
			if !strings.Contains(stdout.String(), "\"healthy\": false") {
				t.Fatalf("expected unhealthy JSON report, got %q", stdout.String())
			}
		})
		err := cli.Run(context.Background(), []string{"doctor"})
		if err == nil || err.Error() != "doctor found failed checks" {
			t.Fatalf("expected doctor found failed checks error, got %v", err)
		}
		output := stdout.String()
		if !strings.Contains(output, "ping") || !strings.Contains(output, "overall_health") {
			t.Fatalf("expected output to include report, got %q", output)
		}
	})

	t.Run("handler error no stdout", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		cli := New(Services{}, &stdout, &stderr, nil, nil)
		cli.SetDoctorHandler(func(context.Context, bool) (DoctorReport, error) {
			return DoctorReport{Full: true, Checks: []DoctorCheck{{Check: "ping", Healthy: true, Message: "ok"}}}, errors.New("boom")
		})
		err := cli.Run(context.Background(), []string{"doctor"})
		if err == nil || err.Error() != "boom" {
			t.Fatalf("expected boom error, got %v", err)
		}
		if stdout.Len() != 0 {
			t.Fatalf("expected no successful output, got %q", stdout.String())
		}
	})
}

func TestRunJSONOutput(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		setup     func(*Services)
		want      []string
		wantTrail string
	}{
		{
			name: "project info json",
			args: []string{"project", "info", "--format", "json"},
			setup: func(services *Services) {
				services.ProjectService = &stubProjectService{project: domain.Project{ID: "p-1", NextIssueNumber: 7}}
			},
			want:      []string{"\"project\"", "\"next_issue_number\"", "7"},
			wantTrail: "\n",
		},
		{
			name: "issue list json",
			args: []string{"issue", "list", "--format", "json"},
			setup: func(services *Services) {
				services.IssueService = &stubIssueService{listPage: domain.IssueList{Items: []domain.IssueProjection{{Issue: domain.Issue{ID: "i-1", DisplayID: "ISSUE-1", Title: "First"}}}, NextCursor: strPtr("cursor"), HasMore: true}}
			},
			want:      []string{"\"items\"", "\"next_cursor\"", "\"cursor\"", "\"has_more\""},
			wantTrail: "\n",
		},
		{
			name: "search json",
			args: []string{"search", "alpha", "--format", "json"},
			setup: func(services *Services) {
				services.SearchService = &stubSearchService{page: domain.SearchPage{Results: []domain.SearchResult{{EntityType: domain.SearchEntityTypeIssue, EntityID: "e-1", Title: "Alpha"}}, NextCursor: strPtr("next"), HasMore: false}}
			},
			want:      []string{"\"results\"", "\"next_cursor\"", "\"has_more\"", "\"results\""},
			wantTrail: "\n",
		},
		{
			name: "pagination metadata includes nil cursor",
			args: []string{"issue", "list", "--format", "json"},
			setup: func(services *Services) {
				services.IssueService = &stubIssueService{listPage: domain.IssueList{}}
			},
			want:      []string{"\"next_cursor\": null", "\"has_more\": false"},
			wantTrail: "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			services := Services{}
			tt.setup(&services)
			cli := New(services, &stdout, &stderr, nil, nil)
			if err := cli.Run(context.Background(), tt.args); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			output := stdout.String()
			for _, token := range tt.want {
				if !strings.Contains(output, token) {
					t.Fatalf("expected output to contain %q, got %q", token, output)
				}
			}
			if !strings.HasSuffix(output, tt.wantTrail) {
				t.Fatalf("expected output to end with %q, got %q", tt.wantTrail, output)
			}
		})
	}
}

func TestRunMapsServiceInputs(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		assertFunc func(*testing.T, *stubIssueService, *stubSearchService, *stubGraphService)
	}{
		{
			name: "issue list default delegation",
			args: []string{"issue", "list", "--type", "epic", "--status", "open", "--effective-status", "open", "--priority", "low", "--limit", "12", "--cursor", "abc", "--include-archived"},
			assertFunc: func(t *testing.T, issueService *stubIssueService, searchService *stubSearchService, graphService *stubGraphService) {
				if len(issueService.listInput.Types) != 1 || issueService.listInput.Types[0] != domain.TypeEpic {
					t.Fatalf("expected type epic, got %#v", issueService.listInput.Types)
				}
				if len(issueService.listInput.Statuses) != 1 || issueService.listInput.Statuses[0] != domain.StatusOpen {
					t.Fatalf("expected status open, got %#v", issueService.listInput.Statuses)
				}
				if len(issueService.listInput.EffectiveStatuses) != 1 || issueService.listInput.EffectiveStatuses[0] != domain.EffectiveStatusOpen {
					t.Fatalf("expected effective status open, got %#v", issueService.listInput.EffectiveStatuses)
				}
				if len(issueService.listInput.Priorities) != 1 || issueService.listInput.Priorities[0] != domain.PriorityLow {
					t.Fatalf("expected priority low, got %#v", issueService.listInput.Priorities)
				}
				if issueService.listInput.Limit != 12 || issueService.listInput.Cursor != "abc" || !issueService.listInput.IncludeArchived {
					t.Fatalf("unexpected issue list input: %#v", issueService.listInput)
				}
			},
		},
		{
			name: "search default delegation",
			args: []string{"search", "alpha", "--entity-type", "issue", "--issue", "ISSUE-1", "--epic", "ISSUE-2", "--status", "open", "--label", "alpha", "--include-archived", "--snippet-length", "33", "--limit", "5", "--cursor", "cursor"},
			assertFunc: func(t *testing.T, issueService *stubIssueService, searchService *stubSearchService, graphService *stubGraphService) {
				if searchService.input.Query != "alpha" {
					t.Fatalf("expected query alpha, got %q", searchService.input.Query)
				}
				if len(searchService.input.EntityTypes) != 1 || searchService.input.EntityTypes[0] != domain.SearchEntityTypeIssue {
					t.Fatalf("expected entity type issue, got %#v", searchService.input.EntityTypes)
				}
				if searchService.input.IssueID == nil || *searchService.input.IssueID != "ISSUE-1" {
					t.Fatalf("expected issue filter ISSUE-1, got %#v", searchService.input.IssueID)
				}
				if searchService.input.EpicID == nil || *searchService.input.EpicID != "ISSUE-2" {
					t.Fatalf("expected epic filter ISSUE-2, got %#v", searchService.input.EpicID)
				}
				if searchService.input.Limit != 5 || searchService.input.Cursor != "cursor" || searchService.input.SnippetLength != 33 || !searchService.input.IncludeArchived {
					t.Fatalf("unexpected search input: %#v", searchService.input)
				}
				if len(searchService.input.Statuses) != 1 || searchService.input.Statuses[0] != domain.StatusOpen {
					t.Fatalf("expected status open, got %#v", searchService.input.Statuses)
				}
				if len(searchService.input.Labels) != 1 || searchService.input.Labels[0] != "alpha" {
					t.Fatalf("expected label alpha, got %#v", searchService.input.Labels)
				}
			},
		},
		{
			name: "graph default delegation",
			args: []string{"graph", "ISSUE-42", "--depth", "3", "--max-nodes", "25", "--direction", "outgoing", "--relation-type", "blocks", "--include-hierarchy", "--include-terminal"},
			assertFunc: func(t *testing.T, issueService *stubIssueService, searchService *stubSearchService, graphService *stubGraphService) {
				if graphService.input.RootIssueID != "ISSUE-42" {
					t.Fatalf("expected root ISSUE-42, got %q", graphService.input.RootIssueID)
				}
				if graphService.input.Depth == nil || *graphService.input.Depth != 3 {
					t.Fatalf("expected depth 3, got %#v", graphService.input.Depth)
				}
				if graphService.input.MaxNodes == nil || *graphService.input.MaxNodes != 25 {
					t.Fatalf("expected max nodes 25, got %#v", graphService.input.MaxNodes)
				}
				if graphService.input.Direction != domain.GraphDirectionOutgoing {
					t.Fatalf("expected outgoing direction, got %q", graphService.input.Direction)
				}
				if len(graphService.input.RelationTypes) != 1 || graphService.input.RelationTypes[0] != domain.RelationTypeBlocks {
					t.Fatalf("expected relation type blocks, got %#v", graphService.input.RelationTypes)
				}
				if graphService.input.IncludeHierarchy == nil || !*graphService.input.IncludeHierarchy {
					t.Fatalf("expected include hierarchy true, got %#v", graphService.input.IncludeHierarchy)
				}
				if graphService.input.IncludeTerminal == nil || !*graphService.input.IncludeTerminal {
					t.Fatalf("expected include terminal true, got %#v", graphService.input.IncludeTerminal)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			issueService := &stubIssueService{}
			searchService := &stubSearchService{}
			graphService := &stubGraphService{}
			services := Services{IssueService: issueService, SearchService: searchService, GraphService: graphService}
			cli := New(services, &stdout, &stderr, nil, nil)
			if err := cli.Run(context.Background(), tt.args); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.assertFunc(t, issueService, searchService, graphService)
		})
	}
}

func TestRunBackupUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cli := New(Services{}, &stdout, &stderr, nil, nil)
	cli.SetBackupHandler(func(context.Context, string) (BackupReport, error) {
		return BackupReport{}, nil
	})
	if err := cli.Run(context.Background(), []string{"backup", "unexpected"}); err == nil {
		t.Fatal("expected usage error")
	}
	if !strings.Contains(stderr.String(), "backup --output PATH") {
		t.Fatalf("expected usage text in stderr, got %q", stderr.String())
	}
}

func TestRunMaintenanceCommands(t *testing.T) {
	t.Run("release attempt table", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		finishedAt := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
		interruptionReason := domain.InterruptionReasonUserRequest
		maintenanceService := &stubMaintenanceService{releaseResult: ports.ForceReleaseAttemptResult{Attempt: domain.WorkAttempt{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Status: domain.AttemptStatusInterrupted, InterruptionReasonCode: &interruptionReason, FinishedAt: &finishedAt}, LatestEventID: 7}}
		cli := New(Services{MaintenanceService: maintenanceService}, &stdout, &stderr, nil, nil)
		if err := cli.Run(context.Background(), []string{"maintenance", "release-attempt", "01ARZ3NDEKTSV4RRFFQ69G5FAV"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		output := stdout.String()
		for _, token := range []string{"attempt_id", "status", "interruption_reason", "finished_at", "latest_event_id", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "interrupted", "user_request", "2026-07-14T12:00:00Z", "7"} {
			if !strings.Contains(output, token) {
				t.Fatalf("expected output to contain %q, got %q", token, output)
			}
		}
		if !strings.HasSuffix(output, "\n") {
			t.Fatalf("expected output to end with newline, got %q", output)
		}
	})

	t.Run("release attempt json", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		maintenanceService := &stubMaintenanceService{releaseResult: ports.ForceReleaseAttemptResult{Attempt: domain.WorkAttempt{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}, LatestEventID: 7}}
		cli := New(Services{MaintenanceService: maintenanceService}, &stdout, &stderr, nil, nil)
		if err := cli.Run(context.Background(), []string{"maintenance", "release-attempt", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "--format", "json"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		output := stdout.String()
		for _, token := range []string{"\"attempt\"", "\"latest_event_id\"", "7"} {
			if !strings.Contains(output, token) {
				t.Fatalf("expected output to contain %q, got %q", token, output)
			}
		}
		if !strings.HasSuffix(output, "\n") {
			t.Fatalf("expected output to end with newline, got %q", output)
		}
	})

	t.Run("rebuild table", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		maintenanceService := &stubMaintenanceService{}
		cli := New(Services{MaintenanceService: maintenanceService}, &stdout, &stderr, nil, nil)
		if err := cli.Run(context.Background(), []string{"maintenance", "rebuild-search-index"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stdout.String() != "search index rebuilt\n" {
			t.Fatalf("unexpected output %q", stdout.String())
		}
	})

	t.Run("rebuild json", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		maintenanceService := &stubMaintenanceService{}
		cli := New(Services{MaintenanceService: maintenanceService}, &stdout, &stderr, nil, nil)
		if err := cli.Run(context.Background(), []string{"maintenance", "rebuild-search-index", "--format", "json"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		output := stdout.String()
		for _, token := range []string{"\"rebuilt\"", "true"} {
			if !strings.Contains(output, token) {
				t.Fatalf("expected output to contain %q, got %q", token, output)
			}
		}
		if !strings.HasSuffix(output, "\n") {
			t.Fatalf("expected output to end with newline, got %q", output)
		}
	})
}

func TestRunMaintenancePropagatesErrorsWithoutSuccessOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	maintenanceService := &stubMaintenanceService{releaseErr: errors.New("boom")}
	cli := New(Services{MaintenanceService: maintenanceService}, &stdout, &stderr, nil, nil)

	err := cli.Run(context.Background(), []string{"maintenance", "release-attempt", "01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected boom error, got %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no successful output, got %q", stdout.String())
	}
}

func TestRunPropagatesServiceErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	issueService := &stubIssueService{showErr: errors.New("boom")}
	cli := New(Services{IssueService: issueService}, &stdout, &stderr, nil, nil)

	err := cli.Run(context.Background(), []string{"issue", "show", "ISSUE-1"})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected boom error, got %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no successful output, got %q", stdout.String())
	}
}

func strPtr(value string) *string {
	return &value
}
