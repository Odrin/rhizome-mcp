package domain

// This file is the single source of truth for the vocabularies and page-size
// policy the MCP and CLI adapters advertise. Before it existed each adapter
// carried its own copy of these lists and numbers, so a domain change could
// silently disagree with what clients were told (ISSUE-203).

// AllIssueTypes lists every supported issue type in the deterministic order
// used for advertised schemas, error messages and documentation.
var AllIssueTypes = []Type{TypeEpic, TypeTask, TypeBug}

// AllStatuses lists every supported stored status in deterministic order. It
// deliberately excludes in_progress, which is an effective status derived from
// an active attempt rather than a stored one.
var AllStatuses = []Status{StatusOpen, StatusReady, StatusBlocked, StatusReview, StatusDone, StatusCancelled}

// AllRelationTypes lists every supported relation type in deterministic order.
var AllRelationTypes = []RelationType{RelationTypeBlocks, RelationTypeRelatedTo, RelationTypeDuplicates}

// AllPriorities lists every supported priority in ascending order of urgency.
var AllPriorities = []Priority{PriorityLow, PriorityMedium, PriorityHigh, PriorityCritical}

// Page-size policy shared by every paginated collection. A zero limit means
// "use the default"; anything above MaxCollectionLimit is rejected rather than
// silently clamped, so a caller asking for more than it can have is told so.
const (
	DefaultIssueListLimit = 20
	DefaultLabelListLimit = 50
	MaxCollectionLimit    = 100
)

// IssueTypeNames returns AllIssueTypes as strings, for schema enum construction.
func IssueTypeNames() []string { return enumNames(AllIssueTypes) }

// StatusNames returns AllStatuses as strings, for schema enum construction.
func StatusNames() []string { return enumNames(AllStatuses) }

// RelationTypeNames returns AllRelationTypes as strings, for schema enum construction.
func RelationTypeNames() []string { return enumNames(AllRelationTypes) }

// PriorityNames returns AllPriorities as strings, for schema enum construction.
func PriorityNames() []string { return enumNames(AllPriorities) }

func enumNames[T ~string](values []T) []string {
	names := make([]string, len(values))
	for index, value := range values {
		names[index] = string(value)
	}
	return names
}

func enumValid[T ~string](value T, allowed []T) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
