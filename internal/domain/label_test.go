package domain_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"rhizome-mcp/internal/domain"
)

func TestNormalizeLabelNamesUsesTrimmedASCIINocaseOrder(t *testing.T) {
	labels, err := domain.NormalizeLabelNames([]string{" Zebra ", "alpha", "Ä"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alpha", "Zebra", "Ä"}; !reflect.DeepEqual(labels, want) {
		t.Fatalf("labels = %#v, want %#v", labels, want)
	}
	display, normalized, err := domain.NormalizeLabelName(" ÄA ")
	if err != nil || display != "ÄA" || normalized != "Äa" {
		t.Fatalf("NormalizeLabelName() = (%q, %q, %v)", display, normalized, err)
	}
}

func TestNormalizeLabelNamesRejectsInvalidDuplicatesAndLimits(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		code   string
	}{
		{name: "blank", labels: []string{" \t "}, code: domain.CodeInvalidArgument},
		{name: "nul", labels: []string{"a\x00b"}, code: domain.CodeInvalidArgument},
		{name: "invalid utf8", labels: []string{string([]byte{utf8.RuneSelf, 0xff})}, code: domain.CodeInvalidArgument},
		{name: "too long", labels: []string{strings.Repeat("x", domain.MaxLabelNameRunes+1)}, code: domain.CodeLimitExceeded},
		{name: "too many", labels: make([]string, domain.MaxLabelsPerIssue+1), code: domain.CodeLimitExceeded},
		{name: "ascii duplicate", labels: []string{"Alpha", "alpha"}, code: domain.CodeInvalidArgument},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := domain.NormalizeLabelNames(test.labels)
			if !errors.Is(err, &domain.Error{Code: test.code}) {
				t.Fatalf("NormalizeLabelNames() error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestLabelAssignmentInputPresenceAndEmptyReplacement(t *testing.T) {
	created, err := (domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "labels", Labels: []string{"B", "a"}, CreateMissingLabels: true,
	}).Validate()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "B"}; !reflect.DeepEqual(created.Labels, want) || !created.CreateMissingLabels {
		t.Fatalf("created labels = %#v, create missing = %v", created.Labels, created.CreateMissingLabels)
	}

	absent, err := (domain.UpdateIssueInput{
		IssueID: "ISSUE-1", ExpectedVersion: 1,
		Changes: domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "changed"}},
	}).Validate()
	if err != nil {
		t.Fatal(err)
	}
	if absent.Changes.Labels.Set {
		t.Fatalf("absent labels = %#v", absent.Changes.Labels)
	}
	empty, err := (domain.UpdateIssueInput{
		IssueID: "ISSUE-1", ExpectedVersion: 1,
		Changes: domain.IssuePatch{Labels: domain.OptionalValue[[]string]{Set: true, Value: []string{}}},
	}).Validate()
	if err != nil {
		t.Fatal(err)
	}
	if !empty.Changes.Labels.Set || empty.Changes.Labels.Value == nil || len(empty.Changes.Labels.Value) != 0 {
		t.Fatalf("explicit empty labels = %#v", empty.Changes.Labels)
	}
	_, err = (domain.UpdateIssueInput{
		IssueID: "ISSUE-1", ExpectedVersion: 1,
		Changes: domain.IssuePatch{Labels: domain.OptionalValue[[]string]{Set: true}},
	}).Validate()
	if !errors.Is(err, &domain.Error{Code: domain.CodeValidationError}) {
		t.Fatalf("null labels error = %v, want VALIDATION_ERROR", err)
	}
}
