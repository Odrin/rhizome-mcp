package domain_test

import (
	"errors"
	"reflect"
	"testing"

	"rhizome-mcp/internal/domain"
)

func TestListIssuesInputValidateDefaultsCopiesAndNormalizes(t *testing.T) {
	parent := "issue-12"
	blocked := true
	input := domain.ListIssuesInput{
		Types:             []domain.Type{domain.TypeBug},
		Statuses:          []domain.Status{domain.StatusReady},
		EffectiveStatuses: []domain.Status{domain.StatusBlocked},
		Priorities:        []domain.Priority{domain.PriorityHigh},
		Labels:            []string{" Zebra ", "alpha", "ALPHA"},
		ParentIssueID:     &parent,
		IsBlocked:         &blocked,
		Limit:             0,
	}
	got, err := input.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if got.Limit != 20 || *got.ParentIssueID != "ISSUE-12" ||
		!reflect.DeepEqual(got.Labels, []string{"alpha", "Zebra"}) {
		t.Fatalf("normalized list input = %#v", got)
	}
	input.Types[0] = domain.TypeEpic
	input.Labels[0] = "changed"
	*input.ParentIssueID = "ISSUE-99"
	if got.Types[0] != domain.TypeBug || got.Labels[1] != "Zebra" || *got.ParentIssueID != "ISSUE-12" {
		t.Fatalf("list input was not defensively copied: %#v", got)
	}
}

func TestListIssuesInputValidateRejectsInvalidEnumsIdentifiersDuplicatesAndLimit(t *testing.T) {
	tests := []domain.ListIssuesInput{
		{Types: []domain.Type{"story"}},
		{Statuses: []domain.Status{domain.StatusReady, domain.StatusReady}},
		{Priorities: []domain.Priority{"urgent"}},
		{ParentIssueID: stringPointer("not-an-issue")},
		{Limit: 101},
	}
	for _, input := range tests {
		if _, err := input.Validate(); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) &&
			!errors.Is(err, &domain.Error{Code: domain.CodeLimitExceeded}) {
			t.Fatalf("Validate(%#v) error = %v", input, err)
		}
	}
}
