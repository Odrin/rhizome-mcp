package ports

import (
	"context"
	"time"

	"rhizome-mcp/internal/domain"
)

type CreateAgentSessionCommand struct {
	Session domain.AgentSession
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
	TouchAgentSession(context.Context, TouchAgentSessionCommand) (domain.AgentSession, error)
	EndAgentSession(context.Context, EndAgentSessionCommand) (domain.AgentSession, error)
}
