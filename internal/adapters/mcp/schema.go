package mcp

import (
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/domain"
)

func tool(name, description string, input, output *jsonschema.Schema) *sdkmcp.Tool {
	return &sdkmcp.Tool{Name: name, Description: description, InputSchema: input, OutputSchema: output}
}

func object(properties map[string]*jsonschema.Schema, required ...string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object", Properties: properties, Required: required,
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
	}
}

func stringSchema() *jsonschema.Schema { return &jsonschema.Schema{Type: "string"} }
func issueIdentifierSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Pattern:     "^(?:[0-9A-HJKMNP-TV-Z]{26}|ISSUE-[1-9][0-9]*)$",
		Description: "Canonical issue identifier (ULID or ISSUE-N).",
	}
}
func nullableIssueIdentifierSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Types:       []string{"string", "null"},
		Pattern:     "^(?:[0-9A-HJKMNP-TV-Z]{26}|ISSUE-[1-9][0-9]*)$",
		Description: "Canonical issue identifier (ULID or ISSUE-N).",
	}
}
func nullableStringSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Types: []string{"string", "null"}}
}
func nullableBoundedStringSchema(maximum int) *jsonschema.Schema {
	return &jsonschema.Schema{Types: []string{"string", "null"}, MaxLength: &maximum}
}
func integerSchema() *jsonschema.Schema { return &jsonschema.Schema{Type: "integer"} }
func boundedIntegerSchema(minimum, maximum int) *jsonschema.Schema {
	min, max := float64(minimum), float64(maximum)
	return &jsonschema.Schema{Type: "integer", Minimum: &min, Maximum: &max}
}
func booleanSchema() *jsonschema.Schema { return &jsonschema.Schema{Type: "boolean"} }
func stringsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "array", Items: stringSchema()}
}
func boundedStringSchema(maximum int) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", MaxLength: &maximum}
}
func boundedStringsSchema(maximum, itemMaximum int) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "array", Items: boundedStringSchema(itemMaximum), MaxItems: &maximum}
}
func enumSchema(values ...string) *jsonschema.Schema {
	enum := make([]any, len(values))
	for index, value := range values {
		enum[index] = value
	}
	return &jsonschema.Schema{Type: "string", Enum: enum}
}

func schemaGetProject() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{"include_instructions": booleanSchema()})
}

func schemaListLabels() *jsonschema.Schema {
	limit := boundedIntegerSchema(0, 100)
	limit.Description = "0 uses the default limit of 50; maximum is 100."
	return object(map[string]*jsonschema.Schema{
		"query": nullableStringSchema(), "limit": limit, "cursor": nullableStringSchema(),
	})
}

func schemaCreateIssue() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"type": stringSchema(), "title": stringSchema(), "description": nullableStringSchema(),
		"acceptance_criteria": nullableStringSchema(), "status": stringSchema(), "priority": stringSchema(),
		"parent_issue_id": nullableIssueIdentifierSchema(), "blocked_reason": nullableStringSchema(),
		"labels": stringsSchema(), "create_missing_labels": booleanSchema(),
		"idempotency_key": nullableBoundedStringSchema(128),
	}, "type", "title")
}

func schemaUpdateIssue() *jsonschema.Schema {
	changes := object(map[string]*jsonschema.Schema{
		"title": stringSchema(), "description": nullableStringSchema(), "acceptance_criteria": nullableStringSchema(),
		"type": stringSchema(), "priority": stringSchema(), "status": stringSchema(),
		"parent_issue_id": nullableStringSchema(), "blocked_reason": nullableStringSchema(),
		"labels": stringsSchema(),
	})
	return object(map[string]*jsonschema.Schema{
		"issue_id": issueIdentifierSchema(), "expected_version": integerSchema(), "changes": changes,
		"create_missing_labels": booleanSchema(), "idempotency_key": nullableStringSchema(),
	}, "issue_id", "expected_version", "changes")
}

func schemaGetIssue() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"issue_id": issueIdentifierSchema(), "view": stringSchema(),
	}, "issue_id")
}

func schemaGetIssueActivity() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"issue_id": issueIdentifierSchema(),
		"types":    &jsonschema.Schema{Type: "array", Items: enumSchema("comments", "decisions", "attempts", "attempt_notes", "events", "artifacts"), MaxItems: intPointer(6), UniqueItems: true},
		"limit":    boundedIntegerSchema(0, 100),
		"cursor":   nullableBoundedStringSchema(4096),
		"order":    enumSchema("newest_first"),
	}, "issue_id")
}

func schemaSearch() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"query":            boundedStringSchema(domain.MaxSearchQueryRunes),
		"entity_types":     &jsonschema.Schema{Type: "array", Items: enumSchema("issue", "comment", "decision", "attempt_note"), MaxItems: intPointer(4), UniqueItems: true},
		"issue_id":         nullableIssueIdentifierSchema(),
		"epic_id":          nullableIssueIdentifierSchema(),
		"statuses":         &jsonschema.Schema{Type: "array", Items: enumSchema("open", "ready", "blocked", "review", "done", "cancelled"), MaxItems: intPointer(6), UniqueItems: true},
		"labels":           boundedStringsSchema(domain.MaxLabelsPerIssue, domain.MaxLabelNameRunes),
		"include_archived": booleanSchema(),
		"limit":            boundedIntegerSchema(0, domain.MaxSearchResults),
		"cursor":           nullableBoundedStringSchema(4096),
		"snippet_length":   boundedIntegerSchema(0, domain.MaxSearchSnippetRunes),
	}, "query")
}

func schemaGetChanges() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"since_event_id": boundedIntegerSchema(0, 9_223_372_036_854_775_807),
		"issue_id":       nullableIssueIdentifierSchema(),
		"event_types":    boundedStringsSchema(domain.MaxChangeEventTypes, domain.MaxEventTypeRunes),
		"limit":          boundedIntegerSchema(0, 200),
	}, "since_event_id")
}

func schemaGetWorkContext() *jsonschema.Schema {
	includeValues := make([]string, len(domain.AllWorkContextIncludes))
	for index, include := range domain.AllWorkContextIncludes {
		includeValues[index] = string(include)
	}
	return object(map[string]*jsonschema.Schema{
		"issue_id": issueIdentifierSchema(),
		"include":  &jsonschema.Schema{Type: "array", Items: enumSchema(includeValues...), MaxItems: intPointer(10), UniqueItems: true},
		"limits":   schemaWorkContextLimits(),
	}, "issue_id")
}

func schemaWorkContextLimits() *jsonschema.Schema {
	relatedIssueSummaries := boundedIntegerSchema(1, 20)
	relatedIssueSummaries.Description = "Applies when include contains related_issue_summaries."
	recentComments := boundedIntegerSchema(1, 20)
	recentComments.Description = "Applies when include contains recent_comments."
	recentAttemptNotes := boundedIntegerSchema(1, 20)
	recentAttemptNotes.Description = "Applies when include contains recent_attempt_notes."
	decisionContent := boundedIntegerSchema(1, 20)
	decisionContent.Description = "Applies when include contains decision_content."
	attemptHistory := boundedIntegerSchema(1, 20)
	attemptHistory.Description = "Applies when include contains attempt_history."
	artifacts := boundedIntegerSchema(1, 20)
	artifacts.Description = "Applies when include contains artifacts."
	changesSincePreviousAttempt := boundedIntegerSchema(1, 20)
	changesSincePreviousAttempt.Description = "Applies when include contains changes_since_previous_attempt."
	return object(map[string]*jsonschema.Schema{
		"related_issue_summaries":        relatedIssueSummaries,
		"recent_comments":                recentComments,
		"recent_attempt_notes":           recentAttemptNotes,
		"decision_content":               decisionContent,
		"attempt_history":                attemptHistory,
		"artifacts":                      artifacts,
		"changes_since_previous_attempt": changesSincePreviousAttempt,
	})
}

func schemaListIssues() *jsonschema.Schema {
	limit := boundedIntegerSchema(0, 100)
	limit.Description = "0 uses the default limit of 20; maximum is 100."
	return object(map[string]*jsonschema.Schema{
		"types": stringsSchema(), "statuses": stringsSchema(), "effective_statuses": stringsSchema(),
		"priorities": stringsSchema(), "labels": stringsSchema(), "parent_issue_id": nullableIssueIdentifierSchema(),
		"is_blocked":       &jsonschema.Schema{Types: []string{"boolean", "null"}},
		"is_claimable":     &jsonschema.Schema{Types: []string{"boolean", "null"}},
		"include_archived": booleanSchema(), "limit": limit, "cursor": nullableStringSchema(), "view": stringSchema(),
	})
}

func schemaArchiveIssue() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"issue_id": issueIdentifierSchema(), "expected_version": integerSchema(), "idempotency_key": nullableStringSchema(),
	}, "issue_id", "expected_version")
}

func schemaAddComment() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"issue_id": issueIdentifierSchema(), "content": boundedStringSchema(50_000),
		"idempotency_key": nullableBoundedStringSchema(128),
	}, "issue_id", "content")
}

func schemaRecordDecision() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"issue_id":        nullableBoundedStringSchema(64),
		"title":           boundedStringSchema(300),
		"summary":         boundedStringSchema(2_000),
		"content":         boundedStringSchema(100_000),
		"status":          enumSchema("active", "superseded", "rejected"),
		"supersedes_id":   nullableBoundedStringSchema(26),
		"idempotency_key": nullableBoundedStringSchema(128),
	}, "title", "summary", "content")
}

func schemaListDecisions() *jsonschema.Schema {
	limit := boundedIntegerSchema(0, 100)
	limit.Description = "0 uses the default limit of 20; maximum is 100."
	return object(map[string]*jsonschema.Schema{
		"issue_id": issueIdentifierSchema(),
		"limit":    limit,
		"cursor":   nullableBoundedStringSchema(4096),
	})
}

func schemaManageIssueRelation() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"action":          enumSchema("add", "remove"),
		"source_issue_id": issueIdentifierSchema(),
		"target_issue_id": issueIdentifierSchema(),
		"relation_type":   enumSchema("blocks", "related_to", "duplicates"),
		"idempotency_key": nullableStringSchema(),
	}, "action", "source_issue_id", "target_issue_id", "relation_type")
}

func schemaGetIssueGraph() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"root_issue_id": issueIdentifierSchema(), "depth": boundedIntegerSchema(0, 5),
		"direction":         enumSchema("outgoing", "incoming", "both"),
		"relation_types":    &jsonschema.Schema{Type: "array", Items: enumSchema("blocks", "related_to", "duplicates"), UniqueItems: true},
		"include_hierarchy": booleanSchema(), "include_terminal": booleanSchema(),
		"max_nodes": boundedIntegerSchema(1, 500), "view": enumSchema("compact"),
	}, "root_issue_id")
}

func schemaGetPlanningGraph() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"root_issue_id": nullableIssueIdentifierSchema(), "depth": boundedIntegerSchema(0, 5), "max_nodes": boundedIntegerSchema(1, 500),
		"include_review": booleanSchema(), "include_related": booleanSchema(),
	})
}

func schemaPlanIssue() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"ref": boundedStringSchema(64), "type": enumSchema("epic", "task", "bug"), "title": boundedStringSchema(300),
		"description": nullableBoundedStringSchema(100000), "acceptance_criteria": nullableBoundedStringSchema(50000),
		"status":   enumSchema("open", "ready", "blocked", "review", "done", "cancelled"),
		"priority": enumSchema("low", "medium", "high", "critical"), "parent_ref": nullableBoundedStringSchema(64),
		"blocked_reason": nullableBoundedStringSchema(100000), "labels": boundedStringsSchema(50, 64), "create_missing_labels": booleanSchema(),
	}, "type", "title")
}
func schemaPlanRelation() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"source_ref": boundedStringSchema(64), "target_ref": boundedStringSchema(64),
		"type": enumSchema("blocks", "related_to", "duplicates"),
	}, "source_ref", "target_ref", "type")
}
func schemaPlanDecision() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"issue_ref": nullableBoundedStringSchema(64), "title": boundedStringSchema(300), "summary": boundedStringSchema(2000),
		"content": boundedStringSchema(100000), "status": enumSchema("active", "superseded", "rejected"),
	}, "title", "summary", "content")
}
func schemaPlanFields(properties map[string]*jsonschema.Schema) {
	properties["issues"] = &jsonschema.Schema{Type: "array", Items: schemaPlanIssue(), MaxItems: intPointer(50)}
	properties["relations"] = &jsonschema.Schema{Type: "array", Items: schemaPlanRelation(), MaxItems: intPointer(100)}
	properties["decisions"] = &jsonschema.Schema{Type: "array", Items: schemaPlanDecision(), MaxItems: intPointer(20)}
}
func intPointer(value int) *int { return &value }
func schemaValidateIssuePlan() *jsonschema.Schema {
	properties := map[string]*jsonschema.Schema{}
	schemaPlanFields(properties)
	return object(properties, "issues", "relations", "decisions")
}
func schemaApplyIssuePlan() *jsonschema.Schema {
	properties := map[string]*jsonschema.Schema{"idempotency_key": boundedStringSchema(128)}
	schemaPlanFields(properties)
	return object(properties, "issues", "relations", "decisions", "idempotency_key")
}

func schemaClaimIssue() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"issue_id": issueIdentifierSchema(), "lease_seconds": boundedIntegerSchema(60, 3600),
		"idempotency_key": nullableBoundedStringSchema(128),
	}, "issue_id")
}

func schemaRenewAttempt() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"attempt_id": boundedStringSchema(26), "lease_token": boundedStringSchema(512),
		"lease_seconds": boundedIntegerSchema(60, 3600),
	}, "attempt_id", "lease_token")
}

func schemaArtifact() *jsonschema.Schema {
	metadata := &jsonschema.Schema{Type: "object"}
	return object(map[string]*jsonschema.Schema{
		"type":     enumSchema("file", "directory", "url", "commit", "branch", "pull_request", "log", "other"),
		"uri":      boundedStringSchema(4_096),
		"title":    nullableBoundedStringSchema(300),
		"metadata": &jsonschema.Schema{OneOf: []*jsonschema.Schema{metadata, &jsonschema.Schema{Type: "null"}}},
	}, "type", "uri")
}

func schemaArtifacts() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "array", Items: schemaArtifact(), MaxItems: intPointer(20)}
}

func schemaSaveAttemptNote() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"attempt_id":      boundedStringSchema(26),
		"lease_token":     boundedStringSchema(512),
		"kind":            enumSchema("progress", "finding", "warning", "checkpoint"),
		"content":         boundedStringSchema(50_000),
		"next_steps":      boundedStringsSchema(20, 1_000),
		"important":       booleanSchema(),
		"artifacts":       schemaArtifacts(),
		"idempotency_key": nullableBoundedStringSchema(128),
	}, "attempt_id", "lease_token", "kind", "content")
}

func schemaFinishAttempt() *jsonschema.Schema {
	acknowledgement := object(map[string]*jsonschema.Schema{
		"issue_version":   boundedIntegerSchema(1, 9_223_372_036_854_775_807),
		"latest_event_id": boundedIntegerSchema(0, 9_223_372_036_854_775_807),
	}, "issue_version", "latest_event_id")
	return object(map[string]*jsonschema.Schema{
		"attempt_id": boundedStringSchema(26), "lease_token": boundedStringSchema(512),
		"outcome":        enumSchema("completed", "failed", "interrupted"),
		"result_summary": boundedStringSchema(50_000),
		"next_steps":     boundedStringsSchema(20, 1_000), "verification": boundedStringsSchema(20, 1_000),
		"target_issue_status":      &jsonschema.Schema{Types: []string{"string", "null"}, Enum: []any{"done", "review", "ready", "blocked", nil}},
		"blocked_reason":           nullableBoundedStringSchema(50_000),
		"review_outcome":           &jsonschema.Schema{Types: []string{"string", "null"}, Enum: []any{"approved", "changes_requested", "blocked", nil}},
		"failure_reason_code":      &jsonschema.Schema{Types: []string{"string", "null"}, Enum: []any{"implementation_error", "environment_error", "missing_dependency", "invalid_requirements", "tests_failed", "context_lost", "timeout", "other", nil}},
		"interruption_reason_code": &jsonschema.Schema{Types: []string{"string", "null"}, Enum: []any{"handoff", "user_request", "context_limit", "client_shutdown", "environment_change", "other", nil}},
		"reason_details":           nullableBoundedStringSchema(50_000),
		"acknowledged_changes":     &jsonschema.Schema{Types: []string{"object", "null"}, OneOf: []*jsonschema.Schema{acknowledgement}},
		"artifacts":                schemaArtifacts(),
		"idempotency_key":          nullableBoundedStringSchema(128),
	}, "attempt_id", "lease_token", "outcome", "result_summary")
}

func schemaProjectOutput() *jsonschema.Schema          { return typedSchema[projectOutput]() }
func schemaLabelListOutput() *jsonschema.Schema        { return typedSchema[labelListOutput]() }
func schemaIssueOutput() *jsonschema.Schema            { return typedSchema[issueDTO]() }
func schemaGetIssueActivityOutput() *jsonschema.Schema { return typedSchema[issueActivityOutput]() }
func schemaSearchOutput() *jsonschema.Schema           { return typedSchema[searchOutput]() }
func schemaChangesOutput() *jsonschema.Schema          { return typedSchema[changesOutput]() }
func schemaAddCommentOutput() *jsonschema.Schema       { return typedSchema[addCommentOutput]() }
func schemaRecordDecisionOutput() *jsonschema.Schema {
	return typedSchema[recordDecisionOutput]()
}
func schemaDecisionListOutput() *jsonschema.Schema   { return typedSchema[decisionListOutput]() }
func schemaGetWorkContextOutput() *jsonschema.Schema { return typedSchema[workContextOutput]() }
func schemaUpdateOutput() *jsonschema.Schema         { return typedSchema[updateIssueOutput]() }
func schemaIssueListOutput() *jsonschema.Schema      { return typedSchema[issueListOutput]() }
func schemaManageIssueRelationOutput() *jsonschema.Schema {
	return typedSchema[manageIssueRelationOutput]()
}
func schemaGraphOutput() *jsonschema.Schema           { return typedSchema[graphOutput]() }
func schemaPlanValidationOutput() *jsonschema.Schema  { return typedSchema[planValidationOutput]() }
func schemaApplyIssuePlanOutput() *jsonschema.Schema  { return typedSchema[applyIssuePlanOutput]() }
func schemaClaimIssueOutput() *jsonschema.Schema      { return typedSchema[claimIssueOutput]() }
func schemaRenewAttemptOutput() *jsonschema.Schema    { return typedSchema[renewAttemptOutput]() }
func schemaSaveAttemptNoteOutput() *jsonschema.Schema { return typedSchema[saveAttemptNoteOutput]() }
func schemaFinishAttemptOutput() *jsonschema.Schema   { return typedSchema[finishAttemptOutput]() }

func typedSchema[T any]() *jsonschema.Schema {
	schema, err := jsonschema.ForType(reflect.TypeFor[T](), &jsonschema.ForOptions{})
	if err != nil {
		panic(err)
	}
	return schema
}
