package domain

import (
	"strings"
	"time"
)

const MaxSessionMetadataRunes = 256

// AgentSession is the durable audit record for one agent connection.
type AgentSession struct {
	ID            string
	ClientName    string
	ClientVersion *string
	AgentLabel    *string
	Model         *string
	InstanceKey   *string
	StartedAt     time.Time
	LastSeenAt    time.Time
	EndedAt       *time.Time
}

// CreateAgentSessionInput contains client metadata for a new session.
type CreateAgentSessionInput struct {
	ClientName    string
	ClientVersion *string
	AgentLabel    *string
	Model         *string
	InstanceKey   *string
}

// Validate returns normalized metadata and defensive copies of optional values.
func (input CreateAgentSessionInput) Validate() (CreateAgentSessionInput, error) {
	clientName := strings.TrimSpace(input.ClientName)
	if clientName == "" {
		return CreateAgentSessionInput{}, validationError("client_name", "REQUIRED", "is required")
	}
	if err := ValidateText("client_name", clientName, MaxSessionMetadataRunes); err != nil {
		return CreateAgentSessionInput{}, err
	}

	normalized := CreateAgentSessionInput{ClientName: clientName}
	var err error
	if normalized.ClientVersion, err = normalizeSessionMetadata("client_version", input.ClientVersion); err != nil {
		return CreateAgentSessionInput{}, err
	}
	if normalized.AgentLabel, err = normalizeSessionMetadata("agent_label", input.AgentLabel); err != nil {
		return CreateAgentSessionInput{}, err
	}
	if normalized.Model, err = normalizeSessionMetadata("model", input.Model); err != nil {
		return CreateAgentSessionInput{}, err
	}
	if normalized.InstanceKey, err = normalizeSessionMetadata("instance_key", input.InstanceKey); err != nil {
		return CreateAgentSessionInput{}, err
	}
	return normalized, nil
}

func normalizeSessionMetadata(field string, value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil, validationError(field, "REQUIRED", "must be nonblank when provided")
	}
	if err := ValidateText(field, normalized, MaxSessionMetadataRunes); err != nil {
		return nil, err
	}
	return &normalized, nil
}

// Clone returns a fully independent session value.
func (session AgentSession) Clone() AgentSession {
	result := session
	result.ClientVersion = copySessionString(session.ClientVersion)
	result.AgentLabel = copySessionString(session.AgentLabel)
	result.Model = copySessionString(session.Model)
	result.InstanceKey = copySessionString(session.InstanceKey)
	if session.EndedAt != nil {
		ended := *session.EndedAt
		result.EndedAt = &ended
	}
	return result
}

func copySessionString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
