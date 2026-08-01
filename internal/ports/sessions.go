package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

type CreateAgentSessionCommand struct {
	Session    domain.AgentSession
	HandleHash []byte
}

type ResolveAgentSessionHandleCommand struct {
	Handle string
}

type ResolveAndTouchAgentSessionHandleCommand struct {
	Handle     string
	OccurredAt time.Time
}

type EndAgentSessionByHandleCommand struct {
	Handle     string
	OccurredAt time.Time
}

type TouchAgentSessionCommand struct {
	SessionID  string
	OccurredAt time.Time
}

type EndAgentSessionCommand struct {
	SessionID  string
	OccurredAt time.Time
}

type AgentSessionRepository interface {
	CreateAgentSession(context.Context, CreateAgentSessionCommand) (domain.AgentSession, error)
	ResolveAgentSessionHandle(context.Context, ResolveAgentSessionHandleCommand) (string, error)
	ResolveAndTouchAgentSessionHandle(context.Context, ResolveAndTouchAgentSessionHandleCommand) (string, error)
	EndAgentSessionByHandle(context.Context, EndAgentSessionByHandleCommand) (domain.AgentSession, error)
	TouchAgentSession(context.Context, TouchAgentSessionCommand) (domain.AgentSession, error)
	EndAgentSession(context.Context, EndAgentSessionCommand) (domain.AgentSession, error)
}
