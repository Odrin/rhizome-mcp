package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
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
	var idempotencyKey string
	var requestHash []byte
	if normalized.IdempotencyKey != nil {
		canonical, err := domain.CanonicalClaimIssueRequest(normalized)
		if err != nil {
			return ClaimIssueResult{}, domain.WrapError(err, domain.CodeStorageFailure, "cannot encode claim request", false)
		}
		hash := sha256.Sum256(canonical)
		requestHash = append([]byte(nil), hash[:]...)
		idempotencyKey = *normalized.IdempotencyKey
		result, found, err := service.repository.LookupClaimIssue(ctx, idempotencyKey, requestHash)
		if err != nil {
			return ClaimIssueResult{}, err
		}
		if found {
			return ClaimIssueResult{Issue: result.Issue, Attempt: result.Attempt, LeaseToken: result.LeaseToken}, nil
		}
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
		Identifier: identifier, AttemptID: id, SessionID: normalized.SessionID, TokenHash: hash[:], LeaseToken: token,
		LeaseDuration: time.Duration(*normalized.LeaseSeconds) * time.Second, OccurredAt: now,
		IdempotencyKey: idempotencyKey, RequestHash: requestHash,
	})
	if err != nil {
		return ClaimIssueResult{}, err
	}
	return ClaimIssueResult{Issue: result.Issue, Attempt: result.Attempt, LeaseToken: result.LeaseToken}, nil
}

func (service *AttemptService) RenewAttempt(ctx context.Context, input domain.RenewAttemptInput) (ports.RenewAttemptResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return ports.RenewAttemptResult{}, err
	}
	hash := sha256.Sum256([]byte(normalized.LeaseToken))
	now := service.clock.Now().UTC()
	return service.repository.RenewAttempt(ctx, ports.RenewAttemptCommand{
		AttemptID: normalized.AttemptID, SessionID: normalized.SessionID, TokenHash: hash[:],
		LeaseDuration: time.Duration(*normalized.LeaseSeconds) * time.Second, OccurredAt: now,
	})
}

func (service *AttemptService) ExpireAttempts(ctx context.Context) (ports.ExpireAttemptsResult, error) {
	now := service.clock.Now().UTC()
	return service.repository.ExpireAttempts(ctx, ports.ExpireAttemptsCommand{OccurredAt: now})
}

// ListActiveAttempts returns a bounded, project-wide projection of currently
// active (leased) attempts. A non-positive or over-limit value defaults to the
// standard bounded collection limit.
func (service *AttemptService) ListActiveAttempts(ctx context.Context, limit int) ([]domain.ActiveAttemptSummary, error) {
	now := service.clock.Now().UTC()
	return service.repository.ListActiveAttempts(ctx, ports.ListActiveAttemptsCommand{Limit: limit, Now: now})
}

func (service *AttemptService) SaveAttemptNote(ctx context.Context, input domain.SaveAttemptNoteInput) (ports.SaveAttemptNoteResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return ports.SaveAttemptNoteResult{}, err
	}

	var idempotencyKey string
	var requestHash []byte
	if normalized.IdempotencyKey != nil {
		canonical, err := domain.CanonicalSaveAttemptNoteRequest(normalized)
		if err != nil {
			return ports.SaveAttemptNoteResult{}, domain.WrapError(err, domain.CodeStorageFailure, "cannot encode save attempt note request", false)
		}
		hash := sha256.Sum256(canonical)
		requestHash = append([]byte(nil), hash[:]...)
		idempotencyKey = *normalized.IdempotencyKey
		result, found, err := service.repository.LookupSaveAttemptNote(ctx, idempotencyKey, requestHash)
		if err != nil {
			return ports.SaveAttemptNoteResult{}, err
		}
		if found {
			return result, nil
		}
	}

	id, err := service.ids.New()
	if err != nil {
		return ports.SaveAttemptNoteResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate attempt note identifier", false)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return ports.SaveAttemptNoteResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate attempt note identifier", false)
	}
	now := service.clock.Now().UTC()
	artifacts := make([]domain.Artifact, len(normalized.Artifacts))
	for index, inputArtifact := range normalized.Artifacts {
		artifactID, err := service.ids.New()
		if err != nil {
			return ports.SaveAttemptNoteResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate artifact identifier", false,
				domain.Detail{Field: "artifacts[" + strconv.Itoa(index) + "].id", Code: "ID_GENERATION_FAILED"})
		}
		if _, err := ids.ParseStrict(artifactID); err != nil {
			return ports.SaveAttemptNoteResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate artifact identifier", false,
				domain.Detail{Field: "artifacts[" + strconv.Itoa(index) + "].id", Code: "INVALID_ULID"})
		}
		artifacts[index] = domain.Artifact{
			ID: artifactID, Type: inputArtifact.Type, URI: inputArtifact.URI,
			Title: inputArtifact.Title, Metadata: append([]byte(nil), inputArtifact.Metadata...),
			CreatedAt: now,
		}
	}
	tokenHash := sha256.Sum256([]byte(normalized.LeaseToken))
	result, err := service.repository.SaveAttemptNote(ctx, ports.SaveAttemptNoteCommand{
		NoteID: id, AttemptID: normalized.AttemptID, SessionID: normalized.SessionID, TokenHash: tokenHash[:], Kind: normalized.Kind,
		Content: normalized.Content, NextSteps: normalized.NextSteps, Important: normalized.Important,
		Artifacts: artifacts, OccurredAt: now, IdempotencyKey: idempotencyKey, RequestHash: requestHash,
	})
	if err != nil {
		return ports.SaveAttemptNoteResult{}, err
	}
	return result, nil
}

func (service *AttemptService) FinishAttempt(ctx context.Context, input domain.FinishAttemptInput) (ports.FinishAttemptResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return ports.FinishAttemptResult{}, err
	}
	var idempotencyKey string
	var requestHash []byte
	if normalized.IdempotencyKey != nil {
		canonical, err := domain.CanonicalFinishAttemptRequest(normalized)
		if err != nil {
			return ports.FinishAttemptResult{}, domain.WrapError(err, domain.CodeStorageFailure, "cannot encode finish request", false)
		}
		hash := sha256.Sum256(canonical)
		requestHash = append([]byte(nil), hash[:]...)
		idempotencyKey = *normalized.IdempotencyKey
		result, found, err := service.repository.LookupFinishedAttempt(ctx, idempotencyKey, requestHash)
		if err != nil {
			return ports.FinishAttemptResult{}, err
		}
		if found {
			return result, nil
		}
	}
	now := service.clock.Now().UTC()
	artifacts := make([]domain.Artifact, len(normalized.Artifacts))
	for index, inputArtifact := range normalized.Artifacts {
		artifactID, err := service.ids.New()
		if err != nil {
			return ports.FinishAttemptResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate artifact identifier", false,
				domain.Detail{Field: "artifacts[" + strconv.Itoa(index) + "].id", Code: "ID_GENERATION_FAILED"})
		}
		if _, err := ids.ParseStrict(artifactID); err != nil {
			return ports.FinishAttemptResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate artifact identifier", false,
				domain.Detail{Field: "artifacts[" + strconv.Itoa(index) + "].id", Code: "INVALID_ULID"})
		}
		var title *string
		if inputArtifact.Title != nil {
			value := *inputArtifact.Title
			title = &value
		}
		artifacts[index] = domain.Artifact{
			ID: artifactID, Type: inputArtifact.Type, URI: inputArtifact.URI, Title: title,
			Metadata: append([]byte(nil), inputArtifact.Metadata...), CreatedAt: now,
		}
	}
	hash := sha256.Sum256([]byte(normalized.LeaseToken))
	return service.repository.FinishAttempt(ctx, ports.FinishAttemptCommand{
		AttemptID: normalized.AttemptID, SessionID: normalized.SessionID, TokenHash: hash[:], Input: normalized,
		Artifacts: artifacts, IdempotencyKey: idempotencyKey, RequestHash: requestHash, OccurredAt: now,
	})
}
