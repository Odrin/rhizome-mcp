package mcp

import (
	"context"
	"encoding/json"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/projectrouting"
)

type projectLeaseContextKey struct{}
type projectRefContextKey struct{}
type projectServicesContextKey struct{}

// WithProjectLease stores a selected lease on the request context.
func WithProjectLease(ctx context.Context, lease projectrouting.ProjectLease) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, projectLeaseContextKey{}, lease)
}

// ProjectLeaseFromContext retrieves the lease from the request context, if present.
func ProjectLeaseFromContext(ctx context.Context) projectrouting.ProjectLease {
	if ctx == nil {
		return nil
	}
	lease, _ := ctx.Value(projectLeaseContextKey{}).(projectrouting.ProjectLease)
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
func WithProjectServices(ctx context.Context, services application.Bundle) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, projectServicesContextKey{}, services)
}

// ProjectServicesFromContext retrieves project-local services from the request context.
func ProjectServicesFromContext(ctx context.Context) application.Bundle {
	if ctx == nil {
		return application.Bundle{}
	}
	if services, ok := ctx.Value(projectServicesContextKey{}).(application.Bundle); ok {
		return services
	}
	if lease := ProjectLeaseFromContext(ctx); lease != nil {
		return lease.Services()
	}
	return application.Bundle{}
}

func projectRefArgument(request *sdkmcp.CallToolRequest) (*string, error) {
	if request == nil || request.Params == nil || len(request.Params.Arguments) == 0 {
		return nil, nil
	}
	var arguments map[string]json.RawMessage
	if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
		return nil, err
	}
	value, ok := arguments["project_ref"]
	if !ok || string(value) == "null" {
		return nil, nil
	}
	var explicit string
	if err := json.Unmarshal(value, &explicit); err != nil {
		return nil, projectrouting.NewInvalidProjectRefError("")
	}
	return &explicit, nil
}

func (adapter *adapter) cloneForLease(lease projectrouting.ProjectLease) *adapter {
	if adapter == nil {
		return nil
	}
	clone := *adapter
	if lease != nil {
		clone.services = lease.Services()
	}
	return &clone
}

func routeProjectRequest[In, Out any](adapter *adapter, toolDef *sdkmcp.Tool, handler func(*adapter, context.Context, *sdkmcp.CallToolRequest, In) (*sdkmcp.CallToolResult, Out, error)) func(context.Context, *sdkmcp.CallToolRequest, In) (*sdkmcp.CallToolResult, Out, error) {
	return func(ctx context.Context, request *sdkmcp.CallToolRequest, input In) (*sdkmcp.CallToolResult, Out, error) {
		explicitRef, err := projectRefArgument(request)
		if err != nil {
			result, _, ferr := adapter.failure(err)
			return result, *new(Out), ferr
		}
		lease, err := adapter.router.Acquire(ctx, explicitRef)
		if err != nil {
			result, _, ferr := adapter.failure(err)
			return result, *new(Out), ferr
		}
		if lease == nil {
			result, _, ferr := adapter.failure(domain.NewError(domain.CodeInvalidArgument, "project router returned a nil lease", false))
			return result, *new(Out), ferr
		}
		defer func() {
			_ = lease.Release()
		}()
		cloned := adapter.cloneForLease(lease)
		ctx = WithProjectLease(ctx, lease)
		ctx = WithProjectRef(ctx, lease.ProjectRef())
		ctx = WithProjectServices(ctx, lease.Services())
		return touchSessionForMutatingTool(cloned, toolDef, func(ctx context.Context, request *sdkmcp.CallToolRequest, input In) (*sdkmcp.CallToolResult, Out, error) {
			return handler(cloned, ctx, request, input)
		})(ctx, request, input)
	}
}
