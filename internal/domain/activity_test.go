package domain

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"
)

const (
	activityIssueID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	activityEntity1 = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	activityEntity2 = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
)

func TestGetIssueActivityInputValidateDefaultsAndCopies(t *testing.T) {
	input := GetIssueActivityInput{IssueID: "issue-7"}
	got, err := input.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if got.IssueID != "ISSUE-7" || got.Limit != 20 || got.Order != ActivityOrderNewestFirst ||
		!reflect.DeepEqual(got.Types, []ActivityCategory{
			ActivityCategoryComments, ActivityCategoryDecisions, ActivityCategoryAttempts,
			ActivityCategoryAttemptNotes, ActivityCategoryEvents, ActivityCategoryArtifacts,
		}) {
		t.Fatalf("normalized input = %#v", got)
	}
	got.Types[0] = ActivityCategoryEvents
	if AllActivityCategories[0] != ActivityCategoryComments {
		t.Fatal("normalized default types share AllActivityCategories")
	}

	types := []ActivityCategory{ActivityCategoryArtifacts, ActivityCategoryComments}
	input = GetIssueActivityInput{
		IssueID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Types: types, Limit: 4,
		Cursor: "opaque cursor", Order: ActivityOrderNewestFirst,
	}
	got, err = input.Validate()
	if err != nil {
		t.Fatal(err)
	}
	types[0] = ActivityCategoryEvents
	if got.Types[0] != ActivityCategoryArtifacts || got.Cursor != "opaque cursor" {
		t.Fatalf("normalized filter was not defensive: %#v", got)
	}
}

func TestGetIssueActivityInputValidateRejectsInvalidValues(t *testing.T) {
	tests := []GetIssueActivityInput{
		{IssueID: "not-an-issue"},
		{IssueID: activityIssueID, Limit: -1},
		{IssueID: activityIssueID, Limit: 101},
		{IssueID: activityIssueID, Types: []ActivityCategory{"unknown"}},
		{IssueID: activityIssueID, Types: []ActivityCategory{ActivityCategoryEvents, ActivityCategoryEvents}},
		{IssueID: activityIssueID, Cursor: "bad\x00cursor"},
		{IssueID: activityIssueID, Cursor: string(make([]byte, 4097))},
		{IssueID: activityIssueID, Order: "oldest_first"},
	}
	for _, input := range tests {
		if _, err := input.Validate(); !errors.Is(err, &Error{Code: CodeInvalidArgument}) &&
			!errors.Is(err, &Error{Code: CodeLimitExceeded}) {
			t.Fatalf("Validate(%#v) error = %v", input, err)
		}
	}
}

func TestActivityItemValidationAcceptsEveryEntity(t *testing.T) {
	now := time.Date(2026, 7, 14, 17, 0, 0, 0, time.UTC)
	items := []ActivityItem{
		{EntityType: ActivityEntityTypeComment, EntityID: activityEntity1, IssueID: activityIssueID,
			OccurredAt: now, Comment: &Comment{ID: activityEntity1, IssueID: activityIssueID, CreatedAt: now}},
		{EntityType: ActivityEntityTypeDecision, EntityID: activityEntity1, IssueID: activityIssueID,
			OccurredAt: now, Decision: &Decision{ID: activityEntity1, IssueID: stringPointer(activityIssueID), CreatedAt: now}},
		{EntityType: ActivityEntityTypeAttempt, EntityID: activityEntity1, IssueID: activityIssueID,
			OccurredAt: now, Attempt: &WorkAttempt{ID: activityEntity1, IssueID: activityIssueID, StartedAt: now}},
		{EntityType: ActivityEntityTypeAttemptNote, EntityID: activityEntity1, IssueID: activityIssueID,
			OccurredAt: now, AttemptNote: &AttemptNote{ID: activityEntity1, CreatedAt: now}},
		{EntityType: ActivityEntityTypeEvent, EntityID: "7", IssueID: activityIssueID,
			OccurredAt: now, Event: &IssueEvent{ID: 7, IssueID: stringPointer(activityIssueID), CreatedAt: now}},
		{EntityType: ActivityEntityTypeArtifact, EntityID: activityEntity1, IssueID: activityIssueID,
			OccurredAt: now, Artifact: &Artifact{ID: activityEntity1, IssueID: activityIssueID, CreatedAt: now}},
	}
	for _, item := range items {
		if err := ValidateActivityItem(item); err != nil {
			t.Errorf("ValidateActivityItem(%s) error = %v", item.EntityType, err)
		}
	}
}

func TestActivityItemValidationRejectsInvalidShapesAndMatches(t *testing.T) {
	now := time.Date(2026, 7, 14, 17, 0, 0, 0, time.UTC)
	valid := ActivityItem{
		EntityType: ActivityEntityTypeComment, EntityID: activityEntity1, IssueID: activityIssueID,
		OccurredAt: now, Comment: &Comment{ID: activityEntity1, IssueID: activityIssueID, CreatedAt: now},
	}
	tests := []ActivityItem{
		{EntityType: "bad", EntityID: activityEntity1, IssueID: activityIssueID, OccurredAt: now},
		{EntityType: ActivityEntityTypeComment, EntityID: "not-ulid", IssueID: activityIssueID,
			OccurredAt: now, Comment: valid.Comment},
		{EntityType: ActivityEntityTypeComment, EntityID: activityEntity1, IssueID: activityIssueID,
			OccurredAt: now, Comment: valid.Comment, Event: &IssueEvent{}},
		{EntityType: ActivityEntityTypeComment, EntityID: activityEntity1, IssueID: activityIssueID,
			OccurredAt: now, Decision: &Decision{}},
		{EntityType: ActivityEntityTypeComment, EntityID: activityEntity1, IssueID: activityIssueID,
			OccurredAt: now, Comment: &Comment{ID: activityEntity2, IssueID: activityIssueID, CreatedAt: now}},
		{EntityType: ActivityEntityTypeComment, EntityID: activityEntity1, IssueID: activityIssueID,
			OccurredAt: now, Comment: &Comment{ID: activityEntity1, IssueID: activityEntity2, CreatedAt: now}},
		{EntityType: ActivityEntityTypeComment, EntityID: activityEntity1, IssueID: activityIssueID,
			OccurredAt: now, Comment: &Comment{ID: activityEntity1, IssueID: activityIssueID, CreatedAt: now.Add(time.Second)}},
		{EntityType: ActivityEntityTypeEvent, EntityID: "01", IssueID: activityIssueID, OccurredAt: now,
			Event: &IssueEvent{ID: 1, IssueID: stringPointer(activityIssueID), CreatedAt: now}},
		{EntityType: ActivityEntityTypeEvent, EntityID: "7", IssueID: activityIssueID, OccurredAt: now,
			Event: &IssueEvent{ID: 7, IssueID: stringPointer(activityEntity2), CreatedAt: now}},
		{EntityType: ActivityEntityTypeAttempt, EntityID: activityEntity1, IssueID: activityIssueID,
			OccurredAt: now, Attempt: &WorkAttempt{ID: activityEntity1, IssueID: activityIssueID, StartedAt: now.Add(time.Second)}},
		{EntityType: ActivityEntityTypeArtifact, EntityID: activityEntity1, IssueID: "not-ulid", OccurredAt: now,
			Artifact: &Artifact{ID: activityEntity1, IssueID: "not-ulid", CreatedAt: now}},
	}
	for index, item := range tests {
		if err := ValidateActivityItem(item); err == nil {
			t.Errorf("test %d unexpectedly passed", index)
		}
	}
}

func TestCloneIssueActivityDeepCopiesAllNestedValues(t *testing.T) {
	now := time.Date(2026, 7, 14, 17, 0, 0, 0, time.UTC)
	session, label, result, reason := "session", "agent", "result", "details"
	failure := FailureReasonCodeOther
	interruption := InterruptionReasonOther
	edited := now.Add(time.Minute)
	title := "title"
	activity := IssueActivity{
		Items: []ActivityItem{{
			EntityType: ActivityEntityTypeAttempt, EntityID: activityEntity1, IssueID: activityIssueID, OccurredAt: now,
			Attempt: &WorkAttempt{
				ID: activityEntity1, IssueID: activityIssueID, SessionID: &session, AgentLabel: &label,
				StartedAt: now, FinishedAt: &edited, ResultSummary: &result, NextSteps: []string{"one"},
				Verification: []string{"two"}, FailureReasonCode: &failure,
				InterruptionReasonCode: &interruption, ReasonDetails: &reason,
			},
		}, {
			EntityType: ActivityEntityTypeAttemptNote, EntityID: activityEntity2, IssueID: activityIssueID, OccurredAt: now,
			AttemptNote: &AttemptNote{ID: activityEntity2, NextSteps: []string{"recover"}, CreatedAt: now},
		}, {
			EntityType: ActivityEntityTypeEvent, EntityID: "7", IssueID: activityIssueID, OccurredAt: now,
			Event: &IssueEvent{ID: 7, IssueID: stringPointer(activityIssueID), SessionID: &session,
				AttemptID: stringPointer(activityEntity1), Payload: []byte(`{"a":1}`), CreatedAt: now},
		}, {
			EntityType: ActivityEntityTypeArtifact, EntityID: activityEntity1, IssueID: activityIssueID, OccurredAt: now,
			Artifact: &Artifact{ID: activityEntity1, IssueID: activityIssueID, Title: &title,
				Metadata: []byte(`{"x":1}`), CreatedAt: now},
		}},
		NextCursor: stringPointer("next"), HasMore: true,
	}
	clone := CloneIssueActivity(activity)
	clone.Items[0].Attempt.SessionID = stringPointer("changed")
	clone.Items[0].Attempt.NextSteps[0] = "changed"
	clone.Items[0].Attempt.FinishedAt = timePointer(now)
	clone.Items[1].AttemptNote.NextSteps[0] = "changed"
	clone.Items[2].Event.Payload[0] = '['
	clone.Items[3].Artifact.Metadata[0] = '['
	*clone.NextCursor = "changed"
	if *activity.Items[0].Attempt.SessionID != session ||
		activity.Items[0].Attempt.NextSteps[0] != "one" ||
		activity.Items[0].Attempt.FinishedAt == clone.Items[0].Attempt.FinishedAt ||
		activity.Items[1].AttemptNote.NextSteps[0] != "recover" ||
		bytes.Equal(activity.Items[2].Event.Payload, clone.Items[2].Event.Payload) ||
		bytes.Equal(activity.Items[3].Artifact.Metadata, clone.Items[3].Artifact.Metadata) ||
		*activity.NextCursor != "next" {
		t.Fatal("clone shares nested mutable data")
	}
}

func TestCloneIssueActivityNormalizesEmptyItems(t *testing.T) {
	clone := CloneIssueActivity(IssueActivity{})
	if clone.Items == nil || len(clone.Items) != 0 {
		t.Fatalf("Items = %#v, want nonnil empty slice", clone.Items)
	}
}

func stringPointer(value string) *string { return &value }

func timePointer(value time.Time) *time.Time { return &value }
