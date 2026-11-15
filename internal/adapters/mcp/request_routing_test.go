package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/projectrouting"
)

func TestRouteProjectRequestRejectsWrongProjectRefTypeBeforeAcquire(t *testing.T) {
	router := &requestRouter{lease: projectrouting.NewStaticLease(projectID, completeProjectServices())}
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
	router := &requestRouter{err: projectrouting.NewProjectRequiredError()}
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

func TestRouteProjectRequestPropagatesProjectNotFound(t *testing.T) {
	router := &requestRouter{err: projectrouting.NewProjectNotFoundError("01ARZ3NDEKTSV4RRFFQ69G5FAV")}
	target := &adapter{router: router}
	handler := routeProjectRequest[struct{}, any](target, nil, func(*adapter, context.Context, *sdkmcp.CallToolRequest, struct{}) (*sdkmcp.CallToolResult, any, error) {
		t.Fatal("handler ran")
		return nil, nil, nil
	})

	request := requestWithArguments(t, map[string]any{"project_ref": "01ARZ3NDEKTSV4RRFFQ69G5FAV"})
	result, _, err := handler(context.Background(), request, struct{}{})
	if err != nil {
		t.Fatalf("handler error = %v, want nil (domain errors route through the structured envelope)", err)
	}
	output, ok := result.StructuredContent.(errorOutput)
	if result == nil || !result.IsError || !ok || output.Code != domain.CodeProjectNotFound {
		t.Fatalf("result = %#v, want project not found", result)
	}
}

func TestRouteProjectRequestPropagatesProjectCapacityExceededAsRetryable(t *testing.T) {
	router := &requestRouter{err: projectrouting.NewProjectCapacityExceededError()}
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
	if result == nil || !result.IsError || !ok || output.Code != domain.CodeProjectCapacityExceeded || !output.Retryable {
		t.Fatalf("result = %#v, want project capacity exceeded/retryable", result)
	}
}

func TestRouteProjectRequestReleasesLeaseAfterHandlerFailure(t *testing.T) {
	releases := 0
	lease := &testLease{projectRef: projectID, services: completeProjectServices(), releaseFn: func() error {
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
	lease        projectrouting.ProjectLease
	err          error
	acquires     int
	openProjects int
	acquiredRef  *string
	openedRoot   string
}

func (router *requestRouter) Acquire(_ context.Context, explicitRef *string) (projectrouting.ProjectLease, error) {
	router.acquires++
	router.acquiredRef = explicitRef
	return router.lease, router.err
}

func (router *requestRouter) OpenProject(_ context.Context, projectRoot string) (projectrouting.ProjectLease, error) {
	router.openProjects++
	router.openedRoot = projectRoot
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

func TestOpenProjectEnforcesMutuallyExclusiveSelectors(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "empty object", raw: `{}`},
		{name: "null project_root", raw: `{"project_root": null}`},
		{name: "null project_id", raw: `{"project_id": null}`},
		{name: "both null", raw: `{"project_root": null, "project_id": null}`},
		{name: "both provided", raw: `{"project_root": "/tmp/repo", "project_id": "` + projectID + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input openProjectInput
			if err := json.Unmarshal([]byte(tc.raw), &input); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.raw, err)
			}
			router := &requestRouter{lease: projectrouting.NewStaticLease(projectID, completeProjectServices())}
			target := &adapter{router: router}
			result, _, err := target.openProject(context.Background(), &sdkmcp.CallToolRequest{}, input)
			if err != nil {
				t.Fatalf("openProject(%s) err = %v, want nil", tc.raw, err)
			}
			output, ok := result.StructuredContent.(errorOutput)
			if result == nil || !result.IsError || !ok || output.Code != domain.CodeInvalidArgument {
				t.Fatalf("openProject(%s) result = %#v, want INVALID_ARGUMENT", tc.raw, result)
			}
			if output.Message != "exactly one of project_root or project_id is required" {
				t.Fatalf("message = %q", output.Message)
			}
			fields := make(map[string]string, len(output.Details))
			for _, d := range output.Details {
				fields[d.Field] = d.Code
			}
			if fields["project_root"] != "EXACTLY_ONE_SELECTOR_REQUIRED" || fields["project_id"] != "EXACTLY_ONE_SELECTOR_REQUIRED" {
				t.Fatalf("details = %#v, want both selectors with EXACTLY_ONE_SELECTOR_REQUIRED", output.Details)
			}
			if router.acquires != 0 || router.openProjects != 0 {
				t.Fatalf("router acquires/openProjects = %d/%d, want 0/0", router.acquires, router.openProjects)
			}
		})
	}
}

// TestOpenProjectRejectsInvalidProjectIDBeforeAcquire exercises malformed,
// lowercase, overlong, and overflow project_id strings directly below the SDK
// schema validation layer to confirm ValidateProjectID runs before Acquire.
func TestOpenProjectRejectsInvalidProjectIDBeforeAcquire(t *testing.T) {
	for _, tc := range []struct {
		name      string
		projectID string
	}{
		{name: "empty", projectID: ""},
		{name: "malformed", projectID: "not-a-ulid"},
		{name: "lowercase", projectID: "01arz3ndektsv4rrffq69g5fav"},
		{name: "overlong", projectID: "01ARZ3NDEKTSV4RRFFQ69G5FAVX"},
		{name: "overflow", projectID: "ZZZZZZZZZZZZZZZZZZZZZZZZZZ"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.projectID
			router := &requestRouter{lease: projectrouting.NewStaticLease(projectID, completeProjectServices())}
			target := &adapter{router: router}
			result, _, err := target.openProject(context.Background(), &sdkmcp.CallToolRequest{}, openProjectInput{ProjectID: &id})
			if err != nil {
				t.Fatalf("openProject(%q) err = %v, want nil", id, err)
			}
			output, ok := result.StructuredContent.(errorOutput)
			if result == nil || !result.IsError || !ok || output.Code != domain.CodeInvalidArgument {
				t.Fatalf("openProject(%q) result = %#v, want INVALID_ARGUMENT", id, result)
			}
			if len(output.Details) != 1 || output.Details[0].Field != "project_id" || output.Details[0].Code != "INVALID_ULID" {
				t.Fatalf("details = %#v, want field=project_id code=INVALID_ULID", output.Details)
			}
			if router.acquires != 0 || router.openProjects != 0 {
				t.Fatalf("router acquires/openProjects = %d/%d, want 0/0", router.acquires, router.openProjects)
			}
		})
	}
}

func TestOpenProjectRoutesSelectorsToDistinctRouterMethods(t *testing.T) {
	id := projectID
	router := &requestRouter{err: projectrouting.NewProjectNotFoundError(id)}
	target := &adapter{router: router}

	_, _, err := target.openProject(context.Background(), &sdkmcp.CallToolRequest{}, openProjectInput{ProjectID: &id})
	if err != nil {
		t.Fatalf("openProject(project_id) err = %v, want nil", err)
	}
	if router.acquires != 1 || router.openProjects != 0 || router.acquiredRef == nil || *router.acquiredRef != id {
		t.Fatalf("after project_id: acquires=%d openProjects=%d acquiredRef=%v", router.acquires, router.openProjects, router.acquiredRef)
	}

	root := "/tmp/repo"
	_, _, err = target.openProject(context.Background(), &sdkmcp.CallToolRequest{}, openProjectInput{ProjectRoot: &root})
	if err != nil {
		t.Fatalf("openProject(project_root) err = %v, want nil", err)
	}
	if router.openProjects != 1 || router.acquires != 1 || router.openedRoot != root {
		t.Fatalf("after project_root: acquires=%d openProjects=%d openedRoot=%q", router.acquires, router.openProjects, router.openedRoot)
	}
}
