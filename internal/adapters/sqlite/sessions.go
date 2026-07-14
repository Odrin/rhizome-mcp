package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

// AgentSessionRepository is the SQLite implementation of the session port.
type AgentSessionRepository struct {
	db *DB
}

func NewAgentSessionRepository(database *DB) (*AgentSessionRepository, error) {
	if database == nil {
		return nil, domain.NewError(domain.CodeStorageConfiguration, "agent session database is required", false)
	}
	return &AgentSessionRepository{db: database}, nil
}

func (repository *AgentSessionRepository) CreateAgentSession(ctx context.Context, command ports.CreateAgentSessionCommand) (domain.AgentSession, error) {
	session, err := validateSessionCommand(command.Session)
	if err != nil {
		return domain.AgentSession{}, err
	}
	err = repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_sessions(
			id, client_name, client_version, agent_label, model, instance_key,
			started_at, last_seen_at, ended_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			session.ID, session.ClientName, nullableSessionString(session.ClientVersion),
			nullableSessionString(session.AgentLabel), nullableSessionString(session.Model),
			nullableSessionString(session.InstanceKey), formatSessionTime(session.StartedAt),
			formatSessionTime(session.LastSeenAt), nullableSessionTime(session.EndedAt))
		return err
	})
	if err != nil {
		return domain.AgentSession{}, err
	}
	return session.Clone(), nil
}

func (repository *AgentSessionRepository) TouchAgentSession(ctx context.Context, command ports.TouchAgentSessionCommand) (domain.AgentSession, error) {
	if err := validateSessionIDCommand(command.SessionID); err != nil {
		return domain.AgentSession{}, err
	}
	if command.OccurredAt.IsZero() {
		return domain.AgentSession{}, invalidSessionCommand("occurred_at", "timestamp is required")
	}
	occurredAt := command.OccurredAt.UTC()
	var result domain.AgentSession
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		session, err := loadAgentSession(ctx, tx, command.SessionID)
		if err != nil {
			return err
		}
		if session.EndedAt != nil {
			return domain.NewError(domain.CodeSessionNotActive, "agent session is not active", false)
		}
		if occurredAt.After(session.LastSeenAt) {
			session.LastSeenAt = occurredAt
			if _, err := tx.ExecContext(ctx, `UPDATE agent_sessions SET last_seen_at = ? WHERE id = ?`,
				formatSessionTime(occurredAt), command.SessionID); err != nil {
				return err
			}
		}
		result = session.Clone()
		return nil
	})
	if err != nil {
		return domain.AgentSession{}, err
	}
	return result, nil
}

func (repository *AgentSessionRepository) EndAgentSession(ctx context.Context, command ports.EndAgentSessionCommand) (domain.AgentSession, error) {
	if err := validateSessionIDCommand(command.SessionID); err != nil {
		return domain.AgentSession{}, err
	}
	if command.OccurredAt.IsZero() {
		return domain.AgentSession{}, invalidSessionCommand("occurred_at", "timestamp is required")
	}
	occurredAt := command.OccurredAt.UTC()
	var result domain.AgentSession
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		session, err := loadAgentSession(ctx, tx, command.SessionID)
		if err != nil {
			return err
		}
		if session.EndedAt != nil {
			result = session.Clone()
			return nil
		}
		next := session.LastSeenAt
		if occurredAt.After(next) {
			next = occurredAt
		}
		session.LastSeenAt = next
		session.EndedAt = sessionTimePointer(next)
		if _, err := tx.ExecContext(ctx, `UPDATE agent_sessions
			SET last_seen_at = ?, ended_at = ? WHERE id = ?`,
			formatSessionTime(next), formatSessionTime(next), command.SessionID); err != nil {
			return err
		}
		result = session.Clone()
		return nil
	})
	if err != nil {
		return domain.AgentSession{}, err
	}
	return result, nil
}

func validateSessionCommand(session domain.AgentSession) (domain.AgentSession, error) {
	if _, err := ids.ParseStrict(session.ID); err != nil {
		return domain.AgentSession{}, invalidSessionID("id")
	}
	normalized, err := (domain.CreateAgentSessionInput{
		ClientName: session.ClientName, ClientVersion: session.ClientVersion,
		AgentLabel: session.AgentLabel, Model: session.Model, InstanceKey: session.InstanceKey,
	}).Validate()
	if err != nil {
		return domain.AgentSession{}, err
	}
	if session.StartedAt.IsZero() {
		return domain.AgentSession{}, invalidSessionCommand("started_at", "timestamp is required")
	}
	if session.LastSeenAt.IsZero() {
		return domain.AgentSession{}, invalidSessionCommand("last_seen_at", "timestamp is required")
	}
	result := domain.AgentSession{
		ID: session.ID, ClientName: normalized.ClientName,
		ClientVersion: normalized.ClientVersion, AgentLabel: normalized.AgentLabel,
		Model: normalized.Model, InstanceKey: normalized.InstanceKey,
		StartedAt: session.StartedAt.UTC(), LastSeenAt: session.LastSeenAt.UTC(),
	}
	if session.EndedAt != nil {
		if session.EndedAt.IsZero() {
			return domain.AgentSession{}, invalidSessionCommand("ended_at", "timestamp is required")
		}
		ended := session.EndedAt.UTC()
		result.EndedAt = &ended
	}
	if result.LastSeenAt.Before(result.StartedAt) ||
		(result.EndedAt != nil && (result.EndedAt.Before(result.StartedAt) || !result.EndedAt.Equal(result.LastSeenAt))) {
		return domain.AgentSession{}, invalidSessionCommand("timestamps", "are inconsistent")
	}
	return result.Clone(), nil
}

func loadAgentSession(ctx context.Context, query Queryer, sessionID string) (domain.AgentSession, error) {
	var (
		id, clientName, startedAt, lastSeenAt                  string
		clientVersion, agentLabel, model, instanceKey, endedAt sql.NullString
	)
	err := query.QueryRowContext(ctx, `SELECT id, client_name, client_version, agent_label, model,
		instance_key, started_at, last_seen_at, ended_at FROM agent_sessions WHERE id = ?`, sessionID).
		Scan(&id, &clientName, &clientVersion, &agentLabel, &model, &instanceKey, &startedAt, &lastSeenAt, &endedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentSession{}, domain.NewError(domain.CodeSessionNotFound, "agent session not found", false)
	}
	if err != nil {
		return domain.AgentSession{}, corruptSessionProjection(err)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.AgentSession{}, corruptSessionProjection(err)
	}
	metadata := domain.CreateAgentSessionInput{
		ClientName: clientName, ClientVersion: nullableSessionPointer(clientVersion),
		AgentLabel: nullableSessionPointer(agentLabel), Model: nullableSessionPointer(model),
		InstanceKey: nullableSessionPointer(instanceKey),
	}
	normalized, err := metadata.Validate()
	if err != nil {
		return domain.AgentSession{}, corruptSessionProjection(err)
	}
	started, err := parseIssueTimestamp("started_at", startedAt)
	if err != nil {
		return domain.AgentSession{}, err
	}
	lastSeen, err := parseIssueTimestamp("last_seen_at", lastSeenAt)
	if err != nil {
		return domain.AgentSession{}, err
	}
	var ended *time.Time
	if endedAt.Valid {
		parsed, err := parseIssueTimestamp("ended_at", endedAt.String)
		if err != nil {
			return domain.AgentSession{}, err
		}
		ended = &parsed
	}
	if started.IsZero() || lastSeen.IsZero() || lastSeen.Before(started) ||
		(ended != nil && (ended.Before(started) || !ended.Equal(lastSeen))) {
		return domain.AgentSession{}, corruptSessionProjection(fmt.Errorf("invalid session temporal state"))
	}
	return domain.AgentSession{
		ID: id, ClientName: normalized.ClientName, ClientVersion: normalized.ClientVersion,
		AgentLabel: normalized.AgentLabel, Model: normalized.Model, InstanceKey: normalized.InstanceKey,
		StartedAt: started, LastSeenAt: lastSeen, EndedAt: ended,
	}, nil
}

func validateSessionIDCommand(value string) error {
	if _, err := ids.ParseStrict(value); err != nil {
		return invalidSessionID("session_id")
	}
	return nil
}

func invalidSessionID(field string) error {
	return domain.NewError(domain.CodeInvalidArgument, "agent session command is invalid", false,
		domain.Detail{Field: field, Code: "INVALID_ULID", Message: "must be a canonical ULID"})
}

func invalidSessionCommand(field, message string) error {
	return domain.NewError(domain.CodeInvalidArgument, "agent session command is invalid", false,
		domain.Detail{Field: field, Code: "INVALID_VALUE", Message: message})
}

func corruptSessionProjection(cause error) error {
	var source *domain.Error
	if errors.As(cause, &source) {
		return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored agent session projection is invalid", false, source.Details...)
	}
	return domain.WrapError(cause, domain.CodeStorageCorrupt, "stored agent session projection is invalid", false)
}

func nullableSessionString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableSessionTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatSessionTime(*value)
}

func nullableSessionPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func sessionTimePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}

func formatSessionTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

var _ ports.AgentSessionRepository = (*AgentSessionRepository)(nil)
