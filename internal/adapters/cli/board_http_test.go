package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rhizome-mcp/internal/domain"
)

func TestBoardHTTPHandlerRejectsUnsupportedMethodsAndUnknownRoutes(t *testing.T) {
	handler := NewBoardHTTPHandler(&stubBoardService{board: boardResultFixture()})

	t.Run("unsupported method", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/", nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
		}
		if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
			t.Fatalf("allow header = %q, want %q", got, "GET, HEAD")
		}
	})

	t.Run("unknown route", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/missing", nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
		}
	})
}

func TestBoardHTTPHandlerServesHTMLAndJSONWithSecurityHeaders(t *testing.T) {
	handler := NewBoardHTTPHandler(&stubBoardService{board: boardResultFixture()})

	t.Run("html", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		if !strings.Contains(recorder.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("content type = %q, want html", recorder.Header().Get("Content-Type"))
		}
		if got := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") {
			t.Fatalf("csp = %q", got)
		}
		if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("nosniff = %q", got)
		}
		if got := recorder.Header().Get("Referrer-Policy"); got != "no-referrer" {
			t.Fatalf("referrer policy = %q", got)
		}
		if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("cache control = %q", got)
		}
		if !strings.Contains(recorder.Body.String(), "Rhizome status board") {
			t.Fatalf("body = %q", recorder.Body.String())
		}
	})

	t.Run("json", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/board", nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		if !strings.Contains(recorder.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("content type = %q, want json", recorder.Header().Get("Content-Type"))
		}
		var payload BoardResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(payload.StatusCounts) == 0 {
			t.Fatal("expected status counts in json payload")
		}
	})
}

func TestBoardHTTPHandlerServesIssueDetailPageAndEscapesText(t *testing.T) {
	description := "Need <script>alert(1)</script>"
	detail := domain.IssueDetail{
		Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", DisplayID: "ISSUE-10", Title: "Unsafe title <b>", Description: &description},
	}
	handler := NewBoardHTTPHandler(&stubBoardService{detail: detail})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/issues/ISSUE-10", nil)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !strings.Contains(recorder.Body.String(), "ISSUE-10") {
		t.Fatalf("body = %q", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "<script>") {
		t.Fatalf("body unexpectedly contained raw script tag: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Need &lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("escaped body = %q", recorder.Body.String())
	}
}

func TestBoardHTTPHandlerHandlesIssueDetailRoutes(t *testing.T) {
	t.Run("lowercase display id", func(t *testing.T) {
		handler := NewBoardHTTPHandler(&stubBoardService{detail: domain.IssueDetail{Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", DisplayID: "ISSUE-10"}}})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/issues/issue-10", nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		if !strings.Contains(recorder.Body.String(), "ISSUE-10") {
			t.Fatalf("body = %q", recorder.Body.String())
		}
	})

	for _, tc := range []struct {
		name       string
		path       string
		wantStatus int
	}{
		{name: "empty", path: "/issues/", wantStatus: http.StatusBadRequest},
		{name: "extra segment", path: "/issues/ISSUE-10/extra", wantStatus: http.StatusBadRequest},
		{name: "encoded slash", path: "/issues/%2F", wantStatus: http.StatusBadRequest},
		{name: "missing", path: "/issues/does-not-exist", wantStatus: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &stubBoardService{err: domain.NewError(domain.CodeIssueNotFound, "missing", false)}
			if tc.name == "missing" {
				service.err = domain.NewError(domain.CodeIssueNotFound, "missing", false)
			} else {
				service.err = domain.NewError(domain.CodeInvalidArgument, "bad", false)
			}
			handler := NewBoardHTTPHandler(service)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
		})
	}
}

func TestRenderServedBoardHTMLUsesDisplayIDLinks(t *testing.T) {
	result := domain.BoardResult{
		GeneratedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		ActiveAttempts: []domain.ActiveAttemptSummary{{
			AttemptID:      "attempt-1",
			IssueID:        "01ARZ3NDEKTSV4RRFFQ69G5FAV",
			IssueDisplayID: "ISSUE-10",
			IssueTitle:     "Active issue",
		}},
		BlockedIssues: []domain.IssueProjection{{
			Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", DisplayID: "ISSUE-20"},
		}},
		ReviewRequests: []domain.ReviewRequest{{
			ID:                 "review-1",
			IssueID:            "01ARZ3NDEKTSV4RRFFQ69G5FAV",
			Status:             domain.ReviewRequestStatusOpen,
			TargetIssueVersion: 1,
			CreatedAt:          time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		}},
		PlanningGraph: domain.GraphResult{Nodes: []domain.IssueProjection{{
			Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", DisplayID: "ISSUE-30"},
		}, {
			Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV2", DisplayID: "ISSUE-40"},
		}}},
	}

	html := renderServedBoardHTML(result)
	for _, want := range []string{`<a href="/issues/ISSUE-10">ISSUE-10</a>`, `<a href="/issues/ISSUE-20">ISSUE-20</a>`} {
		if !strings.Contains(html, want) {
			t.Fatalf("served board HTML missing %q:\n%s", want, html)
		}
	}
	if !strings.Contains(html, `aria-label="ISSUE-30"`) {
		t.Fatalf("served board SVG anchor missing aria-label: %s", html)
	}
	if !strings.Contains(html, `href="/issues/ISSUE-30"`) {
		t.Fatalf("served board SVG anchor missing display-id href: %s", html)
	}
	if !strings.Contains(html, `href="/issues/ISSUE-40"`) {
		t.Fatalf("served board review request mapping missing display-id href: %s", html)
	}

	detail := domain.IssueDetail{
		Issue:               domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", DisplayID: "ISSUE-50"},
		RootIssueProjection: &domain.IssueProjection{Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV2", DisplayID: "ISSUE-60"}},
		Graph:               domain.GraphResult{Nodes: []domain.IssueProjection{{Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV2", DisplayID: "ISSUE-60"}}}},
	}
	detailHTML := renderIssueDetailHTML(detail)
	if !strings.Contains(detailHTML, `<a href="/issues/ISSUE-60">ISSUE-60</a>`) {
		t.Fatalf("detail page missing root projection link: %s", detailHTML)
	}

	staticHTML := renderBoardHTML(result)
	if strings.Contains(staticHTML, "/issues/") {
		t.Fatalf("static board HTML unexpectedly contained issue path: %s", staticHTML)
	}
}

func TestBoardHTTPHandlerHandlesHeadAndContext(t *testing.T) {
	handler := NewBoardHTTPHandler(&stubBoardService{detail: domain.IssueDetail{Issue: domain.Issue{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", DisplayID: "ISSUE-10"}}})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodHead, "/issues/ISSUE-10", nil)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("HEAD body length = %d, want 0", recorder.Body.Len())
	}
}

func TestBoardHTTPHandlerMapsServiceFailuresToInternalServerError(t *testing.T) {
	handler := NewBoardHTTPHandler(&stubBoardService{err: context.Canceled})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
}

type boardRequestContextKey struct{}

type contextTrackingBoardService struct {
	stubBoardService
	seenCtx context.Context
}

func (s *contextTrackingBoardService) GetBoard(ctx context.Context) (domain.BoardResult, error) {
	s.seenCtx = ctx
	return s.stubBoardService.GetBoard(ctx)
}

func (s *contextTrackingBoardService) GetIssueDetail(ctx context.Context, identifier string) (domain.IssueDetail, error) {
	s.seenCtx = ctx
	return s.stubBoardService.GetIssueDetail(ctx, identifier)
}

func TestBoardHTTPHandlerPassesRequestContextToBoardService(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "html", path: "/"},
		{name: "json", path: "/api/board"},
		{name: "detail", path: "/issues/ISSUE-10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &contextTrackingBoardService{stubBoardService: stubBoardService{board: boardResultFixture()}}
			handler := NewBoardHTTPHandler(service)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tc.path, nil).WithContext(context.WithValue(context.Background(), boardRequestContextKey{}, tc.name))
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}
			if service.seenCtx == nil {
				t.Fatal("expected request context to be propagated")
			}
			if got := service.seenCtx.Value(boardRequestContextKey{}); got != tc.name {
				t.Fatalf("request context value = %v, want %q", got, tc.name)
			}
		})
	}
}

func boardResultFixture() domain.BoardResult {
	return domain.BoardResult{GeneratedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC), StatusCounts: []domain.EffectiveStatusCount{{EffectiveStatus: domain.EffectiveStatusOpen, Count: 1}}}
}
