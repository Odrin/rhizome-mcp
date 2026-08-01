package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"

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
	result, err := service.createSession(ctx, normalized, nil)
	if err != nil {
		return domain.AgentSession{}, err
	}
	return result.Clone(), nil
}

func (service *AgentSessionService) CreateWithHandle(ctx context.Context, input domain.CreateAgentSessionInput) (struct {
	Session domain.AgentSession
	Handle  string
}, error) {
	normalized, err := input.Validate()
	if err != nil {
		return struct {
			Session domain.AgentSession
			Handle  string
		}{}, err
	}
	handle, err := service.generateHandle()
	if err != nil {
		return struct {
			Session domain.AgentSession
			Handle  string
		}{}, err
	}
	result, err := service.createSession(ctx, normalized, sessionHandleHash(handle))
	if err != nil {
		return struct {
			Session domain.AgentSession
			Handle  string
		}{}, err
	}
	return struct {
		Session domain.AgentSession
		Handle  string
	}{Session: result.Clone(), Handle: handle}, nil
}

func (service *AgentSessionService) createSession(ctx context.Context, input domain.CreateAgentSessionInput, handleHash []byte) (domain.AgentSession, error) {
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
		ClientName:    input.ClientName,
		ClientVersion: copyApplicationString(input.ClientVersion),
		AgentLabel:    copyApplicationString(input.AgentLabel),
		Model:         copyApplicationString(input.Model),
		InstanceKey:   copyApplicationString(input.InstanceKey),
		StartedAt:     now,
		LastSeenAt:    now,
	}
	result, err := service.repository.CreateAgentSession(ctx, ports.CreateAgentSessionCommand{Session: session, HandleHash: handleHash})
	if err != nil {
		return domain.AgentSession{}, err
	}
	return result.Clone(), nil
}

func (service *AgentSessionService) ResolveHandle(ctx context.Context, handle string) (string, error) {
	if err := validateHandleInput(handle); err != nil {
		return "", err
	}
	return service.repository.ResolveAgentSessionHandle(ctx, ports.ResolveAgentSessionHandleCommand{Handle: handle})
}

func (service *AgentSessionService) ResolveAndTouch(ctx context.Context, handle string) (string, error) {
	if err := validateHandleInput(handle); err != nil {
		return "", err
	}
	now := service.clock.Now().UTC()
	return service.repository.ResolveAndTouchAgentSessionHandle(ctx, ports.ResolveAndTouchAgentSessionHandleCommand{Handle: handle, OccurredAt: now})
}

func (service *AgentSessionService) EndWithHandle(ctx context.Context, handle string) (domain.AgentSession, error) {
	if err := validateHandleInput(handle); err != nil {
		return domain.AgentSession{}, err
	}
	now := service.clock.Now().UTC()
	result, err := service.repository.EndAgentSessionByHandle(ctx, ports.EndAgentSessionByHandleCommand{Handle: handle, OccurredAt: now})
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

func validateHandleInput(value string) error {
	trimmed := value
	if trimmed == "" {
		return domain.NewError(domain.CodeInvalidArgument, "agent_session_handle is invalid", false,
			domain.Detail{Field: "agent_session_handle", Code: "INVALID_HANDLE"})
	}
	if len(trimmed) > 256 {
		return domain.NewError(domain.CodeInvalidArgument, "agent_session_handle is invalid", false,
			domain.Detail{Field: "agent_session_handle", Code: "INVALID_HANDLE"})
	}
	for _, r := range trimmed {
		if r > 127 || (r < 32 && r != 9 && r != 10 && r != 13) {
			return domain.NewError(domain.CodeInvalidArgument, "agent_session_handle is invalid", false,
				domain.Detail{Field: "agent_session_handle", Code: "INVALID_HANDLE"})
		}
	}
	return nil
}

func (service *AgentSessionService) generateHandle() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", domain.WrapError(err, domain.CodeStorageFailure, "cannot generate agent session handle", true)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func sessionHandleHash(handle string) []byte {
	sum := sha256.Sum256([]byte(handle))
	return sum[:]
}

func copyApplicationString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
