package domain

import (
	"testing"
	"time"
)

func TestProjectMetadataPreservesNullableValuesAndNoEventDefault(t *testing.T) {
	name := "Rhizome"
	instructions := "Keep the change focused."
	project := Project{
		ID:              "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Name:            &name,
		Instructions:    &instructions,
		NextIssueNumber: 4,
		CreatedAt:       time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC),
		SchemaVersion:   1,
	}

	if project.Name == nil || *project.Name != name {
		t.Fatalf("name = %#v, want %q", project.Name, name)
	}
	if project.Instructions == nil || *project.Instructions != instructions {
		t.Fatalf("instructions = %#v, want %q", project.Instructions, instructions)
	}
	if project.LatestEventID != 0 {
		t.Fatalf("latest event ID = %d, want 0", project.LatestEventID)
	}
}
