package domain_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"rhizome-mcp/internal/domain"
)

func TestGetWorkContextInputValidate(t *testing.T) {
	const ulid = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

	t.Run("display identifier and empty include", func(t *testing.T) {
		input := domain.GetWorkContextInput{IssueID: "ISSUE-42"}
		got, err := input.Validate()
		if err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		if got.IssueID != "ISSUE-42" {
			t.Fatalf("IssueID = %q, want %q", got.IssueID, "ISSUE-42")
		}
		if got.Include == nil {
			t.Fatal("Include slice was nil")
		}
		if len(got.Include) != 0 {
			t.Fatalf("Include length = %d, want 0", len(got.Include))
		}
		if got.Limits == nil {
			t.Fatal("Limits map was nil")
		}
		if len(got.Limits) != 0 {
			t.Fatalf("Limits length = %d, want 0", len(got.Limits))
		}
	})

	t.Run("canonical identifier and default list limit insertion", func(t *testing.T) {
		input := domain.GetWorkContextInput{
			IssueID: ulid,
			Include: []domain.WorkContextInclude{domain.WorkContextIncludeParentEpic, domain.WorkContextIncludeRelatedIssueSummaries},
		}
		got, err := input.Validate()
		if err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		if got.IssueID != ulid {
			t.Fatalf("IssueID = %q, want %q", got.IssueID, ulid)
		}
		if !reflect.DeepEqual(got.Include, []domain.WorkContextInclude{domain.WorkContextIncludeParentEpic, domain.WorkContextIncludeRelatedIssueSummaries}) {
			t.Fatalf("Include = %#v, want canonical order", got.Include)
		}
		if got.Limits[domain.WorkContextIncludeRelatedIssueSummaries] != domain.DefaultWorkContextRelatedIssueLimit {
			t.Fatalf("related issue summary limit = %d, want %d", got.Limits[domain.WorkContextIncludeRelatedIssueSummaries], domain.DefaultWorkContextRelatedIssueLimit)
		}
		if _, exists := got.Limits[domain.WorkContextIncludeParentEpic]; exists {
			t.Fatal("parent epic limit should not be inserted")
		}
	})

	t.Run("all includes with custom limits", func(t *testing.T) {
		callerInclude := []domain.WorkContextInclude{
			domain.WorkContextIncludeProjectInstructions,
			domain.WorkContextIncludeParentEpic,
			domain.WorkContextIncludeRelations,
			domain.WorkContextIncludeRelatedIssueSummaries,
			domain.WorkContextIncludeRecentComments,
			domain.WorkContextIncludeRecentAttemptNotes,
			domain.WorkContextIncludeDecisionContent,
			domain.WorkContextIncludeAttemptHistory,
			domain.WorkContextIncludeArtifacts,
			domain.WorkContextIncludeChangesSincePreviousAttempt,
		}
		callerLimits := map[domain.WorkContextInclude]int{
			domain.WorkContextIncludeRelatedIssueSummaries:       3,
			domain.WorkContextIncludeRecentComments:              4,
			domain.WorkContextIncludeRecentAttemptNotes:          5,
			domain.WorkContextIncludeDecisionContent:             6,
			domain.WorkContextIncludeAttemptHistory:              7,
			domain.WorkContextIncludeArtifacts:                   8,
			domain.WorkContextIncludeChangesSincePreviousAttempt: 9,
		}
		got, err := domain.GetWorkContextInput{IssueID: ulid, Include: callerInclude, Limits: callerLimits}.Validate()
		if err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		if !reflect.DeepEqual(got.Include, callerInclude) {
			t.Fatalf("Include = %#v, want %#v", got.Include, callerInclude)
		}
		for include, want := range callerLimits {
			if got.Limits[include] != want {
				t.Fatalf("limit for %q = %d, want %d", include, got.Limits[include], want)
			}
		}

		callerInclude[0] = domain.WorkContextIncludeParentEpic
		callerLimits[domain.WorkContextIncludeRecentComments] = 99
		if !reflect.DeepEqual(got.Include, []domain.WorkContextInclude{
			domain.WorkContextIncludeProjectInstructions,
			domain.WorkContextIncludeParentEpic,
			domain.WorkContextIncludeRelations,
			domain.WorkContextIncludeRelatedIssueSummaries,
			domain.WorkContextIncludeRecentComments,
			domain.WorkContextIncludeRecentAttemptNotes,
			domain.WorkContextIncludeDecisionContent,
			domain.WorkContextIncludeAttemptHistory,
			domain.WorkContextIncludeArtifacts,
			domain.WorkContextIncludeChangesSincePreviousAttempt,
		}) {
			t.Fatalf("normalized include was mutated by caller state: %#v", got.Include)
		}
		if got.Limits[domain.WorkContextIncludeRecentComments] != 4 {
			t.Fatalf("normalized limits were mutated by caller state: %#v", got.Limits)
		}
	})

	t.Run("invalid input", func(t *testing.T) {
		cases := []struct {
			name       string
			input      domain.GetWorkContextInput
			field      string
			detailCode string
		}{
			{
				name:       "invalid enum",
				input:      domain.GetWorkContextInput{IssueID: ulid, Include: []domain.WorkContextInclude{"bogus"}},
				field:      "include",
				detailCode: "INVALID_ENUM",
			},
			{
				name:       "duplicate include",
				input:      domain.GetWorkContextInput{IssueID: ulid, Include: []domain.WorkContextInclude{domain.WorkContextIncludeParentEpic, domain.WorkContextIncludeParentEpic}},
				field:      "include",
				detailCode: "DUPLICATE",
			},
			{
				name:       "too many includes",
				input:      domain.GetWorkContextInput{IssueID: ulid, Include: make([]domain.WorkContextInclude, 11)},
				field:      "include",
				detailCode: "OUT_OF_RANGE",
			},
			{
				name:       "unrequested limit",
				input:      domain.GetWorkContextInput{IssueID: ulid, Include: []domain.WorkContextInclude{domain.WorkContextIncludeParentEpic}, Limits: map[domain.WorkContextInclude]int{domain.WorkContextIncludeRelatedIssueSummaries: 5}},
				field:      "limits.related_issue_summaries",
				detailCode: "INVALID_SHAPE",
			},
			{
				name:       "scalar limit",
				input:      domain.GetWorkContextInput{IssueID: ulid, Include: []domain.WorkContextInclude{domain.WorkContextIncludeParentEpic}, Limits: map[domain.WorkContextInclude]int{domain.WorkContextIncludeParentEpic: 5}},
				field:      "limits.parent_epic",
				detailCode: "INVALID_SHAPE",
			},
			{
				name:       "negative list limit",
				input:      domain.GetWorkContextInput{IssueID: ulid, Include: []domain.WorkContextInclude{domain.WorkContextIncludeRelatedIssueSummaries}, Limits: map[domain.WorkContextInclude]int{domain.WorkContextIncludeRelatedIssueSummaries: -1}},
				field:      "limits.related_issue_summaries",
				detailCode: "OUT_OF_RANGE",
			},
			{
				name:       "zero list limit",
				input:      domain.GetWorkContextInput{IssueID: ulid, Include: []domain.WorkContextInclude{domain.WorkContextIncludeRelatedIssueSummaries}, Limits: map[domain.WorkContextInclude]int{domain.WorkContextIncludeRelatedIssueSummaries: 0}},
				field:      "limits.related_issue_summaries",
				detailCode: "OUT_OF_RANGE",
			},
			{
				name:       "list limit above maximum",
				input:      domain.GetWorkContextInput{IssueID: ulid, Include: []domain.WorkContextInclude{domain.WorkContextIncludeRelatedIssueSummaries}, Limits: map[domain.WorkContextInclude]int{domain.WorkContextIncludeRelatedIssueSummaries: 21}},
				field:      "limits.related_issue_summaries",
				detailCode: "OUT_OF_RANGE",
			},
			{
				name:       "invalid issue id",
				input:      domain.GetWorkContextInput{IssueID: "bad"},
				field:      "issue_id",
				detailCode: "INVALID_IDENTIFIER",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := tc.input.Validate()
				if err == nil {
					t.Fatal("Validate() error = nil, want error")
				}
				if !errors.Is(err, &domain.Error{Code: domain.CodeInvalidArgument}) {
					t.Fatalf("Validate() error = %v, want INVALID_ARGUMENT", err)
				}
				var domainErr *domain.Error
				if !errors.As(err, &domainErr) {
					t.Fatalf("error type = %T, want *domain.Error", err)
				}
				found := false
				for _, detail := range domainErr.Details {
					if detail.Field == tc.field && detail.Code == tc.detailCode {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("details = %#v, want field %q code %q", domainErr.Details, tc.field, tc.detailCode)
				}
			})
		}
	})
}

func TestNewEmptyWorkContext(t *testing.T) {
	ctx := domain.NewEmptyWorkContext()
	if ctx.Truncated {
		t.Fatal("Truncated should be false")
	}
	if ctx.ParentEpic != nil {
		t.Fatal("ParentEpic should be nil")
	}
	if ctx.ProjectInstructions != nil {
		t.Fatal("ProjectInstructions should be nil")
	}
	if ctx.PreviousAttempt != nil {
		t.Fatal("PreviousAttempt should be nil")
	}
	if ctx.Checkpoint != nil {
		t.Fatal("Checkpoint should be nil")
	}

	for _, fieldName := range []string{"Blockers", "Decisions", "Warnings", "Relations", "RelatedIssueSummaries", "RecentComments", "RecentAttemptNotes", "DecisionContent", "AttemptHistory", "Artifacts", "ChangesSincePreviousAttempt", "TruncatedSections"} {
		field := reflect.ValueOf(ctx).FieldByName(fieldName)
		if field.Kind() != reflect.Slice {
			t.Fatalf("field %s has kind %s, want slice", fieldName, field.Kind())
		}
		if field.IsNil() {
			t.Fatalf("field %s was nil", fieldName)
		}
		if field.Len() != 0 {
			t.Fatalf("field %s length = %d, want 0", fieldName, field.Len())
		}
	}
}

func TestCloneWorkContext(t *testing.T) {
	const ulid = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	issueID := ulid
	createdAt := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	finishedAt := time.Date(2024, 1, 2, 3, 5, 0, 0, time.UTC)
	laterTime := time.Date(2024, 1, 2, 3, 6, 0, 0, time.UTC)
	issueDescription := "issue description"
	issueCriteria := "issue criteria"
	blockerDescription := "blocker description"
	blockerCriteria := "blocker criteria"
	resultSummary := "result summary"
	attemptResult := "attempt result"
	reasonDetails := "reason details"
	projectInstructions := "follow instructions"
	artifactTitle := "artifact title"
	artifactAttemptID := "attempt-42"
	artifactMetadata := json.RawMessage(`{"foo":"bar"}`)
	failureReasonCode := domain.FailureReasonCode("tests_failed")
	interruptionReasonCode := domain.InterruptionReasonCode("handoff")
	sessionID := "session-1"
	agentLabel := "agent-1"
	eventPayload := json.RawMessage(`{"type":"change"}`)

	source := domain.WorkContext{
		Issue: domain.WorkContextIssue{
			ID:                     issueID,
			DisplayID:              "ISSUE-42",
			Title:                  "Issue title",
			Description:            &issueDescription,
			AcceptanceCriteria:     &issueCriteria,
			EffectiveStatus:        domain.EffectiveStatusBlocked,
			UnresolvedBlockerCount: 2,
			IsBlocked:              true,
		},
		Blockers: []domain.WorkContextIssue{{
			ID:                     "blocker-1",
			DisplayID:              "ISSUE-43",
			Title:                  "Blocker title",
			Description:            &blockerDescription,
			AcceptanceCriteria:     &blockerCriteria,
			EffectiveStatus:        domain.EffectiveStatusOpen,
			UnresolvedBlockerCount: 1,
			IsBlocked:              false,
		}},
		Decisions: []domain.WorkContextDecisionSummary{{
			ID:        "decision-1",
			Title:     "Decision title",
			Summary:   "decision summary",
			Status:    domain.DecisionStatusActive,
			CreatedAt: createdAt,
		}},
		PreviousAttempt: &domain.WorkContextAttemptSummary{
			ID:            "attempt-1",
			Kind:          domain.AttemptKindWork,
			Status:        domain.AttemptStatusCompleted,
			FinishedAt:    &finishedAt,
			ResultSummary: &resultSummary,
			NextSteps:     []string{"step 1", "step 2"},
		},
		Checkpoint: &domain.AttemptNote{
			ID:        "note-1",
			AttemptID: "attempt-1",
			Kind:      domain.AttemptNoteKindCheckpoint,
			Content:   "checkpoint content",
			NextSteps: []string{"next step"},
			Important: true,
			CreatedAt: createdAt,
		},
		Warnings: []string{"warn"},
		ParentEpic: &domain.WorkContextIssue{
			ID:        "epic-1",
			DisplayID: "ISSUE-44",
			Title:     "Epic title",
		},
		Relations: []domain.IssueRelation{{
			ID:            "relation-1",
			SourceIssueID: issueID,
			TargetIssueID: "ISSUE-45",
			Type:          domain.RelationTypeBlocks,
			CreatedAt:     createdAt,
		}},
		RelatedIssueSummaries: []domain.WorkContextIssue{{
			ID:        "issue-2",
			DisplayID: "ISSUE-46",
			Title:     "Related title",
		}},
		RecentComments: []domain.Comment{{
			ID:                 "comment-1",
			IssueID:            issueID,
			Content:            "comment content",
			CreatedBySessionID: &sessionID,
			AuthorLabel:        &agentLabel,
			CreatedAt:          createdAt,
			EditedAt:           &finishedAt,
		}},
		RecentAttemptNotes: []domain.AttemptNote{{
			ID:        "note-2",
			AttemptID: "attempt-1",
			Kind:      domain.AttemptNoteKindWarning,
			Content:   "note content",
			NextSteps: []string{"note step"},
			Important: false,
			CreatedAt: createdAt,
		}},
		DecisionContent: []domain.Decision{{
			ID:        "decision-2",
			IssueID:   &issueID,
			Title:     "Decision content",
			Summary:   "decision summary",
			Content:   "decision body",
			Status:    domain.DecisionStatusActive,
			CreatedAt: createdAt,
		}},
		AttemptHistory: []domain.WorkAttempt{{
			ID:                     "attempt-2",
			IssueID:                issueID,
			SessionID:              &sessionID,
			AgentLabel:             &agentLabel,
			Kind:                   domain.AttemptKindReview,
			Status:                 domain.AttemptStatusFailed,
			IssueVersionAtStart:    12,
			ContextEventIDAtStart:  34,
			LeaseExpiresAt:         createdAt.Add(time.Hour),
			StartedAt:              createdAt,
			LastHeartbeatAt:        createdAt.Add(5 * time.Minute),
			FinishedAt:             &finishedAt,
			ResultSummary:          &attemptResult,
			NextSteps:              []string{"next 1", "next 2"},
			Verification:           []string{"verify 1", "verify 2"},
			FailureReasonCode:      &failureReasonCode,
			InterruptionReasonCode: &interruptionReasonCode,
			ReasonDetails:          &reasonDetails,
		}},
		Artifacts: []domain.Artifact{{
			ID:        "artifact-1",
			IssueID:   issueID,
			AttemptID: &artifactAttemptID,
			Type:      domain.ArtifactTypeFile,
			URI:       "docs/readme.md",
			Title:     &artifactTitle,
			Metadata:  artifactMetadata,
			CreatedAt: createdAt,
		}},
		ProjectInstructions: &projectInstructions,
		ChangesSincePreviousAttempt: []domain.IssueEvent{{
			ID:        7,
			IssueID:   &issueID,
			EventType: "status_changed",
			SessionID: &sessionID,
			AttemptID: &artifactAttemptID,
			Payload:   eventPayload,
			CreatedAt: createdAt,
		}},
		Truncated:         true,
		TruncatedSections: []domain.WorkContextInclude{domain.WorkContextIncludeRecentComments},
	}

	got := domain.CloneWorkContext(source)

	mutatedIssueDescription := "mutated issue description"
	mutatedIssueCriteria := "mutated issue criteria"
	source.Issue.Description = &mutatedIssueDescription
	source.Issue.AcceptanceCriteria = &mutatedIssueCriteria
	source.Blockers[0].Title = "mutated blocker title"
	source.Relations[0].Type = domain.RelationTypeRelatedTo
	source.Decisions[0].Title = "mutated decision title"
	source.PreviousAttempt.FinishedAt = &laterTime
	source.PreviousAttempt.ResultSummary = &attemptResult
	source.PreviousAttempt.NextSteps[0] = "mutated next step"
	source.Checkpoint.NextSteps[0] = "mutated checkpoint step"
	source.Warnings[0] = "mutated warning"
	source.ParentEpic.Title = "mutated epic title"
	source.RelatedIssueSummaries[0].Title = "mutated summary title"
	source.RecentComments[0].Content = "mutated comment"
	source.RecentAttemptNotes[0].Content = "mutated attempt note"
	source.DecisionContent[0].Content = "mutated decision content"
	source.AttemptHistory[0].SessionID = &artifactAttemptID
	source.AttemptHistory[0].AgentLabel = &projectInstructions
	source.AttemptHistory[0].FinishedAt = &laterTime
	source.AttemptHistory[0].ResultSummary = &projectInstructions
	source.AttemptHistory[0].NextSteps[0] = "mutated attempt step"
	source.AttemptHistory[0].Verification[0] = "mutated verification"
	source.AttemptHistory[0].FailureReasonCode = &failureReasonCode
	source.AttemptHistory[0].InterruptionReasonCode = &interruptionReasonCode
	source.AttemptHistory[0].ReasonDetails = &projectInstructions
	source.Artifacts[0].Title = &projectInstructions
	source.Artifacts[0].AttemptID = &sessionID
	source.Artifacts[0].Metadata = json.RawMessage(`{"new":true}`)
	source.ProjectInstructions = &attemptResult
	source.ChangesSincePreviousAttempt[0].IssueID = &artifactAttemptID
	source.ChangesSincePreviousAttempt[0].SessionID = &projectInstructions
	source.ChangesSincePreviousAttempt[0].AttemptID = &attemptResult
	source.ChangesSincePreviousAttempt[0].Payload = json.RawMessage(`{"new":true}`)
	source.TruncatedSections[0] = domain.WorkContextIncludeParentEpic

	if got.Issue.Description == nil || *got.Issue.Description != issueDescription {
		t.Fatalf("Issue.Description = %#v, want %q", got.Issue.Description, issueDescription)
	}
	if got.Issue.AcceptanceCriteria == nil || *got.Issue.AcceptanceCriteria != issueCriteria {
		t.Fatalf("Issue.AcceptanceCriteria = %#v, want %q", got.Issue.AcceptanceCriteria, issueCriteria)
	}
	if got.Blockers[0].Title != "Blocker title" {
		t.Fatalf("Blockers[0].Title = %q, want %q", got.Blockers[0].Title, "Blocker title")
	}
	if got.Relations[0].Type != domain.RelationTypeBlocks {
		t.Fatalf("Relations[0].Type = %q, want %q", got.Relations[0].Type, domain.RelationTypeBlocks)
	}
	if got.Decisions[0].Title != "Decision title" {
		t.Fatalf("Decisions[0].Title = %q, want %q", got.Decisions[0].Title, "Decision title")
	}
	if got.PreviousAttempt == nil || got.PreviousAttempt.FinishedAt == nil || !got.PreviousAttempt.FinishedAt.Equal(finishedAt) {
		t.Fatalf("PreviousAttempt.FinishedAt = %#v, want %v", got.PreviousAttempt.FinishedAt, finishedAt)
	}
	if got.PreviousAttempt.ResultSummary == nil || *got.PreviousAttempt.ResultSummary != resultSummary {
		t.Fatalf("PreviousAttempt.ResultSummary = %#v, want %q", got.PreviousAttempt.ResultSummary, resultSummary)
	}
	if len(got.PreviousAttempt.NextSteps) != 2 || got.PreviousAttempt.NextSteps[0] != "step 1" {
		t.Fatalf("PreviousAttempt.NextSteps = %#v, want [step 1 step 2]", got.PreviousAttempt.NextSteps)
	}
	if got.Checkpoint == nil || len(got.Checkpoint.NextSteps) != 1 || got.Checkpoint.NextSteps[0] != "next step" {
		t.Fatalf("Checkpoint.NextSteps = %#v, want [next step]", got.Checkpoint.NextSteps)
	}
	if got.Warnings[0] != "warn" {
		t.Fatalf("Warnings[0] = %q, want warn", got.Warnings[0])
	}
	if got.ParentEpic == nil || got.ParentEpic.Title != "Epic title" {
		t.Fatalf("ParentEpic.Title = %q, want Epic title", got.ParentEpic.Title)
	}
	if got.RelatedIssueSummaries[0].Title != "Related title" {
		t.Fatalf("RelatedIssueSummaries[0].Title = %q, want Related title", got.RelatedIssueSummaries[0].Title)
	}
	if got.RecentComments[0].Content != "comment content" {
		t.Fatalf("RecentComments[0].Content = %q, want comment content", got.RecentComments[0].Content)
	}
	if got.RecentAttemptNotes[0].Content != "note content" {
		t.Fatalf("RecentAttemptNotes[0].Content = %q, want note content", got.RecentAttemptNotes[0].Content)
	}
	if got.DecisionContent[0].Content != "decision body" {
		t.Fatalf("DecisionContent[0].Content = %q, want decision body", got.DecisionContent[0].Content)
	}
	if got.AttemptHistory[0].SessionID == nil || *got.AttemptHistory[0].SessionID != sessionID {
		t.Fatalf("AttemptHistory[0].SessionID = %#v, want %q", got.AttemptHistory[0].SessionID, sessionID)
	}
	if got.AttemptHistory[0].AgentLabel == nil || *got.AttemptHistory[0].AgentLabel != agentLabel {
		t.Fatalf("AttemptHistory[0].AgentLabel = %#v, want %q", got.AttemptHistory[0].AgentLabel, agentLabel)
	}
	if got.AttemptHistory[0].FinishedAt == nil || !got.AttemptHistory[0].FinishedAt.Equal(finishedAt) {
		t.Fatalf("AttemptHistory[0].FinishedAt = %#v, want %v", got.AttemptHistory[0].FinishedAt, finishedAt)
	}
	if got.AttemptHistory[0].ResultSummary == nil || *got.AttemptHistory[0].ResultSummary != attemptResult {
		t.Fatalf("AttemptHistory[0].ResultSummary = %#v, want %q", got.AttemptHistory[0].ResultSummary, attemptResult)
	}
	if got.AttemptHistory[0].NextSteps[0] != "next 1" {
		t.Fatalf("AttemptHistory[0].NextSteps[0] = %q, want next 1", got.AttemptHistory[0].NextSteps[0])
	}
	if got.AttemptHistory[0].Verification[0] != "verify 1" {
		t.Fatalf("AttemptHistory[0].Verification[0] = %q, want verify 1", got.AttemptHistory[0].Verification[0])
	}
	if got.AttemptHistory[0].FailureReasonCode == nil || *got.AttemptHistory[0].FailureReasonCode != failureReasonCode {
		t.Fatalf("AttemptHistory[0].FailureReasonCode = %#v, want %q", got.AttemptHistory[0].FailureReasonCode, failureReasonCode)
	}
	if got.AttemptHistory[0].InterruptionReasonCode == nil || *got.AttemptHistory[0].InterruptionReasonCode != interruptionReasonCode {
		t.Fatalf("AttemptHistory[0].InterruptionReasonCode = %#v, want %q", got.AttemptHistory[0].InterruptionReasonCode, interruptionReasonCode)
	}
	if got.AttemptHistory[0].ReasonDetails == nil || *got.AttemptHistory[0].ReasonDetails != reasonDetails {
		t.Fatalf("AttemptHistory[0].ReasonDetails = %#v, want %q", got.AttemptHistory[0].ReasonDetails, reasonDetails)
	}
	if got.Artifacts[0].Title == nil || *got.Artifacts[0].Title != artifactTitle {
		t.Fatalf("Artifacts[0].Title = %#v, want %q", got.Artifacts[0].Title, artifactTitle)
	}
	if got.Artifacts[0].AttemptID == nil || *got.Artifacts[0].AttemptID != artifactAttemptID {
		t.Fatalf("Artifacts[0].AttemptID = %#v, want %q", got.Artifacts[0].AttemptID, artifactAttemptID)
	}
	if !reflect.DeepEqual(got.Artifacts[0].Metadata, artifactMetadata) {
		t.Fatalf("Artifacts[0].Metadata = %s, want %s", got.Artifacts[0].Metadata, artifactMetadata)
	}
	if got.ProjectInstructions == nil || *got.ProjectInstructions != projectInstructions {
		t.Fatalf("ProjectInstructions = %#v, want %q", got.ProjectInstructions, projectInstructions)
	}
	if got.ChangesSincePreviousAttempt[0].IssueID == nil || *got.ChangesSincePreviousAttempt[0].IssueID != ulid {
		t.Fatalf("ChangesSincePreviousAttempt[0].IssueID = %#v, want %q", got.ChangesSincePreviousAttempt[0].IssueID, ulid)
	}
	if got.ChangesSincePreviousAttempt[0].SessionID == nil || *got.ChangesSincePreviousAttempt[0].SessionID != sessionID {
		t.Fatalf("ChangesSincePreviousAttempt[0].SessionID = %#v, want %q", got.ChangesSincePreviousAttempt[0].SessionID, sessionID)
	}
	if got.ChangesSincePreviousAttempt[0].AttemptID == nil || *got.ChangesSincePreviousAttempt[0].AttemptID != artifactAttemptID {
		t.Fatalf("ChangesSincePreviousAttempt[0].AttemptID = %#v, want %q", got.ChangesSincePreviousAttempt[0].AttemptID, artifactAttemptID)
	}
	if !reflect.DeepEqual(got.ChangesSincePreviousAttempt[0].Payload, eventPayload) {
		t.Fatalf("ChangesSincePreviousAttempt[0].Payload = %s, want %s", got.ChangesSincePreviousAttempt[0].Payload, eventPayload)
	}
	if got.Truncated != true || len(got.TruncatedSections) != 1 || got.TruncatedSections[0] != domain.WorkContextIncludeRecentComments {
		t.Fatalf("TruncatedSections = %#v, want [recent_comments]", got.TruncatedSections)
	}

	t.Run("preserves nil optionals and nonnil empty slices", func(t *testing.T) {
		emptyContext := domain.NewEmptyWorkContext()
		emptyContext.ParentEpic = nil
		emptyContext.PreviousAttempt = nil
		emptyContext.Checkpoint = nil
		emptyContext.ProjectInstructions = nil
		cloned := domain.CloneWorkContext(emptyContext)
		if cloned.ParentEpic != nil {
			t.Fatal("ParentEpic should remain nil")
		}
		if cloned.PreviousAttempt != nil {
			t.Fatal("PreviousAttempt should remain nil")
		}
		if cloned.Checkpoint != nil {
			t.Fatal("Checkpoint should remain nil")
		}
		if cloned.ProjectInstructions != nil {
			t.Fatal("ProjectInstructions should remain nil")
		}
		for _, fieldName := range []string{"Blockers", "Decisions", "Warnings", "Relations", "RelatedIssueSummaries", "RecentComments", "RecentAttemptNotes", "DecisionContent", "AttemptHistory", "Artifacts", "ChangesSincePreviousAttempt", "TruncatedSections"} {
			field := reflect.ValueOf(cloned).FieldByName(fieldName)
			if field.IsNil() {
				t.Fatalf("field %s should be nonnil empty slice", fieldName)
			}
			if field.Len() != 0 {
				t.Fatalf("field %s length = %d, want 0", fieldName, field.Len())
			}
		}
	})
}
