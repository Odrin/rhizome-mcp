package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"time"

	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/ports"
)

type AttemptService struct {
	repository ports.AttemptRepository
	clock      clock.Clock
	ids        IDGenerator
}

type ClaimIssueResult struct {
	Issue      domain.Issue
	Attempt    domain.WorkAttempt
	LeaseToken string
}

func NewAttemptService(repository ports.AttemptRepository, source clock.Clock, generator IDGenerator) (*AttemptService, error) {
	if repository == nil || source == nil || generator == nil {
		return nil, domain.NewError(domain.CodeInvalidArgument, "attempt dependencies are required", false)
	}
	return &AttemptService{repository: repository, clock: source, ids: generator}, nil
}

func (service *AttemptService) ClaimIssue(ctx context.Context, input domain.ClaimIssueInput) (ClaimIssueResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return ClaimIssueResult{}, err
	}
	identifier, err := domain.ParseIssueIdentifier(normalized.IssueID)
	if err != nil {
		return ClaimIssueResult{}, err
	}
	id, err := service.ids.New()
	if err != nil {
		return ClaimIssueResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate attempt identifier", false)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return ClaimIssueResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate attempt identifier", false)
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return ClaimIssueResult{}, domain.WrapError(err, domain.CodeStorageFailure, "cannot generate lease token", true)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	hash := sha256.Sum256([]byte(token))
	now := service.clock.Now().UTC()
	result, err := service.repository.ClaimIssue(ctx, ports.ClaimIssueCommand{
		Identifier: identifier, AttemptID: id, TokenHash: hash[:],
		LeaseDuration: time.Duration(*normalized.LeaseSeconds) * time.Second, OccurredAt: now,
	})
	if err != nil {
		return ClaimIssueResult{}, err
	}
	return ClaimIssueResult{Issue: result.Issue, Attempt: result.Attempt, LeaseToken: token}, nil
}

func (service *AttemptService) RenewAttempt(ctx context.Context, input domain.RenewAttemptInput) (ports.RenewAttemptResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return ports.RenewAttemptResult{}, err
	}
	hash := sha256.Sum256([]byte(normalized.LeaseToken))
	now := service.clock.Now().UTC()
	return service.repository.RenewAttempt(ctx, ports.RenewAttemptCommand{
		AttemptID: normalized.AttemptID, TokenHash: hash[:],
		LeaseDuration: time.Duration(*normalized.LeaseSeconds) * time.Second, OccurredAt: now,
	})
}

func (service *AttemptService) SaveAttemptNote(ctx context.Context, input domain.SaveAttemptNoteInput) (domain.AttemptNote, error) {
	normalized, err := input.Validate()
	if err != nil {
		return domain.AttemptNote{}, err
	}
	id, err := service.ids.New()
	if err != nil {
		return domain.AttemptNote{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate attempt note identifier", false)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return domain.AttemptNote{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate attempt note identifier", false)
	}
	hash := sha256.Sum256([]byte(normalized.LeaseToken))
	result, err := service.repository.SaveAttemptNote(ctx, ports.SaveAttemptNoteCommand{
		NoteID: id, AttemptID: normalized.AttemptID, TokenHash: hash[:], Kind: normalized.Kind,
		Content: normalized.Content, NextSteps: normalized.NextSteps, Important: normalized.Important,
		OccurredAt: service.clock.Now().UTC(),
	})
	if err != nil {
		return domain.AttemptNote{}, err
	}
	return result.Note, nil
}
