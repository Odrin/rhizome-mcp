package cli

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// flagRegistration matches every flag actually registered on a FlagSet in
// cli.go, e.g. fs.String("format", ...) or fs.Bool("dry-run", ...).
var flagRegistration = regexp.MustCompile(`fs\.(?:String|Bool|Int|Int64|Uint|Float64|Duration|Var)\(\s*"([a-zA-Z0-9-]+)"`)

// TestUsageListsEveryCommandAndFlag is the guard that makes the command table
// worth having. It reads the flag registrations out of the SOURCE rather than
// out of the table, so a flag added to a FlagSet without a matching usage line
// fails here. Deriving the expectations from the table instead would only
// assert the table agrees with itself.
func TestUsageListsEveryCommandAndFlag(t *testing.T) {
	cli := New(Services{}, nil, nil, nil, nil)
	usage := cli.usage()

	for _, name := range CommandNames() {
		if !strings.Contains(usage, " "+name) {
			t.Errorf("usage() does not mention command %q:\n%s", name, usage)
		}
	}

	source, err := os.ReadFile("cli.go")
	if err != nil {
		t.Fatalf("read cli.go: %v", err)
	}
	matches := flagRegistration.FindAllStringSubmatch(string(source), -1)
	if len(matches) == 0 {
		t.Fatal("no flag registrations found in cli.go; the scan regexp is broken, not the usage text")
	}

	seen := map[string]bool{}
	var missing []string
	for _, match := range matches {
		name := match[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		if !strings.Contains(usage, "--"+name) {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("flags registered in cli.go but absent from usage(): %v\n%s", missing, usage)
	}
}

// TestIssueSummaryMatchesMCPIssueFields pins the CLI JSON projection against
// the MCP contract. The two drifted apart once already: the CLI emitted
// parent_id where MCP emits parent_issue_id (ISSUE-207).
//
// intentionalCLIDifferences is deliberately tiny. Every entry is a decision
// that has been made and documented, not a place where drift is tolerated.
func TestIssueSummaryMatchesMCPIssueFields(t *testing.T) {
	intentionalCLIDifferences := map[string]string{
		// The CLI flattens labels to names. It is inspection tooling, where a
		// label id is not actionable, and the flattening is documented in
		// README and docs/05 section 14. The key itself must still be present
		// and identically named, so only the value SHAPE differs.
		"labels": "CLI emits []string of names; MCP emits label objects",
	}

	// Populated, not zero-valued: omitempty fields such as parent_issue_id
	// vanish from a zero value, which would make this test miss a rename.
	parent := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	cliKeys := jsonKeysOf(t, IssueSummary{ParentIssueID: &parent})

	// The MCP issue list item field set: issueDTO embedded in issueListItemDTO
	// (internal/adapters/mcp/dto.go). Transcribed because those DTOs are
	// unexported; keep in step if they change.
	mcpKeys := []string{
		"id", "display_id", "sequence_no", "type", "title", "description",
		"acceptance_criteria", "status", "priority", "parent_issue_id",
		"blocked_reason", "version", "created_at", "updated_at", "closed_at",
		"archived_at", "labels", "effective_status", "unresolved_blocker_count",
		"is_blocked", "is_claimable", "active_attempt_id",
	}

	mcpSet := map[string]bool{}
	for _, key := range mcpKeys {
		mcpSet[key] = true
	}
	cliSet := map[string]bool{}
	for _, key := range cliKeys {
		cliSet[key] = true
	}

	// The CLI is a summary projection, so it may omit MCP fields. What it may
	// NOT do is emit a field under a different name than the MCP contract uses
	// — that is exactly how parent_id/parent_issue_id drifted apart.
	var unexplained []string
	for _, key := range cliKeys {
		if mcpSet[key] {
			continue
		}
		if _, allowed := intentionalCLIDifferences[key]; allowed {
			continue
		}
		unexplained = append(unexplained, key)
	}
	sort.Strings(unexplained)
	if len(unexplained) > 0 {
		t.Errorf("cli.IssueSummary emits %v, which the MCP contract does not name and the allow-list does not explain; a CLI-only field name is how the projection drifts", unexplained)
	}

	// A stale allow-list is its own kind of drift.
	for key := range intentionalCLIDifferences {
		if !cliSet[key] {
			t.Errorf("allow-list names %q, which cli.IssueSummary no longer emits; remove the entry", key)
		}
	}

	if !cliSet["parent_issue_id"] || cliSet["parent_id"] {
		t.Errorf("cli.IssueSummary must emit parent_issue_id and not parent_id; got %v", cliKeys)
	}
}

func jsonKeysOf(t *testing.T, value any) []string {
	t.Helper()
	// Marshal rather than reflect over tags so omitempty and embedding are
	// resolved the same way a consumer sees them.
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %T: %v", value, err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode %T: %v", value, err)
	}
	keys := make([]string, 0, len(decoded))
	for key := range decoded {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		t.Fatalf("%T marshalled to no keys", value)
	}
	return keys
}
