package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
)

const projectID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestStaticProjectRouterAcquireAndLeaseBehavior(t *testing.T) {
	services := completeProjectServices()
	router := NewStaticProjectRouter(projectID, "", services)

	lease, err := router.Acquire(context.Background(), nil)
	if err != nil {
		t.Fatalf("Acquire(nil) error = %v", err)
	}
	if lease == nil || lease.ProjectRef() != projectID {
		t.Fatalf("lease project ref = %q, want %q", lease.ProjectRef(), projectID)
	}
	if lease.IssueService() != services.IssueService || lease.ProjectService() != services.ProjectService ||
		lease.RelationService() != services.RelationService || lease.GraphService() != services.GraphService ||
		lease.PlanningService() != services.PlanningService || lease.CommentService() != services.CommentService ||
		lease.DecisionService() != services.DecisionService || lease.ActivityService() != services.ActivityService ||
		lease.SearchService() != services.SearchService || lease.ReviewService() != services.ReviewService ||
		lease.AttemptService() != services.AttemptService || lease.SessionService() != services.SessionService ||
		lease.WorkContextService() != services.WorkContextService {
		t.Fatalf("lease did not expose the complete project service set: %#v", lease)
	}
	releaseCount := 0
	lease = &staticLease{releaseFn: func() error { releaseCount++; return errors.New("release failed") }}
	if err := lease.Release(); err != nil {
		if err.Error() != "release failed" {
			t.Fatalf("Release() error = %v", err)
		}
	}
	if err := lease.Release(); err == nil || err.Error() != "release failed" {
		t.Fatalf("second Release() error = %v, want cached release error", err)
	}
	if releaseCount != 1 {
		t.Fatalf("release callback count = %d, want 1", releaseCount)
	}
}

func TestStaticProjectRouterValidationAndErrors(t *testing.T) {
	router := NewStaticProjectRouter(projectID, "", ProjectServices{})

	if _, err := router.Acquire(context.Background(), nil); err != nil {
		t.Fatalf("Acquire(nil) error = %v", err)
	}

	for _, badRef := range []string{"", "bad", "01arz3ndektsv4rrffq69g5fav"} {
		var domainErr *domain.Error
		if _, err := router.Acquire(context.Background(), &badRef); !errors.As(err, &domainErr) {
			t.Fatalf("Acquire(%q) error = %v, want domain error", badRef, err)
		} else if domainErr.Code != domain.CodeInvalidArgument || domainErr.Retryable || len(domainErr.Details) != 1 ||
			domainErr.Details[0].Field != "project_ref" || domainErr.Details[0].Code != "INVALID_ULID" {
			t.Fatalf("Acquire(%q) error = %#v, want stable INVALID_ULID detail", badRef, domainErr)
		}
	}

	ref := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	if _, err := router.Acquire(context.Background(), &ref); err != nil {
		t.Fatalf("Acquire(canonical ref) error = %v", err)
	}

	other := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	_, err := router.Acquire(context.Background(), &other)
	if err == nil {
		t.Fatal("Acquire(other canonical ref) succeeded")
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("Acquire(other canonical ref) error = %v, want *domain.Error", err)
	}
	if domainErr.Code != domain.CodeProjectNotFound {
		t.Fatalf("Acquire(other canonical ref) code = %q, want %q", domainErr.Code, domain.CodeProjectNotFound)
	}

	zeroRouter := NewStaticProjectRouter("", "", ProjectServices{})
	if _, err := zeroRouter.Acquire(context.Background(), nil); !errors.As(err, &domainErr) || domainErr.Code != domain.CodeProjectRequired || domainErr.Retryable {
		t.Fatalf("zero-project Acquire(nil) error = %#v, want non-retryable PROJECT_REQUIRED", err)
	}
	if !strings.Contains(domainErr.Message, "open_project") || !strings.Contains(domainErr.Message, "project_ref") {
		t.Fatalf("zero-project Acquire(nil) message = %q", domainErr.Message)
	}
	if _, err := zeroRouter.Acquire(context.Background(), &ref); !errors.As(err, &domainErr) || domainErr.Code != domain.CodeProjectNotFound {
		t.Fatalf("zero-project Acquire(explicit) error = %#v, want PROJECT_NOT_FOUND", err)
	}
}

func TestStaticProjectRouterOpenProject(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	router := NewStaticProjectRouter(projectID, root, ProjectServices{})
	lease, err := router.OpenProject(context.Background(), root)
	if err != nil || lease.ProjectRef() != projectID {
		t.Fatalf("OpenProject(configured root) = %#v, %v", lease, err)
	}
	if _, err := router.OpenProject(context.Background(), "relative"); err == nil {
		t.Fatal("OpenProject(relative root) succeeded")
	}
	if _, err := router.OpenProject(context.Background(), filepath.Join(t.TempDir(), "other")); !errors.Is(err, &domain.Error{Code: domain.CodeProjectNotFound}) {
		t.Fatalf("OpenProject(other root) error = %v, want PROJECT_NOT_FOUND", err)
	}
}

func TestNewServerRetainsRouterAndReleasesBootstrapLeaseOnce(t *testing.T) {
	releaseCount := 0
	lease := &staticLease{
		projectRef: projectID,
		services:   completeProjectServices(),
		releaseFn:  func() error { releaseCount++; return nil },
	}
	router := &stubProjectRouter{lease: lease}
	server, err := NewServer(Options{
		ProjectRouter: router,
		ServerName:    "test-server",
		ServerVersion: "test-version",
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if server.adapter.router != router {
		t.Fatal("NewServer() did not retain its project router")
	}
	if router.acquireCount != 1 || releaseCount != 1 {
		t.Fatalf("bootstrap acquire/release counts = %d/%d, want 1/1", router.acquireCount, releaseCount)
	}
}

func TestContextAccessorsWorkWithLease(t *testing.T) {
	lease := NewStaticLease(projectID, ProjectServices{})
	ctx := WithProjectLease(context.Background(), lease)
	gotLease := ProjectLeaseFromContext(ctx)
	if gotLease == nil || gotLease.ProjectRef() != projectID {
		t.Fatalf("ProjectLeaseFromContext() = %#v, want lease for %q", gotLease, projectID)
	}
	gotRef := ProjectRefFromContext(ctx)
	if gotRef != projectID {
		t.Fatalf("ProjectRefFromContext() = %q, want %q", gotRef, projectID)
	}
	gotServices := ProjectServicesFromContext(ctx)
	if gotServices.IssueService != nil || gotServices.ProjectService != nil {
		t.Fatalf("ProjectServicesFromContext() = %#v", gotServices)
	}
}

func TestFailureEnvelopePreservesStableRoutingErrors(t *testing.T) {
	adapter := &adapter{}
	for _, testCase := range []struct {
		err       *domain.Error
		code      string
		retryable bool
	}{
		{NewProjectRequiredError(), domain.CodeProjectRequired, false},
		{NewProjectCapacityExceededError(), domain.CodeProjectCapacityExceeded, true},
		{NewProjectNotFoundError(projectID), domain.CodeProjectNotFound, false},
		{NewInvalidProjectRefError("bad"), domain.CodeInvalidArgument, false},
	} {
		result, _, err := adapter.failure(testCase.err)
		if err != nil || result == nil || !result.IsError {
			t.Fatalf("failure(%s) = %#v, %v", testCase.code, result, err)
		}
		payload, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatalf("marshal structured content: %v", err)
		}
		var output struct {
			Code      string          `json:"code"`
			Message   string          `json:"message"`
			Retryable bool            `json:"retryable"`
			Details   []domain.Detail `json:"details"`
		}
		if err := json.Unmarshal(payload, &output); err != nil {
			t.Fatalf("unmarshal structured content: %v", err)
		}
		if output.Code != testCase.code || output.Retryable != testCase.retryable {
			t.Fatalf("structured content = %#v", output)
		}
		if testCase.code == domain.CodeInvalidArgument && (len(output.Details) != 1 || output.Details[0].Field != "project_ref" || output.Details[0].Code != "INVALID_ULID") {
			t.Fatalf("invalid project_ref details = %#v", output.Details)
		}
	}
}

func completeProjectServices() ProjectServices {
	return ProjectServices{
		IssueService:       new(application.IssueService),
		ProjectService:     new(application.ProjectService),
		RelationService:    new(application.RelationService),
		GraphService:       new(application.GraphService),
		PlanningService:    new(application.PlanningService),
		CommentService:     new(application.CommentService),
		DecisionService:    new(application.DecisionService),
		ActivityService:    new(application.ActivityService),
		SearchService:      new(application.SearchService),
		ReviewService:      new(application.ReviewService),
		AttemptService:     new(application.AttemptService),
		SessionService:     new(application.AgentSessionService),
		WorkContextService: new(application.WorkContextService),
	}
}

type stubProjectRouter struct {
	lease        ProjectLease
	err          error
	acquireCount int
}

func (router *stubProjectRouter) Acquire(context.Context, *string) (ProjectLease, error) {
	router.acquireCount++
	return router.lease, router.err
}

func (router *stubProjectRouter) OpenProject(context.Context, string) (ProjectLease, error) {
	return router.lease, router.err
}
