package sqlite

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// destinationContentExemptTables are the tables that deliberately do not make
// a destination "non-empty" for logical import. Keeping them listed here
// rather than implied means adding a table to the schema forces an explicit
// decision about which side it belongs on.
var destinationContentExemptTables = map[string]string{
	"projects":            "the destination project row always exists; import updates it rather than inserting one",
	"agent_sessions":      "sessions are excluded from interchange (docs/07 §5); counting them would make any connected project look occupied",
	"idempotency_records": "runtime replay bookkeeping, explicitly excluded (docs/07 §5)",
	"schema_migrations":   "migration state, explicitly excluded (docs/07 §5)",
	"search_index":        "a derived FTS index rebuilt from the content tables, explicitly excluded (docs/07 §5)",
}

var (
	createTablePattern = regexp.MustCompile(`(?mi)^CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_]+)`)
	dropTablePattern   = regexp.MustCompile(`(?mi)^DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?([a-z_]+)`)
	virtualPattern     = regexp.MustCompile(`(?mi)^CREATE\s+VIRTUAL\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_]+)`)
)

// TestLogicalProjectImportDestinationContentCoversEveryDurableTable is the
// ISSUE-233 drift guard. The empty-destination guard silently stopped at the
// tables that existed when logical interchange was written, so a project
// holding only workflow policies was treated as empty and imported into. A
// list that has to be maintained by hand will drift again; this test reads
// the migration SQL and fails whenever a table exists that is neither counted
// as durable destination content nor explicitly exempted above.
func TestLogicalProjectImportDestinationContentCoversEveryDurableTable(t *testing.T) {
	entries, err := filepath.Glob(filepath.Join("..", "..", "migrations", "sql", "*.sql"))
	if err != nil {
		t.Fatalf("glob migration SQL: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("found no migration SQL files to read")
	}
	sort.Strings(entries)

	live := make(map[string]struct{})
	for _, entry := range entries {
		content, err := os.ReadFile(entry)
		if err != nil {
			t.Fatalf("read %s: %v", entry, err)
		}
		text := string(content)
		for _, match := range createTablePattern.FindAllStringSubmatch(text, -1) {
			live[strings.ToLower(match[1])] = struct{}{}
		}
		for _, match := range virtualPattern.FindAllStringSubmatch(text, -1) {
			live[strings.ToLower(match[1])] = struct{}{}
		}
		for _, match := range dropTablePattern.FindAllStringSubmatch(text, -1) {
			delete(live, strings.ToLower(match[1]))
		}
	}

	counted := make(map[string]struct{}, len(logicalProjectImportDestinationContentTables))
	for _, table := range logicalProjectImportDestinationContentTables {
		if _, duplicate := counted[table]; duplicate {
			t.Fatalf("%q is listed twice as destination content", table)
		}
		counted[table] = struct{}{}
		if _, exists := live[table]; !exists {
			t.Fatalf("destination content names %q, which the migrations do not create (or later drop)", table)
		}
		if _, exempt := destinationContentExemptTables[table]; exempt {
			t.Fatalf("%q is both counted as destination content and exempted", table)
		}
	}

	for table := range live {
		if _, ok := counted[table]; ok {
			continue
		}
		if _, ok := destinationContentExemptTables[table]; ok {
			continue
		}
		t.Fatalf("table %q is neither counted as durable destination content nor exempted; "+
			"an import into a project holding only %s rows would silently merge", table, table)
	}
}

// TestLogicalProjectImportDestinationContentQueryIsOneAuthoritativeDefinition
// covers ISSUE-233 AC2: the preflight check and the in-transaction
// race-closing check must read the same definition, not two hand-maintained
// copies of it.
func TestLogicalProjectImportDestinationContentQueryIsOneAuthoritativeDefinition(t *testing.T) {
	for _, table := range logicalProjectImportDestinationContentTables {
		if !strings.Contains(logicalProjectImportDestinationContentQuery, "SELECT 1 FROM "+table) {
			t.Fatalf("destination content query does not probe %q: %s", table, logicalProjectImportDestinationContentQuery)
		}
	}
	if !strings.HasPrefix(logicalProjectImportDestinationContentQuery, "SELECT EXISTS (") {
		t.Fatalf("destination content query = %s, want an EXISTS probe", logicalProjectImportDestinationContentQuery)
	}
}
