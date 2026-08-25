package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/projectrouting"
)

const projectID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestStaticProjectRouterAcquireAndLeaseBehavior(t *testing.T) {
	services := completeProjectServices()
	router := projectrouting.NewStaticProjectRouter(projectID, "", services)

	lease, err := router.Acquire(context.Background(), nil)
	if err != nil {
		t.Fatalf("Acquire(nil) error = %v", err)
	}
	if lease == nil || lease.ProjectRef() != projectID {
		t.Fatalf("lease project ref = %q, want %q", lease.ProjectRef(), projectID)
	}
	leaseServices := lease.Services()
	if leaseServices.IssueService != services.IssueService || leaseServices.ProjectService != services.ProjectService ||
		leaseServices.RelationService != services.RelationService || leaseServices.GraphService != services.GraphService ||
		leaseServices.PlanningService != services.PlanningService || leaseServices.CommentService != services.CommentService ||
		leaseServices.DecisionService != services.DecisionService || leaseServices.ActivityService != services.ActivityService ||
		leaseServices.SearchService != services.SearchService || leaseServices.ReviewService != services.ReviewService ||
		leaseServices.AttemptService != services.AttemptService || leaseServices.SessionService != services.SessionService ||
		leaseServices.WorkContextService != services.WorkContextService {
		t.Fatalf("lease did not expose the complete project service set: %#v", leaseServices)
	}
	releaseCount := 0
	releasingLease := &testLease{releaseFn: func() error { releaseCount++; return errors.New("release failed") }}
	lease = releasingLease
	if err := lease.Release(); err != nil {
		if err.Error() != "release failed" {
			t.Fatalf("Release() error = %v", err)
		}
	}
	if err := lease.Release(); err == nil || err.Error() != "release failed" {
		t.Fatalf("second Release() error = %v, want cached release error", err)
	}
}

type testLease struct {
	projectRef  string
	services    application.Bundle
	releaseFn   func() error
	releaseErr  error
	releaseOnce sync.Once
}

func (l *testLease) ProjectRef() string {
	return l.projectRef
}

func (l *testLease) Services() application.Bundle {
	return l.services
}

func (l *testLease) Release() error {
	l.releaseOnce.Do(func() {
		if l.releaseFn != nil {
			l.releaseErr = l.releaseFn()
		}
	})
	return l.releaseErr
}

func completeProjectServices() application.Bundle {
	return application.Bundle{
		IssueService:          new(application.IssueService),
		ProjectService:        new(application.ProjectService),
		RelationService:       new(application.RelationService),
		GraphService:          new(application.GraphService),
		PlanningService:       new(application.PlanningService),
		CommentService:        new(application.CommentService),
		DecisionService:       new(application.DecisionService),
		ActivityService:       new(application.ActivityService),
		SearchService:         new(application.SearchService),
		ReviewService:         new(application.ReviewService),
		AttemptService:        new(application.AttemptService),
		ReservationService:    new(application.ReservationService),
		SessionService:        new(application.AgentSessionService),
		WorkContextService:    new(application.WorkContextService),
		WorkflowPolicyService: new(application.WorkflowPolicyService),
	}
}

type stubProjectRouter struct {
	lease        projectrouting.ProjectLease
	err          error
	acquireCount int
}

func (router *stubProjectRouter) Acquire(context.Context, *string) (projectrouting.ProjectLease, error) {
	router.acquireCount++
	return router.lease, router.err
}

func (router *stubProjectRouter) OpenProject(context.Context, string) (projectrouting.ProjectLease, error) {
	return router.lease, router.err
}

func TestNewServerRetainsRouterAndReleasesBootstrapLeaseOnce(t *testing.T) {
	releaseCount := 0
	lease := &testLease{
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
	lease := projectrouting.NewStaticLease(projectID, application.Bundle{})
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
		{projectrouting.NewProjectRequiredError(), domain.CodeProjectRequired, false},
		{projectrouting.NewProjectCapacityExceededError(), domain.CodeProjectCapacityExceeded, true},
		{projectrouting.NewProjectNotFoundError(projectID), domain.CodeProjectNotFound, false},
		{projectrouting.NewInvalidProjectRefError("bad"), domain.CodeInvalidArgument, false},
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
