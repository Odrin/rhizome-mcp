package domain_test

import (
	"bytes"
	"errors"
	"testing"

	"rhizome-mcp/internal/domain"
)

func TestSessionAttributionValidationAndCanonicalRequests(t *testing.T) {
	const (
		sessionA = "01BX5ZZKBKACTAV9WEVGEMMVRZ"
		sessionB = "01BX5ZZKBKACTAV9WEVGEMMVS0"
		issueID  = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
		targetID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	)

	assertSessionDetail := func(t *testing.T, err error, wantCode string) {
		t.Helper()
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) || domainErr.Code != wantCode {
			t.Fatalf("err = %v, want code %s", err, wantCode)
		}
		for _, detail := range domainErr.Details {
			if detail.Field == "session_id" {
				return
			}
		}
		t.Fatalf("expected session_id detail in %#v", domainErr.Details)
	}

	t.Run("create_issue", func(t *testing.T) {
		mutable := sessionA
		normalized, err := (domain.CreateIssueInput{
			Type:      domain.TypeTask,
			Title:     "Create with session",
			SessionID: &mutable,
		}).Validate()
		if err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		mutable = sessionB
		if normalized.SessionID == nil || *normalized.SessionID != sessionA {
			t.Fatalf("normalized.SessionID = %#v, want copied %q", normalized.SessionID, sessionA)
		}

		bad := "01bx5zzkbkactav9wevgemmvrz"
		_, err = (domain.CreateIssueInput{
			Type:      domain.TypeTask,
			Title:     "Bad session",
			SessionID: &bad,
		}).Validate()
		assertSessionDetail(t, err, domain.CodeInvalidArgument)

		other := sessionB
		base, err := domain.CanonicalCreateIssueRequest(domain.CreateIssueInput{Type: domain.TypeTask, Title: "Same", Status: domain.StatusOpen, Priority: domain.PriorityMedium})
		if err != nil {
			t.Fatal(err)
		}
		withA, err := domain.CanonicalCreateIssueRequest(domain.CreateIssueInput{Type: domain.TypeTask, Title: "Same", Status: domain.StatusOpen, Priority: domain.PriorityMedium, SessionID: normalized.SessionID})
		if err != nil {
			t.Fatal(err)
		}
		withB, err := domain.CanonicalCreateIssueRequest(domain.CreateIssueInput{Type: domain.TypeTask, Title: "Same", Status: domain.StatusOpen, Priority: domain.PriorityMedium, SessionID: &other})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(base, withA) || !bytes.Equal(base, withB) {
			t.Fatalf("CanonicalCreateIssueRequest differs by session: base=%s withA=%s withB=%s", base, withA, withB)
		}
	})

	t.Run("update_issue", func(t *testing.T) {
		mutable := sessionA
		normalized, err := (domain.UpdateIssueInput{
			IssueID:         issueID,
			ExpectedVersion: 1,
			Changes:         domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "Updated"}},
			SessionID:       &mutable,
		}).Validate()
		if err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		mutable = sessionB
		if normalized.SessionID == nil || *normalized.SessionID != sessionA {
			t.Fatalf("normalized.SessionID = %#v, want copied %q", normalized.SessionID, sessionA)
		}

		bad := "not-a-ulid"
		_, err = (domain.UpdateIssueInput{
			IssueID:         issueID,
			ExpectedVersion: 1,
			Changes:         domain.IssuePatch{Title: domain.OptionalValue[string]{Set: true, Value: "Updated"}},
			SessionID:       &bad,
		}).Validate()
		assertSessionDetail(t, err, domain.CodeValidationError)

		other := sessionB
		base, err := domain.CanonicalUpdateIssueRequest(domain.UpdateIssueInput{IssueID: issueID, ExpectedVersion: 1, Changes: normalized.Changes})
		if err != nil {
			t.Fatal(err)
		}
		withA, err := domain.CanonicalUpdateIssueRequest(domain.UpdateIssueInput{IssueID: issueID, ExpectedVersion: 1, Changes: normalized.Changes, SessionID: normalized.SessionID})
		if err != nil {
			t.Fatal(err)
		}
		withB, err := domain.CanonicalUpdateIssueRequest(domain.UpdateIssueInput{IssueID: issueID, ExpectedVersion: 1, Changes: normalized.Changes, SessionID: &other})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(base, withA) || !bytes.Equal(base, withB) {
			t.Fatalf("CanonicalUpdateIssueRequest differs by session: base=%s withA=%s withB=%s", base, withA, withB)
		}
	})

	t.Run("archive_issue", func(t *testing.T) {
		mutable := sessionA
		normalized, err := (domain.ArchiveIssueInput{
			IssueID:         issueID,
			ExpectedVersion: 1,
			SessionID:       &mutable,
		}).Validate()
		if err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		mutable = sessionB
		if normalized.SessionID == nil || *normalized.SessionID != sessionA {
			t.Fatalf("normalized.SessionID = %#v, want copied %q", normalized.SessionID, sessionA)
		}

		bad := "bad-ulid"
		_, err = (domain.ArchiveIssueInput{
			IssueID:         issueID,
			ExpectedVersion: 1,
			SessionID:       &bad,
		}).Validate()
		assertSessionDetail(t, err, domain.CodeValidationError)

		other := sessionB
		base, err := domain.CanonicalArchiveIssueRequest(domain.ArchiveIssueInput{IssueID: issueID, ExpectedVersion: 1})
		if err != nil {
			t.Fatal(err)
		}
		withA, err := domain.CanonicalArchiveIssueRequest(domain.ArchiveIssueInput{IssueID: issueID, ExpectedVersion: 1, SessionID: normalized.SessionID})
		if err != nil {
			t.Fatal(err)
		}
		withB, err := domain.CanonicalArchiveIssueRequest(domain.ArchiveIssueInput{IssueID: issueID, ExpectedVersion: 1, SessionID: &other})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(base, withA) || !bytes.Equal(base, withB) {
			t.Fatalf("CanonicalArchiveIssueRequest differs by session: base=%s withA=%s withB=%s", base, withA, withB)
		}
	})

	t.Run("manage_issue_relation", func(t *testing.T) {
		mutable := sessionA
		normalized, err := (domain.ManageIssueRelationInput{
			Action:        domain.RelationActionAdd,
			SourceIssueID: issueID,
			TargetIssueID: targetID,
			RelationType:  domain.RelationTypeBlocks,
			SessionID:     &mutable,
		}).Validate()
		if err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		mutable = sessionB
		if normalized.SessionID == nil || *normalized.SessionID != sessionA {
			t.Fatalf("normalized.SessionID = %#v, want copied %q", normalized.SessionID, sessionA)
		}

		bad := " "
		_, err = (domain.ManageIssueRelationInput{
			Action:        domain.RelationActionAdd,
			SourceIssueID: issueID,
			TargetIssueID: targetID,
			RelationType:  domain.RelationTypeBlocks,
			SessionID:     &bad,
		}).Validate()
		assertSessionDetail(t, err, domain.CodeInvalidArgument)

		other := sessionB
		base, err := domain.CanonicalManageIssueRelationRequest(domain.ManageIssueRelationInput{
			Action:        domain.RelationActionAdd,
			SourceIssueID: issueID,
			TargetIssueID: targetID,
			RelationType:  domain.RelationTypeBlocks,
		})
		if err != nil {
			t.Fatal(err)
		}
		withA, err := domain.CanonicalManageIssueRelationRequest(domain.ManageIssueRelationInput{
			Action:        domain.RelationActionAdd,
			SourceIssueID: issueID,
			TargetIssueID: targetID,
			RelationType:  domain.RelationTypeBlocks,
			SessionID:     normalized.SessionID,
		})
		if err != nil {
			t.Fatal(err)
		}
		withB, err := domain.CanonicalManageIssueRelationRequest(domain.ManageIssueRelationInput{
			Action:        domain.RelationActionAdd,
			SourceIssueID: issueID,
			TargetIssueID: targetID,
			RelationType:  domain.RelationTypeBlocks,
			SessionID:     &other,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(base, withA) || !bytes.Equal(base, withB) {
			t.Fatalf("CanonicalManageIssueRelationRequest differs by session: base=%s withA=%s withB=%s", base, withA, withB)
		}
	})
}
