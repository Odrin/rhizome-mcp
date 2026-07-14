package mcp

import (
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
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
func nullableStringSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Types: []string{"string", "null"}}
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
	return object(map[string]*jsonschema.Schema{
		"query": nullableStringSchema(), "limit": integerSchema(), "cursor": nullableStringSchema(),
	})
}

func schemaCreateIssue() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"type": stringSchema(), "title": stringSchema(), "description": nullableStringSchema(),
		"acceptance_criteria": nullableStringSchema(), "status": stringSchema(), "priority": stringSchema(),
		"parent_issue_id": nullableStringSchema(), "blocked_reason": nullableStringSchema(),
		"labels": stringsSchema(), "create_missing_labels": booleanSchema(), "idempotency_key": nullableStringSchema(),
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
		"issue_id": stringSchema(), "expected_version": integerSchema(), "changes": changes,
		"create_missing_labels": booleanSchema(), "idempotency_key": nullableStringSchema(),
	}, "issue_id", "expected_version", "changes")
}

func schemaGetIssue() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"issue_id": stringSchema(), "view": stringSchema(), "include": stringsSchema(),
		"limits": &jsonschema.Schema{Type: "object"},
	}, "issue_id")
}

func schemaListIssues() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"types": stringsSchema(), "statuses": stringsSchema(), "effective_statuses": stringsSchema(),
		"priorities": stringsSchema(), "labels": stringsSchema(), "parent_issue_id": nullableStringSchema(),
		"is_blocked":       &jsonschema.Schema{Types: []string{"boolean", "null"}},
		"is_claimable":     &jsonschema.Schema{Types: []string{"boolean", "null"}},
		"include_archived": booleanSchema(), "limit": integerSchema(), "cursor": nullableStringSchema(), "view": stringSchema(),
	})
}

func schemaArchiveIssue() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"issue_id": stringSchema(), "expected_version": integerSchema(), "idempotency_key": nullableStringSchema(),
	}, "issue_id", "expected_version")
}

func schemaManageIssueRelation() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"action":          enumSchema("add", "remove"),
		"source_issue_id": stringSchema(),
		"target_issue_id": stringSchema(),
		"relation_type":   enumSchema("blocks", "related_to", "duplicates"),
		"idempotency_key": nullableStringSchema(),
	}, "action", "source_issue_id", "target_issue_id", "relation_type")
}

func schemaGetIssueGraph() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"root_issue_id": stringSchema(), "depth": boundedIntegerSchema(0, 5),
		"direction":         enumSchema("outgoing", "incoming", "both"),
		"relation_types":    &jsonschema.Schema{Type: "array", Items: enumSchema("blocks", "related_to", "duplicates"), UniqueItems: true},
		"include_hierarchy": booleanSchema(), "include_terminal": booleanSchema(),
		"max_nodes": boundedIntegerSchema(1, 500), "view": enumSchema("compact"),
	}, "root_issue_id")
}

func schemaGetPlanningGraph() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"root_issue_id": nullableStringSchema(), "depth": boundedIntegerSchema(0, 5), "max_nodes": boundedIntegerSchema(1, 500),
		"include_review": booleanSchema(), "include_related": booleanSchema(),
	})
}

func schemaProjectOutput() *jsonschema.Schema   { return typedSchema[projectOutput]() }
func schemaLabelListOutput() *jsonschema.Schema { return typedSchema[labelListOutput]() }
func schemaIssueOutput() *jsonschema.Schema     { return typedSchema[issueDTO]() }
func schemaUpdateOutput() *jsonschema.Schema    { return typedSchema[updateIssueOutput]() }
func schemaIssueListOutput() *jsonschema.Schema { return typedSchema[issueListOutput]() }
func schemaManageIssueRelationOutput() *jsonschema.Schema {
	return typedSchema[manageIssueRelationOutput]()
}
func schemaGraphOutput() *jsonschema.Schema { return typedSchema[graphOutput]() }

func typedSchema[T any]() *jsonschema.Schema {
	schema, err := jsonschema.ForType(reflect.TypeFor[T](), &jsonschema.ForOptions{})
	if err != nil {
		panic(err)
	}
	return schema
}
