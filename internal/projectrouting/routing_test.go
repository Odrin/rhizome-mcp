package projectrouting

import (
	"context"
	"errors"
	"testing"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
)

const projectID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

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

func TestStaticProjectRouterAcquireRejectsNilContextError(t *testing.T) {
	router := NewStaticProjectRouter(projectID, "", completeProjectServices())
	lease, err := router.Acquire(nil, nil)
	if err != nil || lease == nil {
		t.Fatalf("Acquire(nil, nil) error = %v, want nil error and non-nil lease", err)
	}
}

func TestStaticProjectRouterAcquirePropagatesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	router := NewStaticProjectRouter(projectID, "", completeProjectServices())
	lease, err := router.Acquire(ctx, nil)
	if err != context.Canceled {
		t.Fatalf("Acquire(cancelled ctx, nil) error = %v, want context.Canceled", err)
	}
	if lease != nil {
		t.Fatalf("Acquire(cancelled ctx, nil) lease = %v, want nil", lease)
	}
}

func TestStaticProjectRouterAcquireDefaultRef(t *testing.T) {
	router := NewStaticProjectRouter(projectID, "", completeProjectServices())
	lease, err := router.Acquire(context.Background(), nil)
	if err != nil {
		t.Fatalf("Acquire(background, nil) error = %v", err)
	}
	if lease == nil || lease.ProjectRef() != projectID {
		t.Fatalf("lease ref = %q, want %q", lease.ProjectRef(), projectID)
	}
}

func TestStaticProjectRouterAcquireExplicitRef(t *testing.T) {
	ref := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	router := NewStaticProjectRouter(ref, "", completeProjectServices())
	lease, err := router.Acquire(context.Background(), &ref)
	if err != nil {
		t.Fatalf("Acquire(background, ref) error = %v", err)
	}
	if lease == nil || lease.ProjectRef() != ref {
		t.Fatalf("lease ref = %q, want %q", lease.ProjectRef(), ref)
	}
}

func TestStaticProjectRouterAcquireRejectsWrongExplicitRef(t *testing.T) {
	router := NewStaticProjectRouter(projectID, "", completeProjectServices())
	otherRef := "01ARZ3NDEKTSV4RRFFQ69G5FB0"
	lease, err := router.Acquire(context.Background(), &otherRef)
	if lease != nil {
		t.Fatalf("Acquire(background, wrong ref) lease = %v, want nil", lease)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeProjectNotFound {
		t.Fatalf("Acquire(background, wrong ref) error = %v, want CodeProjectNotFound", err)
	}
}

func TestStaticProjectRouterAcquireRejectsNilRouter(t *testing.T) {
	var router *StaticProjectRouter
	lease, err := router.Acquire(context.Background(), nil)
	if lease != nil {
		t.Fatalf("Acquire on nil router lease = %v, want nil", lease)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeProjectRequired {
		t.Fatalf("Acquire on nil router error = %v, want CodeProjectRequired", err)
	}
}

func TestStaticProjectRouterOpenProjectAcceptsConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	router := NewStaticProjectRouter(projectID, root, completeProjectServices())
	lease, err := router.OpenProject(context.Background(), root)
	if err != nil {
		t.Fatalf("OpenProject(root) error = %v", err)
	}
	if lease == nil || lease.ProjectRef() != projectID {
		t.Fatalf("lease ref = %q, want %q", lease.ProjectRef(), projectID)
	}
}

func TestStaticProjectRouterOpenProjectRejectsUnconfiguredRoot(t *testing.T) {
	router := NewStaticProjectRouter(projectID, t.TempDir(), completeProjectServices())
	otherRoot := t.TempDir()
	lease, err := router.OpenProject(context.Background(), otherRoot)
	if lease != nil {
		t.Fatalf("OpenProject(other root) lease = %v, want nil", lease)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeProjectNotFound {
		t.Fatalf("OpenProject(other root) error = %v, want CodeProjectNotFound", err)
	}
}

func TestValidateProjectRefAcceptsCanonical(t *testing.T) {
	err := ValidateProjectRef("01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err != nil {
		t.Fatalf("ValidateProjectRef(canonical) error = %v", err)
	}
}

func TestValidateProjectRefRejectsNonCanonical(t *testing.T) {
	err := ValidateProjectRef("not-a-ulid")
	if err == nil {
		t.Fatal("ValidateProjectRef(non-canonical) succeeded, want error")
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument {
		t.Fatalf("ValidateProjectRef(non-canonical) error = %v, want CodeInvalidArgument", err)
	}
}
