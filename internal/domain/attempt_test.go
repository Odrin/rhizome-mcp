package domain_test

import (
	"testing"

	"rhizome-mcp/internal/domain"
)

func TestAttemptLeaseInputValidation(t *testing.T) {
	for _, seconds := range []int{domain.MinLeaseSeconds - 1, domain.MaxLeaseSeconds + 1} {
		seconds := seconds
		if _, err := (domain.ClaimIssueInput{IssueID: "ISSUE-1", LeaseSeconds: &seconds}).Validate(); err == nil {
			t.Fatalf("lease %d was accepted", seconds)
		}
	}
	input, err := (domain.ClaimIssueInput{IssueID: "ISSUE-1"}).Validate()
	if err != nil || input.LeaseSeconds == nil || *input.LeaseSeconds != domain.DefaultLeaseSeconds {
		t.Fatalf("default lease = %#v, %v", input, err)
	}
	if _, err := (domain.RenewAttemptInput{AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", LeaseToken: ""}).Validate(); err == nil {
		t.Fatal("empty renewal token was accepted")
	}
}
