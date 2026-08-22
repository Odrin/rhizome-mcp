package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
	projectruntime "rhizome-mcp/internal/runtime"
)

func TestProjectRouterOpenProjectLoadsConfiguredProjectRoot(t *testing.T) {
	projectID := "01ARZ3NDEKTSV4RRFFQ69G5FC0"
	root := t.TempDir()
	payload, err := json.Marshal(map[string]any{"version": 1, "project_id": projectID})
	if err != nil {
		t.Fatalf("Marshal identity: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agent-tracker.json"), payload, 0o600); err != nil {
		t.Fatalf("Write identity: %v", err)
	}

	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, nil)
	calls := 0
	router.composeFn = func(_ context.Context, ref string, _ string, _ clock.Clock, _ sqlite.Options) (*composedServices, *projectruntime.Project, error) {
		calls++
		return newTestBundle(ref), &projectruntime.Project{ProjectID: ref}, nil
	}

	lease, err := router.OpenProject(context.Background(), root)
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	if lease.ProjectRef() != projectID {
		t.Fatalf("lease ref = %q, want %q", lease.ProjectRef(), projectID)
	}
	if calls != 1 {
		t.Fatalf("compose calls = %d, want 1", calls)
	}
}

func TestProjectRouterOpenProjectRejectsInvalidRootsBeforeCompose(t *testing.T) {
	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, nil)
	calls := 0
	router.composeFn = func(context.Context, string, string, clock.Clock, sqlite.Options) (*composedServices, *projectruntime.Project, error) {
		calls++
		return nil, nil, errors.New("unexpected compose")
	}

	for _, root := range []string{"", "relative/path", filepath.Join("relative", "path")} {
		t.Run(fmt.Sprintf("root=%q", root), func(t *testing.T) {
			_, err := router.OpenProject(context.Background(), root)
			if err == nil {
				t.Fatal("OpenProject() succeeded, want error")
			}
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) {
				t.Fatalf("OpenProject() error = %v, want domain.Error", err)
			}
			if domainErr.Code != domain.CodeInvalidArgument {
				t.Fatalf("OpenProject() code = %q, want %q", domainErr.Code, domain.CodeInvalidArgument)
			}
			if calls != 0 {
				t.Fatalf("compose calls = %d, want 0", calls)
			}
		})
	}
}

func TestProjectRouterOpenProjectRejectsNilComposeResult(t *testing.T) {
	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, nil)
	router.composeFn = func(context.Context, string, string, clock.Clock, sqlite.Options) (*composedServices, *projectruntime.Project, error) {
		return nil, nil, nil
	}

	root := t.TempDir()
	payload, err := json.Marshal(map[string]any{"version": 1, "project_id": "01ARZ3NDEKTSV4RRFFQ69G5FC1"})
	if err != nil {
		t.Fatalf("Marshal identity: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agent-tracker.json"), payload, 0o600); err != nil {
		t.Fatalf("Write identity: %v", err)
	}

	_, err = router.OpenProject(context.Background(), root)
	if err == nil {
		t.Fatal("OpenProject() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Fatalf("OpenProject() error = %v, want nil bundle/project error", err)
	}
}

func TestProjectRouterExpireAttemptsProcessesReadyEntriesAndAggregatesErrors(t *testing.T) {
	defaultRef := "01ARZ3NDEKTSV4RRFFQ69G5FC2"
	ref := "01ARZ3NDEKTSV4RRFFQ69G5FC3"

	defaultBundle := newTestBundle(defaultRef)
	defaultBundle.project = &projectruntime.Project{ProjectID: defaultRef}
	defaultRepo := &countingAttemptRepository{results: []ports.ExpireAttemptsResult{{ExpiredAttemptCount: 1}}, errs: []error{errors.New("first expiry failed")}}
	defaultBundle.attemptService = mustNewAttemptService(t, defaultRepo)

	explicitBundle := newTestBundle(ref)
	explicitBundle.project = &projectruntime.Project{ProjectID: ref}
	explicitRepo := &countingAttemptRepository{results: []ports.ExpireAttemptsResult{{ExpiredAttemptCount: 1}}}
	explicitBundle.attemptService = mustNewAttemptService(t, explicitRepo)

	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, nil)
	router.entries[defaultRef] = &projectRouterEntry{ref: defaultRef, pinned: true, state: "ready", bundle: defaultBundle, lastUsed: 1}
	router.entries[ref] = &projectRouterEntry{ref: ref, state: "ready", bundle: explicitBundle, lastUsed: 2}
	router.entries["skipped-ref"] = &projectRouterEntry{ref: "skipped-ref", state: "opening", done: make(chan struct{})}

	result, err := router.ExpireAttempts(context.Background())
	if err == nil {
		t.Fatal("ExpireAttempts() succeeded, want combined error")
	}
	if result.ExpiredAttemptCount != 2 {
		t.Fatalf("ExpiredAttemptCount = %d, want 2", result.ExpiredAttemptCount)
	}
	if !strings.Contains(err.Error(), "first expiry failed") {
		t.Fatalf("ExpireAttempts() error = %v, want combined error", err)
	}
	if router.entries[defaultRef] == nil || router.entries[ref] == nil {
		t.Fatal("expected default and explicit entries to remain cached")
	}
}

func TestProjectRouterCloseReturnsCancellationErrorAndReleaseAfterCloseIsSafe(t *testing.T) {
	ref := "01ARZ3NDEKTSV4RRFFQ69G5FC4"
	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, nil)
	router.composeFn = func(_ context.Context, projectID string, _ string, _ clock.Clock, _ sqlite.Options) (*composedServices, *projectruntime.Project, error) {
		bundle := newTestBundle(projectID)
		bundle.project = &projectruntime.Project{ProjectID: projectID}
		return bundle, &projectruntime.Project{ProjectID: projectID}, nil
	}

	lease, err := router.Acquire(context.Background(), &ref)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := router.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close() error = %v, want context.Canceled", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() after close error = %v", err)
	}

	router.mu.Lock()
	defer router.mu.Unlock()
	if router.activeCount != 0 {
		t.Fatalf("activeCount = %d, want 0 after release after close", router.activeCount)
	}
}

type countingAttemptRepository struct {
	results []ports.ExpireAttemptsResult
	errs    []error
	calls   int
}

func (repo *countingAttemptRepository) ExpireAttempts(_ context.Context, _ ports.ExpireAttemptsCommand) (ports.ExpireAttemptsResult, error) {
	repo.calls++
	index := repo.calls - 1
	if index < len(repo.results) {
		result := repo.results[index]
		if index < len(repo.errs) && repo.errs[index] != nil {
			return result, repo.errs[index]
		}
		return result, nil
	}
	if index < len(repo.errs) && repo.errs[index] != nil {
		return ports.ExpireAttemptsResult{}, repo.errs[index]
	}
	return ports.ExpireAttemptsResult{}, nil
}

func (repo *countingAttemptRepository) ClaimIssue(context.Context, ports.ClaimIssueCommand) (ports.ClaimIssueResult, error) {
	return ports.ClaimIssueResult{}, nil
}

func (repo *countingAttemptRepository) RenewAttempt(context.Context, ports.RenewAttemptCommand) (ports.RenewAttemptResult, error) {
	return ports.RenewAttemptResult{}, nil
}

func (repo *countingAttemptRepository) SaveAttemptNote(context.Context, ports.SaveAttemptNoteCommand) (ports.SaveAttemptNoteResult, error) {
	return ports.SaveAttemptNoteResult{}, nil
}

func (repo *countingAttemptRepository) LookupSaveAttemptNote(context.Context, string, []byte) (ports.SaveAttemptNoteResult, bool, error) {
	return ports.SaveAttemptNoteResult{}, false, nil
}

func (repo *countingAttemptRepository) LookupFinishedAttempt(context.Context, string, []byte) (ports.FinishAttemptResult, bool, error) {
	return ports.FinishAttemptResult{}, false, nil
}

func (repo *countingAttemptRepository) FinishAttempt(context.Context, ports.FinishAttemptCommand) (ports.FinishAttemptResult, error) {
	return ports.FinishAttemptResult{}, nil
}

func (repo *countingAttemptRepository) ForceReleaseAttempt(context.Context, ports.ForceReleaseAttemptCommand) (ports.ForceReleaseAttemptResult, error) {
	return ports.ForceReleaseAttemptResult{}, nil
}

func (repo *countingAttemptRepository) ListActiveAttempts(context.Context, ports.ListActiveAttemptsCommand) ([]domain.ActiveAttemptSummary, error) {
	return nil, nil
}

func (repo *countingAttemptRepository) SubmitGateEvidence(context.Context, ports.SubmitGateEvidenceCommand) (ports.SubmitGateEvidenceResult, error) {
	return ports.SubmitGateEvidenceResult{}, nil
}

func (repo *countingAttemptRepository) LookupSubmitGateEvidence(context.Context, string, []byte) (ports.SubmitGateEvidenceResult, bool, error) {
	return ports.SubmitGateEvidenceResult{}, false, nil
}

func (repo *countingAttemptRepository) ListAttemptEvidence(context.Context, ports.ListAttemptEvidenceCommand) ([]domain.AttemptEvidence, error) {
	return nil, nil
}

func mustNewAttemptService(t *testing.T, repo ports.AttemptRepository) *application.AttemptService {
	t.Helper()
	service, err := application.NewAttemptService(repo, clock.RealClock{}, fakeIDGenerator{})
	if err != nil {
		t.Fatalf("NewAttemptService() error = %v", err)
	}
	return service
}

type fakeIDGenerator struct{}

func (fakeIDGenerator) New() (string, error) {
	return "01ARZ3NDEKTSV4RRFFQ69G5FC5", nil
}
