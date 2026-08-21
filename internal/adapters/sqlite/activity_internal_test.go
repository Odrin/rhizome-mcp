package sqlite

import (
	"testing"

	"rhizome-mcp/internal/domain"
)

// TestActivityRegistryMatchesDomainCategoriesAndEntityTypes proves
// activityRegistry is the single source of truth the ISSUE-216 refactor
// requires: every category in domain.AllActivityCategories has exactly one
// registry entry, every registry category/entity type passes the domain
// Valid() methods, and no two entries share a category or a rank.
func TestActivityRegistryMatchesDomainCategoriesAndEntityTypes(t *testing.T) {
	if len(activityRegistry) != len(domain.AllActivityCategories) {
		t.Fatalf("activityRegistry has %d entries, domain.AllActivityCategories has %d", len(activityRegistry), len(domain.AllActivityCategories))
	}
	seenCategories := make(map[domain.ActivityCategory]bool, len(activityRegistry))
	seenRanks := make(map[int]bool, len(activityRegistry))
	for _, spec := range activityRegistry {
		if seenCategories[spec.Category] {
			t.Fatalf("duplicate category %q in activityRegistry", spec.Category)
		}
		seenCategories[spec.Category] = true
		if seenRanks[spec.Rank] {
			t.Fatalf("duplicate rank %d in activityRegistry", spec.Rank)
		}
		seenRanks[spec.Rank] = true
		if spec.Rank < 1 {
			t.Fatalf("registry entry %q has non-positive rank %d", spec.Category, spec.Rank)
		}
		if !spec.Category.Valid() {
			t.Fatalf("registry category %q fails domain.ActivityCategory.Valid()", spec.Category)
		}
		if !spec.EntityType.Valid() {
			t.Fatalf("registry entity type %q fails domain.ActivityEntityType.Valid()", spec.EntityType)
		}
		if spec.Load == nil {
			t.Fatalf("registry entry %q has no Load func", spec.Category)
		}
	}
	for _, category := range domain.AllActivityCategories {
		if !seenCategories[category] {
			t.Fatalf("domain.AllActivityCategories has %q with no matching activityRegistry entry", category)
		}
	}
	if activityMaxRank != len(activityRegistry) {
		t.Fatalf("activityMaxRank = %d, want %d (ranks must be contiguous starting at 1)", activityMaxRank, len(activityRegistry))
	}
}
