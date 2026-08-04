package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/domain"
)

func TestRouteProjectRequestRejectsWrongProjectRefTypeBeforeAcquire(t *testing.T) {
	router := &requestRouter{lease: NewStaticLease(projectID, completeProjectServices())}
	target := &adapter{router: router}
	request := requestWithArguments(t, map[string]any{"project_ref": 7})
	handler := routeProjectRequest[struct{}, any](target, nil, func(*adapter, context.Context, *sdkmcp.CallToolRequest, struct{}) (*sdkmcp.CallToolResult, any, error) {
		t.Fatal("handler ran")
		return nil, nil, nil
	})

	_, _, err := handler(context.Background(), request, struct{}{})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument || router.acquires != 0 {
		t.Fatalf("handler error/acquires = %v/%d, want invalid argument/0", err, router.acquires)
	}
}

func TestRouteProjectRequestPropagatesSharedProjectRequired(t *testing.T) {
	router := &requestRouter{err: NewProjectRequiredError()}
	target := &adapter{router: router}
	handler := routeProjectRequest[struct{}, any](target, nil, func(*adapter, context.Context, *sdkmcp.CallToolRequest, struct{}) (*sdkmcp.CallToolResult, any, error) {
		t.Fatal("handler ran")
		return nil, nil, nil
	})

	_, _, err := handler(context.Background(), requestWithArguments(t, map[string]any{}), struct{}{})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeProjectRequired || router.acquires != 1 {
		t.Fatalf("handler error/acquires = %v/%d, want project required/1", err, router.acquires)
	}
}

func TestRouteProjectRequestReleasesLeaseAfterHandlerFailure(t *testing.T) {
	releases := 0
	lease := &staticLease{projectRef: projectID, services: completeProjectServices(), releaseFn: func() error {
		releases++
		return nil
	}}
	router := &requestRouter{lease: lease}
	target := &adapter{router: router}
	handler := routeProjectRequest[struct{}, any](target, nil, func(*adapter, context.Context, *sdkmcp.CallToolRequest, struct{}) (*sdkmcp.CallToolResult, any, error) {
		return nil, nil, errors.New("handler failed")
	})

	_, _, err := handler(context.Background(), requestWithArguments(t, map[string]any{}), struct{}{})
	if err == nil || releases != 1 {
		t.Fatalf("handler error/releases = %v/%d, want error/1", err, releases)
	}
}

type requestRouter struct {
	lease    ProjectLease
	err      error
	acquires int
}

func (router *requestRouter) Acquire(context.Context, *string) (ProjectLease, error) {
	router.acquires++
	return router.lease, router.err
}

func (router *requestRouter) OpenProject(context.Context, string) (ProjectLease, error) {
	return router.lease, router.err
}

func requestWithArguments(t *testing.T, arguments map[string]any) *sdkmcp.CallToolRequest {
	t.Helper()
	data, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	return &sdkmcp.CallToolRequest{Params: &sdkmcp.CallToolParamsRaw{Arguments: data}}
}