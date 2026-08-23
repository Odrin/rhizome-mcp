package domain_test

import (
	"errors"
	"strconv"
	"testing"

	"rhizome-mcp/internal/domain"
)

func TestPrepareReservationRequestRejectsEmpty(t *testing.T) {
	_, err := domain.PrepareReservationRequest(nil)
	assertDomainCode(t, err, domain.CodeInvalidArgument)
}

func TestPrepareReservationRequestRejectsTooManyResources(t *testing.T) {
	resources := make([]domain.Resource, domain.MaxReservationResources+1)
	for index := range resources {
		resources[index] = domain.Resource{Kind: domain.ResourceKindLogical, Namespace: "ns", Name: strconv.Itoa(index)}
	}
	_, err := domain.PrepareReservationRequest(resources)
	assertDomainCode(t, err, domain.CodeLimitExceeded)
}

func TestPrepareReservationRequestPropagatesPerResourceValidationErrorWithIndex(t *testing.T) {
	_, err := domain.PrepareReservationRequest([]domain.Resource{
		{Kind: domain.ResourceKindFile, Path: "ok.go"},
		{Kind: domain.ResourceKindFile, Path: "/absolute"},
	})
	assertDomainCode(t, err, domain.CodeInvalidArgument)
	details := domainErrorDetails(t, err)
	if len(details) == 0 || details[0].EntityIndex == nil || *details[0].EntityIndex != 1 {
		t.Fatalf("details = %+v, want the second resource's index tagged", details)
	}
}

func TestPrepareReservationRequestCollapsesExactDuplicates(t *testing.T) {
	prepared, err := domain.PrepareReservationRequest([]domain.Resource{
		{Kind: domain.ResourceKindFile, Path: "a/b.go"},
		{Kind: domain.ResourceKindFile, Path: "A/B.GO"},
	})
	if err != nil {
		t.Fatalf("PrepareReservationRequest() error = %v", err)
	}
	if len(prepared) != 1 || prepared[0].Index != 0 {
		t.Fatalf("prepared = %+v, want the first occurrence retained", prepared)
	}
}

func TestPrepareReservationRequestRejectsInternalOverlapThatIsNotAnExactDuplicate(t *testing.T) {
	_, err := domain.PrepareReservationRequest([]domain.Resource{
		{Kind: domain.ResourceKindDirectory, Path: "src"},
		{Kind: domain.ResourceKindFile, Path: "src/main.go"},
	})
	assertDomainCode(t, err, domain.CodeInvalidReservationSet)
	details := domainErrorDetails(t, err)
	if len(details) == 0 || details[0].EntityIndex == nil || *details[0].EntityIndex != 1 {
		t.Fatalf("details = %+v, want the overlapping (second) resource's index", details)
	}
}

func TestPrepareReservationRequestAcceptsNonOverlappingResourcesAcrossKinds(t *testing.T) {
	prepared, err := domain.PrepareReservationRequest([]domain.Resource{
		{Kind: domain.ResourceKindFile, Path: "a.go"},
		{Kind: domain.ResourceKindDirectory, Path: "other"},
		{Kind: domain.ResourceKindGlob, Path: "docs/**"},
		{Kind: domain.ResourceKindLogical, Namespace: "file", Name: "a.go"}, // same string form as the file key, different kind
	})
	if err != nil {
		t.Fatalf("PrepareReservationRequest() error = %v", err)
	}
	if len(prepared) != 4 {
		t.Fatalf("prepared = %d resources, want 4 (no cross-kind key collision)", len(prepared))
	}
}

func domainErrorDetails(t *testing.T, err error) []domain.Detail {
	t.Helper()
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error = %v, want *domain.Error", err)
	}
	return domainErr.Details
}
