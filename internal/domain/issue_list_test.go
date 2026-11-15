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
		EffectiveStatuses: []domain.EffectiveStatus{domain.EffectiveStatusBlocked},
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
	if got.Limit != 20 || got.Sort != domain.IssueSortPriority || *got.ParentIssueID != "ISSUE-12" ||
		!reflect.DeepEqual(got.Labels, []string{"alpha", "Zebra"}) {
		t.Fatalf("normalized list input = %#v", got)
	}
	for _, sortOrder := range domain.AllIssueSortOrders {
		validated, err := (domain.ListIssuesInput{Sort: sortOrder}).Validate()
		if err != nil || validated.Sort != sortOrder {
			t.Fatalf("Validate(Sort=%q) = (%#v, %v)", sortOrder, validated, err)
		}
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
		{Sort: domain.IssueSortOrder("newest")},
		{Sort: domain.IssueSortOrder(" priority")},
	}
	for _, input := range tests {
		if _, err := input.Validate(); !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) &&
			!errors.Is(err, &domain.Error{Code: domain.CodeLimitExceeded}) {
			t.Fatalf("Validate(%#v) error = %v", input, err)
		}
	}
	if _, err := (domain.ListIssuesInput{Sort: domain.IssueSortOrder("newest")}).Validate(); err == nil {
		t.Fatal("expected error for invalid sort order")
	} else {
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeInvalidArgument ||
			len(domainErr.Details) != 1 || domainErr.Details[0].Field != "sort" || domainErr.Details[0].Code != "INVALID_ENUM" {
			t.Fatalf("invalid sort error = %#v, want INVALID_ARGUMENT detail(field=sort, code=INVALID_ENUM)", err)
		}
	}
}
