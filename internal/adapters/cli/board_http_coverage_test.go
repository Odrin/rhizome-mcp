package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rhizome-mcp/internal/domain"
)

type captureBoardService struct {
	stubBoardService
	searchCalls int
	boardCalls  int
}

func (s *captureBoardService) GetBoard(context.Context) (domain.BoardResult, error) {
	s.boardCalls++
	return s.stubBoardService.GetBoard(context.Background())
}

func (s *captureBoardService) Search(ctx context.Context, input domain.SearchInput) (domain.SearchPage, error) {
	s.searchCalls++
	return s.stubBoardService.Search(ctx, input)
}

func TestBoardHTTPHandlerCoversHeadAndConditionalRequestBranches(t *testing.T) {
	service := &stubBoardService{board: boardResultFixture(), detail: domain.IssueDetail{Issue: domain.Issue{ID: "issue-1", DisplayID: "ISSUE-1", Title: "Initial title"}}, searchPage: domain.SearchPage{Results: []domain.SearchResult{{EntityType: domain.SearchEntityTypeIssue, EntityID: "issue-1", Title: "Alpha", Snippet: "match"}}}}
	handler := NewBoardHTTPHandler(service)

	t.Run("board page head has no body", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodHead, "/", nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		if recorder.Body.Len() != 0 {
			t.Fatalf("body length = %d, want 0", recorder.Body.Len())
		}
		if !strings.Contains(recorder.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("content type = %q", recorder.Header().Get("Content-Type"))
		}
	})

	t.Run("detail api head returns etag and no body", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodHead, "/api/issues/ISSUE-1", nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		if recorder.Body.Len() != 0 {
			t.Fatalf("body length = %d, want 0", recorder.Body.Len())
		}
		if recorder.Header().Get("ETag") == "" {
			t.Fatal("expected etag header")
		}
	})

	t.Run("search api and search page head are bodyless", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			path string
		}{
			{name: "page", path: "/search?q=alpha"},
			{name: "api", path: "/api/search?q=alpha"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodHead, tc.path, nil)
				handler.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusOK {
					t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
				}
				if recorder.Body.Len() != 0 {
					t.Fatalf("body length = %d, want 0", recorder.Body.Len())
				}
			})
		}
	})

	t.Run("etag matching handles wildcard quoted and weak values", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			ifNone   string
			wantCode int
			wantBody bool
			path     string
		}{
			{name: "wildcard", path: "/api/board", ifNone: "*", wantCode: http.StatusNotModified, wantBody: false},
			{name: "quoted", path: "/api/board", ifNone: "quoted", wantCode: http.StatusNotModified, wantBody: false},
			{name: "weak", path: "/api/issues/ISSUE-1", ifNone: "weak", wantCode: http.StatusNotModified, wantBody: false},
			{name: "non-match", path: "/api/issues/ISSUE-1", ifNone: "non-match", wantCode: http.StatusOK, wantBody: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var etag string
				if tc.path == "/api/board" {
					recorder := httptest.NewRecorder()
					request := httptest.NewRequest(http.MethodGet, tc.path, nil)
					handler.ServeHTTP(recorder, request)
					etag = recorder.Header().Get("ETag")
					if etag == "" {
						t.Fatal("expected initial etag")
					}
					switch tc.name {
					case "wildcard":
						tc.ifNone = etag
					case "quoted":
						tc.ifNone = fmt.Sprintf("\"%s\"", etag)
					}
				} else {
					recorder := httptest.NewRecorder()
					request := httptest.NewRequest(http.MethodGet, "/api/issues/ISSUE-1", nil)
					handler.ServeHTTP(recorder, request)
					etag = recorder.Header().Get("ETag")
					if etag == "" {
						t.Fatal("expected initial etag")
					}
					switch tc.name {
					case "weak":
						tc.ifNone = fmt.Sprintf("W/%s", etag)
					case "non-match":
						tc.ifNone = "\"different\""
					}
				}
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodGet, tc.path, nil)
				request.Header.Set("If-None-Match", tc.ifNone)
				handler.ServeHTTP(recorder, request)
				if recorder.Code != tc.wantCode {
					t.Fatalf("status = %d, want %d", recorder.Code, tc.wantCode)
				}
				if recorder.Body.Len() == 0 && tc.wantBody {
					t.Fatal("expected response body")
				}
				if recorder.Body.Len() != 0 && !tc.wantBody {
					t.Fatalf("expected empty body, got %q", recorder.Body.String())
				}
			})
		}
	})
}

func TestBoardHTTPHandlerRendersSearchPageStatesAndEscapesResults(t *testing.T) {
	service := &captureBoardService{stubBoardService: stubBoardService{board: boardResultFixture(), searchPage: domain.SearchPage{Results: []domain.SearchResult{{EntityType: domain.SearchEntityTypeIssue, EntityID: "issue-1", IssueID: stringPtr("ISSUE-1"), Title: "<strong>Alpha</strong>", Snippet: "match <script>alert(1)</script>", Score: 2.5}}}}}
	handler := NewBoardHTTPHandler(service)

	t.Run("empty query shows the initial prompt", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/search", nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		if !strings.Contains(recorder.Body.String(), "Enter a search query to find issues, comments, decisions, reviews, and attempt notes.") {
			t.Fatalf("body = %q", recorder.Body.String())
		}
	})

	t.Run("escaped results and query values render in the page", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/search?q=alpha&entity_type=issue", nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		body := recorder.Body.String()
		if !strings.Contains(body, `value="alpha"`) {
			t.Fatalf("body missing query value: %s", body)
		}
		if !strings.Contains(body, "&lt;strong&gt;Alpha&lt;/strong&gt;") {
			t.Fatalf("body missing escaped title: %s", body)
		}
		if !strings.Contains(body, "match &lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Fatalf("body missing escaped snippet: %s", body)
		}
		if !strings.Contains(body, `href="/issues/ISSUE-1"`) {
			t.Fatalf("body missing issue link: %s", body)
		}
		if service.searchCalls != 1 {
			t.Fatalf("search calls = %d, want 1", service.searchCalls)
		}
	})

	t.Run("invalid search requests show the invalid state without calling search", func(t *testing.T) {
		service.searchCalls = 0
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/search?q=alpha&unsupported=1", nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		body := recorder.Body.String()
		if !strings.Contains(body, "Invalid search query.") {
			t.Fatalf("body = %q", body)
		}
		if service.searchCalls != 0 {
			t.Fatalf("search calls = %d, want 0", service.searchCalls)
		}
	})

	t.Run("service errors render the unavailable state", func(t *testing.T) {
		service.searchCalls = 0
		service.stubBoardService.searchErr = errors.New("boom")
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/search?q=alpha", nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		body := recorder.Body.String()
		if !strings.Contains(body, "Search temporarily unavailable.") {
			t.Fatalf("body = %q", body)
		}
	})
}

func TestBoardHTTPHandlerRejectsMalformedPathsAndInvalidSearchRequests(t *testing.T) {
	service := &captureBoardService{stubBoardService: stubBoardService{board: boardResultFixture()}}
	handler := NewBoardHTTPHandler(service)

	for _, tc := range []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "unsupported method on issue api", method: http.MethodPost, path: "/api/issues/ISSUE-1", wantStatus: http.StatusMethodNotAllowed},
		{name: "unsupported method on search api", method: http.MethodPost, path: "/api/search?q=alpha", wantStatus: http.StatusMethodNotAllowed},
		{name: "extra issue path segment", method: http.MethodGet, path: "/issues/ISSUE-1/extra", wantStatus: http.StatusBadRequest},
		{name: "missing issue identifier", method: http.MethodGet, path: "/issues/", wantStatus: http.StatusBadRequest},
		{name: "unknown route", method: http.MethodGet, path: "/api/missing", wantStatus: http.StatusNotFound},
		{name: "invalid search limit", method: http.MethodGet, path: "/api/search?q=alpha&limit=9000", wantStatus: http.StatusBadRequest},
		{name: "invalid search param", method: http.MethodGet, path: "/api/search?q=alpha&unsupported=1", wantStatus: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service.searchCalls = 0
			service.boardCalls = 0
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tc.method, tc.path, nil)
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
			if tc.method == http.MethodPost && tc.path == "/api/issues/ISSUE-1" {
				if service.boardCalls != 0 || service.searchCalls != 0 {
					t.Fatalf("expected no service calls on method rejection, got board=%d search=%d", service.boardCalls, service.searchCalls)
				}
			}
			if tc.path == "/api/search?q=alpha&limit=9000" || tc.path == "/api/search?q=alpha&unsupported=1" {
				if service.searchCalls != 0 {
					t.Fatalf("expected no search calls for invalid search input, got %d", service.searchCalls)
				}
			}
			if tc.path == "/issues/ISSUE-1/extra" || tc.path == "/issues/" {
				if service.boardCalls != 0 {
					t.Fatalf("expected no service calls for malformed issue path, got %d", service.boardCalls)
				}
			}
		})
	}
}

func TestBoardHTTPHandlerMapsServiceErrorsToPublicResponses(t *testing.T) {
	service := &captureBoardService{stubBoardService: stubBoardService{board: boardResultFixture()}}
	handler := NewBoardHTTPHandler(service)

	t.Run("issue detail service errors become 500", func(t *testing.T) {
		service.stubBoardService.err = errors.New("boom")
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/issues/ISSUE-1", nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
		}
		if !strings.Contains(recorder.Body.String(), http.StatusText(http.StatusInternalServerError)) {
			t.Fatalf("body = %q", recorder.Body.String())
		}
	})

	t.Run("search page service errors become 500", func(t *testing.T) {
		service.stubBoardService.err = errors.New("boom")
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/search?q=alpha", nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
		}
	})
}
