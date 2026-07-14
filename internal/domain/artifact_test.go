package domain_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"rhizome-mcp/internal/domain"
)

func TestValidateArtifactInputsNormalizesAndCopies(t *testing.T) {
	title := "source"
	metadata := json.RawMessage(`{ "language": "go" }`)
	input := []domain.ArtifactInput{{
		Type: domain.ArtifactTypeFile, URI: "internal/application/attempt_service.go",
		Title: &title, Metadata: metadata,
	}}
	normalized, err := domain.ValidateArtifactInputs("artifacts", input)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized) != 1 || string(normalized[0].Metadata) != `{"language":"go"}` ||
		normalized[0].Title == nil || *normalized[0].Title != title {
		t.Fatalf("normalized = %#v", normalized)
	}
	inputMetadata := string(input[0].Metadata)
	normalized[0].Metadata[0] = '['
	if string(input[0].Metadata) != inputMetadata {
		t.Fatalf("normalized metadata changed caller input: %s", input[0].Metadata)
	}
	title = "changed"
	metadata[2] = 'x'
	if normalized[0].Title == nil || *normalized[0].Title != "source" {
		t.Fatalf("normalized title was not defensive: %#v", normalized[0])
	}
	if string(normalized[0].Metadata) != `["language":"go"}` {
		t.Fatalf("normalized metadata was not defensive: %s", normalized[0].Metadata)
	}
}

func TestValidateArtifactInputsSafetyRulesAndFieldPaths(t *testing.T) {
	valid := func(kind domain.ArtifactType, uri string) domain.ArtifactInput {
		return domain.ArtifactInput{Type: kind, URI: uri}
	}
	for _, input := range []domain.ArtifactInput{
		valid(domain.ArtifactTypeFile, "../outside"),
		valid(domain.ArtifactTypeFile, "/absolute"),
		valid(domain.ArtifactTypeFile, "C:/outside.txt"),
		valid(domain.ArtifactTypeDirectory, "c:relative"),
		valid(domain.ArtifactTypeFile, `dir\file`),
		valid(domain.ArtifactTypeDirectory, "."),
		valid(domain.ArtifactTypeURL, "relative"),
		valid(domain.ArtifactTypeURL, "ftp://example.invalid/file"),
		valid(domain.ArtifactTypeURL, "https://user:pass@example.invalid/file"),
		{Type: "invalid", URI: "value"},
	} {
		_, err := domain.ValidateArtifactInputs("artifacts", []domain.ArtifactInput{input})
		if !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
			t.Fatalf("input %#v error = %v", input, err)
		}
		if len(err.(*domain.Error).Details) == 0 || !strings.HasPrefix(err.(*domain.Error).Details[0].Field, "artifacts[0].") {
			t.Fatalf("input %#v has no nested field detail: %v", input, err)
		}
	}
	for _, input := range []domain.ArtifactInput{
		{Type: domain.ArtifactTypeFile, URI: " "},
		{Type: domain.ArtifactTypeFile, URI: strings.Repeat("x", domain.MaxArtifactURIRunes+1)},
		{Type: domain.ArtifactTypeFile, URI: "file", Title: artifactStringPointer(" ")},
		{Type: domain.ArtifactTypeFile, URI: "file", Title: artifactStringPointer(strings.Repeat("x", domain.MaxTitleRunes+1))},
		{Type: domain.ArtifactTypeFile, URI: "file", Metadata: json.RawMessage(`[]`)},
		{Type: domain.ArtifactTypeFile, URI: "file", Metadata: json.RawMessage(`{`)},
		{Type: domain.ArtifactTypeFile, URI: "file", Metadata: json.RawMessage{}},
	} {
		_, err := domain.ValidateArtifactInputs("artifacts", []domain.ArtifactInput{input})
		if err == nil {
			t.Fatalf("invalid artifact accepted: %#v", input)
		}
	}
}

func TestValidateArtifactInputsLimitsAndMetadata(t *testing.T) {
	values := make([]domain.ArtifactInput, domain.MaxArtifactsPerAttemptMutation+1)
	for index := range values {
		values[index] = domain.ArtifactInput{Type: domain.ArtifactTypeOther, URI: "ref"}
	}
	_, err := domain.ValidateArtifactInputs("artifacts", values)
	if !errors.Is(err, &domain.Error{Code: domain.CodeLimitExceeded}) {
		t.Fatalf("too many artifacts error = %v", err)
	}
	if len(err.(*domain.Error).Details) != 1 || err.(*domain.Error).Details[0].Code != "MAX_ITEMS" {
		t.Fatalf("too many artifacts details = %#v", err)
	}
	_, err = domain.ValidateArtifactInputs("artifacts", []domain.ArtifactInput{{
		Type: domain.ArtifactTypeOther, URI: "ref",
		Metadata: json.RawMessage(strings.Repeat("x", domain.MaxArtifactMetadataBytes+1)),
	}})
	if !errors.Is(err, &domain.Error{Code: domain.CodeLimitExceeded}) ||
		err.(*domain.Error).Details[0].Code != "MAX_BYTES" {
		t.Fatalf("oversized metadata error = %v", err)
	}
	for _, metadata := range []json.RawMessage{nil, json.RawMessage("null")} {
		normalized, err := domain.ValidateArtifactInputs("artifacts", []domain.ArtifactInput{{
			Type: domain.ArtifactTypeOther, URI: "ref", Metadata: metadata,
		}})
		if err != nil || normalized[0].Metadata != nil {
			t.Fatalf("absent metadata = %#v, %v", normalized, err)
		}
	}
}

func artifactStringPointer(value string) *string {
	return &value
}
