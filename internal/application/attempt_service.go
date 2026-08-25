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
	Issue        domain.Issue
	Projection   domain.IssueProjection
	Attempt      domain.WorkAttempt
	Reservations []domain.Reservation
	LeaseToken   string
}

type RenewAttemptResult struct {
	LeaseExpiresAt time.Time
	ServerTime     time.Time
}

type SaveAttemptNoteResult struct {
	Note      domain.AttemptNote
	Artifacts []domain.Artifact
}

type FinishAttemptResult struct {
	Attempt       domain.WorkAttempt
	Issue         domain.Issue
	Warnings      []string
	LatestEventID int64
	Artifacts     []domain.Artifact
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
	}
	// No read-only fast path here: the persisted idempotency response never
	// carries a lease token, so a replay of a still-active attempt must
	// always go through the write path below to rotate the lease and issue
	// a fresh one (see ports.AttemptRepository.ClaimIssue).
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
	resources, err := service.newReservationResourceInputs(normalized.Resources, "resources")
	if err != nil {
		return ClaimIssueResult{}, err
	}
	result, err := service.repository.ClaimIssue(ctx, ports.ClaimIssueCommand{
		Identifier: identifier, AttemptID: id, SessionID: normalized.SessionID, TokenHash: hash[:], LeaseToken: token,
		LeaseDuration: time.Duration(*normalized.LeaseSeconds) * time.Second, OccurredAt: now, Resources: resources,
		IdempotencyKey: idempotencyKey, RequestHash: requestHash,
	})
	if err != nil {
		return ClaimIssueResult{}, err
	}
	return ClaimIssueResult{
		Issue: result.Issue, Projection: result.Projection, Attempt: result.Attempt,
		Reservations: result.Reservations, LeaseToken: result.LeaseToken,
	}, nil
}

// newReservationResourceInputs generates one ULID per requested resource
// (ID generation is an application-layer concern -- ports.ReservationResourceInput's
// own doc comment), preserving order. field names the caller's input slice
// in generated error details (e.g. "resources" for ClaimIssue,
// "resources" for ReserveResources), matching the artifacts[N].id
// convention FinishAttempt/SaveAttemptNote already use.
func (service *AttemptService) newReservationResourceInputs(resources []domain.Resource, field string) ([]ports.ReservationResourceInput, error) {
	if len(resources) == 0 {
		return nil, nil
	}
	inputs := make([]ports.ReservationResourceInput, len(resources))
	for index, resource := range resources {
		id, err := service.ids.New()
		if err != nil {
			return nil, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate reservation identifier", false,
				domain.Detail{Field: field + "[" + strconv.Itoa(index) + "].id", Code: "ID_GENERATION_FAILED"})
		}
		if _, err := ids.ParseStrict(id); err != nil {
			return nil, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate reservation identifier", false,
				domain.Detail{Field: field + "[" + strconv.Itoa(index) + "].id", Code: "INVALID_ULID"})
		}
		inputs[index] = ports.ReservationResourceInput{ID: id, Resource: resource}
	}
	return inputs, nil
}

func (service *AttemptService) RenewAttempt(ctx context.Context, input domain.RenewAttemptInput) (RenewAttemptResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return RenewAttemptResult{}, err
	}
	hash := sha256.Sum256([]byte(normalized.LeaseToken))
	now := service.clock.Now().UTC()
	result, err := service.repository.RenewAttempt(ctx, ports.RenewAttemptCommand{
		AttemptID: normalized.AttemptID, SessionID: normalized.SessionID, TokenHash: hash[:],
		LeaseDuration: time.Duration(*normalized.LeaseSeconds) * time.Second, OccurredAt: now,
	})
	if err != nil {
		return RenewAttemptResult{}, err
	}
	return RenewAttemptResult{
		LeaseExpiresAt: result.LeaseExpiresAt,
		ServerTime:     result.ServerTime,
	}, nil
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

func (service *AttemptService) SaveAttemptNote(ctx context.Context, input domain.SaveAttemptNoteInput) (SaveAttemptNoteResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return SaveAttemptNoteResult{}, err
	}

	var idempotencyKey string
	var requestHash []byte
	if normalized.IdempotencyKey != nil {
		canonical, err := domain.CanonicalSaveAttemptNoteRequest(normalized)
		if err != nil {
			return SaveAttemptNoteResult{}, domain.WrapError(err, domain.CodeStorageFailure, "cannot encode save attempt note request", false)
		}
		hash := sha256.Sum256(canonical)
		requestHash = append([]byte(nil), hash[:]...)
		idempotencyKey = *normalized.IdempotencyKey
		result, found, err := service.repository.LookupSaveAttemptNote(ctx, idempotencyKey, requestHash)
		if err != nil {
			return SaveAttemptNoteResult{}, err
		}
		if found {
			return SaveAttemptNoteResult{Note: result.Note, Artifacts: result.Artifacts}, nil
		}
	}

	id, err := service.ids.New()
	if err != nil {
		return SaveAttemptNoteResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate attempt note identifier", false)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return SaveAttemptNoteResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate attempt note identifier", false)
	}
	now := service.clock.Now().UTC()
	artifacts := make([]domain.Artifact, len(normalized.Artifacts))
	for index, inputArtifact := range normalized.Artifacts {
		artifactID, err := service.ids.New()
		if err != nil {
			return SaveAttemptNoteResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate artifact identifier", false,
				domain.Detail{Field: "artifacts[" + strconv.Itoa(index) + "].id", Code: "ID_GENERATION_FAILED"})
		}
		if _, err := ids.ParseStrict(artifactID); err != nil {
			return SaveAttemptNoteResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate artifact identifier", false,
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
		return SaveAttemptNoteResult{}, err
	}
	return SaveAttemptNoteResult{Note: result.Note, Artifacts: result.Artifacts}, nil
}

// SubmitGateEvidence validates and idempotently upserts one lease-authenticated
// evidence record (ISSUE-171).
func (service *AttemptService) SubmitGateEvidence(ctx context.Context, input domain.SubmitGateEvidenceInput) (ports.SubmitGateEvidenceResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return ports.SubmitGateEvidenceResult{}, err
	}

	var idempotencyKey string
	var requestHash []byte
	if normalized.IdempotencyKey != nil {
		canonical, err := domain.CanonicalSubmitGateEvidenceRequest(normalized)
		if err != nil {
			return ports.SubmitGateEvidenceResult{}, domain.WrapError(err, domain.CodeStorageFailure, "cannot encode submit gate evidence request", false)
		}
		hash := sha256.Sum256(canonical)
		requestHash = append([]byte(nil), hash[:]...)
		idempotencyKey = *normalized.IdempotencyKey
		result, found, err := service.repository.LookupSubmitGateEvidence(ctx, idempotencyKey, requestHash)
		if err != nil {
			return ports.SubmitGateEvidenceResult{}, err
		}
		if found {
			return result, nil
		}
	}

	id, err := service.ids.New()
	if err != nil {
		return ports.SubmitGateEvidenceResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate evidence identifier", false)
	}
	if _, err := ids.ParseStrict(id); err != nil {
		return ports.SubmitGateEvidenceResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate evidence identifier", false)
	}
	now := service.clock.Now().UTC()
	tokenHash := sha256.Sum256([]byte(normalized.LeaseToken))
	result, err := service.repository.SubmitGateEvidence(ctx, ports.SubmitGateEvidenceCommand{
		EvidenceID: id, AttemptID: normalized.AttemptID, TokenHash: tokenHash[:], Key: normalized.Key,
		Result: normalized.Result, Summary: normalized.Summary, Details: normalized.Details, ArtifactIDs: normalized.ArtifactIDs,
		OccurredAt: now, IdempotencyKey: idempotencyKey, RequestHash: requestHash,
	})
	if err != nil {
		return ports.SubmitGateEvidenceResult{}, err
	}
	return result, nil
}

// ListAttemptEvidence returns every current evidence record for one attempt.
func (service *AttemptService) ListAttemptEvidence(ctx context.Context, attemptID string) ([]domain.AttemptEvidence, error) {
	return service.repository.ListAttemptEvidence(ctx, ports.ListAttemptEvidenceCommand{AttemptID: attemptID})
}

// ReserveResources validates and idempotently adds resources to one active
// work attempt's reservations, all-or-nothing (ISSUE-180: reserve_resources).
func (service *AttemptService) ReserveResources(ctx context.Context, input domain.ReserveResourcesInput) (ports.ReserveResourcesResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return ports.ReserveResourcesResult{}, err
	}
	var idempotencyKey string
	var requestHash []byte
	if normalized.IdempotencyKey != nil {
		canonical, err := domain.CanonicalReserveResourcesRequest(normalized)
		if err != nil {
			return ports.ReserveResourcesResult{}, domain.WrapError(err, domain.CodeStorageFailure, "cannot encode reserve resources request", false)
		}
		hash := sha256.Sum256(canonical)
		requestHash = append([]byte(nil), hash[:]...)
		idempotencyKey = *normalized.IdempotencyKey
		result, found, err := service.repository.LookupReserveResources(ctx, idempotencyKey, requestHash)
		if err != nil {
			return ports.ReserveResourcesResult{}, err
		}
		if found {
			return result, nil
		}
	}
	resources, err := service.newReservationResourceInputs(normalized.Resources, "resources")
	if err != nil {
		return ports.ReserveResourcesResult{}, err
	}
	now := service.clock.Now().UTC()
	tokenHash := sha256.Sum256([]byte(normalized.LeaseToken))
	return service.repository.ReserveResources(ctx, ports.ReserveResourcesCommand{
		AttemptID: normalized.AttemptID, SessionID: normalized.SessionID, TokenHash: tokenHash[:], Resources: resources,
		OccurredAt: now, IdempotencyKey: idempotencyKey, RequestHash: requestHash,
	})
}

// ReleaseResources validates and idempotently releases reservations owned
// by one active work attempt (ISSUE-180: release_resources).
func (service *AttemptService) ReleaseResources(ctx context.Context, input domain.ReleaseResourcesInput) (ports.ReleaseResourcesResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return ports.ReleaseResourcesResult{}, err
	}
	var idempotencyKey string
	var requestHash []byte
	if normalized.IdempotencyKey != nil {
		canonical, err := domain.CanonicalReleaseResourcesRequest(normalized)
		if err != nil {
			return ports.ReleaseResourcesResult{}, domain.WrapError(err, domain.CodeStorageFailure, "cannot encode release resources request", false)
		}
		hash := sha256.Sum256(canonical)
		requestHash = append([]byte(nil), hash[:]...)
		idempotencyKey = *normalized.IdempotencyKey
		result, found, err := service.repository.LookupReleaseResources(ctx, idempotencyKey, requestHash)
		if err != nil {
			return ports.ReleaseResourcesResult{}, err
		}
		if found {
			return result, nil
		}
	}
	now := service.clock.Now().UTC()
	tokenHash := sha256.Sum256([]byte(normalized.LeaseToken))
	return service.repository.ReleaseResources(ctx, ports.ReleaseResourcesCommand{
		AttemptID: normalized.AttemptID, SessionID: normalized.SessionID, TokenHash: tokenHash[:], ReservationIDs: normalized.ReservationIDs,
		OccurredAt: now, IdempotencyKey: idempotencyKey, RequestHash: requestHash,
	})
}

func (service *AttemptService) FinishAttempt(ctx context.Context, input domain.FinishAttemptInput) (FinishAttemptResult, error) {
	normalized, err := input.Validate()
	if err != nil {
		return FinishAttemptResult{}, err
	}
	var idempotencyKey string
	var requestHash []byte
	if normalized.IdempotencyKey != nil {
		canonical, err := domain.CanonicalFinishAttemptRequest(normalized)
		if err != nil {
			return FinishAttemptResult{}, domain.WrapError(err, domain.CodeStorageFailure, "cannot encode finish request", false)
		}
		hash := sha256.Sum256(canonical)
		requestHash = append([]byte(nil), hash[:]...)
		idempotencyKey = *normalized.IdempotencyKey
		result, found, err := service.repository.LookupFinishedAttempt(ctx, idempotencyKey, requestHash)
		if err != nil {
			return FinishAttemptResult{}, err
		}
		if found {
			return FinishAttemptResult{
				Attempt:       result.Attempt,
				Issue:         result.Issue,
				Warnings:      result.Warnings,
				LatestEventID: result.LatestEventID,
				Artifacts:     result.Artifacts,
			}, nil
		}
	}
	now := service.clock.Now().UTC()
	artifacts := make([]domain.Artifact, len(normalized.Artifacts))
	for index, inputArtifact := range normalized.Artifacts {
		artifactID, err := service.ids.New()
		if err != nil {
			return FinishAttemptResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate artifact identifier", false,
				domain.Detail{Field: "artifacts[" + strconv.Itoa(index) + "].id", Code: "ID_GENERATION_FAILED"})
		}
		if _, err := ids.ParseStrict(artifactID); err != nil {
			return FinishAttemptResult{}, domain.WrapError(err, domain.CodeIDGeneration, "cannot generate artifact identifier", false,
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
	result, err := service.repository.FinishAttempt(ctx, ports.FinishAttemptCommand{
		AttemptID: normalized.AttemptID, SessionID: normalized.SessionID, TokenHash: hash[:], Input: normalized,
		Artifacts: artifacts, IdempotencyKey: idempotencyKey, RequestHash: requestHash, OccurredAt: now,
	})
	if err != nil {
		return FinishAttemptResult{}, err
	}
	return FinishAttemptResult{
		Attempt:       result.Attempt,
		Issue:         result.Issue,
		Warnings:      result.Warnings,
		LatestEventID: result.LatestEventID,
		Artifacts:     result.Artifacts,
	}, nil
}
