package domain

import (
	"reflect"
	"testing"
)

func TestNormalizeIssuePlanSortsErrorsAndNormalizesDefaults(t *testing.T) {
	result := NormalizeIssuePlan(IssuePlan{
		Issues: []PlannedIssue{
			{Ref: "first", Type: Type("wrong"), Title: ""},
			{Ref: "first", Type: TypeTask, Title: "valid", Labels: []string{"B", "a"}},
		},
		Relations: []PlannedRelation{{SourceRef: "first", TargetRef: "first", Type: RelationType("wrong")}},
		Decisions: []PlannedDecision{{Title: "", Summary: "", Content: "", Status: "wrong"}},
	})
	if result.Valid || len(result.Errors) < 6 {
		t.Fatalf("validation = %#v", result)
	}
	for i := 1; i < len(result.Errors); i++ {
		left, right := result.Errors[i-1], result.Errors[i]
		if compareEntityIndex(left.EntityIndex, right.EntityIndex) > 0 ||
			compareEntityIndex(left.EntityIndex, right.EntityIndex) == 0 && (left.Field > right.Field ||
				left.Field == right.Field && left.Code > right.Code) {
			t.Fatalf("errors are not sorted: %#v", result.Errors)
		}
	}
	if !reflect.DeepEqual(result.NormalizedPlan.Issues[1].Labels, []string{"a", "B"}) {
		t.Fatalf("normalized labels = %#v", result.NormalizedPlan.Issues[1].Labels)
	}
}
