// Package projectrouting manages selection and lifecycle of the active project lease.
package projectrouting

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
	Services() application.Bundle
	Release() error
}

// StaticProjectRouter is a temporary adapter that routes to one fixed project.
type StaticProjectRouter struct {
	defaultProjectRef string
	projectRoot       string
	services          application.Bundle
}

const projectRequiredMessage = "project_ref is required; call open_project and pass its project_ref, or configure a default project"

// NewStaticProjectRouter constructs a temporary static router for one default project.
func NewStaticProjectRouter(projectRef, projectRoot string, services application.Bundle) *StaticProjectRouter {
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
		return NewStaticLease(router.defaultProjectRef, router.services), nil
	}
	if router == nil || router.defaultProjectRef == "" {
		return nil, NewProjectRequiredError()
	}
	if err := ValidateProjectRef(router.defaultProjectRef); err != nil {
		return nil, err
	}
	return NewStaticLease(router.defaultProjectRef, router.services), nil
}

// OpenProject validates the absolute project root and resolves the static default project.
func (router *StaticProjectRouter) OpenProject(ctx context.Context, absoluteRoot string) (ProjectLease, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if absoluteRoot == "" {
		return nil, InvalidProjectRootError("project_root is required", "REQUIRED")
	}
	if !filepath.IsAbs(absoluteRoot) {
		return nil, InvalidProjectRootError("project_root must be an absolute path", "ABSOLUTE_PATH_REQUIRED")
	}
	if router == nil || router.defaultProjectRef == "" || router.projectRoot == "" || filepath.Clean(absoluteRoot) != filepath.Clean(router.projectRoot) {
		return nil, domain.NewError(domain.CodeProjectNotFound, fmt.Sprintf("project at root %q was not found", absoluteRoot), false)
	}
	if err := ValidateProjectRef(router.defaultProjectRef); err != nil {
		return nil, err
	}
	return NewStaticLease(router.defaultProjectRef, router.services), nil
}

type staticLease struct {
	projectRef  string
	services    application.Bundle
	releaseFn   func() error
	releaseErr  error
	releaseOnce sync.Once
}

// NewStaticLease constructs a lease for a static project-router selection.
func NewStaticLease(projectRef string, services application.Bundle) ProjectLease {
	return &staticLease{projectRef: projectRef, services: services}
}

func (lease *staticLease) ProjectRef() string {
	if lease == nil {
		return ""
	}
	return lease.projectRef
}

func (lease *staticLease) Services() application.Bundle {
	if lease == nil {
		return application.Bundle{}
	}
	return lease.services
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

// InvalidProjectRootError reports invalid project_root argument.
func InvalidProjectRootError(message, code string) *domain.Error {
	return domain.NewError(domain.CodeInvalidArgument, message, false,
		domain.Detail{Field: "project_root", Code: code, Message: message})
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
