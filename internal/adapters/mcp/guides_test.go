package mcp_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// repoRoot returns the absolute path to the repository root by finding the
// directory containing this test file and walking up to find the repo root.
func repoRoot(t *testing.T) string {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("Could not determine test file location")
	}
	// Walk up from internal/adapters/mcp to repo root
	repoDir := filepath.Join(filepath.Dir(testFile), "..", "..", "..")
	absPath, err := filepath.Abs(repoDir)
	if err != nil {
		t.Fatalf("Could not get absolute path: %v", err)
	}
	return absPath
}

// TestGuidesSkillReferencesMatchEmbeddedGuides verifies that the skill reference
// files are byte-identical to the embedded guide assets and that they are served
// correctly through the MCP server.
func TestGuidesSkillReferencesMatchEmbeddedGuides(t *testing.T) {
	root := repoRoot(t)
	guideFiles := []string{"agent-workflow.md", "issue-lifecycle.md", "multi-agent-handoff.md"}

	for _, file := range guideFiles {
		// Read from guide_assets
		assetPath := filepath.Join(root, "internal/adapters/mcp/guide_assets", file)
		assetBytes, err := os.ReadFile(assetPath)
		if err != nil {
			t.Fatalf("Failed to read asset %s: %v", assetPath, err)
		}

		// Read from skill references
		skillPath := filepath.Join(root, ".github/skills/rhizome-task-workflow/references", file)
		skillBytes, err := os.ReadFile(skillPath)
		if err != nil {
			t.Fatalf("Failed to read skill reference %s: %v", skillPath, err)
		}

		if !bytes.Equal(assetBytes, skillBytes) {
			t.Errorf("Skill reference %s does not match embedded asset. Run: go generate ./internal/adapters/mcp/...", file)
		}
	}

	// Verify the reference directory contains exactly these three files
	refDir := filepath.Join(root, ".github/skills/rhizome-task-workflow/references")
	entries, err := os.ReadDir(refDir)
	if err != nil {
		t.Fatalf("Failed to read reference directory: %v", err)
	}

	foundFiles := make(map[string]bool)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			foundFiles[entry.Name()] = true
		}
	}

	for _, expected := range guideFiles {
		if !foundFiles[expected] {
			t.Errorf("Expected file %s not found in references directory", expected)
		}
	}
	if len(foundFiles) != len(guideFiles) {
		t.Errorf("Reference directory has %d files, expected %d. Found: %v", len(foundFiles), len(guideFiles), foundFiles)
	}

	// Verify the guides are served correctly through the MCP server
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "guides-test.db"))
	defer db.Close(context.Background())
	options := composeServices(t, db, source)
	client, stop := newClient(t, options)
	defer stop()

	resources, err := client.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}

	wantURIs := []string{
		"rhizome://guides/agent-workflow",
		"rhizome://guides/issue-lifecycle",
		"rhizome://guides/multi-agent-handoff",
	}

	if len(resources.Resources) != len(wantURIs) {
		t.Errorf("Expected %d resources, got %d", len(wantURIs), len(resources.Resources))
	}

	for i, resource := range resources.Resources {
		if i >= len(wantURIs) {
			break
		}
		if resource.URI != wantURIs[i] {
			t.Errorf("Resource %d URI = %q, want %q", i, resource.URI, wantURIs[i])
		}

		read, err := client.ReadResource(context.Background(), &sdkmcp.ReadResourceParams{URI: resource.URI})
		if err != nil {
			t.Fatalf("ReadResource(%q) error = %v", resource.URI, err)
		}

		if len(read.Contents) != 1 {
			t.Errorf("ReadResource(%q) returned %d contents, want 1", resource.URI, len(read.Contents))
			continue
		}

		servedContent := read.Contents[0].Text
		expectedFile := ""
		for i, expectedURI := range wantURIs {
			if expectedURI == resource.URI {
				expectedFile = guideFiles[i]
				break
			}
		}

		if expectedFile != "" {
			expectedBytes, err := os.ReadFile(filepath.Join(root, "internal/adapters/mcp/guide_assets", expectedFile))
			if err != nil {
				t.Fatalf("Failed to read expected file %s: %v", expectedFile, err)
			}
			expectedContent := string(expectedBytes)

			if servedContent != expectedContent {
				t.Errorf("Served content for %s does not match guide asset", resource.URI)
			}
		}
	}
}

// TestGuidesDocumentedToolCountsMatchCatalog extracts tool counts from documentation
// and verifies they match the actual tool catalog.
func TestGuidesDocumentedToolCountsMatchCatalog(t *testing.T) {
	root := repoRoot(t)

	// Read README.md
	readmeBytes, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("Failed to read README.md: %v", err)
	}
	readmeText := string(readmeBytes)

	// Read docs/03-mcp-tools.md
	docsBytes, err := os.ReadFile(filepath.Join(root, "docs/03-mcp-tools.md"))
	if err != nil {
		t.Fatalf("Failed to read docs/03-mcp-tools.md: %v", err)
	}
	docsText := string(docsBytes)

	// Extract README: "The server exposes (\d+) tools"
	readmeRe := regexp.MustCompile(`The server exposes (\d+) tools`)
	readmeMatches := readmeRe.FindAllStringSubmatch(readmeText, -1)
	if len(readmeMatches) != 1 {
		t.Fatalf("Expected exactly 1 match for server exposes tools in README.md, got %d", len(readmeMatches))
	}
	documentedFull := readmeMatches[0][1]

	// Extract docs: "exposes (\d+) full tools, (\d+) agent tools, (\d+) migration tools, and (\d+) read-only tools"
	docsCountsRe := regexp.MustCompile(`exposes (\d+) full tools, (\d+) agent tools, (\d+) migration tools, and (\d+) read-only tools`)
	docsCountsMatches := docsCountsRe.FindAllStringSubmatch(docsText, -1)
	if len(docsCountsMatches) != 1 {
		t.Fatalf("Expected exactly 1 match for tool counts in docs/03-mcp-tools.md, got %d", len(docsCountsMatches))
	}
	documentedFullDocs := docsCountsMatches[0][1]
	documentedAgent := docsCountsMatches[0][2]
	documentedMigration := docsCountsMatches[0][3]
	documentedReadOnly := docsCountsMatches[0][4]

	// Extract docs: "every group, all (\d+) tools"
	docsAllToolsRe := regexp.MustCompile(`every group, all (\d+) tools`)
	docsAllMatches := docsAllToolsRe.FindAllStringSubmatch(docsText, -1)
	if len(docsAllMatches) != 1 {
		t.Fatalf("Expected exactly 1 match for every group all tools in docs/03-mcp-tools.md, got %d", len(docsAllMatches))
	}
	documentedAllTools := docsAllMatches[0][1]

	// Verify consistency
	if documentedFull != documentedFullDocs || documentedFull != documentedAllTools {
		t.Errorf("Inconsistent full tool count: README=%s, docs (full)=%s, docs (all)=%s", documentedFull, documentedFullDocs, documentedAllTools)
	}

	// Extract agent profile count: "(**`agent`** ...(\d+) tools)"
	agentProfileRe := regexp.MustCompile(`(?m)-\s+\*\*.*?agent.*?\*\*\s+\((\d+) tools\)`)
	agentProfileMatches := agentProfileRe.FindAllStringSubmatch(docsText, -1)
	if len(agentProfileMatches) != 1 {
		t.Fatalf("Expected exactly 1 match for agent profile count in docs/03-mcp-tools.md, got %d", len(agentProfileMatches))
	}
	documentedAgentProfile := agentProfileMatches[0][1]

	if documentedAgent != documentedAgentProfile {
		t.Errorf("Inconsistent agent tool count: section count=%s, profile count=%s", documentedAgent, documentedAgentProfile)
	}

	// Extract migration profile count: "(**`migration`** ...(\d+) tools)"
	migrationProfileRe := regexp.MustCompile(`(?m)-\s+\*\*.*?migration.*?\*\*\s+\((\d+) tools\)`)
	migrationProfileMatches := migrationProfileRe.FindAllStringSubmatch(docsText, -1)
	if len(migrationProfileMatches) != 1 {
		t.Fatalf("Expected exactly 1 match for migration profile count in docs/03-mcp-tools.md, got %d", len(migrationProfileMatches))
	}
	documentedMigrationProfile := migrationProfileMatches[0][1]

	if documentedMigration != documentedMigrationProfile {
		t.Errorf("Inconsistent migration tool count: section count=%s, profile count=%s", documentedMigration, documentedMigrationProfile)
	}

	// Get actual tool counts from the catalog
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "tool-counts-test.db"))
	defer db.Close(context.Background())

	profiles := map[string]string{
		"full":      documentedFull,
		"agent":     documentedAgent,
		"migration": documentedMigration,
		"read-only": documentedReadOnly,
	}

	for profileName, expectedCount := range profiles {
		options := composeServices(t, db, source)
		options.ToolProfile = profileName
		client, stop := newClient(t, options)
		tools, err := client.ListTools(context.Background(), nil)
		if err != nil {
			stop()
			t.Fatalf("ListTools(%s) error = %v", profileName, err)
		}
		actualCount := fmt.Sprintf("%d", len(tools.Tools))
		if actualCount != expectedCount {
			stop()
			t.Errorf("Profile %s: documented count %s, actual count %s", profileName, expectedCount, actualCount)
			continue
		}
		stop()
	}
}

// TestGuidesDocumentedToolInventoryMatchesCatalog extracts the tool inventory
// from docs/03-mcp-tools.md section 3.1 and verifies it matches the catalog.
func TestGuidesDocumentedToolInventoryMatchesCatalog(t *testing.T) {
	root := repoRoot(t)

	docsBytes, err := os.ReadFile(filepath.Join(root, "docs/03-mcp-tools.md"))
	if err != nil {
		t.Fatalf("Failed to read docs/03-mcp-tools.md: %v", err)
	}
	docsText := string(docsBytes)

	// Find the section starting with "## 3.1. Tool inventory"
	inventoryStart := strings.Index(docsText, "## 3.1. Tool inventory")
	if inventoryStart == -1 {
		t.Fatalf("Could not find '## 3.1. Tool inventory' section in docs/03-mcp-tools.md")
	}

	// Find the first line starting with "### " after the section
	remaining := docsText[inventoryStart:]
	nextSection := strings.Index(remaining, "\n### ")
	if nextSection == -1 {
		nextSection = len(remaining)
	}

	inventorySection := remaining[:nextSection]

	// Parse lines matching ^(\d+)\. `([a-z_]+)`$
	toolPattern := regexp.MustCompile("(?m)^(\\d+)\\.\\s+`([a-z_]+)`$")
	matches := toolPattern.FindAllStringSubmatch(inventorySection, -1)

	if len(matches) == 0 {
		t.Fatalf("Could not find any tools in the inventory section")
	}

	// Verify ordinals run 1..N with no gaps
	lastOrdinal := 0
	documentedTools := make([]string, 0)
	for _, match := range matches {
		ordinal := 0
		fmt.Sscanf(match[1], "%d", &ordinal)
		if ordinal != lastOrdinal+1 {
			t.Errorf("Ordinal gap: expected %d, got %d", lastOrdinal+1, ordinal)
		}
		lastOrdinal = ordinal
		documentedTools = append(documentedTools, match[2])
	}

	// Get the actual tool catalog
	catalogTools := toolNamesFor(t, "")

	// Convert to sets for comparison
	documentedSet := make(map[string]bool)
	for _, name := range documentedTools {
		documentedSet[name] = true
	}

	catalogSet := make(map[string]bool)
	for _, name := range catalogTools {
		catalogSet[name] = true
	}

	// Find missing and extra tools
	var missing, extra []string
	for _, name := range catalogTools {
		if !documentedSet[name] {
			missing = append(missing, name)
		}
	}
	for _, name := range documentedTools {
		if !catalogSet[name] {
			extra = append(extra, name)
		}
	}

	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("Tool inventory mismatch in docs/03-mcp-tools.md section 3.1:")
		if len(missing) > 0 {
			t.Errorf("  Missing from documentation: %v", missing)
		}
		if len(extra) > 0 {
			t.Errorf("  Documented but not in catalog: %v", extra)
		}
	}

	if len(documentedTools) != len(catalogTools) {
		t.Errorf("Tool count mismatch: documented %d, catalog %d", len(documentedTools), len(catalogTools))
	}
}
