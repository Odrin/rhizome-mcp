package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// The Claude Code plugin ships a copy of the canonical .github/skills tree
// because plugin skills are only discovered from a skills/ directory at the
// plugin root, while the skills CLI convention and the guidesync generator
// target .github/skills. The copy must never drift; refresh it with
// scripts/sync-plugin-skills.sh.
func TestPluginSkillsMatchCanonicalSkills(t *testing.T) {
	canonical := collectSkillFiles(t, filepath.Join(".github", "skills"))
	plugin := collectSkillFiles(t, filepath.Join("plugins", "rhizome-mcp", "skills"))

	for rel, canonicalContent := range canonical {
		pluginContent, ok := plugin[rel]
		if !ok {
			t.Errorf("missing from plugin copy: %s (run scripts/sync-plugin-skills.sh)", rel)
			continue
		}
		if !bytes.Equal(canonicalContent, pluginContent) {
			t.Errorf("plugin copy differs for %s (run scripts/sync-plugin-skills.sh)", rel)
		}
	}
	for rel := range plugin {
		if _, ok := canonical[rel]; !ok {
			t.Errorf("stale file in plugin copy: %s (run scripts/sync-plugin-skills.sh)", rel)
		}
	}
}

func TestPluginManifestsParse(t *testing.T) {
	for _, path := range []string{
		filepath.Join(".claude-plugin", "marketplace.json"),
		filepath.Join("plugins", "rhizome-mcp", ".claude-plugin", "plugin.json"),
		filepath.Join("plugins", "rhizome-mcp", ".mcp.json"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Errorf("%s is not valid JSON: %v", path, err)
		}
	}

	raw, err := os.ReadFile(filepath.Join("plugins", "rhizome-mcp", ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	var manifest struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse plugin.json: %v", err)
	}
	// The plugin name is an immutable install slug; renaming breaks existing
	// installs with plugin-not-found.
	if manifest.Name != "rhizome-mcp" {
		t.Errorf("plugin name changed to %q; it must remain rhizome-mcp", manifest.Name)
	}
}

func collectSkillFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() == ".DS_Store" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[rel] = content
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(files) == 0 {
		t.Fatalf("no files found under %s", root)
	}
	return files
}
