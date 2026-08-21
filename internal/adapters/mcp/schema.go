package mcp

import (
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/domain"
)

func tool(name, description string, input, output *jsonschema.Schema, hints *sdkmcp.ToolAnnotations) *sdkmcp.Tool {
	if name != "open_project" {
		input = withProjectRef(input)
	}
	return &sdkmcp.Tool{Name: name, Description: description, InputSchema: input, OutputSchema: output, Annotations: hints}
}

// toolHints is the one explicit, reviewable annotation decision required for
// every registered tool: adding a tool without calling this (or changing
// tool's signature) fails to compile. destructiveHint and idempotentHint
// follow the actual write behavior of the handler, not its name or whether it
// happens to accept an optional idempotency_key — see docs/03-mcp-tools.md
// for the per-tool rationale, especially the non-obvious idempotent cases
// (version- or lease-gated mutations that fail safely on exact repeat).
func toolHints(readOnly, destructive, idempotent, openWorld bool) *sdkmcp.ToolAnnotations {
	return &sdkmcp.ToolAnnotations{
		ReadOnlyHint:    readOnly,
		DestructiveHint: &destructive,
		IdempotentHint:  idempotent,
		OpenWorldHint:   &openWorld,
	}
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
func reviewRequestIdentifierSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Pattern:     "^[0-9A-HJKMNP-TV-Z]{26}$",
		Description: "Canonical review request identifier (ULID).",
	}
}
func nullableReviewRequestIdentifierSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Types: []string{"string", "null"}, Pattern: "^[0-9A-HJKMNP-TV-Z]{26}$", Description: "Canonical review request identifier (ULID)."}
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
func withDescription(schema *jsonschema.Schema, description string) *jsonschema.Schema {
	schema.Description = description
	return schema
}
func enumSchema(values ...string) *jsonschema.Schema {
	enum := make([]any, len(values))
	for index, value := range values {
		enum[index] = value
	}
	return &jsonschema.Schema{Type: "string", Enum: enum}
}

func schemaGetProject() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{"include_instructions": booleanSchema()}))
}

func withAgentSessionHandle(schema *jsonschema.Schema) *jsonschema.Schema {
	if schema == nil {
		return nil
	}
	if schema.Properties == nil {
		schema.Properties = map[string]*jsonschema.Schema{}
	}
	schema.Properties["agent_session_handle"] = withDescription(nullableBoundedStringSchema(256), "Optional durable session handle for request attribution.")
	return schema
}

func withProjectRef(schema *jsonschema.Schema) *jsonschema.Schema {
	if schema == nil {
		return nil
	}
	if schema.Properties == nil {
		schema.Properties = map[string]*jsonschema.Schema{}
	}
	if _, exists := schema.Properties["project_ref"]; exists {
		return schema
	}
	schema.Properties["project_ref"] = nullableProjectRefSchema()
	return schema
}

func nullableProjectRefSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Types: []string{"string", "null"}, MaxLength: intPointer(26), Pattern: "^[0-9A-HJKMNP-TV-Z]{26}$", Description: "Project reference returned by open_project. Pass it explicitly for stateless routing; omit only when using a configured default."}
}

func schemaOpenProject() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{"project_root": stringSchema()}, "project_root")
}

func schemaCreateAgentSession() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"client_name":    withDescription(stringSchema(), "Required client identity."),
		"client_version": withDescription(nullableBoundedStringSchema(256), "Optional client version."),
		"agent_label":    withDescription(nullableBoundedStringSchema(256), "Optional human-readable agent identity."),
		"model":          withDescription(nullableBoundedStringSchema(256), "Optional model identifier."),
		"instance_key":   withDescription(nullableBoundedStringSchema(256), "Optional stable key for this client instance."),
	}, "client_name")
}

func schemaEndAgentSession() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{"agent_session_handle": boundedStringSchema(256)}, "agent_session_handle")
}

func schemaExportProject() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{"delivery": enumSchema("artifact", "inline")}))
}

func schemaExportProjectOutput() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", AnyOf: []*jsonschema.Schema{typedSchema[domain.LogicalProjectDocument](), typedSchema[exportArtifactOutput]()}}
}

func schemaValidateImport() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{"document": nullableBoundedStringSchema(domain.MaxLogicalProjectImportBytes), "source_uri": nullableBoundedStringSchema(4096)}))
}

func schemaValidateImportOutput() *jsonschema.Schema {
	return typedSchema[domain.LogicalProjectImportDryRun]()
}

func schemaApplyImport() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{"document": nullableBoundedStringSchema(domain.MaxLogicalProjectImportBytes), "source_uri": nullableBoundedStringSchema(4096)}))
}

func schemaApplyImportOutput() *jsonschema.Schema {
	return typedSchema[domain.LogicalProjectImportApplyResult]()
}

func schemaListLabels() *jsonschema.Schema {
	limit := boundedIntegerSchema(0, 100)
	limit.Description = "0 uses the default limit of 50; maximum is 100."
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"query": nullableStringSchema(), "limit": limit, "cursor": nullableStringSchema(),
	}))
}

func schemaCreateIssue() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"type": stringSchema(), "title": stringSchema(), "description": nullableStringSchema(),
		"acceptance_criteria": nullableStringSchema(), "status": stringSchema(), "priority": stringSchema(),
		"parent_issue_id": nullableIssueIdentifierSchema(), "blocked_reason": nullableStringSchema(),
		"labels": stringsSchema(), "create_missing_labels": booleanSchema(),
		"idempotency_key": nullableBoundedStringSchema(128), "view": enumSchema("compact", "full"),
	}, "type", "title"))
}

func schemaUpdateIssue() *jsonschema.Schema {
	changes := object(map[string]*jsonschema.Schema{
		"title": stringSchema(), "description": nullableStringSchema(), "acceptance_criteria": nullableStringSchema(),
		"type": stringSchema(), "priority": stringSchema(), "status": stringSchema(),
		"parent_issue_id": nullableStringSchema(), "blocked_reason": nullableStringSchema(),
		"labels": stringsSchema(),
	})
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"issue_id": issueIdentifierSchema(), "expected_version": integerSchema(), "changes": changes,
		"create_missing_labels": booleanSchema(), "idempotency_key": nullableBoundedStringSchema(128), "view": enumSchema("compact", "full"),
	}, "issue_id", "expected_version", "changes"))
}

func schemaGetIssue() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"issue_id": issueIdentifierSchema(), "view": enumSchema("compact", "standard", "full"),
	}, "issue_id"))
}

func schemaGetIssueActivity() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"issue_id": issueIdentifierSchema(),
		"types":    &jsonschema.Schema{Type: "array", Items: enumSchema("comments", "decisions", "reviews", "attempts", "attempt_notes", "events", "artifacts"), MaxItems: intPointer(7), UniqueItems: true},
		"limit":    boundedIntegerSchema(0, 100),
		"cursor":   nullableBoundedStringSchema(4096),
		"order":    enumSchema("newest_first"),
	}, "issue_id"))
}

func schemaSearch() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"query":            withDescription(boundedStringSchema(domain.MaxSearchQueryRunes), "Required full-text terms."),
		"entity_types":     withDescription(&jsonschema.Schema{Type: "array", Items: enumSchema("issue", "comment", "decision", "review", "attempt_note"), MaxItems: intPointer(5), UniqueItems: true}, "Optional result types; empty includes all."),
		"issue_id":         withDescription(nullableIssueIdentifierSchema(), "Optional issue scope (ULID or ISSUE-N)."),
		"epic_id":          withDescription(nullableIssueIdentifierSchema(), "Optional epic scope (ULID or ISSUE-N)."),
		"statuses":         withDescription(&jsonschema.Schema{Type: "array", Items: enumSchema("open", "ready", "blocked", "review", "done", "cancelled"), MaxItems: intPointer(6), UniqueItems: true}, "Optional issue-status filter."),
		"labels":           withDescription(boundedStringsSchema(domain.MaxLabelsPerIssue, domain.MaxLabelNameRunes), "Optional label filter."),
		"include_archived": withDescription(booleanSchema(), "Include archived issue records."),
		"limit":            withDescription(boundedIntegerSchema(0, domain.MaxSearchResults), "0 uses the default; 1-100 caps results."),
		"cursor":           withDescription(nullableBoundedStringSchema(4096), "Cursor from a previous page."),
		"snippet_length":   withDescription(boundedIntegerSchema(0, domain.MaxSearchSnippetRunes), "0 uses the default; 1-1000 caps excerpts in runes."),
	}, "query"))
}

func schemaGetChanges() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"since_event_id": boundedIntegerSchema(0, 9_223_372_036_854_775_807),
		"issue_id":       nullableIssueIdentifierSchema(),
		"event_types":    boundedStringsSchema(domain.MaxChangeEventTypes, domain.MaxEventTypeRunes),
		"limit":          boundedIntegerSchema(0, 200),
	}, "since_event_id"))
}

func schemaGetWorkContext() *jsonschema.Schema {
	includeValues := make([]string, len(domain.AllWorkContextIncludes))
	for index, include := range domain.AllWorkContextIncludes {
		includeValues[index] = string(include)
	}
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"issue_id": withDescription(issueIdentifierSchema(), "Issue whose work context to load."),
		"include":  withDescription(&jsonschema.Schema{Type: "array", Items: enumSchema(includeValues...), MaxItems: intPointer(10), UniqueItems: true}, "Optional unique context sections; empty returns the compact default."),
		"limits":   withDescription(schemaWorkContextLimits(), "Optional 1-20 bounds for requested list sections only."),
	}, "issue_id"))
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
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"types": stringsSchema(), "statuses": stringsSchema(), "effective_statuses": stringsSchema(),
		"priorities": stringsSchema(), "labels": stringsSchema(), "parent_issue_id": nullableIssueIdentifierSchema(),
		"is_blocked":       &jsonschema.Schema{Types: []string{"boolean", "null"}},
		"is_claimable":     &jsonschema.Schema{Types: []string{"boolean", "null"}},
		"include_archived": booleanSchema(), "limit": limit, "cursor": nullableStringSchema(), "view": enumSchema("compact", "full"),
	}))
}

func schemaArchiveIssue() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"issue_id": issueIdentifierSchema(), "expected_version": integerSchema(), "idempotency_key": nullableBoundedStringSchema(128), "view": enumSchema("compact", "full"),
	}, "issue_id", "expected_version"))
}

func schemaGetReviewRequest() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{"review_request_id": reviewRequestIdentifierSchema()}, "review_request_id"))
}

func schemaListReviewRequests() *jsonschema.Schema {
	limit := boundedIntegerSchema(1, 100)
	limit.Description = "Maximum is 100; the default is 20."
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"status":    &jsonschema.Schema{Type: "string", Enum: []any{"open", "claimed", "approved", "changes_requested", "blocked", "cancelled", "superseded"}},
		"claimable": &jsonschema.Schema{Type: "boolean"},
		"limit":     limit,
		"cursor":    nullableBoundedStringSchema(64),
	}))
}

func schemaCancelReviewRequest() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"review_request_id": reviewRequestIdentifierSchema(),
		"expected_version":  integerSchema(),
	}, "review_request_id", "expected_version"))
}

func schemaReplaceReviewRequest() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"predecessor_request_id":       reviewRequestIdentifierSchema(),
		"predecessor_expected_version": boundedIntegerSchema(1, 9_223_372_036_854_775_807),
		"target_issue_version":         boundedIntegerSchema(1, 9_223_372_036_854_775_807),
		"target_event_id":              boundedIntegerSchema(0, 9_223_372_036_854_775_807),
		"artifact_ids":                 boundedStringsSchema(domain.MaxReviewArtifactIDs, 4_096),
		"idempotency_key":              boundedStringSchema(domain.MaxIdempotencyKeyRunes),
	}, "predecessor_request_id", "predecessor_expected_version", "target_issue_version", "target_event_id", "idempotency_key"))
}

func schemaAddComment() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"issue_id": issueIdentifierSchema(), "content": boundedStringSchema(50_000),
		"idempotency_key": nullableBoundedStringSchema(128),
	}, "issue_id", "content"))
}

func schemaRecordDecision() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"issue_id":      nullableBoundedStringSchema(64),
		"title":         boundedStringSchema(300),
		"summary":       boundedStringSchema(2_000),
		"content":       boundedStringSchema(100_000),
		"status":        enumSchema("active", "superseded", "rejected"),
		"supersedes_id": nullableBoundedStringSchema(26),
	}, "title", "summary", "content"))
}

func schemaListDecisions() *jsonschema.Schema {
	limit := boundedIntegerSchema(0, 100)
	limit.Description = "0 uses the default limit of 20; maximum is 100."
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"issue_id": issueIdentifierSchema(),
		"limit":    limit,
		"cursor":   nullableBoundedStringSchema(4096),
	}))
}

func schemaManageIssueRelation() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"action":          enumSchema("add", "remove"),
		"source_issue_id": issueIdentifierSchema(),
		"target_issue_id": issueIdentifierSchema(),
		"relation_type":   enumSchema("blocks", "related_to", "duplicates"),
		"idempotency_key": nullableBoundedStringSchema(128),
	}, "action", "source_issue_id", "target_issue_id", "relation_type"))
}

func schemaGetIssueGraph() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"root_issue_id":     withDescription(issueIdentifierSchema(), "Issue at the graph traversal root."),
		"depth":             withDescription(boundedIntegerSchema(0, 5), "Relation hops from root; default 2."),
		"direction":         withDescription(enumSchema("outgoing", "incoming", "both"), "Relation traversal direction."),
		"relation_types":    withDescription(&jsonschema.Schema{Type: "array", Items: enumSchema("blocks", "related_to", "duplicates"), UniqueItems: true}, "Optional relation kinds; empty includes all."),
		"include_hierarchy": withDescription(booleanSchema(), "Include derived epic hierarchy edges."),
		"include_terminal":  withDescription(booleanSchema(), "Include terminal issue nodes."),
		"max_nodes":         withDescription(boundedIntegerSchema(1, 500), "Maximum returned nodes; default 100."),
		"view":              withDescription(enumSchema("compact"), "Only compact graph nodes are available."),
	}, "root_issue_id"))
}

func schemaGetPlanningGraph() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"root_issue_id": nullableIssueIdentifierSchema(), "depth": boundedIntegerSchema(0, 5), "max_nodes": boundedIntegerSchema(1, 500),
		"include_review": booleanSchema(), "include_related": booleanSchema(),
	}))
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
	properties := map[string]*jsonschema.Schema{"include_normalized_plan": booleanSchema()}
	schemaPlanFields(properties)
	return withAgentSessionHandle(object(properties, "issues", "relations", "decisions"))
}
func schemaApplyIssuePlan() *jsonschema.Schema {
	properties := map[string]*jsonschema.Schema{"idempotency_key": boundedStringSchema(128)}
	schemaPlanFields(properties)
	return withAgentSessionHandle(object(properties, "issues", "relations", "decisions", "idempotency_key"))
}

func schemaClaimIssue() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"issue_id":        withDescription(issueIdentifierSchema(), "Claimable ready or review issue (ULID or ISSUE-N)."),
		"lease_seconds":   withDescription(boundedIntegerSchema(60, 3600), "Requested lease duration in seconds."),
		"idempotency_key": withDescription(nullableBoundedStringSchema(128), "Optional key that replays the same claim request."),
		"view":            withDescription(enumSchema("compact", "full"), "Response shape; compact is the default."),
	}, "issue_id"))
}

func schemaRenewAttempt() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"attempt_id": boundedStringSchema(26), "lease_token": boundedStringSchema(512),
		"lease_seconds": boundedIntegerSchema(60, 3600),
	}, "attempt_id", "lease_token"))
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
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"attempt_id":      withDescription(boundedStringSchema(26), "Active attempt receiving the note."),
		"lease_token":     withDescription(boundedStringSchema(512), "Secret proof of the active attempt lease."),
		"kind":            withDescription(enumSchema("progress", "finding", "warning", "checkpoint"), "How the note should be classified."),
		"content":         withDescription(boundedStringSchema(50_000), "Required restartable note content."),
		"next_steps":      withDescription(boundedStringsSchema(20, 1_000), "Optional concrete actions after this note."),
		"important":       withDescription(booleanSchema(), "Marks the note as important."),
		"artifacts":       withDescription(schemaArtifacts(), "Optional artifacts created or referenced by this work."),
		"idempotency_key": withDescription(nullableBoundedStringSchema(128), "Optional key that replays the same note request."),
	}, "attempt_id", "lease_token", "kind", "content"))
}

func schemaFinishAttempt() *jsonschema.Schema {
	acknowledgement := object(map[string]*jsonschema.Schema{
		"issue_version":   boundedIntegerSchema(1, 9_223_372_036_854_775_807),
		"latest_event_id": boundedIntegerSchema(0, 9_223_372_036_854_775_807),
	}, "issue_version", "latest_event_id")
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"attempt_id": boundedStringSchema(26), "lease_token": boundedStringSchema(512),
		"outcome":        enumSchema("completed", "failed", "interrupted"),
		"result_summary": boundedStringSchema(50_000),
		"next_steps":     boundedStringsSchema(20, 1_000), "verification": boundedStringsSchema(20, 1_000),
		"view":                     enumSchema("compact", "full"),
		"target_issue_status":      &jsonschema.Schema{Types: []string{"string", "null"}, Enum: []any{"done", "review", "ready", "blocked", nil}},
		"blocked_reason":           nullableBoundedStringSchema(50_000),
		"review_outcome":           &jsonschema.Schema{Types: []string{"string", "null"}, Enum: []any{"approved", "changes_requested", "blocked", nil}},
		"failure_reason_code":      &jsonschema.Schema{Types: []string{"string", "null"}, Enum: []any{"implementation_error", "environment_error", "missing_dependency", "invalid_requirements", "tests_failed", "context_lost", "timeout", "other", nil}},
		"interruption_reason_code": &jsonschema.Schema{Types: []string{"string", "null"}, Enum: []any{"handoff", "user_request", "context_limit", "client_shutdown", "environment_change", "other", nil}},
		"reason_details":           nullableBoundedStringSchema(50_000),
		"acknowledged_changes":     &jsonschema.Schema{OneOf: []*jsonschema.Schema{acknowledgement, &jsonschema.Schema{Type: "null"}}},
		"artifacts":                schemaArtifacts(),
		"idempotency_key":          nullableBoundedStringSchema(128),
	}, "attempt_id", "lease_token", "outcome", "result_summary"))
}

func schemaProjectOutput() *jsonschema.Schema { return typedSchema[projectOutput]() }
func schemaCreateAgentSessionOutput() *jsonschema.Schema {
	return typedSchema[createAgentSessionOutput]()
}
func schemaEndAgentSessionOutput() *jsonschema.Schema { return typedSchema[endAgentSessionOutput]() }
func schemaLabelListOutput() *jsonschema.Schema       { return typedSchema[labelListOutput]() }
func schemaIssueOutput() *jsonschema.Schema           { return schemaCreateIssueUnion() }
func schemaCreateIssueOutput() *jsonschema.Schema     { return schemaCreateIssueUnion() }
func schemaArchiveIssueOutput() *jsonschema.Schema    { return schemaArchiveIssueUnion() }
func schemaGetIssueOutput() *jsonschema.Schema {
	properties := map[string]*jsonschema.Schema{
		"id":                  stringSchema(),
		"display_id":          stringSchema(),
		"sequence_no":         integerSchema(),
		"type":                stringSchema(),
		"title":               stringSchema(),
		"version":             integerSchema(),
		"updated_at":          stringSchema(),
		"status":              stringSchema(),
		"priority":            stringSchema(),
		"parent_issue_id":     nullableStringSchema(),
		"blocked_reason":      nullableStringSchema(),
		"created_at":          stringSchema(),
		"closed_at":           nullableStringSchema(),
		"archived_at":         nullableStringSchema(),
		"description":         nullableStringSchema(),
		"acceptance_criteria": nullableStringSchema(),
		"labels": &jsonschema.Schema{AnyOf: []*jsonschema.Schema{
			{Type: "array", Items: typedSchema[labelReferenceDTO]()},
			{Type: "array", Items: typedSchema[labelDTO]()},
		}},
	}
	return object(properties,
		"id", "display_id", "sequence_no", "type", "title", "version", "updated_at",
	)
}
func schemaReviewRequestOutput() *jsonschema.Schema { return typedSchema[reviewRequestDTO]() }
func schemaReviewRequestListOutput() *jsonschema.Schema {
	return typedSchema[reviewRequestListOutput]()
}
func schemaReplaceReviewRequestOutput() *jsonschema.Schema {
	return typedSchema[replaceReviewRequestOutput]()
}
func schemaGetIssueActivityOutput() *jsonschema.Schema { return typedSchema[issueActivityOutput]() }
func schemaSearchOutput() *jsonschema.Schema           { return typedSchema[searchOutput]() }
func schemaChangesOutput() *jsonschema.Schema          { return typedSchema[changesOutput]() }
func schemaAddCommentOutput() *jsonschema.Schema       { return typedSchema[addCommentOutput]() }
func schemaRecordDecisionOutput() *jsonschema.Schema {
	return typedSchema[recordDecisionOutput]()
}
func schemaDecisionListOutput() *jsonschema.Schema   { return typedSchema[decisionListOutput]() }
func schemaGetWorkContextOutput() *jsonschema.Schema { return typedSchema[workContextOutput]() }
func schemaUpdateOutput() *jsonschema.Schema         { return schemaUpdateIssueUnion() }

func schemaCreateIssueUnion() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", OneOf: []*jsonschema.Schema{typedSchema[issueDTO](), typedSchema[createIssueCompactOutput]()}}
}

func schemaUpdateIssueUnion() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", OneOf: []*jsonschema.Schema{typedSchema[updateIssueOutput](), typedSchema[updateIssueCompactOutput]()}}
}

func schemaArchiveIssueUnion() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", OneOf: []*jsonschema.Schema{typedSchema[issueDTO](), typedSchema[archiveIssueCompactOutput]()}}
}

func schemaClaimIssueUnion() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", OneOf: []*jsonschema.Schema{typedSchema[claimIssueOutput](), typedSchema[claimIssueCompactOutput]()}}
}

func schemaFinishAttemptUnion() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", OneOf: []*jsonschema.Schema{typedSchema[finishAttemptOutput](), typedSchema[finishAttemptCompactOutput]()}}
}

// schemaIssueListItem describes one list_issues item. list_issues returns one
// of two item shapes depending on the request's view (compact, the default,
// or full), so this schema is hand-built rather than derived from a single Go
// type via typedSchema: its required fields are exactly the compact
// projection (identifiers, title, type, status, effective_status, priority,
// claimability, blocker count, labels, updated_at), and every full-only field
// (description, acceptance_criteria, parent_issue_id, blocked_reason,
// version, created_at, closed_at, archived_at, active_attempt_id) is declared
// as an optional property. A compact item satisfies this schema by omitting
// the optional properties; a full item satisfies it by including them all.
func schemaIssueListItem() *jsonschema.Schema {
	properties := map[string]*jsonschema.Schema{
		"id":                       stringSchema(),
		"display_id":               stringSchema(),
		"sequence_no":              integerSchema(),
		"type":                     stringSchema(),
		"title":                    stringSchema(),
		"status":                   stringSchema(),
		"priority":                 stringSchema(),
		"effective_status":         stringSchema(),
		"unresolved_blocker_count": integerSchema(),
		"is_blocked":               booleanSchema(),
		"is_claimable":             booleanSchema(),
		"labels":                   &jsonschema.Schema{Type: "array", Items: typedSchema[labelDTO]()},
		"updated_at":               stringSchema(),
		// Present only when view: "full" is requested.
		"description":         nullableStringSchema(),
		"acceptance_criteria": nullableStringSchema(),
		"parent_issue_id":     nullableStringSchema(),
		"blocked_reason":      nullableStringSchema(),
		"version":             integerSchema(),
		"created_at":          stringSchema(),
		"closed_at":           nullableStringSchema(),
		"archived_at":         nullableStringSchema(),
		"active_attempt_id":   nullableStringSchema(),
	}
	return object(properties,
		"id", "display_id", "sequence_no", "type", "title", "status", "priority",
		"effective_status", "unresolved_blocker_count", "is_blocked", "is_claimable",
		"labels", "updated_at",
	)
}

func schemaIssueListOutput() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"items":        &jsonschema.Schema{Type: "array", Items: schemaIssueListItem()},
		"next_cursor":  nullableStringSchema(),
		"has_more":     booleanSchema(),
		"next_actions": stringsSchema(),
	}, "items", "next_cursor", "has_more", "next_actions")
}
func schemaManageIssueRelationOutput() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"relation": typedSchema[relationDTO](),
		"affected_issues": &jsonschema.Schema{Type: "array", Items: object(map[string]*jsonschema.Schema{
			"id":                       stringSchema(),
			"display_id":               stringSchema(),
			"version":                  integerSchema(),
			"status":                   stringSchema(),
			"effective_status":         stringSchema(),
			"unresolved_blocker_count": integerSchema(),
			"is_blocked":               booleanSchema(),
			"is_claimable":             booleanSchema(),
		}, "id", "display_id", "version", "status", "effective_status", "unresolved_blocker_count", "is_blocked", "is_claimable")},
		"changed": booleanSchema(),
	}, "relation", "affected_issues", "changed")
}
func schemaGraphOutput() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"root_issue_id": nullableIssueIdentifierSchema(),
		"nodes": &jsonschema.Schema{Type: "array", Items: object(map[string]*jsonschema.Schema{
			"id":                       stringSchema(),
			"display_id":               stringSchema(),
			"sequence_no":              integerSchema(),
			"type":                     stringSchema(),
			"title":                    stringSchema(),
			"status":                   stringSchema(),
			"effective_status":         stringSchema(),
			"priority":                 stringSchema(),
			"unresolved_blocker_count": integerSchema(),
			"is_blocked":               booleanSchema(),
			"is_claimable":             booleanSchema(),
		}, "id", "display_id", "sequence_no", "type", "title", "status", "effective_status", "priority", "unresolved_blocker_count", "is_blocked", "is_claimable")},
		"edges":             &jsonschema.Schema{Type: "array", Items: typedSchema[graphEdgeDTO]()},
		"entry_points":      &jsonschema.Schema{Type: "array", Items: stringSchema()},
		"blocking_nodes":    &jsonschema.Schema{Type: "array", Items: stringSchema()},
		"summary":           typedSchema[graphSummaryDTO](),
		"warnings":          &jsonschema.Schema{Type: "array", Items: stringSchema()},
		"truncated":         booleanSchema(),
		"truncation_reason": nullableStringSchema(),
		"next_actions":      stringsSchema(),
	}, "nodes", "edges", "entry_points", "summary", "truncated", "next_actions")
}
func schemaPlanValidationOutput() *jsonschema.Schema  { return typedSchema[planValidationOutput]() }
func schemaApplyIssuePlanOutput() *jsonschema.Schema  { return typedSchema[applyIssuePlanOutput]() }
func schemaClaimIssueOutput() *jsonschema.Schema      { return schemaClaimIssueUnion() }
func schemaRenewAttemptOutput() *jsonschema.Schema    { return typedSchema[renewAttemptOutput]() }
func schemaSaveAttemptNoteOutput() *jsonschema.Schema { return typedSchema[saveAttemptNoteOutput]() }
func schemaFinishAttemptOutput() *jsonschema.Schema   { return schemaFinishAttemptUnion() }

func typedSchema[T any]() *jsonschema.Schema {
	schema, err := jsonschema.ForType(reflect.TypeFor[T](), &jsonschema.ForOptions{})
	if err != nil {
		panic(err)
	}
	return schema
}
