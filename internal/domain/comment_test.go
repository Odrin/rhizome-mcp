package domain_test

import (
	"errors"
	"strings"
	"testing"

	"rhizome-mcp/internal/domain"
)

const commentSessionID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"

func TestAddCommentInputValidateNormalizesAndPreservesContent(t *testing.T) {
	session := commentSessionID
	input := domain.AddCommentInput{IssueID: "issue-42", Content: "  **keep Markdown whitespace**  ", SessionID: &session}

	normalized, err := input.Validate()
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if normalized.IssueID != "ISSUE-42" || normalized.Content != input.Content ||
		normalized.SessionID == input.SessionID || normalized.SessionID == nil || *normalized.SessionID != session {
		t.Fatalf("normalized input = %#v", normalized)
	}
	session = "changed"
	if *normalized.SessionID != commentSessionID {
		t.Fatal("normalized session ID shares caller storage")
	}
}

func TestAddCommentInputValidateRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		input domain.AddCommentInput
		code  string
	}{
		{"issue", domain.AddCommentInput{IssueID: "ISSUE-0", Content: "content"}, domain.CodeInvalidArgument},
		{"blank", domain.AddCommentInput{IssueID: "ISSUE-1", Content: " \n\t"}, domain.CodeInvalidArgument},
		{"nul", domain.AddCommentInput{IssueID: "ISSUE-1", Content: "bad\x00content"}, domain.CodeInvalidArgument},
		{"oversized", domain.AddCommentInput{IssueID: "ISSUE-1", Content: strings.Repeat("x", domain.MaxCommentRunes+1)}, domain.CodeLimitExceeded},
		{"session", domain.AddCommentInput{IssueID: "ISSUE-1", Content: "content", SessionID: commentStringPointer("bad")}, domain.CodeInvalidArgument},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.input.Validate()
			if !errors.Is(err, &domain.Error{Code: test.code}) {
				t.Fatalf("Validate() error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestCloneCommentCopiesPointerFields(t *testing.T) {
	session := commentSessionID
	label := "agent"
	comment := domain.Comment{CreatedBySessionID: &session, AuthorLabel: &label}
	cloned := domain.CloneComment(comment)
	session = "changed"
	label = "changed"
	if cloned.CreatedBySessionID == comment.CreatedBySessionID || cloned.AuthorLabel == comment.AuthorLabel ||
		*cloned.CreatedBySessionID != commentSessionID || *cloned.AuthorLabel != "agent" {
		t.Fatalf("clone = %#v", cloned)
	}
}

func commentStringPointer(value string) *string { return &value }
