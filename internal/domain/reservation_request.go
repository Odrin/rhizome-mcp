package domain

import "fmt"

// Namespace returns the logical resource's normalized namespace; empty for
// path-kind resources.
func (r NormalizedResource) Namespace() string { return r.namespace }

// Name returns the logical resource's normalized, trimmed name; empty for
// path-kind resources.
func (r NormalizedResource) Name() string { return r.name }

// resourceIdentity distinguishes resources by kind and comparison key.
// Key() alone is not a safe cross-kind identity: a logical resource's key
// is "namespace:name" with no kind prefix, so e.g. logical{namespace:
// "file", name: "x"} and file{path: "x"} both produce the key "file:x"
// despite never overlapping per Overlaps.
type resourceIdentity struct {
	kind ResourceKind
	key  string
}

// PreparedResource pairs a normalized resource with the index of its
// first-occurrence in the original request, so a caller can map the
// surviving, deduplicated resources back to caller-supplied per-resource
// state (e.g. a pre-generated storage ID) without re-normalizing.
type PreparedResource struct {
	Index    int
	Resource NormalizedResource
}

// PrepareReservationRequest normalizes and validates a batch resource
// acquisition request per ISSUE-178's locked contract: every resource is
// normalized, the batch is capped at MaxReservationResources, exact
// duplicates (identical kind and comparison key) collapse to their
// first occurrence, and any other pairwise overlap within the request is
// rejected with CodeInvalidReservationSet. The returned slice preserves
// first-occurrence order and contains no internal overlaps, so a caller can
// check each returned resource against externally stored reservations
// independently.
func PrepareReservationRequest(resources []Resource) ([]PreparedResource, error) {
	if len(resources) == 0 {
		return nil, validationError("resources", "REQUIRED", "must not be empty")
	}
	if len(resources) > MaxReservationResources {
		return nil, NewError(
			CodeLimitExceeded,
			fmt.Sprintf("resources exceeds the maximum count of %d", MaxReservationResources),
			false,
			Detail{Field: "resources", Code: "MAX_ITEMS", Message: fmt.Sprintf("maximum %d", MaxReservationResources)},
		)
	}

	prepared := make([]PreparedResource, 0, len(resources))
	seen := make(map[resourceIdentity]struct{}, len(resources))
	for index, resource := range resources {
		candidate, err := Normalize(resource)
		if err != nil {
			return nil, wrapResourceRequestError(index, err)
		}

		identity := resourceIdentity{kind: candidate.Kind(), key: candidate.Key()}
		if _, duplicate := seen[identity]; duplicate {
			continue
		}
		for _, existing := range prepared {
			if Overlaps(candidate, existing.Resource) {
				overlapIndex := index
				return nil, NewError(CodeInvalidReservationSet, "requested resources overlap each other", false,
					Detail{EntityIndex: &overlapIndex, Field: "resources", Code: "INTERNAL_OVERLAP"})
			}
		}
		seen[identity] = struct{}{}
		prepared = append(prepared, PreparedResource{Index: index, Resource: candidate})
	}
	return prepared, nil
}

func wrapResourceRequestError(index int, err error) error {
	idx := index
	domainErr, ok := err.(*Error)
	if !ok || len(domainErr.Details) == 0 {
		return NewError(CodeInvalidArgument, err.Error(), false, Detail{EntityIndex: &idx, Field: "resources"})
	}
	details := make([]Detail, len(domainErr.Details))
	for i, d := range domainErr.Details {
		d.EntityIndex = &idx
		details[i] = d
	}
	return NewError(domainErr.Code, domainErr.Message, domainErr.Retryable, details...)
}
