package testutil

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// PlaceholderULIDs are syntactically valid canonical ULIDs (Crockford base32,
// excluding I/L/O/U) used as generic placeholders for pattern-constrained
// identifier fields that require a bare ULID rather than an ISSUE-N display ID.
var PlaceholderULIDs = []string{
	"01ARZ3NDEKTSV4RRFFQ69G5FAV",
	"01ARZ3NDEKTSV4RRFFQ69G5FAW",
	"01ARZ3NDEKTSV4RRFFQ69G5FAX",
	"01ARZ3NDEKTSV4RRFFQ69G5FAY",
	"01ARZ3NDEKTSV4RRFFQ69G5FAZ",
}

// PlaceholderValue derives a schema-conformant placeholder for one property
// schema. It favors declared enum values, then pattern-aware identifiers, then
// a generic value for the declared JSON type. Fidelity beyond "passes JSON
// schema validation" is not required: business-level rejections downstream of
// the handler are an acceptable, non-failing outcome for this test.
func PlaceholderValue(schema *jsonschema.Schema, counter *int) any {
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
		return PlaceholderULIDs[(*counter-1)%len(PlaceholderULIDs)]
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

// DecodeInputSchema decodes the client-side JSON representation of a tool's
// input schema (a map[string]any, per the SDK's Tool.InputSchema contract)
// back into a typed jsonschema.Schema for property introspection.
func DecodeInputSchema(tool *sdkmcp.Tool) (*jsonschema.Schema, error) {
	if tool.InputSchema == nil {
		return nil, nil
	}
	return DecodeSchema(tool.InputSchema)
}

// DecodeSchema decodes an already-deserialized input schema (as delivered by a
// raw JSON-RPC tools/list response, where there is no *sdkmcp.Tool to read
// from) into the same jsonschema.Schema that DecodeInputSchema produces, so
// both transports derive placeholder arguments from identical type, enum and
// pattern information.
func DecodeSchema(raw any) (*jsonschema.Schema, error) {
	if raw == nil {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("marshal input schema: %w", err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("unmarshal input schema: %w", err)
	}
	return &schema, nil
}
