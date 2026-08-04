package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
)

// ProjectRouter selects the active project lease for a request context.
type ProjectRouter interface {
	Acquire(ctx context.Context, explicitRef *string) (ProjectLease, error)
	OpenProject(ctx context.Context, absoluteRoot string) (ProjectLease, error)
}

// ProjectLease exposes the selected canonical project and its project-local services.
type ProjectLease interface {
	ProjectRef() string
	IssueService() *application.IssueService
	ProjectService() *application.ProjectService
	RelationService() *application.RelationService
	GraphService() *application.GraphService
	PlanningService() *application.PlanningService
	CommentService() *application.CommentService
	DecisionService() *application.DecisionService
	ActivityService() *application.ActivityService
	SearchService() *application.SearchService
	ReviewService() *application.ReviewService
	AttemptService() *application.AttemptService
	SessionService() *application.AgentSessionService
	WorkContextService() *application.WorkContextService
	Release() error
}

// ProjectServices is the adapter-facing bundle of project-local services.
type ProjectServices struct {
	IssueService       *application.IssueService
	ProjectService     *application.ProjectService
	RelationService    *application.RelationService
	GraphService       *application.GraphService
	PlanningService    *application.PlanningService
	CommentService     *application.CommentService
	DecisionService    *application.DecisionService
	ActivityService    *application.ActivityService
	SearchService      *application.SearchService
	ReviewService      *application.ReviewService
	AttemptService     *application.AttemptService
	SessionService     *application.AgentSessionService
	WorkContextService *application.WorkContextService
}

type projectLeaseContextKey struct{}
type projectRefContextKey struct{}
type projectServicesContextKey struct{}

// WithProjectLease stores a selected lease on the request context.
func WithProjectLease(ctx context.Context, lease ProjectLease) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, projectLeaseContextKey{}, lease)
}

// ProjectLeaseFromContext retrieves the lease from the request context, if present.
func ProjectLeaseFromContext(ctx context.Context) ProjectLease {
	if ctx == nil {
		return nil
	}
	lease, _ := ctx.Value(projectLeaseContextKey{}).(ProjectLease)
	return lease
}

// WithProjectRef stores a selected project reference on the request context.
func WithProjectRef(ctx context.Context, projectRef string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, projectRefContextKey{}, projectRef)
}

// ProjectRefFromContext retrieves the selected project reference from the request context.
func ProjectRefFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if lease := ProjectLeaseFromContext(ctx); lease != nil {
		return lease.ProjectRef()
	}
	projectRef, _ := ctx.Value(projectRefContextKey{}).(string)
	return projectRef
}

// WithProjectServices stores project-local services on the request context.
func WithProjectServices(ctx context.Context, services ProjectServices) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, projectServicesContextKey{}, services)
}

// ProjectServicesFromContext retrieves project-local services from the request context.
func ProjectServicesFromContext(ctx context.Context) ProjectServices {
	if ctx == nil {
		return ProjectServices{}
	}
	if services, ok := ctx.Value(projectServicesContextKey{}).(ProjectServices); ok {
		return services
	}
	if lease := ProjectLeaseFromContext(ctx); lease != nil {
		return ProjectServices{
			IssueService:       lease.IssueService(),
			ProjectService:     lease.ProjectService(),
			RelationService:    lease.RelationService(),
			GraphService:       lease.GraphService(),
			PlanningService:    lease.PlanningService(),
			CommentService:     lease.CommentService(),
			DecisionService:    lease.DecisionService(),
			ActivityService:    lease.ActivityService(),
			SearchService:      lease.SearchService(),
			ReviewService:      lease.ReviewService(),
			AttemptService:     lease.AttemptService(),
			SessionService:     lease.SessionService(),
			WorkContextService: lease.WorkContextService(),
		}
	}
	return ProjectServices{}
}

// StaticProjectRouter is a temporary adapter that routes to one fixed project.
type StaticProjectRouter struct {
	defaultProjectRef string
	projectRoot       string
	services          ProjectServices
}

const projectRequiredMessage = "project_ref is required; pass project_ref or configure a default project"

// NewStaticProjectRouter constructs a temporary static router for one default project.
func NewStaticProjectRouter(projectRef, projectRoot string, services ProjectServices) *StaticProjectRouter {
	return &StaticProjectRouter{defaultProjectRef: projectRef, projectRoot: projectRoot, services: services}
}

// Acquire resolves the selected project for the context.
func (router *StaticProjectRouter) Acquire(ctx context.Context, explicitRef *string) (ProjectLease, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if explicitRef != nil {
		if err := ValidateProjectRef(*explicitRef); err != nil {
			return nil, err
		}
		if router == nil || *explicitRef != router.defaultProjectRef {
			return nil, NewProjectNotFoundError(*explicitRef)
		}
		return newStaticLease(router.defaultProjectRef, router.services), nil
	}
	if router == nil || router.defaultProjectRef == "" {
		return nil, NewProjectRequiredError()
	}
	if err := ValidateProjectRef(router.defaultProjectRef); err != nil {
		return nil, err
	}
	return newStaticLease(router.defaultProjectRef, router.services), nil
}

// OpenProject validates the absolute project root and resolves the static default project.
func (router *StaticProjectRouter) OpenProject(ctx context.Context, absoluteRoot string) (ProjectLease, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if absoluteRoot == "" {
		return nil, invalidProjectRootError("project_root is required", "REQUIRED")
	}
	if !filepath.IsAbs(absoluteRoot) {
		return nil, invalidProjectRootError("project_root must be an absolute path", "ABSOLUTE_PATH_REQUIRED")
	}
	if router == nil || router.defaultProjectRef == "" || router.projectRoot == "" || filepath.Clean(absoluteRoot) != filepath.Clean(router.projectRoot) {
		return nil, domain.NewError(domain.CodeProjectNotFound, fmt.Sprintf("project at root %q was not found", absoluteRoot), false)
	}
	if err := ValidateProjectRef(router.defaultProjectRef); err != nil {
		return nil, err
	}
	return newStaticLease(router.defaultProjectRef, router.services), nil
}

type staticLease struct {
	projectRef  string
	services    ProjectServices
	releaseFn   func() error
	releaseErr  error
	releaseOnce sync.Once
}

// NewStaticLease constructs a lease for a static project-router selection.
func NewStaticLease(projectRef string, services ProjectServices) ProjectLease {
	return &staticLease{projectRef: projectRef, services: services}
}

func newStaticLease(projectRef string, services ProjectServices) ProjectLease {
	return NewStaticLease(projectRef, services)
}

func (lease *staticLease) ProjectRef() string {
	if lease == nil {
		return ""
	}
	return lease.projectRef
}

func (lease *staticLease) IssueService() *application.IssueService {
	if lease == nil {
		return nil
	}
	return lease.services.IssueService
}
func (lease *staticLease) ProjectService() *application.ProjectService {
	if lease == nil {
		return nil
	}
	return lease.services.ProjectService
}
func (lease *staticLease) RelationService() *application.RelationService {
	if lease == nil {
		return nil
	}
	return lease.services.RelationService
}
func (lease *staticLease) GraphService() *application.GraphService {
	if lease == nil {
		return nil
	}
	return lease.services.GraphService
}
func (lease *staticLease) PlanningService() *application.PlanningService {
	if lease == nil {
		return nil
	}
	return lease.services.PlanningService
}
func (lease *staticLease) CommentService() *application.CommentService {
	if lease == nil {
		return nil
	}
	return lease.services.CommentService
}
func (lease *staticLease) DecisionService() *application.DecisionService {
	if lease == nil {
		return nil
	}
	return lease.services.DecisionService
}
func (lease *staticLease) ActivityService() *application.ActivityService {
	if lease == nil {
		return nil
	}
	return lease.services.ActivityService
}
func (lease *staticLease) SearchService() *application.SearchService {
	if lease == nil {
		return nil
	}
	return lease.services.SearchService
}
func (lease *staticLease) ReviewService() *application.ReviewService {
	if lease == nil {
		return nil
	}
	return lease.services.ReviewService
}
func (lease *staticLease) AttemptService() *application.AttemptService {
	if lease == nil {
		return nil
	}
	return lease.services.AttemptService
}
func (lease *staticLease) SessionService() *application.AgentSessionService {
	if lease == nil {
		return nil
	}
	return lease.services.SessionService
}
func (lease *staticLease) WorkContextService() *application.WorkContextService {
	if lease == nil {
		return nil
	}
	return lease.services.WorkContextService
}

func (lease *staticLease) Release() error {
	if lease == nil {
		return nil
	}
	lease.releaseOnce.Do(func() {
		if lease.releaseFn != nil {
			lease.releaseErr = lease.releaseFn()
		}
	})
	return lease.releaseErr
}

// ValidateProjectRef accepts only canonical uppercase ULIDs.
func ValidateProjectRef(value string) error {
	if _, err := ids.ParseStrict(value); err != nil {
		return NewInvalidProjectRefError(value)
	}
	return nil
}

// NewProjectRequiredError reports that neither an explicit nor default project was selected.
func NewProjectRequiredError() *domain.Error {
	return domain.NewError(domain.CodeProjectRequired, projectRequiredMessage, false)
}

// NewProjectCapacityExceededError reports temporary exhaustion of router capacity.
func NewProjectCapacityExceededError() *domain.Error {
	return domain.NewError(domain.CodeProjectCapacityExceeded, "project capacity is temporarily exhausted; retry after an active request completes", true)
}

// NewProjectNotFoundError reports that a canonical project reference is unknown.
func NewProjectNotFoundError(projectRef string) *domain.Error {
	return domain.NewError(domain.CodeProjectNotFound, fmt.Sprintf("project %q was not found", projectRef), false)
}

// NewInvalidProjectRefError reports a malformed or noncanonical project reference.
func NewInvalidProjectRefError(_ string) *domain.Error {
	message := "project_ref must be a canonical uppercase ULID"
	return domain.NewError(domain.CodeInvalidArgument, message, false,
		domain.Detail{Field: "project_ref", Code: "INVALID_ULID", Message: message})
}

func invalidProjectRootError(message, code string) *domain.Error {
	return domain.NewError(domain.CodeInvalidArgument, message, false,
		domain.Detail{Field: "project_root", Code: code, Message: message})
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
