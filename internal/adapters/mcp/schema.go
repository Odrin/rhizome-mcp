package mcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/domain"
)

// strictOutputSchemas retains every tool's full (strict) output schema, keyed
// by tool name. tools/list advertises the compact projection produced by
// advertisedOutputSchema; the strict original stays the validation contract —
// the output-conformance suite resolves it from here and validates every real
// response against it, so trimming the advertisement never weakens what CI
// proves about actual outputs.
var strictOutputSchemas sync.Map

func tool(name, description string, input, output *jsonschema.Schema, hints *sdkmcp.ToolAnnotations) *sdkmcp.Tool {
	if name != "open_project" {
		input = withProjectRef(input)
	}
	strictOutputSchemas.Store(name, output)
	return &sdkmcp.Tool{Name: name, Description: description, InputSchema: input, OutputSchema: advertisedOutputSchema(output), Annotations: hints}
}

// advertisedShallowMarker tags a strict-schema branch whose full field detail
// is intentionally withheld from the advertised catalog. The text after the
// marker becomes the advertised branch's description.
const advertisedShallowMarker = "advertise-shallow:"

// advertisedOutputSchema derives the catalog-advertised projection of a
// strict output schema. The projection is lossless for what an agent uses —
// every field name, type, nullability, enum, and description survives — but
// drops pure validator strictness that repeats information or encodes none:
// per-object `required` arrays (each one re-lists every property name) and
// `additionalProperties: false`. Branches tagged with advertisedShallowMarker
// (legacy view:"full" payloads, the inline export document) collapse to a
// one-line described object, and `oneOf` relaxes to `anyOf` so a shallow
// branch can never make the advertised union unsatisfiable. Server-side
// behavior and the strict validation contract are unchanged: responses are
// still validated against the strict original (see strictOutputSchemas).
func advertisedOutputSchema(strict *jsonschema.Schema) *jsonschema.Schema {
	if strict == nil {
		return nil
	}
	data, err := json.Marshal(strict)
	if err != nil {
		panic(err)
	}
	var tree any
	if err := json.Unmarshal(data, &tree); err != nil {
		panic(err)
	}
	tree = compactAdvertisedNode(tree)
	compacted, err := json.Marshal(tree)
	if err != nil {
		panic(err)
	}
	advertised := &jsonschema.Schema{}
	if err := json.Unmarshal(compacted, advertised); err != nil {
		panic(err)
	}
	return advertised
}

func compactAdvertisedNode(node any) any {
	switch typed := node.(type) {
	case map[string]any:
		if comment, ok := typed["$comment"].(string); ok && strings.HasPrefix(comment, advertisedShallowMarker) {
			return map[string]any{"type": "object", "description": strings.TrimPrefix(comment, advertisedShallowMarker)}
		}
		delete(typed, "required")
		delete(typed, "additionalProperties")
		if branches, ok := typed["oneOf"]; ok {
			typed["anyOf"] = branches
			delete(typed, "oneOf")
		}
		for key, value := range typed {
			typed[key] = compactAdvertisedNode(value)
		}
		return typed
	case []any:
		for index, value := range typed {
			typed[index] = compactAdvertisedNode(value)
		}
		return typed
	default:
		return node
	}
}

// legacyFullViewBranch marks the strict schema of a view:"full" response
// shape so the advertised catalog replaces it with a one-line described
// object. The compact default stays fully specified in the advertisement;
// the legacy payload keeps its complete strict schema for validation and is
// documented field-by-field in docs/03-mcp-tools.md.
func legacyFullViewBranch(schema *jsonschema.Schema) *jsonschema.Schema {
	schema.Comment = advertisedShallowMarker + `Legacy full payload returned only for view:"full"; field-level detail in docs/03-mcp-tools.md.`
	return schema
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
	document := typedSchema[domain.LogicalProjectDocument]()
	document.Comment = advertisedShallowMarker + `Complete logical project document returned only for delivery:"inline"; format specified in docs/07-logical-interchange.md.`
	return &jsonschema.Schema{Type: "object", AnyOf: []*jsonschema.Schema{typedSchema[exportArtifactOutput](), document}}
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
		"entity_types":     withDescription(&jsonschema.Schema{Type: "array", Items: enumSchema("issue", "comment", "decision", "review", "attempt_note", "workflow_policy", "gate_evidence"), MaxItems: intPointer(7), UniqueItems: true}, "Optional result types; empty includes all."),
		"issue_id":         withDescription(nullableIssueIdentifierSchema(), "Optional issue scope (ULID or ISSUE-N)."),
		"epic_id":          withDescription(nullableIssueIdentifierSchema(), "Optional epic scope (ULID or ISSUE-N)."),
		"statuses":         withDescription(&jsonschema.Schema{Type: "array", Items: enumSchema(domain.StatusNames()...), MaxItems: intPointer(6), UniqueItems: true}, "Optional issue-status filter."),
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
		"issue_id":          withDescription(issueIdentifierSchema(), "Issue whose work context to load."),
		"include":           withDescription(&jsonschema.Schema{Type: "array", Items: enumSchema(includeValues...), MaxItems: intPointer(domain.MaxWorkContextIncludes), UniqueItems: true}, "Optional unique context sections; empty returns the compact default."),
		"limits":            withDescription(schemaWorkContextLimits(), "Optional 1-20 bounds for requested list sections only."),
		"desired_resources": withDescription(schemaResources(), "Optional resources to diagnose against active reservations elsewhere in the project (not resources being acquired). Drives the default-context conflict warning and, with reservation_conflicts requested, bounded conflict rows."),
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
	resourceReservations := boundedIntegerSchema(1, 20)
	resourceReservations.Description = "Applies when include contains resource_reservations."
	reservationConflicts := boundedIntegerSchema(1, 20)
	reservationConflicts.Description = "Applies when include contains reservation_conflicts."
	return object(map[string]*jsonschema.Schema{
		"related_issue_summaries":        relatedIssueSummaries,
		"recent_comments":                recentComments,
		"recent_attempt_notes":           recentAttemptNotes,
		"decision_content":               decisionContent,
		"attempt_history":                attemptHistory,
		"artifacts":                      artifacts,
		"changes_since_previous_attempt": changesSincePreviousAttempt,
		"resource_reservations":          resourceReservations,
		"reservation_conflicts":          reservationConflicts,
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

func schemaCreateReviewRequest() *jsonschema.Schema {
	purposes := boundedStringsSchema(domain.MaxReviewPurposes, domain.MaxPolicyKeyRunes)
	purposes.Description = "Purposes this review covers; defaults to [implementation]. Must cover every purpose an active review_approval policy currently requires for this target, or the call fails with REVIEW_PURPOSE_REQUIRED."
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"issue_id":             withDescription(issueIdentifierSchema(), "Issue to request review of. Its open or claimed request, if any, must be resolved first: a second request for the same target fails with REVIEW_ALREADY_EXISTS unless its content is identical, which replays instead."),
		"target_issue_version": withDescription(boundedIntegerSchema(1, 9_223_372_036_854_775_807), "Issue version this review covers, from get_issue; must be current or the call fails with STALE_REVIEW_TARGET."),
		"target_event_id":      withDescription(boundedIntegerSchema(0, 9_223_372_036_854_775_807), "Project event ID the reviewed work is current as of (latest_event_id from open_project, get_changes, or finish_attempt); reviewed work changing after it fails with STALE_REVIEW_TARGET."),
		"artifact_ids":         withDescription(boundedStringsSchema(domain.MaxReviewArtifactIDs, 4_096), "Artifact IDs the frozen target covers."),
		"purposes":             purposes,
	}, "issue_id", "target_issue_version", "target_event_id"))
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
	purposes := boundedStringsSchema(domain.MaxReviewPurposes, domain.MaxPolicyKeyRunes)
	purposes.Description = "Purposes the successor covers. Omit to inherit the predecessor's purposes; a non-empty list must cover every purpose an active review_approval policy currently requires for this target, or the call fails with REVIEW_PURPOSE_REQUIRED."
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"predecessor_request_id":       withDescription(reviewRequestIdentifierSchema(), "Review request (ULID) to supersede; must be open. A claimed predecessor fails with REVIEW_REQUEST_CLAIMED, a resolved one with REVIEW_REQUEST_NOT_REPLACEABLE."),
		"predecessor_expected_version": withDescription(boundedIntegerSchema(1, 9_223_372_036_854_775_807), "Predecessor request version from get_review_request; a mismatch fails with VERSION_CONFLICT (re-read and retry)."),
		"target_issue_version":         withDescription(boundedIntegerSchema(1, 9_223_372_036_854_775_807), "Issue version the successor reviews, from get_issue; must be current or the call fails with STALE_REVIEW_TARGET."),
		"target_event_id":              withDescription(boundedIntegerSchema(0, 9_223_372_036_854_775_807), "Project event ID the reviewed work is current as of (latest_event_id from open_project, get_changes, or finish_attempt); reviewed work changing after it fails with STALE_REVIEW_TARGET."),
		"artifact_ids":                 withDescription(boundedStringsSchema(domain.MaxReviewArtifactIDs, 4_096), "Artifact IDs the successor's frozen target covers. Not inherited: omitting this leaves the successor without artifacts even if the predecessor had them."),
		"purposes":                     purposes,
		"idempotency_key":              withDescription(boundedStringSchema(domain.MaxIdempotencyKeyRunes), "Required key for idempotent retries; identical requests replay the saved response, a reused key with a different request fails with IDEMPOTENCY_CONFLICT."),
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
		"relation_type":   enumSchema(domain.RelationTypeNames()...),
		"idempotency_key": nullableBoundedStringSchema(128),
	}, "action", "source_issue_id", "target_issue_id", "relation_type"))
}

func schemaGetIssueGraph() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"root_issue_id":     withDescription(issueIdentifierSchema(), "Issue at the graph traversal root."),
		"depth":             withDescription(boundedIntegerSchema(0, 5), "Relation hops from root; default 2."),
		"direction":         withDescription(enumSchema("outgoing", "incoming", "both"), "Relation traversal direction."),
		"relation_types":    withDescription(&jsonschema.Schema{Type: "array", Items: enumSchema(domain.RelationTypeNames()...), UniqueItems: true}, "Optional relation kinds; empty includes all."),
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
		"include_terminal": booleanSchema(),
	}))
}

func schemaPlanIssue() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"ref": boundedStringSchema(64), "type": enumSchema(domain.IssueTypeNames()...), "title": boundedStringSchema(300),
		"description": nullableBoundedStringSchema(100000), "acceptance_criteria": nullableBoundedStringSchema(50000),
		"status":   enumSchema(domain.StatusNames()...),
		"priority": enumSchema(domain.PriorityNames()...), "parent_ref": nullableBoundedStringSchema(64),
		"blocked_reason": nullableBoundedStringSchema(100000), "labels": boundedStringsSchema(50, 64), "create_missing_labels": booleanSchema(),
	}, "type", "title")
}
func schemaPlanRelation() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"source_ref": boundedStringSchema(64), "target_ref": boundedStringSchema(64),
		"type": enumSchema(domain.RelationTypeNames()...),
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
		"lease_seconds":   withDescription(boundedIntegerSchema(domain.MinLeaseSeconds, domain.MaxLeaseSeconds), "Requested lease duration in seconds; omit to use the server default."),
		"resources":       withDescription(schemaResources(), "Optional resources to reserve atomically with the claim, all-or-nothing; a conflict fails the whole claim. Rejected if the claim resolves to a review attempt."),
		"idempotency_key": withDescription(nullableBoundedStringSchema(128), "Optional key that replays the same claim request."),
		"view":            withDescription(enumSchema("compact", "full"), "Response shape; compact is the default."),
	}, "issue_id"))
}

// schemaResource describes one caller-supplied reservation target before
// normalization (docs/02 §18): kind is always required, and only the
// path-kind fields (path) or the logical-kind fields (namespace, name)
// apply, per kind -- the domain layer, not this schema, enforces which.
func schemaResource() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"kind":      withDescription(enumSchema("file", "directory", "glob", "logical"), "Resource kind; path kinds (file, directory, glob) use path, logical uses namespace and name."),
		"path":      withDescription(boundedStringSchema(domain.MaxResourcePathRunes), "Project-relative path; unused for kind=logical."),
		"namespace": withDescription(boundedStringSchema(domain.MaxLogicalNamespaceRunes), "Logical resource namespace ([a-z][a-z0-9.-]{0,63}); unused for path kinds."),
		"name":      withDescription(boundedStringSchema(domain.MaxLogicalNameRunes), "Logical resource name; unused for path kinds."),
	}, "kind")
}

func schemaResources() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "array", Items: schemaResource(), MaxItems: intPointer(domain.MaxReservationResources)}
}

func schemaReserveResources() *jsonschema.Schema {
	// Not MinItems-bounded: an empty resources array is a domain validation
	// error (CodeInvalidArgument via PrepareReservationRequest), not a
	// protocol-level schema rejection -- matching every other required
	// array in this catalog (see boundedStringsSchema), so the failure
	// carries the structured MCP error envelope (ISSUE-197 AC5) instead of
	// a bare schema-validation rejection with no structuredContent.
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"attempt_id":      withDescription(boundedStringSchema(26), "Active work attempt receiving the reservations."),
		"lease_token":     withDescription(boundedStringSchema(512), "Secret proof of the active attempt lease."),
		"resources":       withDescription(schemaResources(), "Resources to add, all-or-nothing; must not be empty."),
		"idempotency_key": withDescription(nullableBoundedStringSchema(128), "Optional key that replays the same reservation request."),
	}, "attempt_id", "lease_token", "resources"))
}

func schemaReleaseResources() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"attempt_id":      withDescription(boundedStringSchema(26), "Active work attempt releasing the reservations."),
		"lease_token":     withDescription(boundedStringSchema(512), "Secret proof of the active attempt lease."),
		"reservation_ids": withDescription(boundedStringsSchema(domain.MaxReleaseResourceIDs, 26), "Reservation IDs to release; empty releases every active reservation this attempt owns."),
		"idempotency_key": withDescription(nullableBoundedStringSchema(128), "Optional key that replays the same release request."),
	}, "attempt_id", "lease_token"))
}

func schemaListResourceReservations() *jsonschema.Schema {
	limit := boundedIntegerSchema(0, domain.MaxReservationHistoryLimit)
	limit.Description = "0 uses the default limit of 20; maximum is 100."
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"issue_id":   withDescription(nullableIssueIdentifierSchema(), "Optional issue filter (ULID or ISSUE-N)."),
		"attempt_id": withDescription(nullableBoundedStringSchema(26), "Optional attempt filter."),
		"kind":       withDescription(&jsonschema.Schema{Types: []string{"string", "null"}, Enum: []any{"file", "directory", "glob", "logical", nil}}, "Optional resource kind filter."),
		"active":     withDescription(&jsonschema.Schema{Types: []string{"boolean", "null"}}, "Optional lifecycle filter: true for active only, false for released only, omitted for both."),
		"limit":      limit,
		"cursor":     nullableBoundedStringSchema(4096),
	}))
}

func schemaGetResourceReservation() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"reservation_id": boundedStringSchema(26),
		"view":           withDescription(enumSchema("compact", "full"), "Response shape; compact (default) omits the normalized comparison key and version; full includes both."),
	}, "reservation_id"))
}

func schemaRenewAttempt() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"attempt_id": boundedStringSchema(26), "lease_token": boundedStringSchema(512),
		"lease_seconds": boundedIntegerSchema(domain.MinLeaseSeconds, domain.MaxLeaseSeconds),
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
		"attempt_id":      withDescription(boundedStringSchema(26), "Attempt ULID receiving the note; its lease must still be active."),
		"lease_token":     withDescription(boundedStringSchema(512), "Opaque lease proof from claim_issue; required."),
		"kind":            withDescription(enumSchema("progress", "finding", "warning", "checkpoint"), "Note classification: progress for routine status, finding for discoveries worth keeping, warning for risks or pitfalls, checkpoint for restartable state a successor resumes from (get_work_context surfaces the latest checkpoint). For durable decisions use record_decision; for issue-level discussion use add_comment."),
		"content":         withDescription(boundedStringSchema(50_000), "Note body. Write checkpoints as self-contained restartable state (what is done, what remains, how to verify), not a transcript."),
		"next_steps":      withDescription(boundedStringsSchema(20, 1_000), "Optional concrete actions after this note."),
		"important":       withDescription(booleanSchema(), "Marks the note as important in activity and work-context views."),
		"artifacts":       withDescription(schemaArtifacts(), "Optional artifacts created or referenced by this work."),
		"idempotency_key": withDescription(nullableBoundedStringSchema(128), "Optional key for idempotent retries; replays the exact response for identical requests. Bare repeats without it append duplicate notes."),
	}, "attempt_id", "lease_token", "kind", "content"))
}

func schemaFinishAttempt() *jsonschema.Schema {
	acknowledgement := object(map[string]*jsonschema.Schema{
		"issue_version":   boundedIntegerSchema(1, 9_223_372_036_854_775_807),
		"latest_event_id": boundedIntegerSchema(0, 9_223_372_036_854_775_807),
	}, "issue_version", "latest_event_id")
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"attempt_id":               withDescription(boundedStringSchema(26), "Attempt ULID to finish; required."),
		"lease_token":              withDescription(boundedStringSchema(512), "Opaque lease proof from claim_issue; required."),
		"outcome":                  withDescription(enumSchema("completed", "failed", "interrupted"), "Outcome classification; required. One of: completed, failed, interrupted."),
		"result_summary":           withDescription(boundedStringSchema(50_000), "Free-form completion summary or error description; required."),
		"next_steps":               withDescription(boundedStringsSchema(20, 1_000), "Optional recovery or handoff steps (up to 20 items, 1000 chars each)."),
		"verification":             withDescription(boundedStringsSchema(20, 1_000), "Optional verification items demonstrated or tested (up to 20 items, 1000 chars each)."),
		"view":                     withDescription(enumSchema("compact", "full"), "Output format: compact (default) or full with complete payloads."),
		"target_issue_status":      withDescription(&jsonschema.Schema{Types: []string{"string", "null"}, Enum: []any{"done", "review", "ready", "blocked", nil}}, "Final issue status for work completion (outcome=completed, kind=work). Required for work completion. One of: done, review, ready, blocked. Forbidden for failed, interrupted, or review completion."),
		"blocked_reason":           withDescription(nullableBoundedStringSchema(50_000), "Reason why a work attempt completed to blocked or review attempt completed to blocked. Required if target_issue_status=blocked (kind=work) or review_outcome=blocked (kind=review). Forbidden otherwise."),
		"review_outcome":           withDescription(&jsonschema.Schema{Types: []string{"string", "null"}, Enum: []any{"approved", "changes_requested", "blocked", nil}}, "Review classification for review completion (outcome=completed, kind=review). Required for review completion. One of: approved, changes_requested, blocked. Forbidden for failed, interrupted, or work completion."),
		"failure_reason_code":      withDescription(&jsonschema.Schema{Types: []string{"string", "null"}, Enum: []any{"implementation_error", "environment_error", "missing_dependency", "invalid_requirements", "tests_failed", "context_lost", "timeout", "other", nil}}, "Reason code for failed outcome (outcome=failed). Required if outcome=failed. Forbidden for completed or interrupted outcomes."),
		"interruption_reason_code": withDescription(&jsonschema.Schema{Types: []string{"string", "null"}, Enum: []any{"handoff", "user_request", "context_limit", "client_shutdown", "environment_change", "other", nil}}, "Reason code for interrupted outcome (outcome=interrupted). Required if outcome=interrupted. Forbidden for completed or failed outcomes."),
		"reason_details":           withDescription(nullableBoundedStringSchema(50_000), "Additional details about failure, interruption, or blocked outcome. Only allowed when target_issue_status=blocked (kind=work) or review_outcome=blocked (kind=review)."),
		"acknowledged_changes":     withDescription(&jsonschema.Schema{OneOf: []*jsonschema.Schema{acknowledgement, &jsonschema.Schema{Type: "null"}}}, "Optional acknowledgement of issue and event changes to prevent loss on concurrent updates."),
		"artifacts":                withDescription(schemaArtifacts(), "Optional artifact references (links, files, or other work products) created or discovered during the attempt."),
		"idempotency_key":          withDescription(nullableBoundedStringSchema(128), "Optional key for idempotent retries; replays exact response for identical normalized requests."),
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
	return &jsonschema.Schema{Type: "object", OneOf: []*jsonschema.Schema{typedSchema[createIssueCompactOutput](), legacyFullViewBranch(typedSchema[issueDTO]())}}
}

func schemaUpdateIssueUnion() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", OneOf: []*jsonschema.Schema{typedSchema[updateIssueCompactOutput](), legacyFullViewBranch(typedSchema[updateIssueOutput]())}}
}

func schemaArchiveIssueUnion() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", OneOf: []*jsonschema.Schema{typedSchema[archiveIssueCompactOutput](), legacyFullViewBranch(typedSchema[issueDTO]())}}
}

func schemaClaimIssueUnion() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", OneOf: []*jsonschema.Schema{typedSchema[claimIssueCompactOutput](), legacyFullViewBranch(typedSchema[claimIssueOutput]())}}
}

func schemaFinishAttemptUnion() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", OneOf: []*jsonschema.Schema{typedSchema[finishAttemptCompactOutput](), legacyFullViewBranch(typedSchema[finishAttemptOutput]())}}
}

// schemaIssueListItem describes one list_issues item. list_issues returns one
// of two item shapes depending on the request's view (compact, the default,
// or full), so this schema is hand-built rather than derived from a single Go
// type via typedSchema: its required fields are exactly the compact
// projection (identifiers, title, type, status, effective_status, priority,
// claimability, blocker count, labels, version, updated_at), and every
// full-only field (description, acceptance_criteria, parent_issue_id,
// blocked_reason, created_at, closed_at, archived_at, active_attempt_id) is
// declared as an optional property. A compact item satisfies this schema by
// omitting the optional properties; a full item satisfies it by including
// them all.
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
		"version":                  integerSchema(),
		"updated_at":               stringSchema(),
		// Present only when view: "full" is requested.
		"description":         nullableStringSchema(),
		"acceptance_criteria": nullableStringSchema(),
		"parent_issue_id":     nullableStringSchema(),
		"blocked_reason":      nullableStringSchema(),
		"created_at":          stringSchema(),
		"closed_at":           nullableStringSchema(),
		"archived_at":         nullableStringSchema(),
		"active_attempt_id":   nullableStringSchema(),
	}
	return object(properties,
		"id", "display_id", "sequence_no", "type", "title", "status", "priority",
		"effective_status", "unresolved_blocker_count", "is_blocked", "is_claimable",
		"labels", "version", "updated_at",
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

// schemaReservation describes one reservation in both the shape every
// mutation and list tool returns and get_resource_reservation's own
// compact/full toggle. Like schemaIssueListItem, this is hand-built rather
// than derived from reservationDTO via typedSchema: comparison_value and
// version are declared as optional properties (present only for
// get_resource_reservation's view=full) rather than expressed as a second
// schema, so one shape covers every caller.
func schemaReservation() *jsonschema.Schema {
	properties := map[string]*jsonschema.Schema{
		"id":             stringSchema(),
		"issue_id":       stringSchema(),
		"attempt_id":     stringSchema(),
		"kind":           stringSchema(),
		"display_value":  stringSchema(),
		"status":         stringSchema(),
		"created_at":     stringSchema(),
		"released_at":    nullableStringSchema(),
		"release_reason": nullableStringSchema(),
		// Present only when get_resource_reservation's view: "full" is requested.
		"comparison_value": stringSchema(),
		"version":          integerSchema(),
	}
	return object(properties,
		"id", "issue_id", "attempt_id", "kind", "display_value", "status", "created_at",
	)
}

func schemaReserveResourcesOutput() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"reservations": &jsonschema.Schema{Type: "array", Items: schemaReservation()},
		"next_actions": stringsSchema(),
	}, "reservations", "next_actions")
}

func schemaReleaseResourcesOutput() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"reservations": &jsonschema.Schema{Type: "array", Items: schemaReservation()},
		"next_actions": stringsSchema(),
	}, "reservations", "next_actions")
}

func schemaReservationListOutput() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"items":        &jsonschema.Schema{Type: "array", Items: schemaReservation()},
		"next_cursor":  nullableStringSchema(),
		"has_more":     booleanSchema(),
		"next_actions": stringsSchema(),
	}, "items", "next_cursor", "has_more", "next_actions")
}

func schemaGetResourceReservationOutput() *jsonschema.Schema { return schemaReservation() }

// rawMessageSchema overrides reflection's default translation of
// json.RawMessage ([]byte underneath) into an array-of-integers schema:
// json.RawMessage's custom MarshalJSON/UnmarshalJSON pass the underlying
// bytes through as literal embedded JSON, so on the wire it's always
// whatever JSON value it holds -- an object, for every event payload this
// codebase produces -- never an array of numbers. An empty schema accepts
// any JSON value, which is accurate for a field whose shape genuinely
// varies by event_type (see ISSUE-199: this drift was invisible because
// success() never runs the SDK's own output validation).
var rawMessageSchema = &jsonschema.Schema{}

func typedSchema[T any]() *jsonschema.Schema {
	schema, err := jsonschema.ForType(reflect.TypeFor[T](), &jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[json.RawMessage](): rawMessageSchema,
		},
	})
	if err != nil {
		panic(err)
	}
	return schema
}

// --- ISSUE-174: workflow policy and gate schemas ---

func schemaPolicyRequirement() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"key":                  boundedStringSchema(64),
		"kind":                 enumSchema("issue_field_nonblank", "attempt_evidence", "review_approval"),
		"field":                boundedStringSchema(64),
		"evidence_key":         boundedStringSchema(64),
		"purpose":              boundedStringSchema(64),
		"allow_not_applicable": booleanSchema(),
	}, "key", "kind")
}

func schemaPolicySelector() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"issue_types": &jsonschema.Schema{Type: "array", Items: enumSchema(domain.IssueTypeNames()...), UniqueItems: true},
		"labels_all":  boundedStringsSchema(domain.MaxLabelsPerIssue, domain.MaxLabelNameRunes),
	})
}

func schemaManageWorkflowPolicy() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"action":           enumSchema("create", "update", "archive"),
		"policy_id":        nullableBoundedStringSchema(26),
		"expected_version": integerSchema(),
		"selector":         schemaPolicySelector(),
		"requirements": &jsonschema.Schema{
			Type: "array", Items: schemaPolicyRequirement(), MaxItems: intPointer(domain.MaxPolicyRequirements),
		},
		"idempotency_key": nullableBoundedStringSchema(128),
	}, "action"))
}

func schemaGetWorkflowPolicy() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"policy_id": boundedStringSchema(26),
		"view":      enumSchema("compact", "full"),
	}, "policy_id"))
}

func schemaListWorkflowPolicies() *jsonschema.Schema {
	limit := boundedIntegerSchema(0, domain.MaxWorkflowPolicyListLimit)
	limit.Description = "0 uses the default limit of 50; maximum is 100."
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"status": enumSchema("active", "archived"),
		"limit":  limit,
		"cursor": nullableBoundedStringSchema(4096),
	}))
}

func schemaEvaluateGates() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"issue_id":          issueIdentifierSchema(),
		"enforcement_point": enumSchema("claim_work", "complete_work_to_review", "complete_work_to_done", "approve_review"),
		"attempt_id":        nullableBoundedStringSchema(26),
		"review_target_id":  nullableBoundedStringSchema(26),
	}, "issue_id", "enforcement_point"))
}

func schemaWorkflowPolicyOutput() *jsonschema.Schema { return typedSchema[workflowPolicyDTO]() }
func schemaWorkflowPolicyListOutput() *jsonschema.Schema {
	return typedSchema[workflowPolicyListOutput]()
}
func schemaEvaluateGatesOutput() *jsonschema.Schema { return typedSchema[evaluateGatesOutput]() }

func schemaSubmitGateEvidence() *jsonschema.Schema {
	return withAgentSessionHandle(object(map[string]*jsonschema.Schema{
		"attempt_id":      boundedStringSchema(26),
		"lease_token":     boundedStringSchema(512),
		"key":             boundedStringSchema(64),
		"result":          enumSchema("satisfied", "not_applicable"),
		"summary":         boundedStringSchema(2_000),
		"details":         boundedStringSchema(50_000),
		"artifact_ids":    boundedStringsSchema(20, 26),
		"idempotency_key": nullableBoundedStringSchema(128),
	}, "attempt_id", "lease_token", "key", "result", "summary"))
}

func schemaSubmitGateEvidenceOutput() *jsonschema.Schema {
	return typedSchema[submitGateEvidenceOutput]()
}
