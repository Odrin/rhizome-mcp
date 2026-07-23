package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// placeholderULIDs are syntactically valid canonical ULIDs (Crockford base32,
// excluding I/L/O/U) used as generic placeholders for pattern-constrained
// identifier fields that require a bare ULID rather than an ISSUE-N display ID.
var placeholderULIDs = []string{
	"01ARZ3NDEKTSV4RRFFQ69G5FAV",
	"01ARZ3NDEKTSV4RRFFQ69G5FAW",
	"01ARZ3NDEKTSV4RRFFQ69G5FAX",
	"01ARZ3NDEKTSV4RRFFQ69G5FAY",
	"01ARZ3NDEKTSV4RRFFQ69G5FAZ",
}

// TestAdvertisedSchemaPropertiesAreNeverRejectedAsUnsupported iterates every
// tool in the advertised catalog and, for every optional input schema
// property, calls the tool with a minimal schema-valid input plus that one
// property set to a schema-conformant placeholder value. It fails if the
// handler rejects that property as unsupported (the ISSUE-66 bug shape: a
// property is declared in the published input schema but any handler-side
// rejection marks it UNSUPPORTED regardless of value). This is a generality
// regression test: it does not require the call to fully succeed, only that
// the specific advertised property is never the reason for an UNSUPPORTED
// rejection.
func TestAdvertisedSchemaPropertiesAreNeverRejectedAsUnsupported(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "schema-coverage.db"))
	defer db.Close(ctx)
	client, stop := newClient(t, composeServices(t, db, source))
	defer stop()

	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("no tools advertised")
	}

	for _, tool := range tools.Tools {
		tool := tool
		t.Run(tool.Name, func(t *testing.T) {
			schema := decodeInputSchema(t, tool)
			if schema == nil || len(schema.Properties) == 0 {
				return
			}
			required := make(map[string]bool, len(schema.Required))
			for _, name := range schema.Required {
				required[name] = true
			}
			var counter int
			base := make(map[string]any, len(schema.Required))
			for _, name := range schema.Required {
				base[name] = placeholderValue(schema.Properties[name], &counter)
			}
			tested := 0
			for name, propertySchema := range schema.Properties {
				if required[name] {
					continue
				}
				tested++
				input := make(map[string]any, len(base)+1)
				for key, value := range base {
					input[key] = value
				}
				input[name] = placeholderValue(propertySchema, &counter)
				result := call(t, client, tool.Name, input)
				if field, ok := unsupportedFieldRejected(t, result); ok && field == name {
					t.Fatalf("%s advertises %q in its input schema but its handler rejects it as unsupported: %#v",
						tool.Name, name, result)
				}
			}
			if tested == 0 {
				t.Skipf("%s has no optional properties to probe", tool.Name)
			}
		})
	}
}

// decodeInputSchema decodes the client-side JSON representation of a tool's
// input schema (a map[string]any, per the SDK's Tool.InputSchema contract)
// back into a typed jsonschema.Schema for property introspection.
func decodeInputSchema(t *testing.T, tool *sdkmcp.Tool) *jsonschema.Schema {
	t.Helper()
	if tool.InputSchema == nil {
		return nil
	}
	data, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal %s input schema: %v", tool.Name, err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("unmarshal %s input schema: %v", tool.Name, err)
	}
	return &schema
}

// placeholderValue derives a schema-conformant placeholder for one property
// schema. It favors declared enum values, then pattern-aware identifiers, then
// a generic value for the declared JSON type. Fidelity beyond "passes JSON
// schema validation" is not required: business-level rejections downstream of
// the handler are an acceptable, non-failing outcome for this test.
func placeholderValue(schema *jsonschema.Schema, counter *int) any {
	if schema == nil {
		return "placeholder"
	}
	if len(schema.Enum) > 0 {
		return schema.Enum[0]
	}
	if schema.Pattern != "" {
		*counter++
		if strings.Contains(schema.Pattern, "ISSUE-") {
			return fmt.Sprintf("ISSUE-%d", *counter)
		}
		return placeholderULIDs[(*counter-1)%len(placeholderULIDs)]
	}
	if len(schema.OneOf) > 0 {
		// null is a member of every OneOf union used by this catalog
		// (nullable acknowledgement/metadata shapes).
		return nil
	}
	types := schema.Types
	if len(types) == 0 && schema.Type != "" {
		types = []string{schema.Type}
	}
	for _, kind := range types {
		switch kind {
		case "string":
			return "placeholder"
		case "integer", "number":
			if schema.Minimum != nil {
				return *schema.Minimum
			}
			return float64(1)
		case "boolean":
			return true
		case "array":
			return []any{}
		case "object":
			return map[string]any{}
		}
	}
	return "placeholder"
}

// unsupportedFieldRejected reports the field named by a domain-error-shaped
// UNSUPPORTED detail in result, if any. A schema-level (protocol) rejection
// never carries this shape, so it is never mistaken for a handler-level
// unsupported-field rejection.
func unsupportedFieldRejected(t *testing.T, result *sdkmcp.CallToolResult) (string, bool) {
	t.Helper()
	if result == nil || !result.IsError || result.StructuredContent == nil {
		return "", false
	}
	var output struct {
		Details []struct {
			Field string `json:"field"`
			Code  string `json:"code"`
		} `json:"details"`
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	for _, detail := range output.Details {
		if detail.Code == "UNSUPPORTED" {
			return detail.Field, true
		}
	}
	return "", false
}
