package domain

import (
	"bytes"
	"encoding/json"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	MaxArtifactsPerAttemptMutation = 20
	MaxArtifactURIRunes            = 4_096
	MaxArtifactMetadataBytes       = 8_192
)

type ArtifactType string

const (
	ArtifactTypeFile        ArtifactType = "file"
	ArtifactTypeDirectory   ArtifactType = "directory"
	ArtifactTypeURL         ArtifactType = "url"
	ArtifactTypeCommit      ArtifactType = "commit"
	ArtifactTypeBranch      ArtifactType = "branch"
	ArtifactTypePullRequest ArtifactType = "pull_request"
	ArtifactTypeLog         ArtifactType = "log"
	ArtifactTypeOther       ArtifactType = "other"
)

func (value ArtifactType) Valid() bool {
	switch value {
	case ArtifactTypeFile, ArtifactTypeDirectory, ArtifactTypeURL, ArtifactTypeCommit,
		ArtifactTypeBranch, ArtifactTypePullRequest, ArtifactTypeLog, ArtifactTypeOther:
		return true
	default:
		return false
	}
}

type ArtifactInput struct {
	Type     ArtifactType
	URI      string
	Title    *string
	Metadata json.RawMessage
}

type Artifact struct {
	ID        string
	IssueID   string
	AttemptID *string
	Type      ArtifactType
	URI       string
	Title     *string
	Metadata  json.RawMessage
	CreatedAt time.Time
}

func ValidateArtifactInputs(field string, values []ArtifactInput) ([]ArtifactInput, error) {
	normalized, err := CopyBounded(field, values, MaxArtifactsPerAttemptMutation)
	if err != nil {
		return nil, err
	}
	for index := range normalized {
		item := &normalized[index]
		itemField := func(name string) string {
			return field + "[" + formatIndex(index) + "]." + name
		}
		if !item.Type.Valid() {
			return nil, validationError(itemField("type"), "INVALID_ENUM", "is invalid")
		}
		if strings.TrimSpace(item.URI) == "" {
			return nil, validationError(itemField("uri"), "REQUIRED", "is required")
		}
		if err := ValidateText(itemField("uri"), item.URI, MaxArtifactURIRunes); err != nil {
			return nil, err
		}
		if err := validateArtifactURI(itemField("uri"), item.Type, item.URI); err != nil {
			return nil, err
		}
		if item.Title != nil {
			if strings.TrimSpace(*item.Title) == "" {
				return nil, validationError(itemField("title"), "REQUIRED", "is required")
			}
			if err := ValidateText(itemField("title"), *item.Title, MaxTitleRunes); err != nil {
				return nil, err
			}
			title := *item.Title
			item.Title = &title
		}
		metadata, err := normalizeArtifactMetadata(itemField("metadata"), item.Metadata)
		if err != nil {
			return nil, err
		}
		item.Metadata = metadata
	}
	return normalized, nil
}

func validateArtifactURI(field string, kind ArtifactType, value string) error {
	switch kind {
	case ArtifactTypeFile, ArtifactTypeDirectory:
		hasWindowsVolume := len(value) >= 2 &&
			((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) &&
			value[1] == ':'
		if strings.HasPrefix(value, "/") || hasWindowsVolume || strings.ContainsRune(value, '\\') || path.Clean(value) == "." {
			return validationError(field, "INVALID_PATH", "must be a project-relative path")
		}
		for _, segment := range strings.Split(value, "/") {
			if segment == ".." {
				return validationError(field, "INVALID_PATH", "must not traverse outside the project root")
			}
		}
	case ArtifactTypeURL:
		parsed, err := url.ParseRequestURI(value)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" ||
			(strings.ToLower(parsed.Scheme) != "http" && strings.ToLower(parsed.Scheme) != "https") ||
			parsed.User != nil {
			return validationError(field, "INVALID_URL", "must be an absolute http or https URL without credentials")
		}
	}
	return nil
}

func normalizeArtifactMetadata(field string, value json.RawMessage) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	if len(value) > MaxArtifactMetadataBytes {
		return nil, NewError(
			CodeLimitExceeded,
			field+" exceeds the maximum size of "+formatIndex(MaxArtifactMetadataBytes)+" bytes",
			false,
			Detail{Field: field, Code: "MAX_BYTES", Message: "maximum " + formatIndex(MaxArtifactMetadataBytes)},
		)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		return nil, validationError(field, "INVALID_JSON", "must contain valid JSON")
	}
	if compact.String() == "null" {
		return nil, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(compact.Bytes(), &object); err != nil {
		return nil, validationError(field, "INVALID_JSON_TYPE", "must be a JSON object")
	}
	return slices.Clone(json.RawMessage(compact.Bytes())), nil
}

func CloneArtifactInput(value ArtifactInput) ArtifactInput {
	result := value
	result.Title = cloneString(value.Title)
	result.Metadata = slices.Clone(value.Metadata)
	return result
}

func CloneArtifact(value Artifact) Artifact {
	result := value
	result.AttemptID = cloneString(value.AttemptID)
	result.Title = cloneString(value.Title)
	result.Metadata = slices.Clone(value.Metadata)
	return result
}

func CloneArtifacts(values []Artifact) []Artifact {
	if values == nil {
		return nil
	}
	result := make([]Artifact, len(values))
	for index, value := range values {
		result[index] = CloneArtifact(value)
	}
	return result
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func formatIndex(value int) string {
	return strconv.Itoa(value)
}
