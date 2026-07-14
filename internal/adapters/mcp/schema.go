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
func booleanSchema() *jsonschema.Schema { return &jsonschema.Schema{Type: "boolean"} }
func stringsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "array", Items: stringSchema()}
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

func schemaProjectOutput() *jsonschema.Schema   { return typedSchema[projectOutput]() }
func schemaLabelListOutput() *jsonschema.Schema { return typedSchema[labelListOutput]() }
func schemaIssueOutput() *jsonschema.Schema     { return typedSchema[issueDTO]() }
func schemaUpdateOutput() *jsonschema.Schema    { return typedSchema[updateIssueOutput]() }
func schemaIssueListOutput() *jsonschema.Schema { return typedSchema[issueListOutput]() }

func typedSchema[T any]() *jsonschema.Schema {
	schema, err := jsonschema.ForType(reflect.TypeFor[T](), &jsonschema.ForOptions{})
	if err != nil {
		panic(err)
	}
	return schema
}
