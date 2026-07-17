package domain_test

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"rhizome-mcp/internal/domain"
)

const parentID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestCreateIssueInputValidateDefaultsAndOptionalText(t *testing.T) {
	description := "description"
	criteria := "acceptance"
	input, err := (domain.CreateIssueInput{
		Type:               domain.TypeTask,
		Title:              "Ship it",
		Description:        &description,
		AcceptanceCriteria: &criteria,
	}).Validate()
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if input.Status != domain.StatusOpen || input.Priority != domain.PriorityMedium {
		t.Fatalf("defaults = status %q, priority %q", input.Status, input.Priority)
	}
	description = "changed"
	if *input.Description != "description" {
		t.Fatal("Validate() did not defensively copy optional text")
	}
}

func TestCreateIssueInputValidateTextLimitsAndEncoding(t *testing.T) {
	tests := []struct {
		name  string
		input domain.CreateIssueInput
		code  string
	}{
		{name: "blank title", input: domain.CreateIssueInput{Type: domain.TypeTask, Title: " \t "}, code: domain.CodeInvalidArgument},
		{name: "title NUL", input: domain.CreateIssueInput{Type: domain.TypeTask, Title: "a\x00b"}, code: domain.CodeInvalidArgument},
		{name: "title invalid UTF-8", input: domain.CreateIssueInput{Type: domain.TypeTask, Title: string([]byte{utf8.RuneSelf, 0xff})}, code: domain.CodeInvalidArgument},
		{name: "title too long", input: domain.CreateIssueInput{Type: domain.TypeTask, Title: strings.Repeat("a", domain.MaxTitleRunes+1)}, code: domain.CodeLimitExceeded},
		{name: "description too long", input: domain.CreateIssueInput{Type: domain.TypeTask, Title: "title", Description: stringPointer(strings.Repeat("a", domain.MaxDescriptionRunes+1))}, code: domain.CodeLimitExceeded},
		{name: "criteria too long", input: domain.CreateIssueInput{Type: domain.TypeTask, Title: "title", AcceptanceCriteria: stringPointer(strings.Repeat("a", domain.MaxAcceptanceCriteriaRunes+1))}, code: domain.CodeLimitExceeded},
		{name: "description NUL", input: domain.CreateIssueInput{Type: domain.TypeTask, Title: "title", Description: stringPointer("a\x00b")}, code: domain.CodeInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.input.Validate()
			if !errors.Is(err, &domain.Error{Code: tt.code}) {
				t.Fatalf("Validate() error = %v, want %s", err, tt.code)
			}
		})
	}
}

func TestCreateIssueInputValidateBlockedReasonContract(t *testing.T) {
	tests := []struct {
		name  string
		input domain.CreateIssueInput
		code  string
	}{
		{name: "blocked requires reason", input: domain.CreateIssueInput{Type: domain.TypeBug, Title: "bug", Status: domain.StatusBlocked}, code: domain.CodeInvalidArgument},
		{name: "blocked rejects blank reason", input: domain.CreateIssueInput{Type: domain.TypeBug, Title: "bug", Status: domain.StatusBlocked, BlockedReason: stringPointer("  ")}, code: domain.CodeInvalidArgument},
		{name: "non-blocked rejects reason", input: domain.CreateIssueInput{Type: domain.TypeBug, Title: "bug", BlockedReason: stringPointer("stale")}, code: domain.CodeInvalidArgument},
		{name: "blocked accepts reason", input: domain.CreateIssueInput{Type: domain.TypeBug, Title: "bug", Status: domain.StatusBlocked, BlockedReason: stringPointer("dependency")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.input.Validate()
			if tt.code == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if !errors.Is(err, &domain.Error{Code: tt.code}) {
				t.Fatalf("Validate() error = %v, want %s", err, tt.code)
			}
		})
	}
}

func TestCreateIssueInputValidateParentReferenceAndTypeRules(t *testing.T) {
	tests := []struct {
		name       string
		input      domain.CreateIssueInput
		wantParent string
		code       string
	}{
		{name: "epic cannot have parent", input: domain.CreateIssueInput{Type: domain.TypeEpic, Title: "epic", ParentID: stringPointer(parentID)}, code: domain.CodeInvalidEpicParent},
		{name: "task accepts ULID parent", input: domain.CreateIssueInput{Type: domain.TypeTask, Title: "task", ParentID: stringPointer(parentID)}, wantParent: parentID},
		{name: "bug accepts display parent", input: domain.CreateIssueInput{Type: domain.TypeBug, Title: "bug", ParentID: stringPointer("issue-42")}, wantParent: "ISSUE-42"},
		{name: "invalid parent identifier", input: domain.CreateIssueInput{Type: domain.TypeTask, Title: "task", ParentID: stringPointer("not-an-id")}, code: domain.CodeInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized, err := tt.input.Validate()
			if tt.code == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				if normalized.ParentID == nil || *normalized.ParentID != tt.wantParent {
					t.Fatalf("normalized parent = %v, want %q", normalized.ParentID, tt.wantParent)
				}
				return
			}
			if !errors.Is(err, &domain.Error{Code: tt.code}) {
				t.Fatalf("Validate() error = %v, want %s", err, tt.code)
			}
		})
	}
}

func TestCreateIssueIdempotencyKeyValidationAndCanonicalRequest(t *testing.T) {
	key := "  retry-key  "
	input := domain.CreateIssueInput{Type: domain.TypeTask, Title: "Ship it", IdempotencyKey: &key}
	normalized, err := input.Validate()
	if err != nil || normalized.IdempotencyKey == nil || *normalized.IdempotencyKey != "retry-key" {
		t.Fatalf("normalized key = %#v, %v", normalized.IdempotencyKey, err)
	}
	key = "changed"
	if *normalized.IdempotencyKey != "retry-key" {
		t.Fatal("idempotency key was not defensively copied")
	}
	first, err := domain.CanonicalCreateIssueRequest(normalized)
	if err != nil {
		t.Fatal(err)
	}
	different := normalized
	different.Title = "different"
	second, err := domain.CanonicalCreateIssueRequest(different)
	if err != nil || string(first) == string(second) {
		t.Fatal("request change did not change canonical bytes")
	}
	for _, value := range []string{" ", strings.Repeat("x", domain.MaxIdempotencyKeyRunes+1)} {
		value := value
		_, err := (domain.CreateIssueInput{Type: domain.TypeTask, Title: "Ship it", IdempotencyKey: &value}).Validate()
		if !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) && !errors.Is(err, &domain.Error{Code: domain.CodeLimitExceeded}) {
			t.Fatalf("key %q error = %v", value, err)
		}
	}
}

func stringPointer(value string) *string { return &value }
