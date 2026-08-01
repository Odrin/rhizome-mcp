package sqlite

import (
	"context"
	"crypto/sha256"
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
			started_at, last_seen_at, ended_at, handle_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			session.ID, session.ClientName, nullableSessionString(session.ClientVersion),
			nullableSessionString(session.AgentLabel), nullableSessionString(session.Model),
			nullableSessionString(session.InstanceKey), formatSessionTime(session.StartedAt),
			formatSessionTime(session.LastSeenAt), nullableSessionTime(session.EndedAt), nullableSessionBytes(command.HandleHash))
		return err
	})
	if err != nil {
		return domain.AgentSession{}, err
	}
	return session.Clone(), nil
}

func (repository *AgentSessionRepository) ResolveAgentSessionHandle(ctx context.Context, command ports.ResolveAgentSessionHandleCommand) (string, error) {
	if err := validateHandleString(command.Handle); err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(command.Handle))
	var sessionID string
	err := repository.db.Read(ctx, func(ctx context.Context, query Queryer) error {
		return query.QueryRowContext(ctx, `SELECT id FROM agent_sessions WHERE handle_hash = ?`, hash[:]).Scan(&sessionID)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", domain.NewError(domain.CodeSessionNotFound, "agent session not found", false, domain.Detail{Field: "agent_session_handle", Code: "NOT_FOUND"})
		}
		return "", err
	}
	if _, err := ids.ParseStrict(sessionID); err != nil {
		return "", corruptSessionProjection(err)
	}
	return sessionID, nil
}

func (repository *AgentSessionRepository) ResolveAndTouchAgentSessionHandle(ctx context.Context, command ports.ResolveAndTouchAgentSessionHandleCommand) (string, error) {
	if err := validateHandleString(command.Handle); err != nil {
		return "", err
	}
	if command.OccurredAt.IsZero() {
		return "", invalidSessionCommand("occurred_at", "timestamp is required")
	}
	hash := sha256.Sum256([]byte(command.Handle))
	occurredAt := command.OccurredAt.UTC()
	var sessionID string
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		session, err := loadAgentSessionByHandle(ctx, tx, hash[:])
		if err != nil {
			return err
		}
		if session.EndedAt != nil {
			return domain.NewError(domain.CodeSessionNotActive, "agent session is not active", false, domain.Detail{Field: "agent_session_handle", Code: "ENDED"})
		}
		if occurredAt.After(session.LastSeenAt) {
			session.LastSeenAt = occurredAt
			if _, err := tx.ExecContext(ctx, `UPDATE agent_sessions SET last_seen_at = ? WHERE id = ?`,
				formatSessionTime(occurredAt), session.ID); err != nil {
				return err
			}
		}
		sessionID = session.ID
		return nil
	})
	if err != nil {
		return "", err
	}
	return sessionID, nil
}

func (repository *AgentSessionRepository) EndAgentSessionByHandle(ctx context.Context, command ports.EndAgentSessionByHandleCommand) (domain.AgentSession, error) {
	if err := validateHandleString(command.Handle); err != nil {
		return domain.AgentSession{}, err
	}
	if command.OccurredAt.IsZero() {
		return domain.AgentSession{}, invalidSessionCommand("occurred_at", "timestamp is required")
	}
	hash := sha256.Sum256([]byte(command.Handle))
	occurredAt := command.OccurredAt.UTC()
	var result domain.AgentSession
	err := repository.db.Write(ctx, func(ctx context.Context, tx Executor) error {
		session, err := loadAgentSessionByHandle(ctx, tx, hash[:])
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
		if _, err := tx.ExecContext(ctx, `UPDATE agent_sessions SET last_seen_at = ?, ended_at = ? WHERE id = ?`,
			formatSessionTime(next), formatSessionTime(next), session.ID); err != nil {
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

func loadAgentSessionByHandle(ctx context.Context, query Queryer, handleHash []byte) (domain.AgentSession, error) {
	var (
		id, clientName, startedAt, lastSeenAt                  string
		clientVersion, agentLabel, model, instanceKey, endedAt sql.NullString
	)
	err := query.QueryRowContext(ctx, `SELECT id, client_name, client_version, agent_label, model,
		instance_key, started_at, last_seen_at, ended_at FROM agent_sessions WHERE handle_hash = ?`, handleHash).
		Scan(&id, &clientName, &clientVersion, &agentLabel, &model, &instanceKey, &startedAt, &lastSeenAt, &endedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentSession{}, domain.NewError(domain.CodeSessionNotFound, "agent session not found", false, domain.Detail{Field: "agent_session_handle", Code: "NOT_FOUND"})
	}
	if err != nil {
		return domain.AgentSession{}, corruptSessionProjection(err)
	}
	return loadAgentSessionFromRow(id, clientName, clientVersion, agentLabel, model, instanceKey, startedAt, lastSeenAt, endedAt)
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
	return loadAgentSessionFromRow(id, clientName, clientVersion, agentLabel, model, instanceKey, startedAt, lastSeenAt, endedAt)
}

func loadAgentSessionFromRow(id, clientName string, clientVersion, agentLabel, model, instanceKey sql.NullString, startedAt, lastSeenAt string, endedAt sql.NullString) (domain.AgentSession, error) {
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

func validateHandleString(value string) error {
	trimmed := value
	if trimmed == "" {
		return domain.NewError(domain.CodeInvalidArgument, "agent session command is invalid", false,
			domain.Detail{Field: "agent_session_handle", Code: "INVALID_HANDLE"})
	}
	if len(trimmed) > 256 {
		return domain.NewError(domain.CodeInvalidArgument, "agent session command is invalid", false,
			domain.Detail{Field: "agent_session_handle", Code: "INVALID_HANDLE"})
	}
	for _, r := range trimmed {
		if r > 127 || (r < 32 && r != 9 && r != 10 && r != 13) {
			return domain.NewError(domain.CodeInvalidArgument, "agent session command is invalid", false,
				domain.Detail{Field: "agent_session_handle", Code: "INVALID_HANDLE"})
		}
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

func nullableSessionBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
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
