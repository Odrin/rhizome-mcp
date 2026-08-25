package mcp_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// Catalog byte budgets. The tool catalog is part of every agent session's
// fixed token cost — a client sends the entire tools/list result to the model
// before any work happens — so it gets the same treatment docs/03 section 3
// gives response payloads: a documented budget enforced by a test. The
// per-tool budget keeps any single tool from quietly ballooning; the total
// budget keeps the whole full-profile catalog bounded. Measured after the
// catalog-compaction change: ~96 KiB total, largest tool (get_work_context)
// ~11 KiB; the budgets leave headroom for organic growth without allowing a
// regression to the pre-compaction ~129 KiB catalog.
const (
	catalogTotalByteBudget   = 112 * 1024
	catalogPerToolByteBudget = 16 * 1024
)

// TestToolCatalogStaysWithinByteBudget serializes every advertised tool
// definition (name, description, schemas, annotations) exactly as tools/list
// delivers it and asserts the documented catalog byte budgets hold.
func TestToolCatalogStaysWithinByteBudget(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "catalog-budget.db"))
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

	total := 0
	for _, tool := range tools.Tools {
		data, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("marshal %s: %v", tool.Name, err)
		}
		if len(data) > catalogPerToolByteBudget {
			t.Errorf("tool %s definition = %d bytes, want <= %d bytes (documented per-tool budget)", tool.Name, len(data), catalogPerToolByteBudget)
		}
		total += len(data)
	}
	if total > catalogTotalByteBudget {
		t.Errorf("tool catalog = %d bytes across %d tools, want <= %d bytes (documented budget)", total, len(tools.Tools), catalogTotalByteBudget)
	}
	t.Logf("tool catalog = %d bytes across %d tools (budget %d bytes)", total, len(tools.Tools), catalogTotalByteBudget)
}

// TestAdvertisedOutputSchemasAreCompactProjections asserts the structural
// invariant behind the catalog budget: no advertised output schema carries
// per-object `required` arrays or `additionalProperties` — validator-only
// strictness that repeats every property name without informing an agent.
// The strict originals (with both intact) remain the validation contract;
// see advertisedOutputSchema and the output-conformance suite.
func TestAdvertisedOutputSchemasAreCompactProjections(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "catalog-projection.db"))
	defer db.Close(ctx)
	client, stop := newClient(t, composeServices(t, db, source))
	defer stop()

	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	for _, tool := range tools.Tools {
		if tool.OutputSchema == nil {
			continue
		}
		data, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatalf("marshal %s output schema: %v", tool.Name, err)
		}
		var tree any
		if err := json.Unmarshal(data, &tree); err != nil {
			t.Fatalf("unmarshal %s output schema: %v", tool.Name, err)
		}
		for _, key := range []string{"required", "additionalProperties", "oneOf"} {
			if schemaTreeContainsKey(tree, key) {
				t.Errorf("%s advertised output schema contains %q; advertised outputs must be compact projections", tool.Name, key)
			}
		}
	}
}

func schemaTreeContainsKey(node any, key string) bool {
	switch typed := node.(type) {
	case map[string]any:
		if _, ok := typed[key]; ok {
			return true
		}
		for _, value := range typed {
			if schemaTreeContainsKey(value, key) {
				return true
			}
		}
	case []any:
		for _, value := range typed {
			if schemaTreeContainsKey(value, key) {
				return true
			}
		}
	}
	return false
}
