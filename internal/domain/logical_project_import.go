package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"rhizome-mcp/internal/ids"
)

const (
	// CodeUnsupportedFormatVersion identifies unsupported logical project format or version.
	CodeUnsupportedFormatVersion = "UNSUPPORTED_FORMAT_VERSION"
	// CodeUnsupportedField identifies an unknown JSON object field in a v1 logical project document.
	CodeUnsupportedField = "UNSUPPORTED_FIELD"
	// MaxLogicalProjectImportBytes bounds the maximum import payload size.
	MaxLogicalProjectImportBytes = 1 << 20
)

// LogicalProjectImportPlan is the parsed and validated logical project import plan.
type LogicalProjectImportPlan struct {
	Document LogicalProjectDocument
	DryRun   LogicalProjectImportDryRun
}

// LogicalProjectImportDryRun captures deterministic dry-run state for import validation.
type LogicalProjectImportDryRun struct {
	Counts    LogicalProjectImportCounts     `json:"counts"`
	Conflicts []LogicalProjectImportConflict `json:"conflicts"`
	Writes    LogicalProjectImportWrites     `json:"writes"`
}

// LogicalProjectImportCounts summarizes the import surface by entity type.
type LogicalProjectImportCounts struct {
	Project      int `json:"project"`
	Issues       int `json:"issues"`
	Labels       int `json:"labels"`
	IssueLabels  int `json:"issue_labels"`
	Relations    int `json:"relations"`
	Comments     int `json:"comments"`
	Decisions    int `json:"decisions"`
	Attempts     int `json:"attempts"`
	AttemptNotes int `json:"attempt_notes"`
	Artifacts    int `json:"artifacts"`
	Events       int `json:"events"`
}

// LogicalProjectImportConflict is one deterministic dry-run conflict.
type LogicalProjectImportConflict struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

// LogicalProjectImportWrites summarizes write-side effects for dry-run results.
type LogicalProjectImportWrites struct {
	Count int `json:"count"`
}

// ParseLogicalProjectImportPlan validates a logical project document and returns a deterministic dry run.
func ParseLogicalProjectImportPlan(document []byte) (LogicalProjectImportPlan, error) {
	plan := LogicalProjectImportPlan{}
	if len(bytes.TrimSpace(document)) == 0 {
		return plan, invalidArgumentPath("$.document", "EMPTY_DOCUMENT", "document is required")
	}
	if len(document) > MaxLogicalProjectImportBytes {
		return plan, NewError(
			CodeLimitExceeded,
			"document exceeds the maximum size of 1048576 bytes",
			false,
			Detail{Field: "$.document", Code: "MAX_BYTES", Message: "maximum 1048576"},
		)
	}
	if err := validateLogicalProjectDocumentStructure(document); err != nil {
		return plan, err
	}
	var parsed LogicalProjectDocument
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return plan, decodeError(err, "$.document")
	}
	if err := validateLogicalProjectDocumentSemantics(&parsed); err != nil {
		return plan, err
	}
	plan.Document = parsed
	plan.DryRun = LogicalProjectImportDryRun{
		Counts: LogicalProjectImportCounts{
			Project:      1,
			Issues:       len(parsed.Issues),
			Labels:       len(parsed.Labels),
			IssueLabels:  len(parsed.IssueLabels),
			Relations:    len(parsed.Relations),
			Comments:     len(parsed.Comments),
			Decisions:    len(parsed.Decisions),
			Attempts:     len(parsed.Attempts),
			AttemptNotes: len(parsed.AttemptNotes),
			Artifacts:    len(parsed.Artifacts),
			Events:       len(parsed.Events),
		},
		Conflicts: nil,
		Writes:    LogicalProjectImportWrites{Count: 0},
	}
	return plan, nil
}

func validateLogicalProjectDocumentStructure(document []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	if err := validateTopLevelLogicalProjectDocument(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != nil {
		if err == io.EOF {
			return nil
		}
		return decodeError(err, "$.document")
	}
	return invalidArgumentPath("$.document", "TRAILING_DATA", "contains trailing JSON data")
}

func validateTopLevelLogicalProjectDocument(decoder *json.Decoder) error {
	return validateObject(decoder, "$", map[string]validatorFunc{
		"format":        validateAnyJSONValue,
		"version":       validateAnyJSONValue,
		"exported_at":   validateAnyJSONValue,
		"project":       validateLogicalProjectProjectObject,
		"issues":        validateLogicalIssueArray,
		"labels":        validateLogicalLabelArray,
		"issue_labels":  validateLogicalIssueLabelArray,
		"relations":     validateLogicalRelationArray,
		"comments":      validateLogicalCommentArray,
		"decisions":     validateLogicalDecisionArray,
		"attempts":      validateLogicalAttemptArray,
		"attempt_notes": validateLogicalAttemptNoteArray,
		"artifacts":     validateLogicalArtifactArray,
		"events":        validateLogicalEventArray,
	}, []string{"format", "version", "exported_at", "project", "issues", "labels", "issue_labels", "relations", "comments", "decisions", "attempts", "attempt_notes", "artifacts", "events"})
}

func validateLogicalProjectProjectObject(decoder *json.Decoder, path string) error {
	return validateObject(decoder, path, map[string]validatorFunc{
		"id":           validateAnyJSONValue,
		"name":         validateAnyJSONValue,
		"instructions": validateAnyJSONValue,
		"created_at":   validateAnyJSONValue,
		"updated_at":   validateAnyJSONValue,
	}, []string{"id", "name", "instructions", "created_at", "updated_at"})
}

func validateLogicalIssueArray(decoder *json.Decoder, path string) error {
	return validateArray(decoder, path, func(decoder *json.Decoder, indexPath string) error {
		return validateObject(decoder, indexPath, map[string]validatorFunc{
			"id":                    validateAnyJSONValue,
			"type":                  validateAnyJSONValue,
			"title":                 validateAnyJSONValue,
			"description":           validateAnyJSONValue,
			"acceptance_criteria":   validateAnyJSONValue,
			"status":                validateAnyJSONValue,
			"priority":              validateAnyJSONValue,
			"parent_id":             validateAnyJSONValue,
			"blocked_reason":        validateAnyJSONValue,
			"created_by_session_id": validateAnyJSONValue,
			"created_at":            validateAnyJSONValue,
			"updated_at":            validateAnyJSONValue,
			"closed_at":             validateAnyJSONValue,
		}, []string{"id", "type", "title", "description", "acceptance_criteria", "status", "priority", "parent_id", "blocked_reason", "created_by_session_id", "created_at", "updated_at", "closed_at"})
	})
}

func validateLogicalLabelArray(decoder *json.Decoder, path string) error {
	return validateArray(decoder, path, func(decoder *json.Decoder, indexPath string) error {
		return validateObject(decoder, indexPath, map[string]validatorFunc{
			"id":          validateAnyJSONValue,
			"name":        validateAnyJSONValue,
			"description": validateAnyJSONValue,
			"created_at":  validateAnyJSONValue,
		}, []string{"id", "name", "description", "created_at"})
	})
}

func validateLogicalIssueLabelArray(decoder *json.Decoder, path string) error {
	return validateArray(decoder, path, func(decoder *json.Decoder, indexPath string) error {
		return validateObject(decoder, indexPath, map[string]validatorFunc{
			"issue_id": validateAnyJSONValue,
			"label_id": validateAnyJSONValue,
		}, []string{"issue_id", "label_id"})
	})
}

func validateLogicalRelationArray(decoder *json.Decoder, path string) error {
	return validateArray(decoder, path, func(decoder *json.Decoder, indexPath string) error {
		return validateObject(decoder, indexPath, map[string]validatorFunc{
			"id":                    validateAnyJSONValue,
			"source_issue_id":       validateAnyJSONValue,
			"target_issue_id":       validateAnyJSONValue,
			"type":                  validateAnyJSONValue,
			"created_by_session_id": validateAnyJSONValue,
			"created_at":            validateAnyJSONValue,
		}, []string{"id", "source_issue_id", "target_issue_id", "type", "created_by_session_id", "created_at"})
	})
}

func validateLogicalCommentArray(decoder *json.Decoder, path string) error {
	return validateArray(decoder, path, func(decoder *json.Decoder, indexPath string) error {
		return validateObject(decoder, indexPath, map[string]validatorFunc{
			"id":                    validateAnyJSONValue,
			"issue_id":              validateAnyJSONValue,
			"content":               validateAnyJSONValue,
			"created_by_session_id": validateAnyJSONValue,
			"author_label":          validateAnyJSONValue,
			"created_at":            validateAnyJSONValue,
			"edited_at":             validateAnyJSONValue,
		}, []string{"id", "issue_id", "content", "created_by_session_id", "author_label", "created_at", "edited_at"})
	})
}

func validateLogicalDecisionArray(decoder *json.Decoder, path string) error {
	return validateArray(decoder, path, func(decoder *json.Decoder, indexPath string) error {
		return validateObject(decoder, indexPath, map[string]validatorFunc{
			"id":                    validateAnyJSONValue,
			"issue_id":              validateAnyJSONValue,
			"title":                 validateAnyJSONValue,
			"summary":               validateAnyJSONValue,
			"content":               validateAnyJSONValue,
			"status":                validateAnyJSONValue,
			"supersedes_id":         validateAnyJSONValue,
			"created_by_session_id": validateAnyJSONValue,
			"created_at":            validateAnyJSONValue,
		}, []string{"id", "issue_id", "title", "summary", "content", "status", "supersedes_id", "created_by_session_id", "created_at"})
	})
}

func validateLogicalAttemptArray(decoder *json.Decoder, path string) error {
	return validateArray(decoder, path, func(decoder *json.Decoder, indexPath string) error {
		return validateObject(decoder, indexPath, map[string]validatorFunc{
			"id":                        validateAnyJSONValue,
			"issue_id":                  validateAnyJSONValue,
			"session_id":                validateAnyJSONValue,
			"agent_label":               validateAnyJSONValue,
			"kind":                      validateAnyJSONValue,
			"status":                    validateAnyJSONValue,
			"issue_version_at_start":    validateAnyJSONValue,
			"context_event_id_at_start": validateAnyJSONValue,
			"lease_expires_at":          validateAnyJSONValue,
			"started_at":                validateAnyJSONValue,
			"last_heartbeat_at":         validateAnyJSONValue,
			"finished_at":               validateAnyJSONValue,
			"result_summary":            validateAnyJSONValue,
			"next_steps":                validateAnyJSONValue,
			"verification":              validateAnyJSONValue,
			"failure_reason_code":       validateAnyJSONValue,
			"interruption_reason_code":  validateAnyJSONValue,
			"reason_details":            validateAnyJSONValue,
		}, []string{"id", "issue_id", "session_id", "agent_label", "kind", "status", "issue_version_at_start", "context_event_id_at_start", "lease_expires_at", "started_at", "last_heartbeat_at", "finished_at", "result_summary", "next_steps", "verification", "failure_reason_code", "interruption_reason_code", "reason_details"})
	})
}

func validateLogicalAttemptNoteArray(decoder *json.Decoder, path string) error {
	return validateArray(decoder, path, func(decoder *json.Decoder, indexPath string) error {
		return validateObject(decoder, indexPath, map[string]validatorFunc{
			"id":         validateAnyJSONValue,
			"attempt_id": validateAnyJSONValue,
			"kind":       validateAnyJSONValue,
			"content":    validateAnyJSONValue,
			"next_steps": validateAnyJSONValue,
			"important":  validateAnyJSONValue,
			"created_at": validateAnyJSONValue,
		}, []string{"id", "attempt_id", "kind", "content", "next_steps", "important", "created_at"})
	})
}

func validateLogicalArtifactArray(decoder *json.Decoder, path string) error {
	return validateArray(decoder, path, func(decoder *json.Decoder, indexPath string) error {
		return validateObject(decoder, indexPath, map[string]validatorFunc{
			"id":         validateAnyJSONValue,
			"issue_id":   validateAnyJSONValue,
			"attempt_id": validateAnyJSONValue,
			"type":       validateAnyJSONValue,
			"uri":        validateAnyJSONValue,
			"title":      validateAnyJSONValue,
			"metadata":   validateAnyJSONValue,
			"created_at": validateAnyJSONValue,
		}, []string{"id", "issue_id", "attempt_id", "type", "uri", "title", "metadata", "created_at"})
	})
}

func validateLogicalEventArray(decoder *json.Decoder, path string) error {
	return validateArray(decoder, path, func(decoder *json.Decoder, indexPath string) error {
		return validateObject(decoder, indexPath, map[string]validatorFunc{
			"source_id":  validateAnyJSONValue,
			"issue_id":   validateAnyJSONValue,
			"event_type": validateAnyJSONValue,
			"session_id": validateAnyJSONValue,
			"attempt_id": validateAnyJSONValue,
			"payload":    validateAnyJSONValue,
			"created_at": validateAnyJSONValue,
		}, []string{"source_id", "issue_id", "event_type", "session_id", "attempt_id", "payload", "created_at"})
	})
}

type validatorFunc func(*json.Decoder, string) error

func validateObject(decoder *json.Decoder, path string, validators map[string]validatorFunc, required []string) error {
	token, err := decoder.Token()
	if err != nil {
		return decodeError(err, path)
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return invalidArgumentPath(path, "INVALID_JSON_TYPE", "expected an object")
	}
	seen := make(map[string]struct{}, len(validators))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return decodeError(err, path)
		}
		key, ok := token.(string)
		if !ok {
			return invalidArgumentPath(path, "INVALID_JSON_TYPE", "expected an object key")
		}
		if _, exists := seen[key]; exists {
			return invalidArgumentPath(joinPath(path, key), "DUPLICATE_KEY", "duplicate JSON object key")
		}
		seen[key] = struct{}{}
		validator, found := validators[key]
		if !found {
			return unsupportedFieldError(joinPath(path, key))
		}
		if err := validator(decoder, joinPath(path, key)); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return decodeError(err, path)
	}
	for _, key := range required {
		if _, exists := seen[key]; !exists {
			return invalidArgumentPath(joinPath(path, key), "REQUIRED", "is required")
		}
	}
	return nil
}

func validateArray(decoder *json.Decoder, path string, itemValidator func(*json.Decoder, string) error) error {
	token, err := decoder.Token()
	if err != nil {
		return decodeError(err, path)
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '[' {
		return invalidArgumentPath(path, "INVALID_JSON_TYPE", "expected an array")
	}
	index := 0
	for decoder.More() {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		if err := itemValidator(decoder, itemPath); err != nil {
			return err
		}
		index++
	}
	if _, err := decoder.Token(); err != nil {
		return decodeError(err, path)
	}
	return nil
}

func validateAnyJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return decodeError(err, path)
	}
	switch value := token.(type) {
	case json.Delim:
		if value == '{' {
			return validateArbitraryObject(decoder, path)
		}
		if value == '[' {
			return validateArbitraryArray(decoder, path)
		}
		return invalidArgumentPath(path, "INVALID_JSON_TYPE", "unexpected JSON delimiter")
	case string, bool, float64, nil:
		return nil
	default:
		return invalidArgumentPath(path, "INVALID_JSON_TYPE", "unexpected JSON value")
	}
}

func validateArbitraryObject(decoder *json.Decoder, path string) error {
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return decodeError(err, path)
		}
		key, ok := token.(string)
		if !ok {
			return invalidArgumentPath(path, "INVALID_JSON_TYPE", "expected an object key")
		}
		if _, exists := seen[key]; exists {
			return invalidArgumentPath(joinPath(path, key), "DUPLICATE_KEY", "duplicate JSON object key")
		}
		seen[key] = struct{}{}
		if err := validateAnyJSONValue(decoder, joinPath(path, key)); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return decodeError(err, path)
	}
	return nil
}

func validateArbitraryArray(decoder *json.Decoder, path string) error {
	index := 0
	for decoder.More() {
		if err := validateAnyJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
		index++
	}
	if _, err := decoder.Token(); err != nil {
		return decodeError(err, path)
	}
	return nil
}

func validateLogicalProjectDocumentSemantics(document *LogicalProjectDocument) error {
	if document.Format != "rhizome-logical-project" {
		return unsupportedFormatVersionError("$.format")
	}
	if document.Version != 1 {
		return unsupportedFormatVersionError("$.version")
	}
	if err := requireNonEmptyString("$.exported_at", document.ExportedAt); err != nil {
		return err
	}
	if err := validateUTCTimestamp("$.exported_at", document.ExportedAt); err != nil {
		return err
	}
	if err := requireNonEmptyString("$.project.id", document.Project.ID); err != nil {
		return err
	}
	if err := validateCanonicalULID("$.project.id", document.Project.ID); err != nil {
		return err
	}
	if err := validateUTCTimestamp("$.project.created_at", document.Project.CreatedAt); err != nil {
		return err
	}
	if err := validateUTCTimestamp("$.project.updated_at", document.Project.UpdatedAt); err != nil {
		return err
	}

	seenIDs := make(map[string]string)
	issueIDs := make(map[string]struct{})
	issueTypes := make(map[string]string)
	labelIDs := make(map[string]struct{})
	relationIDs := make(map[string]struct{})
	commentIDs := make(map[string]struct{})
	decisionIDs := make(map[string]struct{})
	attemptIDs := make(map[string]struct{})
	attemptNoteIDs := make(map[string]struct{})
	artifactIDs := make(map[string]struct{})
	for index, issue := range document.Issues {
		path := fmt.Sprintf("$.issues[%d]", index)
		if err := requireNonEmptyString(path+".id", issue.ID); err != nil {
			return err
		}
		if err := validateCanonicalULID(path+".id", issue.ID); err != nil {
			return err
		}
		if _, exists := seenIDs[issue.ID]; exists {
			return invalidArgumentPath(path+".id", "DUPLICATE_ID", "duplicate logical ID")
		}
		seenIDs[issue.ID] = "issue"
		issueIDs[issue.ID] = struct{}{}
		issueTypes[issue.ID] = issue.Type
		if err := requireNonEmptyString(path+".type", issue.Type); err != nil {
			return err
		}
		if _, err := ParseType(issue.Type); err != nil {
			return invalidArgumentPath(path+".type", "INVALID_ENUM", err.Error())
		}
		if err := requireNonEmptyString(path+".title", issue.Title); err != nil {
			return err
		}
		if err := requireNonEmptyString(path+".status", issue.Status); err != nil {
			return err
		}
		if _, err := ParseStatus(issue.Status); err != nil {
			return invalidArgumentPath(path+".status", "INVALID_ENUM", err.Error())
		}
		if err := requireNonEmptyString(path+".priority", issue.Priority); err != nil {
			return err
		}
		if _, err := ParsePriority(issue.Priority); err != nil {
			return invalidArgumentPath(path+".priority", "INVALID_ENUM", err.Error())
		}
		if err := validateUTCTimestamp(path+".created_at", issue.CreatedAt); err != nil {
			return err
		}
		if err := validateUTCTimestamp(path+".updated_at", issue.UpdatedAt); err != nil {
			return err
		}
		if issue.ParentID != nil {
			if err := validateNullableCanonicalULID(path+".parent_id", *issue.ParentID); err != nil {
				return err
			}
		}
		if issue.Status == "blocked" && (issue.BlockedReason == nil || strings.TrimSpace(*issue.BlockedReason) == "") {
			return invalidArgumentPath(path+".blocked_reason", "REQUIRED", "required when status is blocked")
		}
		if issue.Type == "epic" && issue.ParentID != nil {
			return invalidArgumentPath(path+".parent_id", "INVALID_PARENT", "epics cannot have a parent")
		}
		if issue.CreatedBySessionID != nil {
			return unsupportedSessionReferenceError(path + ".created_by_session_id")
		}
		if issue.ClosedAt != nil {
			if err := validateNullableUTCTimestamp(path+".closed_at", *issue.ClosedAt); err != nil {
				return err
			}
		}
	}

	for index, issue := range document.Issues {
		path := fmt.Sprintf("$.issues[%d]", index)
		if issue.ParentID != nil {
			if issue.Type == "epic" {
				return invalidArgumentPath(path+".parent_id", "INVALID_PARENT", "epics cannot have a parent")
			}
			if issue.Type != "task" && issue.Type != "bug" {
				return invalidArgumentPath(path+".parent_id", "INVALID_PARENT", "issues with a parent must be task or bug")
			}
			parentID := *issue.ParentID
			if err := validateNullableCanonicalULID(path+".parent_id", parentID); err != nil {
				return err
			}
			if err := validateReference(path+".parent_id", parentID, issueIDs, "issue"); err != nil {
				return err
			}
			parentType := issueTypes[parentID]
			if parentType != "epic" {
				return invalidArgumentPath(path+".parent_id", "INVALID_PARENT", "parent issue must be an epic")
			}
		}
	}

	labelNames := make(map[string]struct{})
	for index, label := range document.Labels {
		path := fmt.Sprintf("$.labels[%d]", index)
		if err := requireNonEmptyString(path+".id", label.ID); err != nil {
			return err
		}
		if err := validateCanonicalULID(path+".id", label.ID); err != nil {
			return err
		}
		if _, exists := seenIDs[label.ID]; exists {
			return invalidArgumentPath(path+".id", "DUPLICATE_ID", "duplicate logical ID")
		}
		seenIDs[label.ID] = "label"
		labelIDs[label.ID] = struct{}{}
		if err := requireNonEmptyString(path+".name", label.Name); err != nil {
			return err
		}
		nameKey := strings.ToLower(label.Name)
		if _, exists := labelNames[nameKey]; exists {
			return invalidArgumentPath(path+".name", "DUPLICATE_LABEL_NAME", "label names must be unique case-insensitively")
		}
		labelNames[nameKey] = struct{}{}
		if err := validateUTCTimestamp(path+".created_at", label.CreatedAt); err != nil {
			return err
		}
	}

	seenIssueLabels := make(map[string]struct{})
	for index, link := range document.IssueLabels {
		path := fmt.Sprintf("$.issue_labels[%d]", index)
		if err := validateReference(path+".issue_id", link.IssueID, issueIDs, "issue"); err != nil {
			return err
		}
		if err := validateReference(path+".label_id", link.LabelID, labelIDs, "label"); err != nil {
			return err
		}
		key := link.IssueID + "\x00" + link.LabelID
		if _, exists := seenIssueLabels[key]; exists {
			return invalidArgumentPath(path, "DUPLICATE_ISSUE_LABEL", "issue-label tuples must be unique")
		}
		seenIssueLabels[key] = struct{}{}
	}

	seenRelationKeys := make(map[string]struct{})
	for index, relation := range document.Relations {
		path := fmt.Sprintf("$.relations[%d]", index)
		if err := requireNonEmptyString(path+".id", relation.ID); err != nil {
			return err
		}
		if err := validateCanonicalULID(path+".id", relation.ID); err != nil {
			return err
		}
		if _, exists := seenIDs[relation.ID]; exists {
			return invalidArgumentPath(path+".id", "DUPLICATE_ID", "duplicate logical ID")
		}
		seenIDs[relation.ID] = "relation"
		relationIDs[relation.ID] = struct{}{}
		if err := validateReference(path+".source_issue_id", relation.SourceIssueID, issueIDs, "issue"); err != nil {
			return err
		}
		if err := validateReference(path+".target_issue_id", relation.TargetIssueID, issueIDs, "issue"); err != nil {
			return err
		}
		if relation.SourceIssueID == relation.TargetIssueID {
			return invalidArgumentPath(path+".target_issue_id", "SELF_RELATION", "self-relations are not allowed")
		}
		if _, err := ParseRelationType(relation.Type); err != nil {
			return invalidArgumentPath(path+".type", "INVALID_ENUM", err.Error())
		}
		if relation.Type == "related_to" && strings.Compare(relation.SourceIssueID, relation.TargetIssueID) >= 0 {
			return invalidArgumentPath(path+".target_issue_id", "NONCANONICAL_RELATION", "related_to relations must be ordered source_id < target_id")
		}
		key := relation.Type + ":" + relation.SourceIssueID + ":" + relation.TargetIssueID
		if _, exists := seenRelationKeys[key]; exists {
			return invalidArgumentPath(path, "DUPLICATE_RELATION", "relation identity must be unique")
		}
		seenRelationKeys[key] = struct{}{}
		if err := validateUTCTimestamp(path+".created_at", relation.CreatedAt); err != nil {
			return err
		}
		if relation.CreatedBySessionID != nil {
			return unsupportedSessionReferenceError(path + ".created_by_session_id")
		}
	}

	for index, comment := range document.Comments {
		path := fmt.Sprintf("$.comments[%d]", index)
		if err := requireNonEmptyString(path+".id", comment.ID); err != nil {
			return err
		}
		if err := validateCanonicalULID(path+".id", comment.ID); err != nil {
			return err
		}
		if _, exists := seenIDs[comment.ID]; exists {
			return invalidArgumentPath(path+".id", "DUPLICATE_ID", "duplicate logical ID")
		}
		seenIDs[comment.ID] = "comment"
		commentIDs[comment.ID] = struct{}{}
		if err := validateReference(path+".issue_id", comment.IssueID, issueIDs, "issue"); err != nil {
			return err
		}
		if err := requireNonEmptyString(path+".content", comment.Content); err != nil {
			return err
		}
		if err := validateUTCTimestamp(path+".created_at", comment.CreatedAt); err != nil {
			return err
		}
		if comment.EditedAt != nil {
			if err := validateNullableUTCTimestamp(path+".edited_at", *comment.EditedAt); err != nil {
				return err
			}
		}
		if comment.CreatedBySessionID != nil {
			return unsupportedSessionReferenceError(path + ".created_by_session_id")
		}
	}

	for index, decision := range document.Decisions {
		path := fmt.Sprintf("$.decisions[%d]", index)
		if err := requireNonEmptyString(path+".id", decision.ID); err != nil {
			return err
		}
		if err := validateCanonicalULID(path+".id", decision.ID); err != nil {
			return err
		}
		if _, exists := seenIDs[decision.ID]; exists {
			return invalidArgumentPath(path+".id", "DUPLICATE_ID", "duplicate logical ID")
		}
		seenIDs[decision.ID] = "decision"
		decisionIDs[decision.ID] = struct{}{}
		if decision.IssueID != nil {
			if err := validateNullableCanonicalULID(path+".issue_id", *decision.IssueID); err != nil {
				return err
			}
			if err := validateReference(path+".issue_id", *decision.IssueID, issueIDs, "issue"); err != nil {
				return err
			}
		}
		if err := requireNonEmptyString(path+".title", decision.Title); err != nil {
			return err
		}
		if err := requireNonEmptyString(path+".summary", decision.Summary); err != nil {
			return err
		}
		if err := requireNonEmptyString(path+".content", decision.Content); err != nil {
			return err
		}
		if !DecisionStatus(decision.Status).Valid() {
			return invalidArgumentPath(path+".status", "INVALID_ENUM", "unsupported decision status")
		}
		if decision.SupersedesID != nil {
			if err := validateNullableCanonicalULID(path+".supersedes_id", *decision.SupersedesID); err != nil {
				return err
			}
			if err := validateDecisionSupersedesReference(path+".supersedes_id", *decision.SupersedesID, decision, document.Decisions); err != nil {
				return err
			}
		}
		if err := validateUTCTimestamp(path+".created_at", decision.CreatedAt); err != nil {
			return err
		}
		if decision.CreatedBySessionID != nil {
			return unsupportedSessionReferenceError(path + ".created_by_session_id")
		}
	}

	for index, attempt := range document.Attempts {
		path := fmt.Sprintf("$.attempts[%d]", index)
		if err := requireNonEmptyString(path+".id", attempt.ID); err != nil {
			return err
		}
		if err := validateCanonicalULID(path+".id", attempt.ID); err != nil {
			return err
		}
		if _, exists := seenIDs[attempt.ID]; exists {
			return invalidArgumentPath(path+".id", "DUPLICATE_ID", "duplicate logical ID")
		}
		seenIDs[attempt.ID] = "attempt"
		attemptIDs[attempt.ID] = struct{}{}
		if err := validateReference(path+".issue_id", attempt.IssueID, issueIDs, "issue"); err != nil {
			return err
		}
		if attempt.Status == "active" {
			return invalidArgumentPath(path+".status", "UNSUPPORTED_ACTIVE_ATTEMPT", "active attempts are not supported")
		}
		if !AttemptKind(attempt.Kind).Valid() {
			return invalidArgumentPath(path+".kind", "INVALID_ENUM", "unsupported attempt kind")
		}
		if !AttemptStatus(attempt.Status).Valid() {
			return invalidArgumentPath(path+".status", "INVALID_ENUM", "unsupported attempt status")
		}
		if err := validateUTCTimestamp(path+".lease_expires_at", attempt.LeaseExpiresAt); err != nil {
			return err
		}
		if err := validateUTCTimestamp(path+".started_at", attempt.StartedAt); err != nil {
			return err
		}
		if err := validateUTCTimestamp(path+".last_heartbeat_at", attempt.LastHeartbeatAt); err != nil {
			return err
		}
		if attempt.FinishedAt != nil {
			if err := validateNullableUTCTimestamp(path+".finished_at", *attempt.FinishedAt); err != nil {
				return err
			}
		}
		if attempt.SessionID != nil {
			return unsupportedSessionReferenceError(path + ".session_id")
		}
		if attempt.AgentLabel != nil {
			if err := validateNullableNonEmptyString(path+".agent_label", *attempt.AgentLabel); err != nil {
				return err
			}
		}
	}

	for index, note := range document.AttemptNotes {
		path := fmt.Sprintf("$.attempt_notes[%d]", index)
		if err := requireNonEmptyString(path+".id", note.ID); err != nil {
			return err
		}
		if err := validateCanonicalULID(path+".id", note.ID); err != nil {
			return err
		}
		if _, exists := seenIDs[note.ID]; exists {
			return invalidArgumentPath(path+".id", "DUPLICATE_ID", "duplicate logical ID")
		}
		seenIDs[note.ID] = "attempt_note"
		attemptNoteIDs[note.ID] = struct{}{}
		if err := validateReference(path+".attempt_id", note.AttemptID, attemptIDs, "attempt"); err != nil {
			return err
		}
		if err := validateUTCTimestamp(path+".created_at", note.CreatedAt); err != nil {
			return err
		}
	}

	for index, artifact := range document.Artifacts {
		path := fmt.Sprintf("$.artifacts[%d]", index)
		if err := requireNonEmptyString(path+".id", artifact.ID); err != nil {
			return err
		}
		if err := validateCanonicalULID(path+".id", artifact.ID); err != nil {
			return err
		}
		if _, exists := seenIDs[artifact.ID]; exists {
			return invalidArgumentPath(path+".id", "DUPLICATE_ID", "duplicate logical ID")
		}
		seenIDs[artifact.ID] = "artifact"
		artifactIDs[artifact.ID] = struct{}{}
		if err := validateReference(path+".issue_id", artifact.IssueID, issueIDs, "issue"); err != nil {
			return err
		}
		if artifact.AttemptID != nil {
			if err := validateNullableCanonicalULID(path+".attempt_id", *artifact.AttemptID); err != nil {
				return err
			}
			if err := validateReference(path+".attempt_id", *artifact.AttemptID, attemptIDs, "attempt"); err != nil {
				return err
			}
		}
		if err := requireNonEmptyString(path+".type", artifact.Type); err != nil {
			return err
		}
		if !ArtifactType(artifact.Type).Valid() {
			return invalidArgumentPath(path+".type", "INVALID_ENUM", "unsupported artifact type")
		}
		if err := requireNonEmptyString(path+".uri", artifact.URI); err != nil {
			return err
		}
		if err := validateUTCTimestamp(path+".created_at", artifact.CreatedAt); err != nil {
			return err
		}
		if err := validateArtifactURI(path+".uri", ArtifactType(artifact.Type), artifact.URI); err != nil {
			return err
		}
		if err := validateArtifactMetadata(path+".metadata", artifact.Metadata); err != nil {
			return err
		}
	}

	for index, event := range document.Events {
		path := fmt.Sprintf("$.events[%d]", index)
		if err := requireNonEmptyString(path+".event_type", event.EventType); err != nil {
			return err
		}
		if err := validateUTCTimestamp(path+".created_at", event.CreatedAt); err != nil {
			return err
		}
		if event.IssueID != nil {
			if err := validateNullableCanonicalULID(path+".issue_id", *event.IssueID); err != nil {
				return err
			}
			if err := validateReference(path+".issue_id", *event.IssueID, issueIDs, "issue"); err != nil {
				return err
			}
		}
		if event.AttemptID != nil {
			if err := validateNullableCanonicalULID(path+".attempt_id", *event.AttemptID); err != nil {
				return err
			}
			if err := validateReference(path+".attempt_id", *event.AttemptID, attemptIDs, "attempt"); err != nil {
				return err
			}
		}
		if event.SessionID != nil {
			return unsupportedSessionReferenceError(path + ".session_id")
		}
		if !isValidEventPayload(event.Payload) {
			return invalidArgumentPath(path+".payload", "INVALID_JSON", "payload must be valid JSON")
		}
	}

	if err := validateBlocksAcyclicity(document.Relations); err != nil {
		return err
	}
	return nil
}

func validateDecisionSupersedesReference(path, target string, current LogicalDecision, decisions []LogicalDecision) error {
	for _, candidate := range decisions {
		if candidate.ID != target {
			continue
		}
		if current.IssueID == nil && candidate.IssueID == nil {
			return nil
		}
		if current.IssueID != nil && candidate.IssueID != nil && *current.IssueID == *candidate.IssueID {
			return nil
		}
		break
	}
	return invalidArgumentPath(path, "INVALID_REFERENCE", "supersedes_id must reference an included decision in the same issue scope")
}

func validateBlocksAcyclicity(relations []LogicalRelation) error {
	adjacency := make(map[string][]string)
	for _, relation := range relations {
		if relation.Type != "blocks" {
			continue
		}
		adjacency[relation.SourceIssueID] = append(adjacency[relation.SourceIssueID], relation.TargetIssueID)
	}
	visited := make(map[string]bool)
	stack := make(map[string]bool)
	var walk func(string) error
	walk = func(node string) error {
		if stack[node] {
			return invalidArgumentPath("$.relations", "BLOCKS_CYCLE", "blocks relation graph must be acyclic")
		}
		if visited[node] {
			return nil
		}
		visited[node] = true
		stack[node] = true
		for _, next := range adjacency[node] {
			if err := walk(next); err != nil {
				return err
			}
		}
		stack[node] = false
		return nil
	}
	for node := range adjacency {
		if err := walk(node); err != nil {
			return err
		}
	}
	return nil
}

func validateReference(path, target string, known map[string]struct{}, entity string) error {
	if _, exists := known[target]; !exists {
		return invalidArgumentPath(path, "INVALID_REFERENCE", "reference does not resolve to an included "+entity)
	}
	return nil
}

func validateArtifactMetadata(path string, metadata json.RawMessage) error {
	if metadata == nil {
		return nil
	}
	if len(metadata) > 8_192 {
		return invalidArgumentPath(path, "MAX_BYTES", "artifact metadata exceeds the maximum size")
	}
	if !json.Valid(metadata) {
		return invalidArgumentPath(path, "INVALID_JSON", "artifact metadata must be valid JSON")
	}
	var value any
	if err := json.Unmarshal(metadata, &value); err != nil {
		return invalidArgumentPath(path, "INVALID_JSON", "artifact metadata must be valid JSON")
	}
	if value == nil {
		return nil
	}
	if _, ok := value.(map[string]any); !ok {
		return invalidArgumentPath(path, "INVALID_JSON_TYPE", "artifact metadata must be a JSON object")
	}
	return nil
}

func isValidEventPayload(payload json.RawMessage) bool {
	if len(payload) == 0 {
		return false
	}
	return json.Valid(payload)
}

func validateCanonicalULID(path, value string) error {
	if _, err := ids.ParseStrict(value); err != nil {
		return invalidArgumentPath(path, "INVALID_ULID", "must be a canonical ULID")
	}
	return nil
}

func validateNullableCanonicalULID(path, value string) error {
	if strings.TrimSpace(value) == "" {
		return invalidArgumentPath(path, "REQUIRED", "cannot be blank")
	}
	return validateCanonicalULID(path, value)
}

func validateUTCTimestamp(path, value string) error {
	if err := requireNonEmptyString(path, value); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return invalidArgumentPath(path, "INVALID_TIMESTAMP", "must be a valid RFC3339 UTC timestamp")
	}
	if parsed.Location() != time.UTC {
		return invalidArgumentPath(path, "INVALID_TIMESTAMP", "must be a UTC timestamp")
	}
	return nil
}

func validateNullableUTCTimestamp(path, value string) error {
	if strings.TrimSpace(value) == "" {
		return invalidArgumentPath(path, "REQUIRED", "cannot be blank")
	}
	return validateUTCTimestamp(path, value)
}

func requireNonEmptyString(path, value string) error {
	if strings.TrimSpace(value) == "" {
		return invalidArgumentPath(path, "REQUIRED", "is required")
	}
	return nil
}

func validateNullableNonEmptyString(path, value string) error {
	if strings.TrimSpace(value) == "" {
		return invalidArgumentPath(path, "REQUIRED", "cannot be blank")
	}
	return nil
}

func unsupportedFormatVersionError(path string) *Error {
	return NewError(
		CodeUnsupportedFormatVersion,
		"unsupported format or version",
		false,
		Detail{Field: path, Code: "UNSUPPORTED_FORMAT_VERSION"},
	)
}

func unsupportedFieldError(path string) *Error {
	return NewError(
		CodeUnsupportedField,
		"unsupported field",
		false,
		Detail{Field: path, Code: "UNSUPPORTED_FIELD"},
	)
}

func invalidArgumentPath(path, code, message string) *Error {
	return NewError(
		CodeInvalidArgument,
		message,
		false,
		Detail{Field: path, Code: code},
	)
}

func unsupportedSessionReferenceError(path string) *Error {
	return invalidArgumentPath(path, "UNSUPPORTED_SESSION_REFERENCE", "must be null")
}

func decodeError(err error, path string) *Error {
	if err == nil {
		return nil
	}
	return NewError(
		CodeInvalidArgument,
		"malformed JSON document",
		false,
		Detail{Field: path, Code: "INVALID_JSON", Message: err.Error()},
	)
}

func joinPath(base, field string) string {
	if base == "$" {
		return "$" + "." + field
	}
	return base + "." + field
}

func sortConflicts(conflicts []LogicalProjectImportConflict) {
	sort.SliceStable(conflicts, func(i, j int) bool {
		if conflicts[i].Code != conflicts[j].Code {
			return conflicts[i].Code < conflicts[j].Code
		}
		if conflicts[i].Field != conflicts[j].Field {
			return conflicts[i].Field < conflicts[j].Field
		}
		return conflicts[i].Message < conflicts[j].Message
	})
}

// AddDestinationConflicts appends deterministic import conflicts for a nonempty destination.
func AddDestinationConflicts(plan *LogicalProjectImportPlan, conflictCode, message, field string) {
	plan.DryRun.Conflicts = append(plan.DryRun.Conflicts, LogicalProjectImportConflict{Code: conflictCode, Message: message, Field: field})
	sortConflicts(plan.DryRun.Conflicts)
}
