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

	result, _, err := handler(context.Background(), request, struct{}{})
	if err != nil {
		t.Fatalf("handler error = %v, want nil (domain errors route through the structured envelope)", err)
	}
	output, ok := result.StructuredContent.(errorOutput)
	if result == nil || !result.IsError || !ok || output.Code != domain.CodeInvalidArgument || router.acquires != 0 {
		t.Fatalf("result/acquires = %#v/%d, want invalid argument/0", result, router.acquires)
	}
}

func TestRouteProjectRequestPropagatesSharedProjectRequired(t *testing.T) {
	router := &requestRouter{err: NewProjectRequiredError()}
	target := &adapter{router: router}
	handler := routeProjectRequest[struct{}, any](target, nil, func(*adapter, context.Context, *sdkmcp.CallToolRequest, struct{}) (*sdkmcp.CallToolResult, any, error) {
		t.Fatal("handler ran")
		return nil, nil, nil
	})

	result, _, err := handler(context.Background(), requestWithArguments(t, map[string]any{}), struct{}{})
	if err != nil {
		t.Fatalf("handler error = %v, want nil (domain errors route through the structured envelope)", err)
	}
	output, ok := result.StructuredContent.(errorOutput)
	if result == nil || !result.IsError || !ok || output.Code != domain.CodeProjectRequired || router.acquires != 1 {
		t.Fatalf("result/acquires = %#v/%d, want project required/1", result, router.acquires)
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

// TestTouchSessionForMutatingToolRejectsNonStringHandle exercises the
// malformed-argument path directly below the SDK's schema validation layer:
// a live client call can never reach this branch because agent_session_handle
// is declared as a string in the tool schema, so the SDK rejects a non-string
// value before the handler runs. Constructing the raw request here is the
// only way to prove this branch itself routes through the structured
// envelope with an INVALID_HANDLE detail.
func TestTouchSessionForMutatingToolRejectsNonStringHandle(t *testing.T) {
	target := &adapter{}
	handler := touchSessionForMutatingTool[struct{}, any](target, nil, func(context.Context, *sdkmcp.CallToolRequest, struct{}) (*sdkmcp.CallToolResult, any, error) {
		t.Fatal("handler ran")
		return nil, nil, nil
	})

	request := requestWithArguments(t, map[string]any{"agent_session_handle": 123})
	result, _, err := handler(context.Background(), request, struct{}{})
	if err != nil {
		t.Fatalf("handler error = %v, want nil (domain errors route through the structured envelope)", err)
	}
	output, ok := result.StructuredContent.(errorOutput)
	if result == nil || !result.IsError || !ok || output.Code != domain.CodeInvalidArgument {
		t.Fatalf("result = %#v, want invalid argument", result)
	}
	found := false
	for _, detail := range output.Details {
		if detail.Field == "agent_session_handle" && detail.Code == "INVALID_HANDLE" {
			found = true
		}
	}
	if !found {
		t.Fatalf("details = %#v, want a detail with field=agent_session_handle code=INVALID_HANDLE", output.Details)
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
