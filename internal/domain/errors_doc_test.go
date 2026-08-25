package domain

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	errorCodesDocPath = "../../docs/03-mcp-tools.md"
	errorCodesSection = "## 13. Error codes"
	docsDirPath       = "../../docs"
)

var (
	codeConstantPattern = regexp.MustCompile(`(?m)^\tCode[A-Za-z]+\s*=\s*"([A-Z_]+)"$`)
	specDocPattern      = regexp.MustCompile(`^\d{2}-[a-z0-9-]+\.md$`)
)

// TestErrorCodesDocMatchesDomain fails when the documented error-code catalog
// in docs/03 section 13 drifts from the Code* constants in errors.go.
func TestErrorCodesDocMatchesDomain(t *testing.T) {
	declared := declaredErrorCodes(t)
	documented := documentedErrorCodes(t)

	if len(declared) == 0 {
		t.Fatal("no Code* constants parsed from errors.go")
	}
	if strings.Join(declared, "\n") == strings.Join(documented, "\n") {
		return
	}
	for _, code := range missingFrom(documented, declared) {
		t.Errorf("error code %q is declared in errors.go but absent from %s %s", code, errorCodesDocPath, errorCodesSection)
	}
	for _, code := range missingFrom(declared, documented) {
		t.Errorf("error code %q is documented in %s %s but no Code* constant declares it", code, errorCodesDocPath, errorCodesSection)
	}
	if !sort.StringsAreSorted(documented) {
		t.Errorf("documented error codes must be sorted; got %v", documented)
	}
}

// TestSpecIndexCoversEveryDoc fails when a docs/NN-*.md specification document
// is missing from a file that enumerates the specification set.
func TestSpecIndexCoversEveryDoc(t *testing.T) {
	documents := specDocuments(t)
	if len(documents) == 0 {
		t.Fatal("no specification documents found")
	}
	for _, index := range []string{"../../SPEC.md", "../../AGENT_BRIEF.md", "../../README.md"} {
		content := readRepoFile(t, index)
		for _, document := range documents {
			if !strings.Contains(content, document) {
				t.Errorf("%s does not reference docs/%s", index, document)
			}
		}
	}
}

func declaredErrorCodes(t *testing.T) []string {
	t.Helper()
	source := readRepoFile(t, "errors.go")
	var codes []string
	for _, match := range codeConstantPattern.FindAllStringSubmatch(source, -1) {
		codes = append(codes, match[1])
	}
	sort.Strings(codes)
	return codes
}

func documentedErrorCodes(t *testing.T) []string {
	t.Helper()
	content := readRepoFile(t, errorCodesDocPath)
	sectionStart := strings.Index(content, errorCodesSection)
	if sectionStart < 0 {
		t.Fatalf("section %q not found in %s", errorCodesSection, errorCodesDocPath)
	}
	section := content[sectionStart:]
	blockStart := strings.Index(section, "```text")
	if blockStart < 0 {
		t.Fatalf("no fenced code block in section %q", errorCodesSection)
	}
	block := section[blockStart+len("```text"):]
	blockEnd := strings.Index(block, "```")
	if blockEnd < 0 {
		t.Fatalf("unterminated code block in section %q", errorCodesSection)
	}
	var codes []string
	for _, line := range strings.Split(block[:blockEnd], "\n") {
		if line = strings.TrimSpace(line); line != "" {
			codes = append(codes, line)
		}
	}
	return codes
}

func specDocuments(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(docsDirPath)
	if err != nil {
		t.Fatalf("read %s: %v", docsDirPath, err)
	}
	var documents []string
	for _, entry := range entries {
		if !entry.IsDir() && specDocPattern.MatchString(entry.Name()) {
			documents = append(documents, entry.Name())
		}
	}
	sort.Strings(documents)
	return documents
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func missingFrom(haystack, needles []string) []string {
	present := make(map[string]struct{}, len(haystack))
	for _, value := range haystack {
		present[value] = struct{}{}
	}
	var missing []string
	for _, needle := range needles {
		if _, ok := present[needle]; !ok {
			missing = append(missing, needle)
		}
	}
	return missing
}
