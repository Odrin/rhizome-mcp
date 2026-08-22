package domain

import (
	"encoding/json"
	"strings"
	"time"

	"rhizome-mcp/internal/ids"
)

// See docs/02 §17 (ISSUE-171: lease-authenticated structured gate evidence).

const (
	// MaxGateEvidenceArtifactIDs bounds the artifact reference count on one
	// evidence submission.
	MaxGateEvidenceArtifactIDs = 20
	// MaxGateEvidenceKeyRunes bounds an evidence key, matching MaxPolicyKeyRunes
	// since a submission's key must match a requirement's evidence_key.
	MaxGateEvidenceKeyRunes = MaxPolicyKeyRunes
	// MaxGateEvidenceSummaryRunes bounds the required human-readable summary.
	MaxGateEvidenceSummaryRunes = 2_000
	// MaxGateEvidenceDetailsRunes bounds the optional free-form details.
	MaxGateEvidenceDetailsRunes = 50_000
)

// EvidenceResult identifies the outcome an evidence submission records.
type EvidenceResult string

const (
	EvidenceResultSatisfied     EvidenceResult = "satisfied"
	EvidenceResultNotApplicable EvidenceResult = "not_applicable"
)

// Valid reports whether result is one of the two supported evidence outcomes.
func (result EvidenceResult) Valid() bool {
	switch result {
	case EvidenceResultSatisfied, EvidenceResultNotApplicable:
		return true
	default:
		return false
	}
}

// SubmitGateEvidenceInput is caller-supplied evidence-submission input before
// validation. AttemptID and LeaseToken authenticate the call; the issue is
// derived from the attempt, never caller-supplied.
type SubmitGateEvidenceInput struct {
	AttemptID      string
	LeaseToken     string
	Key            string
	Result         EvidenceResult
	Summary        string
	Details        string
	ArtifactIDs    []string
	IdempotencyKey *string
}

// Validate validates and normalizes a submission. It does not check the
// evidence key against the attempt's frozen requirement snapshot or verify
// artifact ownership: those checks require storage reads and are performed
// by the repository inside the authenticated write transaction (docs/02
// §17 / AC2).
func (input SubmitGateEvidenceInput) Validate() (SubmitGateEvidenceInput, error) {
	if _, err := ids.ParseStrict(input.AttemptID); err != nil {
		return SubmitGateEvidenceInput{}, validationError("attempt_id", "INVALID_ULID", "must be a canonical ULID")
	}
	if err := ValidateText("lease_token", input.LeaseToken, MaxLeaseTokenRunes); err != nil {
		return SubmitGateEvidenceInput{}, err
	}
	if strings.TrimSpace(input.LeaseToken) == "" {
		return SubmitGateEvidenceInput{}, validationError("lease_token", "REQUIRED", "is required")
	}
	if err := ValidateText("key", input.Key, MaxGateEvidenceKeyRunes); err != nil {
		return SubmitGateEvidenceInput{}, err
	}
	key := strings.TrimSpace(input.Key)
	if key == "" {
		return SubmitGateEvidenceInput{}, validationError("key", "REQUIRED", "must not be blank")
	}
	if !input.Result.Valid() {
		return SubmitGateEvidenceInput{}, invalidEnum("result", string(input.Result))
	}
	if err := ValidateText("summary", input.Summary, MaxGateEvidenceSummaryRunes); err != nil {
		return SubmitGateEvidenceInput{}, err
	}
	summary := strings.TrimSpace(input.Summary)
	if summary == "" {
		return SubmitGateEvidenceInput{}, validationError("summary", "REQUIRED", "must not be blank")
	}
	if err := ValidateText("details", input.Details, MaxGateEvidenceDetailsRunes); err != nil {
		return SubmitGateEvidenceInput{}, err
	}
	artifactIDs, err := CopyBounded("artifact_ids", input.ArtifactIDs, MaxGateEvidenceArtifactIDs)
	if err != nil {
		return SubmitGateEvidenceInput{}, err
	}
	seen := make(map[string]struct{}, len(artifactIDs))
	for index, artifactID := range artifactIDs {
		if _, err := ids.ParseStrict(artifactID); err != nil {
			return SubmitGateEvidenceInput{}, NewError(CodeInvalidArgument, "artifact_ids must be canonical ULIDs", false,
				Detail{EntityIndex: &index, Field: "artifact_ids", Code: "INVALID_ULID"})
		}
		if _, exists := seen[artifactID]; exists {
			return SubmitGateEvidenceInput{}, NewError(CodeInvalidArgument, "artifact_ids must not contain duplicates", false,
				Detail{EntityIndex: &index, Field: "artifact_ids", Code: "DUPLICATE"})
		}
		seen[artifactID] = struct{}{}
	}
	var idempotencyKey *string
	if input.IdempotencyKey != nil {
		if err := ValidateText("idempotency_key", *input.IdempotencyKey, MaxIdempotencyKeyRunes); err != nil {
			return SubmitGateEvidenceInput{}, err
		}
		trimmed := strings.TrimSpace(*input.IdempotencyKey)
		if trimmed == "" {
			return SubmitGateEvidenceInput{}, validationError("idempotency_key", "REQUIRED", "must not be blank when present")
		}
		idempotencyKey = &trimmed
	}

	return SubmitGateEvidenceInput{
		AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, Key: key, Result: input.Result,
		Summary: summary, Details: input.Details, ArtifactIDs: artifactIDs, IdempotencyKey: idempotencyKey,
	}, nil
}

// AttemptEvidence is one persisted, versioned evidence record: at most one
// current record exists per (attempt, key) pair. Records become immutable
// once their owning attempt is no longer active, but are never deleted.
type AttemptEvidence struct {
	ID          string
	AttemptID   string
	IssueID     string
	Key         string
	Result      EvidenceResult
	Summary     string
	Details     string
	ArtifactIDs []string
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CanonicalSubmitGateEvidenceRequest returns the deterministic JSON encoding
// of a validated submission, used to derive its idempotency request hash.
func CanonicalSubmitGateEvidenceRequest(input SubmitGateEvidenceInput) ([]byte, error) {
	request := struct {
		AttemptID   string   `json:"attempt_id"`
		LeaseToken  string   `json:"lease_token"`
		Key         string   `json:"key"`
		Result      string   `json:"result"`
		Summary     string   `json:"summary"`
		Details     string   `json:"details"`
		ArtifactIDs []string `json:"artifact_ids"`
	}{
		AttemptID: input.AttemptID, LeaseToken: input.LeaseToken, Key: input.Key,
		Result: string(input.Result), Summary: input.Summary, Details: input.Details,
		ArtifactIDs: input.ArtifactIDs,
	}
	if request.ArtifactIDs == nil {
		request.ArtifactIDs = []string{}
	}
	return json.Marshal(request)
}

// CloneAttemptEvidence returns a record with no shared slice data.
func CloneAttemptEvidence(evidence AttemptEvidence) AttemptEvidence {
	evidence.ArtifactIDs = append([]string(nil), evidence.ArtifactIDs...)
	return evidence
}
