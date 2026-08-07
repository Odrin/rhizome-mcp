package cli

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"rhizome-mcp/internal/domain"
)

// renderBoardHTML renders a fully self-contained HTML status board: no
// <script src=...>, no <link rel="stylesheet" href=...>, no CDN or network
// references of any kind. The dependency/planning graph is rendered as
// hand-built inline SVG (see renderBoardGraphSVG), and the same graph is also
// included as portable Mermaid source text for copying into any renderer.
func renderBoardHTML(result domain.BoardResult) (string, error) {
	vm := newBoardStaticPageViewModel(result)
	return renderBoardTemplate("boardStaticPage", vm)
}

func renderServedBoardHTML(result domain.BoardResult) (string, error) {
	return renderServedBoardHTMLWithSearchState(result, servedBoardSearchState{})
}

func renderServedBoardHTMLWithSearchState(result domain.BoardResult, state servedBoardSearchState) (string, error) {
	vm := newBoardServedPageViewModel(result, state)
	return renderBoardTemplate("boardServedPage", vm)
}

type servedBoardSearchState struct {
	Query         string
	EntityType    string
	StatusMessage string
	Results       []domain.SearchResult
	HasMore       bool
	Invalid       bool
	Error         bool
}

func renderIssueDetailHTML(detail domain.IssueDetail) (string, error) {
	vm := newIssueDetailPageViewModel(detail)
	return renderBoardTemplate("boardIssueDetailPage", vm)
}

func renderBoardTemplate(name string, data any) (string, error) {
	var buf bytes.Buffer
	if err := boardTemplates.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func buildBoardSearchStatusMessage(query string, count int, hasMore bool, invalid bool, searchErr bool) string {
	switch {
	case invalid:
		return "Invalid search query."
	case searchErr:
		return "Search temporarily unavailable."
	case strings.TrimSpace(query) == "":
		return "Initial search: enter a query to find issues, comments, decisions, reviews, and attempt notes."
	case count == 0:
		return fmt.Sprintf("No results found for %q.", query)
	case hasMore:
		return fmt.Sprintf("Showing %d results for %q.", count, query)
	default:
		return fmt.Sprintf("Showing %d result(s) for %q.", count, query)
	}
}

func EffectiveStatusForIssue(detail domain.IssueDetail) domain.EffectiveStatus {
	status, err := domain.EffectiveStatusFor(detail.Issue.Status, detail.LatestAttempt != nil)
	if err != nil {
		return domain.EffectiveStatus(detail.Issue.Status)
	}
	return status
}

func formatIssueDetailTimestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func issueActivitySummary(item domain.ActivityItem) string {
	switch item.EntityType {
	case domain.ActivityEntityTypeComment:
		if item.Comment != nil && strings.TrimSpace(item.Comment.Content) != "" {
			return strings.TrimSpace(item.Comment.Content)
		}
	case domain.ActivityEntityTypeDecision:
		if item.Decision != nil {
			if strings.TrimSpace(item.Decision.Title) != "" {
				return item.Decision.Title
			}
			if strings.TrimSpace(item.Decision.Summary) != "" {
				return item.Decision.Summary
			}
		}
	case domain.ActivityEntityTypeAttempt:
		if item.Attempt != nil {
			if item.Attempt.ResultSummary != nil && strings.TrimSpace(*item.Attempt.ResultSummary) != "" {
				return *item.Attempt.ResultSummary
			}
			if item.Attempt.FailureReasonCode != nil {
				return string(*item.Attempt.FailureReasonCode)
			}
		}
	case domain.ActivityEntityTypeReview:
		if item.Review != nil {
			if strings.TrimSpace(string(item.Review.Status)) != "" {
				return string(item.Review.Status)
			}
		}
	case domain.ActivityEntityTypeEvent:
		if item.Event != nil && item.Event.EventType != "" {
			return item.Event.EventType
		}
	case domain.ActivityEntityTypeArtifact:
		if item.Artifact != nil && item.Artifact.Title != nil && strings.TrimSpace(*item.Artifact.Title) != "" {
			return *item.Artifact.Title
		}
	case domain.ActivityEntityTypeAttemptNote:
		if item.AttemptNote != nil && strings.TrimSpace(item.AttemptNote.Content) != "" {
			return item.AttemptNote.Content
		}
	}
	return string(item.EntityType)
}
