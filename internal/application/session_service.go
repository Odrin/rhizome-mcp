package application

import (
	"context"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

// AgentSessionService manages durable agent session lifecycle records.
type AgentSessionService struct {
	repository ports.AgentSessionRepository
	clock      clock.Clock
	ids        IDGenerator
}

func NewAgentSessionService(repository ports.AgentSessionRepository, source clock.Clock, generator IDGenerator) (*AgentSessionService, error) {
	if repository == nil || source == nil || generator == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "agent session dependencies are required", false)
	}
	return &AgentSessionService{repository: repository, clock: source, ids: generator}, nil
}

func (service *AgentSessionService) Create(ctx context.Context, input domain.CreateAgentSessionInput) (domain.AgentSession, error) {
	normalized, err := input.Validate()
	if err != nil {
		return domain.AgentSession{}, err
	}
	id, err := service.ids.New()
	if err != nil {
		return domain.AgentSession{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate agent session identifier", false)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.AgentSession{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate agent session identifier", false)
	}
	now := service.clock.Now().UTC()
	session := domain.AgentSession{
		ID:            id,
		ClientName:    normalized.ClientName,
		ClientVersion: copyApplicationString(normalized.ClientVersion),
		AgentLabel:    copyApplicationString(normalized.AgentLabel),
		Model:         copyApplicationString(normalized.Model),
		InstanceKey:   copyApplicationString(normalized.InstanceKey),
		StartedAt:     now,
		LastSeenAt:    now,
	}
	result, err := service.repository.CreateAgentSession(ctx, ports.CreateAgentSessionCommand{Session: session})
	if err != nil {
		return domain.AgentSession{}, err
	}
	return result.Clone(), nil
}

func (service *AgentSessionService) Touch(ctx context.Context, sessionID string) (domain.AgentSession, error) {
	if err := validateSessionID(sessionID); err != nil {
		return domain.AgentSession{}, err
	}
	now := service.clock.Now().UTC()
	result, err := service.repository.TouchAgentSession(ctx, ports.TouchAgentSessionCommand{
		SessionID: sessionID, OccurredAt: now,
	})
	if err != nil {
		return domain.AgentSession{}, err
	}
	return result.Clone(), nil
}

func (service *AgentSessionService) End(ctx context.Context, sessionID string) (domain.AgentSession, error) {
	if err := validateSessionID(sessionID); err != nil {
		return domain.AgentSession{}, err
	}
	now := service.clock.Now().UTC()
	result, err := service.repository.EndAgentSession(ctx, ports.EndAgentSessionCommand{
		SessionID: sessionID, OccurredAt: now,
	})
	if err != nil {
		return domain.AgentSession{}, err
	}
	return result.Clone(), nil
}

func validateSessionID(value string) error {
	if _, err := ids.ParseStrict(value); err != nil {
		return domain.NewError(domain.CodeInvalidArgument, "session_id must be a canonical ULID", false,
			domain.Detail{Field: "session_id", Code: "INVALID_ULID"})
	}
	return nil
}

func copyApplicationString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
