package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ids"
	"rhizome-mcp/internal/migrations"
	projectruntime "rhizome-mcp/internal/runtime"
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

// ISSUE-181: the JSON API's active_reservations field is a filtered
// BoardReservation (never a raw domain.Reservation), and its
// issue_display_id is resolved from the matching ActiveAttempts row rather
// than left blank.
func TestBoardHTTPHandlerJSONIncludesActiveReservationsWithIssueDisplayID(t *testing.T) {
	board := boardResultFixture()
	board.ActiveAttempts = []domain.ActiveAttemptSummary{{AttemptID: "attempt-1", IssueID: "issue-1", IssueDisplayID: "ISSUE-1", IssueTitle: "Work", Kind: domain.AttemptKindWork}}
	board.ActiveReservations = []domain.Reservation{{ID: "reservation-1", IssueID: "issue-1", AttemptID: "attempt-1", Kind: domain.ResourceKindFile, DisplayValue: "a.go", Status: domain.ReservationStatusActive, ComparisonValue: "file:a.go", Version: 3}}
	handler := NewBoardHTTPHandler(&stubBoardService{board: board})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/board", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var payload BoardResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.ActiveReservations) != 1 {
		t.Fatalf("active reservations = %+v, want 1", payload.ActiveReservations)
	}
	reservation := payload.ActiveReservations[0]
	if reservation.ID != "reservation-1" || reservation.AttemptID != "attempt-1" || reservation.Kind != "file" || reservation.DisplayValue != "a.go" {
		t.Fatalf("reservation = %+v", reservation)
	}
	if reservation.IssueDisplayID != "ISSUE-1" {
		t.Fatalf("reservation issue_display_id = %q, want ISSUE-1", reservation.IssueDisplayID)
	}
	if strings.Contains(recorder.Body.String(), "file:a.go") {
		t.Fatalf("json response leaked the internal comparison key: %s", recorder.Body.String())
	}
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
	if !strings.Contains(recorder.Body.String(), "<script>") {
		t.Fatalf("body missing inline refresh script: %s", recorder.Body.String())
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

	html, err := renderServedBoardHTML(result)
	if err != nil {
		t.Fatalf("renderServedBoardHTML: %v", err)
	}
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
	detailHTML, err := renderIssueDetailHTML(detail)
	if err != nil {
		t.Fatalf("renderIssueDetailHTML: %v", err)
	}
	if !strings.Contains(detailHTML, `<a href="/issues/ISSUE-60">ISSUE-60</a>`) {
		t.Fatalf("detail page missing root projection link: %s", detailHTML)
	}

	staticHTML, err := renderBoardHTML(result)
	if err != nil {
		t.Fatalf("renderBoardHTML: %v", err)
	}
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

func TestBoardHTTPHandlerSearchAPIMapsParamsAndErrors(t *testing.T) {
	t.Run("invalid params return 400 json", func(t *testing.T) {
		handler := NewBoardHTTPHandler(&stubBoardService{})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/search?q=alpha&limit=abc", nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
		}
		if !strings.Contains(recorder.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("content type = %q, want json", recorder.Header().Get("Content-Type"))
		}
		if !strings.Contains(recorder.Body.String(), `"error":"invalid search request"`) {
			t.Fatalf("body = %q", recorder.Body.String())
		}
	})

	t.Run("valid search returns dto and head bodyless", func(t *testing.T) {
		handler := NewBoardHTTPHandler(&stubBoardService{searchPage: domain.SearchPage{Results: []domain.SearchResult{{EntityType: domain.SearchEntityTypeIssue, EntityID: "issue-1", IssueID: strPtr("ISSUE-1"), Title: "First", Snippet: "match [alpha]", Score: 1.5}}, NextCursor: strPtr("next"), HasMore: true}})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/search?q=alpha&entity_type=issue&limit=2&snippet_length=11&include_archived=true", nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		var payload struct {
			Results []struct {
				EntityType string  `json:"entity_type"`
				EntityID   string  `json:"entity_id"`
				IssueID    *string `json:"issue_id"`
				Title      string  `json:"title"`
				Snippet    string  `json:"snippet"`
				Score      float64 `json:"score"`
			} `json:"results"`
			NextCursor *string `json:"next_cursor"`
			HasMore    bool    `json:"has_more"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if payload.Results[0].EntityType != "issue" || payload.NextCursor == nil || !payload.HasMore || payload.Results[0].Snippet != "match [alpha]" {
			t.Fatalf("payload = %#v", payload)
		}
	})

	t.Run("invalid fts error maps 400", func(t *testing.T) {
		handler := NewBoardHTTPHandler(&stubBoardService{searchErr: domain.NewError(domain.CodeInvalidArgument, "invalid fts", false)})
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/search?q=bad", nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
		}
	})
}

func TestBoardHTTPHandlerServesSearchPageStateAndLinks(t *testing.T) {
	handler := NewBoardHTTPHandler(&stubBoardService{board: boardResultFixture(), searchPage: domain.SearchPage{Results: []domain.SearchResult{{EntityType: domain.SearchEntityTypeIssue, EntityID: "issue-1", IssueID: strPtr("ISSUE-1"), Title: "Alpha", Snippet: "match"}, {EntityType: domain.SearchEntityTypeComment, EntityID: "comment-1", Title: "Comment", Snippet: "match"}}}})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/search?q=alpha&entity_type=issue", nil)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `href="/issues/ISSUE-1"`) {
		t.Fatalf("body missing owning issue link: %s", body)
	}
	if !strings.Contains(body, "No owning issue") {
		t.Fatalf("body missing orphan marker: %s", body)
	}
	if !strings.Contains(body, "Showing 2 result(s) for \"alpha\".") && !strings.Contains(body, "Showing 2 result(s) for &#34;alpha&#34;.") {
		t.Fatalf("body missing rendered status: %s", body)
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

func TestBoardHTTPHandlerUsesSemanticETagsForBoardAndDetailAPI(t *testing.T) {
	initialBoard := boardResultFixture()
	initialBoard.GeneratedAt = time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	service := &stubBoardService{board: initialBoard, detail: domain.IssueDetail{Issue: domain.Issue{ID: "issue-1", DisplayID: "ISSUE-1", Title: "Initial title"}}}
	handler := NewBoardHTTPHandler(service)

	boardRecorder := httptest.NewRecorder()
	boardRequest := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	handler.ServeHTTP(boardRecorder, boardRequest)
	if boardRecorder.Code != http.StatusOK {
		t.Fatalf("board api status = %d, want %d", boardRecorder.Code, http.StatusOK)
	}
	etag := boardRecorder.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected board api ETag header")
	}
	if boardRecorder.Body.Len() == 0 {
		t.Fatal("expected board api body")
	}

	matchRecorder := httptest.NewRecorder()
	matchRequest := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	matchRequest.Header.Set("If-None-Match", etag)
	handler.ServeHTTP(matchRecorder, matchRequest)
	if matchRecorder.Code != http.StatusNotModified {
		t.Fatalf("board api recheck status = %d, want %d", matchRecorder.Code, http.StatusNotModified)
	}
	if got := matchRecorder.Header().Get("ETag"); got != etag {
		t.Fatalf("board api recheck ETag = %q, want %q", got, etag)
	}
	if matchRecorder.Body.Len() != 0 {
		t.Fatalf("board api 304 body length = %d, want 0", matchRecorder.Body.Len())
	}

	generatedAtOnlyBoard := initialBoard
	generatedAtOnlyBoard.GeneratedAt = time.Date(2026, 8, 6, 13, 0, 0, 0, time.UTC)
	service.board = generatedAtOnlyBoard
	generatedAtOnlyRecorder := httptest.NewRecorder()
	generatedAtOnlyRequest := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	handler.ServeHTTP(generatedAtOnlyRecorder, generatedAtOnlyRequest)
	if generatedAtOnlyRecorder.Code != http.StatusOK {
		t.Fatalf("board api generated-at-only status = %d, want %d", generatedAtOnlyRecorder.Code, http.StatusOK)
	}
	generatedAtOnlyETag := generatedAtOnlyRecorder.Header().Get("ETag")
	if generatedAtOnlyETag != etag {
		t.Fatalf("board api generated-at-only ETag = %q, want %q", generatedAtOnlyETag, etag)
	}

	generatedAtOnlyMatchRecorder := httptest.NewRecorder()
	generatedAtOnlyMatchRequest := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	generatedAtOnlyMatchRequest.Header.Set("If-None-Match", generatedAtOnlyETag)
	handler.ServeHTTP(generatedAtOnlyMatchRecorder, generatedAtOnlyMatchRequest)
	if generatedAtOnlyMatchRecorder.Code != http.StatusNotModified {
		t.Fatalf("board api generated-at-only recheck status = %d, want %d", generatedAtOnlyMatchRecorder.Code, http.StatusNotModified)
	}
	if generatedAtOnlyMatchRecorder.Body.Len() != 0 {
		t.Fatalf("board api generated-at-only 304 body length = %d, want 0", generatedAtOnlyMatchRecorder.Body.Len())
	}

	changedBoard := initialBoard
	changedBoard.GeneratedAt = time.Date(2026, 8, 6, 14, 0, 0, 0, time.UTC)
	changedBoard.StatusCounts = []domain.EffectiveStatusCount{{EffectiveStatus: domain.EffectiveStatusOpen, Count: 2}}
	service.board = changedBoard
	changedRecorder := httptest.NewRecorder()
	changedRequest := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	handler.ServeHTTP(changedRecorder, changedRequest)
	if changedRecorder.Code != http.StatusOK {
		t.Fatalf("board api changed status = %d, want %d", changedRecorder.Code, http.StatusOK)
	}
	if got := changedRecorder.Header().Get("ETag"); got == etag {
		t.Fatalf("board api changed ETag = %q, want different", got)
	}

	changedDetail := service.detail
	changedDetail.Issue.Title = "Changed title"
	service.detail = changedDetail
	detailRecorder := httptest.NewRecorder()
	detailRequest := httptest.NewRequest(http.MethodGet, "/api/issues/ISSUE-1", nil)
	handler.ServeHTTP(detailRecorder, detailRequest)
	if detailRecorder.Code != http.StatusOK {
		t.Fatalf("detail api status = %d, want %d", detailRecorder.Code, http.StatusOK)
	}
	if detailRecorder.Body.Len() == 0 {
		t.Fatal("expected detail api body")
	}
	detailETag := detailRecorder.Header().Get("ETag")
	if detailETag == "" {
		t.Fatal("expected detail api ETag header")
	}

	detailMatchRecorder := httptest.NewRecorder()
	detailMatchRequest := httptest.NewRequest(http.MethodHead, "/api/issues/ISSUE-1", nil)
	detailMatchRequest.Header.Set("If-None-Match", detailETag)
	handler.ServeHTTP(detailMatchRecorder, detailMatchRequest)
	if detailMatchRecorder.Code != http.StatusNotModified {
		t.Fatalf("detail api recheck status = %d, want %d", detailMatchRecorder.Code, http.StatusNotModified)
	}
	if detailMatchRecorder.Body.Len() != 0 {
		t.Fatalf("detail api 304 body length = %d, want 0", detailMatchRecorder.Body.Len())
	}
}

// ISSUE-181: the served board's live-refresh poll relies entirely on the
// semantic ETag changing when reservation state changes (acquire, release,
// or version bump), since the ETag payload is a separate minimal
// projection from the HTML/JSON renderers.
func TestBoardHTTPHandlerETagReflectsReservationChanges(t *testing.T) {
	board := boardResultFixture()
	board.ActiveReservations = []domain.Reservation{
		{ID: "reservation-1", IssueID: "issue-1", AttemptID: "attempt-1", Kind: domain.ResourceKindFile, DisplayValue: "a.go", Status: domain.ReservationStatusActive, Version: 1},
	}
	service := &stubBoardService{board: board}
	handler := NewBoardHTTPHandler(service)

	initialRecorder := httptest.NewRecorder()
	handler.ServeHTTP(initialRecorder, httptest.NewRequest(http.MethodGet, "/api/board", nil))
	initialETag := initialRecorder.Header().Get("ETag")
	if initialETag == "" {
		t.Fatal("expected board api ETag header")
	}

	t.Run("new active reservation changes the etag", func(t *testing.T) {
		withExtra := board
		withExtra.ActiveReservations = append([]domain.Reservation{}, board.ActiveReservations...)
		withExtra.ActiveReservations = append(withExtra.ActiveReservations, domain.Reservation{
			ID: "reservation-2", IssueID: "issue-1", AttemptID: "attempt-1", Kind: domain.ResourceKindFile, DisplayValue: "b.go", Status: domain.ReservationStatusActive, Version: 1,
		})
		service.board = withExtra
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/board", nil))
		if got := recorder.Header().Get("ETag"); got == initialETag {
			t.Fatalf("etag unchanged after acquiring a new reservation: %q", got)
		}
	})

	t.Run("released reservation changes the etag even with the same id", func(t *testing.T) {
		released := board
		released.ActiveReservations = nil
		service.board = released
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/board", nil))
		if got := recorder.Header().Get("ETag"); got == initialETag {
			t.Fatalf("etag unchanged after the only active reservation was released: %q", got)
		}
	})

	t.Run("unrelated board fields keep the etag stable", func(t *testing.T) {
		service.board = board
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/board", nil))
		if got := recorder.Header().Get("ETag"); got != initialETag {
			t.Fatalf("etag = %q, want unchanged %q for identical reservation state", got, initialETag)
		}
	})
}

func TestBoardHTTPHandlerMapsDetailAPIErrorResponses(t *testing.T) {
	handler := NewBoardHTTPHandler(&stubBoardService{err: domain.NewError(domain.CodeIssueNotFound, "missing", false)})
	for _, tc := range []struct {
		name       string
		path       string
		wantStatus int
	}{
		{name: "bad path", path: "/api/issues/", wantStatus: http.StatusBadRequest},
		{name: "missing issue", path: "/api/issues/ISSUE-404", wantStatus: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
		})
	}
}

func TestServedBoardHTMLIncludesLiveRefreshBootstrap(t *testing.T) {
	html, err := renderServedBoardHTML(boardResultFixture())
	if err != nil {
		t.Fatalf("renderServedBoardHTML: %v", err)
	}
	for _, want := range []string{"data-board-main", "data-board-endpoint=\"/api/board\"", "data-board-route=\"/\"", "visibilitychange", "pagehide", "If-None-Match"} {
		if !strings.Contains(html, want) {
			t.Fatalf("served board HTML missing %q: %s", want, html)
		}
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

// TestBoardResponseFromDomainCopiesTruncationFlags verifies that
// boardResponseFromDomain correctly copies all four truncation flags from
// the domain BoardResult to the CLI BoardResponse projection.
func TestBoardResponseFromDomainCopiesTruncationFlags(t *testing.T) {
	result := domain.BoardResult{
		GeneratedAt:  time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		StatusCounts: []domain.EffectiveStatusCount{{EffectiveStatus: domain.EffectiveStatusOpen, Count: 1}},
		Truncation: domain.BoardTruncation{
			BlockedIssues:      true,
			ActiveAttempts:     false,
			ActiveReservations: true,
			ReviewRequests:     false,
		},
	}

	response := boardResponseFromDomain(result)

	if !response.Truncation.BlockedIssues {
		t.Fatalf("Truncation.BlockedIssues = false, want true")
	}
	if response.Truncation.ActiveAttempts {
		t.Fatalf("Truncation.ActiveAttempts = true, want false")
	}
	if !response.Truncation.ActiveReservations {
		t.Fatalf("Truncation.ActiveReservations = false, want true")
	}
	if response.Truncation.ReviewRequests {
		t.Fatalf("Truncation.ReviewRequests = true, want false")
	}
}

// TestBoardResponseFromDomainMarshalsTruncationJSON verifies that the
// BoardResponse's Truncation field marshals to JSON with the expected
// structure and keys.
func TestBoardResponseFromDomainMarshalsTruncationJSON(t *testing.T) {
	result := domain.BoardResult{
		GeneratedAt:  time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		StatusCounts: []domain.EffectiveStatusCount{{EffectiveStatus: domain.EffectiveStatusOpen, Count: 1}},
		Truncation: domain.BoardTruncation{
			BlockedIssues:      true,
			ActiveAttempts:     false,
			ActiveReservations: true,
			ReviewRequests:     false,
		},
	}

	response := boardResponseFromDomain(result)
	jsonBytes, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	jsonStr := string(jsonBytes)
	if !strings.Contains(jsonStr, `"truncation"`) {
		t.Fatalf("JSON missing truncation field: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"blocked_issues":true`) {
		t.Fatalf("JSON missing or wrong blocked_issues: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"active_attempts":false`) {
		t.Fatalf("JSON missing or wrong active_attempts: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"active_reservations":true`) {
		t.Fatalf("JSON missing or wrong active_reservations: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"review_requests":false`) {
		t.Fatalf("JSON missing or wrong review_requests: %s", jsonStr)
	}
}

// TestSemanticBoardETagReflectsTruncationChanges verifies that the semantic
// ETag changes when any truncation flag changes, mirroring the pattern for
// gate changes.
func TestSemanticBoardETagReflectsTruncationChanges(t *testing.T) {
	baseBoard := domain.BoardResult{
		GeneratedAt:  time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		StatusCounts: []domain.EffectiveStatusCount{{EffectiveStatus: domain.EffectiveStatusOpen, Count: 1}},
		Truncation: domain.BoardTruncation{
			BlockedIssues:      false,
			ActiveAttempts:     false,
			ActiveReservations: false,
			ReviewRequests:     false,
		},
	}

	tests := []struct {
		name       string
		modifyFn   func(*domain.BoardResult)
		expectDiff bool
	}{
		{
			name: "blocked_issues flag change",
			modifyFn: func(b *domain.BoardResult) {
				b.Truncation.BlockedIssues = true
			},
			expectDiff: true,
		},
		{
			name: "active_attempts flag change",
			modifyFn: func(b *domain.BoardResult) {
				b.Truncation.ActiveAttempts = true
			},
			expectDiff: true,
		},
		{
			name: "active_reservations flag change",
			modifyFn: func(b *domain.BoardResult) {
				b.Truncation.ActiveReservations = true
			},
			expectDiff: true,
		},
		{
			name: "review_requests flag change",
			modifyFn: func(b *domain.BoardResult) {
				b.Truncation.ReviewRequests = true
			},
			expectDiff: true,
		},
	}

	baseETag := semanticBoardETag(baseBoard)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modified := baseBoard
			tt.modifyFn(&modified)
			modifiedETag := semanticBoardETag(modified)

			if tt.expectDiff && modifiedETag == baseETag {
				t.Fatalf("ETag unchanged after modifying truncation: %q", baseETag)
			}
			if !tt.expectDiff && modifiedETag != baseETag {
				t.Fatalf("ETag changed unexpectedly: %q -> %q", baseETag, modifiedETag)
			}
		})
	}
}

type sqliteBackedBoardHTTPService struct {
	boardService       *application.BoardService
	issueDetailService *application.IssueDetailService
	searchService      *application.SearchService
}

func (s sqliteBackedBoardHTTPService) GetBoard(ctx context.Context) (domain.BoardResult, error) {
	return s.boardService.GetBoard(ctx)
}

func (s sqliteBackedBoardHTTPService) GetIssueDetail(ctx context.Context, identifier string) (domain.IssueDetail, error) {
	return s.issueDetailService.GetIssueDetail(ctx, identifier)
}

func (s sqliteBackedBoardHTTPService) Search(ctx context.Context, input domain.SearchInput) (domain.SearchPage, error) {
	return s.searchService.Search(ctx, input)
}

// TestBoardHTTPHandlerServesArchivedIssueDetailHTMLAndJSON verifies the HTTP
// detail routes return 200 and render archived issue metadata cleanly.
func TestBoardHTTPHandlerServesArchivedIssueDetailHTMLAndJSON(t *testing.T) {
	archivedAt := time.Date(2026, 8, 8, 12, 30, 0, 0, time.UTC)
	issue := domain.Issue{
		ID:         "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		DisplayID:  "ISSUE-42",
		Title:      "Archived issue",
		Type:       domain.TypeTask,
		Status:     domain.StatusDone,
		Priority:   domain.PriorityMedium,
		Version:    2,
		CreatedAt:  archivedAt.Add(-time.Hour),
		UpdatedAt:  archivedAt,
		ArchivedAt: &archivedAt,
	}
	rootID := issue.ID
	detail := domain.IssueDetail{
		Issue:               issue,
		RootIssueProjection: &domain.IssueProjection{Issue: issue, EffectiveStatus: domain.EffectiveStatusDone},
		Graph:               domain.GraphResult{RootIssueID: &rootID, Nodes: []domain.IssueProjection{}, Edges: []domain.GraphEdge{}},
	}
	handler := NewBoardHTTPHandler(&stubBoardService{detail: detail})

	htmlRecorder := httptest.NewRecorder()
	handler.ServeHTTP(htmlRecorder, httptest.NewRequest(http.MethodGet, "/issues/ISSUE-42", nil))
	if htmlRecorder.Code != http.StatusOK {
		t.Fatalf("GET /issues/ISSUE-42 status = %d, want %d", htmlRecorder.Code, http.StatusOK)
	}
	htmlBody := htmlRecorder.Body.String()
	if !strings.Contains(htmlBody, "ISSUE-42") || !strings.Contains(htmlBody, archivedAt.Format(time.RFC3339)) {
		t.Fatalf("HTML detail missing archived metadata: %s", htmlBody)
	}
	if strings.Contains(htmlBody, "Related graph") {
		t.Fatalf("HTML detail unexpectedly included Related graph section for empty graph: %s", htmlBody)
	}

	jsonRecorder := httptest.NewRecorder()
	handler.ServeHTTP(jsonRecorder, httptest.NewRequest(http.MethodGet, "/api/issues/ISSUE-42", nil))
	if jsonRecorder.Code != http.StatusOK {
		t.Fatalf("GET /api/issues/ISSUE-42 status = %d, want %d", jsonRecorder.Code, http.StatusOK)
	}
	var decoded domain.IssueDetail
	if err := json.Unmarshal(jsonRecorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode /api/issues/ISSUE-42 response: %v", err)
	}
	if decoded.Issue.ArchivedAt == nil || !decoded.Issue.ArchivedAt.Equal(archivedAt) {
		t.Fatalf("decoded.Issue.ArchivedAt = %v, want %v", decoded.Issue.ArchivedAt, archivedAt)
	}
	if decoded.RootIssueProjection == nil || decoded.RootIssueProjection.ArchivedAt == nil || !decoded.RootIssueProjection.ArchivedAt.Equal(archivedAt) {
		t.Fatalf("decoded.RootIssueProjection = %#v, want ArchivedAt %v", decoded.RootIssueProjection, archivedAt)
	}
	if len(decoded.Graph.Nodes) != 0 || len(decoded.Graph.Edges) != 0 {
		t.Fatalf("decoded.Graph = %#v, want empty nodes and edges", decoded.Graph)
	}
}

// TestBoardHTTPHandlerServesArchivedIssueDetailWithSQLiteIssueDetailService
// exercises the full SQLite -> IssueDetailService -> BoardHTTPHandler path to
// prove GetIssueGraph's CodeIssueArchived error path is avoided on archived
// issues while activity, reservations, gates, and active issue graphs still work.
func TestBoardHTTPHandlerServesArchivedIssueDetailWithSQLiteIssueDetailService(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	source := clock.NewFakeClock(now)
	dbPath := filepath.Join(t.TempDir(), "board-http-archived-detail.db")
	db, err := sqlite.Open(ctx, dbPath, sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close(ctx) })
	if _, err := migrations.Migrate(ctx, db, source); err != nil {
		t.Fatalf("migrations.Migrate: %v", err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at) VALUES (?, 1, ?, ?)`,
			"01ARZ3NDEKTSV4RRFFQ69G5FAV", sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(now))
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	generator, err := ids.NewGenerator(source, rand.Reader)
	if err != nil {
		t.Fatalf("ids.NewGenerator: %v", err)
	}
	issueRepo, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatalf("NewIssueRepository: %v", err)
	}
	attemptRepo, err := sqlite.NewAttemptRepository(db)
	if err != nil {
		t.Fatalf("NewAttemptRepository: %v", err)
	}
	reservationRepo, err := sqlite.NewReservationRepository(db)
	if err != nil {
		t.Fatalf("NewReservationRepository: %v", err)
	}
	reviewRepo, err := sqlite.NewReviewRepository(db)
	if err != nil {
		t.Fatalf("NewReviewRepository: %v", err)
	}
	graphRepo, err := sqlite.NewGraphRepository(db)
	if err != nil {
		t.Fatalf("NewGraphRepository: %v", err)
	}
	activityRepo, err := sqlite.NewActivityRepository(db)
	if err != nil {
		t.Fatalf("NewActivityRepository: %v", err)
	}
	commentRepo, err := sqlite.NewCommentRepository(db)
	if err != nil {
		t.Fatalf("NewCommentRepository: %v", err)
	}
	searchRepo, err := sqlite.NewSearchRepository(db)
	if err != nil {
		t.Fatalf("NewSearchRepository: %v", err)
	}
	workflowPolicyRepo, err := sqlite.NewWorkflowPolicyRepository(db)
	if err != nil {
		t.Fatalf("NewWorkflowPolicyRepository: %v", err)
	}

	issueService, err := application.NewIssueService(issueRepo, source, generator)
	if err != nil {
		t.Fatalf("NewIssueService: %v", err)
	}
	attemptService, err := application.NewAttemptService(attemptRepo, source, generator)
	if err != nil {
		t.Fatalf("NewAttemptService: %v", err)
	}
	reservationService, err := application.NewReservationService(reservationRepo)
	if err != nil {
		t.Fatalf("NewReservationService: %v", err)
	}
	reviewService, err := application.NewReviewService(reviewRepo, issueRepo, source, generator)
	if err != nil {
		t.Fatalf("NewReviewService: %v", err)
	}
	graphService, err := application.NewGraphService(graphRepo, source)
	if err != nil {
		t.Fatalf("NewGraphService: %v", err)
	}
	activityService, err := application.NewActivityService(activityRepo)
	if err != nil {
		t.Fatalf("NewActivityService: %v", err)
	}
	commentService, err := application.NewCommentService(commentRepo, source, generator)
	if err != nil {
		t.Fatalf("NewCommentService: %v", err)
	}
	searchService, err := application.NewSearchService(searchRepo)
	if err != nil {
		t.Fatalf("NewSearchService: %v", err)
	}
	workflowPolicyService, err := application.NewWorkflowPolicyService(workflowPolicyRepo, source, generator)
	if err != nil {
		t.Fatalf("NewWorkflowPolicyService: %v", err)
	}
	boardService, err := application.NewBoardService(issueService, attemptService, reservationService, reviewService, graphService, workflowPolicyService, source)
	if err != nil {
		t.Fatalf("NewBoardService: %v", err)
	}
	issueDetailService, err := application.NewIssueDetailService(issueService, graphService, activityService, reservationService, workflowPolicyService)
	if err != nil {
		t.Fatalf("NewIssueDetailService: %v", err)
	}

	created, err := issueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type:   domain.TypeTask,
		Title:  "Archived end-to-end detail",
		Status: domain.StatusReady,
	})
	if err != nil {
		t.Fatalf("CreateIssue(archived target): %v", err)
	}
	if _, err := commentService.AddComment(ctx, domain.AddCommentInput{
		IssueID: created.ID,
		Content: "note recorded before archival",
	}); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	claim, err := attemptService.ClaimIssue(ctx, domain.ClaimIssueInput{
		IssueID:   created.ID,
		Resources: []domain.Resource{{Kind: domain.ResourceKindFile, Path: "archived_detail.go"}},
	})
	if err != nil {
		t.Fatalf("ClaimIssue: %v", err)
	}
	targetStatus := domain.StatusDone
	if _, err := attemptService.FinishAttempt(ctx, domain.FinishAttemptInput{
		AttemptID:         claim.Attempt.ID,
		LeaseToken:        claim.LeaseToken,
		Outcome:           domain.AttemptOutcomeCompleted,
		ResultSummary:     "completed prior to archival",
		TargetIssueStatus: &targetStatus,
	}); err != nil {
		t.Fatalf("FinishAttempt: %v", err)
	}
	currentIssue, err := issueService.GetIssue(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	source.Advance(time.Minute)
	archived, err := issueService.ArchiveIssue(ctx, domain.ArchiveIssueInput{
		IssueID:         created.ID,
		ExpectedVersion: currentIssue.Version,
	})
	if err != nil {
		t.Fatalf("ArchiveIssue: %v", err)
	}

	// Prove the real SQLite graph service rejects this archived root so the
	// HTTP 200 below genuinely proves IssueDetailService avoids GetIssueGraph.
	if _, err := graphService.GetIssueGraph(ctx, domain.GetIssueGraphInput{RootIssueID: created.ID}); !errors.Is(err, &domain.Error{Code: domain.CodeIssueArchived}) {
		t.Fatalf("GetIssueGraph(archived) error = %v, want %s", err, domain.CodeIssueArchived)
	}

	active, err := issueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type:   domain.TypeTask,
		Title:  "Active issue detail",
		Status: domain.StatusReady,
	})
	if err != nil {
		t.Fatalf("CreateIssue(active): %v", err)
	}

	handler := NewBoardHTTPHandler(sqliteBackedBoardHTTPService{
		boardService:       boardService,
		issueDetailService: issueDetailService,
		searchService:      searchService,
	})

	for _, path := range []string{"/issues/" + created.DisplayID, "/issues/" + created.ID} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d (body=%q)", path, recorder.Code, http.StatusOK, recorder.Body.String())
		}
		body := recorder.Body.String()
		for _, want := range []string{
			created.DisplayID,
			"Archived end-to-end detail",
			archived.Issue.ArchivedAt.Format(time.RFC3339),
			"note recorded before archival",
			"archived_detail.go",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("GET %s HTML missing %q:\n%s", path, want, body)
			}
		}
		if strings.Contains(body, "Related graph") {
			t.Fatalf("GET %s HTML unexpectedly rendered Related graph for archived issue", path)
		}
	}

	for _, path := range []string{"/api/issues/" + created.DisplayID, "/api/issues/" + created.ID} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d (body=%q)", path, recorder.Code, http.StatusOK, recorder.Body.String())
		}
		var detail domain.IssueDetail
		if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil {
			t.Fatalf("GET %s unmarshal: %v", path, err)
		}
		if detail.Issue.ID != created.ID {
			t.Fatalf("GET %s Issue.ID = %q, want %q", path, detail.Issue.ID, created.ID)
		}
		if detail.Issue.ArchivedAt == nil || !detail.Issue.ArchivedAt.Equal(*archived.Issue.ArchivedAt) {
			t.Fatalf("GET %s Issue.ArchivedAt = %v, want %v", path, detail.Issue.ArchivedAt, archived.Issue.ArchivedAt)
		}
		if detail.RootIssueProjection == nil || detail.RootIssueProjection.ArchivedAt == nil || !detail.RootIssueProjection.ArchivedAt.Equal(*archived.Issue.ArchivedAt) {
			t.Fatalf("GET %s RootIssueProjection = %#v, want ArchivedAt %v", path, detail.RootIssueProjection, archived.Issue.ArchivedAt)
		}
		if detail.Graph.RootIssueID == nil || *detail.Graph.RootIssueID != created.ID {
			t.Fatalf("GET %s Graph.RootIssueID = %#v, want %q", path, detail.Graph.RootIssueID, created.ID)
		}
		if len(detail.Graph.Nodes) != 0 || len(detail.Graph.Edges) != 0 {
			t.Fatalf("GET %s Graph = %#v, want empty nodes and edges", path, detail.Graph)
		}
		if len(detail.Activity.Items) == 0 {
			t.Fatalf("GET %s expected non-empty activity", path)
		}
		if len(detail.Reservations) != 1 || detail.Reservations[0].DisplayValue != "archived_detail.go" {
			t.Fatalf("GET %s Reservations = %#v, want archived_detail.go", path, detail.Reservations)
		}
		if detail.Gates.Point != domain.EnforcementPointClaimWork {
			t.Fatalf("GET %s Gates.Point = %q, want %q", path, detail.Gates.Point, domain.EnforcementPointClaimWork)
		}
	}

	// Non-archived issues still run GetIssueGraph and populate Graph.Nodes.
	activeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(activeRecorder, httptest.NewRequest(http.MethodGet, "/api/issues/"+active.DisplayID, nil))
	if activeRecorder.Code != http.StatusOK {
		t.Fatalf("GET /api/issues/%s status = %d, want %d", active.DisplayID, activeRecorder.Code, http.StatusOK)
	}
	var activeDetail domain.IssueDetail
	if err := json.Unmarshal(activeRecorder.Body.Bytes(), &activeDetail); err != nil {
		t.Fatalf("unmarshal active issue detail: %v", err)
	}
	if len(activeDetail.Graph.Nodes) != 1 || activeDetail.Graph.Nodes[0].ID != active.ID {
		t.Fatalf("active issue Graph.Nodes = %#v, want 1 node (%q)", activeDetail.Graph.Nodes, active.ID)
	}
}

func boardResultFixture() domain.BoardResult {
	return domain.BoardResult{GeneratedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC), StatusCounts: []domain.EffectiveStatusCount{{EffectiveStatus: domain.EffectiveStatusOpen, Count: 1}}}
}

type sseFallbackBoardService struct {
	mu    sync.Mutex
	board domain.BoardResult
	err   error
}

func (s *sseFallbackBoardService) GetBoard(_ context.Context) (domain.BoardResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.board, s.err
}

func (s *sseFallbackBoardService) SetBoard(board domain.BoardResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.board = board
}

func (s *sseFallbackBoardService) GetIssueDetail(_ context.Context, _ string) (domain.IssueDetail, error) {
	return domain.IssueDetail{}, nil
}

func (s *sseFallbackBoardService) Search(_ context.Context, _ domain.SearchInput) (domain.SearchPage, error) {
	return domain.SearchPage{}, nil
}

type sseWatermarkBoardService struct {
	sseFallbackBoardService
	wmMu      sync.Mutex
	watermark string
	wmErr     error
}

func (s *sseWatermarkBoardService) GetBoardWatermark(_ context.Context) (string, error) {
	s.wmMu.Lock()
	defer s.wmMu.Unlock()
	return s.watermark, s.wmErr
}

func (s *sseWatermarkBoardService) SetWatermark(watermark string) {
	s.wmMu.Lock()
	defer s.wmMu.Unlock()
	s.watermark = watermark
}

type sqliteBoardSSEHTTPService struct {
	boardService *application.BoardService
}

func (s sqliteBoardSSEHTTPService) GetBoard(ctx context.Context) (domain.BoardResult, error) {
	return s.boardService.GetBoard(ctx)
}

func (s sqliteBoardSSEHTTPService) GetBoardWatermark(ctx context.Context) (string, error) {
	return s.boardService.GetBoardWatermark(ctx)
}

func (s sqliteBoardSSEHTTPService) GetIssueDetail(_ context.Context, _ string) (domain.IssueDetail, error) {
	return domain.IssueDetail{}, nil
}

func (s sqliteBoardSSEHTTPService) Search(_ context.Context, _ domain.SearchInput) (domain.SearchPage, error) {
	return domain.SearchPage{}, nil
}

type sseDeadlineRecorderWriter struct {
	*httptest.ResponseRecorder
	mu            sync.Mutex
	deadlineCalls []time.Time
	deadlineErr   error
}

func (w *sseDeadlineRecorderWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadlineCalls = append(w.deadlineCalls, deadline)
	return w.deadlineErr
}

type sseFailingResponseWriter struct {
	header          http.Header
	status          int
	writes          int
	failAfterWrites int
}

func (w *sseFailingResponseWriter) Header() http.Header  { return w.header }
func (w *sseFailingResponseWriter) WriteHeader(code int) { w.status = code }
func (w *sseFailingResponseWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes >= w.failAfterWrites {
		return 0, errors.New("write failed")
	}
	return len(p), nil
}

func startSSEChunkReader(t *testing.T, resp *http.Response) <-chan string {
	t.Helper()
	chunks := make(chan string, 32)
	go func() {
		defer close(chunks)
		reader := bufio.NewReader(resp.Body)
		var buf strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				buf.WriteString(line)
				if line == "\n" {
					chunks <- buf.String()
					buf.Reset()
				}
			}
			if err != nil {
				if buf.Len() > 0 {
					chunks <- buf.String()
				}
				return
			}
		}
	}()
	return chunks
}

func waitForSSEChunk(t *testing.T, chunks <-chan string, substr string, timeout time.Duration) string {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				t.Fatalf("SSE stream closed before observing %q", substr)
			}
			if strings.Contains(chunk, substr) {
				return chunk
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for SSE chunk containing %q", substr)
		}
	}
}

func TestBoardHTTPHandlerEventsHeadersMethodAndServiceUnavailable(t *testing.T) {
	t.Run("nil board service returns 503 with security headers", func(t *testing.T) {
		handler := NewBoardHTTPHandler(nil)

		getRec := httptest.NewRecorder()
		handler.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/api/events", nil))
		if getRec.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET /api/events status = %d, want %d", getRec.Code, http.StatusServiceUnavailable)
		}
		if got := getRec.Body.String(); got != "service unavailable" {
			t.Fatalf("GET /api/events body = %q, want %q", got, "service unavailable")
		}
		if csp := getRec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self'") {
			t.Fatalf("missing CSP connect-src 'self': %q", csp)
		}

		headRec := httptest.NewRecorder()
		handler.ServeHTTP(headRec, httptest.NewRequest(http.MethodHead, "/api/events", nil))
		if headRec.Code != http.StatusServiceUnavailable {
			t.Fatalf("HEAD /api/events status = %d, want %d", headRec.Code, http.StatusServiceUnavailable)
		}
		if headRec.Body.Len() != 0 {
			t.Fatalf("HEAD /api/events body = %q, want empty", headRec.Body.String())
		}
	})

	service := &sseWatermarkBoardService{watermark: "wm-head"}
	handler := NewBoardHTTPHandler(service)

	t.Run("HEAD returns SSE headers without body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/api/events", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("HEAD /api/events status = %d, want %d", rec.Code, http.StatusOK)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("HEAD /api/events body = %q, want empty", rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
			t.Fatalf("Content-Type = %q, want text/event-stream; charset=utf-8", got)
		}
		if got := rec.Header().Get("X-Accel-Buffering"); got != "no" {
			t.Fatalf("X-Accel-Buffering = %q, want no", got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control = %q, want no-store", got)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
		}
		if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
			t.Fatalf("Referrer-Policy = %q, want no-referrer", got)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self'") {
			t.Fatalf("CSP = %q, want connect-src 'self'", csp)
		}
	})

	t.Run("non-GET/HEAD rejected with 405", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/events", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST /api/events status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
		if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
			t.Fatalf("Allow = %q, want GET, HEAD", allow)
		}
	})

	t.Run("runtime wrapper rejects foreign host and cross-origin on /api/events", func(t *testing.T) {
		wrapped := projectruntime.WrapHTTPHandlerWithBodyLimit(handler, "127.0.0.1:8080", slog.Default(), 0)

		badHostReq := httptest.NewRequest(http.MethodHead, "http://evil.example/api/events", nil)
		badHostReq.Host = "evil.example"
		badHostRec := httptest.NewRecorder()
		wrapped.ServeHTTP(badHostRec, badHostReq)
		if badHostRec.Code != http.StatusBadRequest {
			t.Fatalf("foreign Host status = %d, want %d", badHostRec.Code, http.StatusBadRequest)
		}

		wrongPortReq := httptest.NewRequest(http.MethodHead, "http://127.0.0.1:9999/api/events", nil)
		wrongPortReq.Host = "127.0.0.1:9999"
		wrongPortRec := httptest.NewRecorder()
		wrapped.ServeHTTP(wrongPortRec, wrongPortReq)
		if wrongPortRec.Code != http.StatusMisdirectedRequest {
			t.Fatalf("wrong-port Host status = %d, want %d", wrongPortRec.Code, http.StatusMisdirectedRequest)
		}

		badOriginReq := httptest.NewRequest(http.MethodHead, "http://127.0.0.1:8080/api/events", nil)
		badOriginReq.Host = "127.0.0.1:8080"
		badOriginReq.Header.Set("Origin", "https://evil.example")
		badOriginRec := httptest.NewRecorder()
		wrapped.ServeHTTP(badOriginRec, badOriginReq)
		if badOriginRec.Code != http.StatusForbidden {
			t.Fatalf("cross-origin status = %d, want %d", badOriginRec.Code, http.StatusForbidden)
		}

		validReq := httptest.NewRequest(http.MethodHead, "http://127.0.0.1:8080/api/events", nil)
		validReq.Host = "127.0.0.1:8080"
		validReq.Header.Set("Origin", "http://127.0.0.1:8080")
		validRec := httptest.NewRecorder()
		wrapped.ServeHTTP(validRec, validReq)
		if validRec.Code != http.StatusOK {
			t.Fatalf("valid loopback Host+Origin status = %d, want %d", validRec.Code, http.StatusOK)
		}
	})

	t.Run("initial watermark error returns 500", func(t *testing.T) {
		errSvc := &sseWatermarkBoardService{wmErr: errors.New("boom")}
		errRec := httptest.NewRecorder()
		NewBoardHTTPHandler(errSvc).ServeHTTP(errRec, httptest.NewRequest(http.MethodGet, "/api/events", nil))
		if errRec.Code != http.StatusInternalServerError {
			t.Fatalf("initial watermark failure status = %d, want %d", errRec.Code, http.StatusInternalServerError)
		}

		fallbackErrSvc := &sseFallbackBoardService{err: errors.New("fallback boom")}
		fallbackErrRec := httptest.NewRecorder()
		NewBoardHTTPHandler(fallbackErrSvc).ServeHTTP(fallbackErrRec, httptest.NewRequest(http.MethodGet, "/api/events", nil))
		if fallbackErrRec.Code != http.StatusInternalServerError {
			t.Fatalf("initial fallback GetBoard failure status = %d, want %d", fallbackErrRec.Code, http.StatusInternalServerError)
		}

		canceledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		canceledRec := httptest.NewRecorder()
		NewBoardHTTPHandler(errSvc).ServeHTTP(canceledRec, httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(canceledCtx))
	})

	t.Run("defaults non-positive options, handles transient poll errors, and exits on write error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		rec := &sseFailingResponseWriter{header: make(http.Header)}
		rec.failAfterWrites = 1
		newBoardHTTPHandlerWithOptions(service, boardHTTPOptions{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx))
		cancel()

		svc := &sseWatermarkBoardService{watermark: "wm-a"}
		streamCtx, streamCancel := context.WithCancel(context.Background())
		rec2 := &sseDeadlineRecorderWriter{ResponseRecorder: httptest.NewRecorder()}
		done := make(chan struct{})
		go func() {
			defer close(done)
			newBoardHTTPHandlerWithOptions(svc, boardHTTPOptions{
				pollInterval:      5 * time.Millisecond,
				keepaliveInterval: time.Second,
			}).ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(streamCtx))
		}()
		time.Sleep(15 * time.Millisecond)
		svc.wmMu.Lock()
		svc.wmErr = errors.New("transient")
		svc.wmMu.Unlock()
		time.Sleep(15 * time.Millisecond)
		svc.wmMu.Lock()
		svc.wmErr = nil
		svc.watermark = "wm-b"
		svc.wmMu.Unlock()
		time.Sleep(20 * time.Millisecond)
		streamCancel()
		<-done
		if !strings.Contains(rec2.Body.String(), `"wm-b"`) {
			t.Fatalf("SSE body after transient error recovery = %q, want wm-b", rec2.Body.String())
		}
	})

	t.Run("clears write deadline with zero time on ResponseController writer", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
		rec := &sseDeadlineRecorderWriter{ResponseRecorder: httptest.NewRecorder()}
		done := make(chan struct{})
		go func() {
			defer close(done)
			newBoardHTTPHandlerWithOptions(service, boardHTTPOptions{
				pollInterval:      5 * time.Millisecond,
				keepaliveInterval: time.Second,
			}).ServeHTTP(rec, req)
		}()
		time.Sleep(25 * time.Millisecond)
		cancel()
		<-done

		rec.mu.Lock()
		calls := append([]time.Time(nil), rec.deadlineCalls...)
		rec.mu.Unlock()
		if len(calls) != 1 || !calls[0].IsZero() {
			t.Fatalf("SetWriteDeadline calls = %v, want single zero time", calls)
		}
		if !strings.Contains(rec.Body.String(), "retry: 2000\n\nevent: ready\n") {
			t.Fatalf("SSE initial body = %q, missing retry/ready frames", rec.Body.String())
		}
	})
}

func TestBoardHTTPHandlerEventsSSEStreamingAndFallback(t *testing.T) {
	t.Run("streams ready, keepalive, board_changed and exits promptly on disconnect", func(t *testing.T) {
		svc := &sseWatermarkBoardService{watermark: "wm-1"}
		handlerExited := make(chan struct{}, 4)
		sseHandler := newBoardHTTPHandlerWithOptions(svc, boardHTTPOptions{
			pollInterval:      10 * time.Millisecond,
			keepaliveInterval: 30 * time.Millisecond,
		})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				select {
				case handlerExited <- struct{}{}:
				default:
				}
			}()
			sseHandler.ServeHTTP(w, r)
		}))
		defer server.Close()

		reqCtx, cancel := context.WithCancel(context.Background())
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, server.URL+"/api/events", nil)
		if err != nil {
			t.Fatalf("NewRequestWithContext: %v", err)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("Do GET /api/events: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
			t.Fatalf("Content-Type = %q, want text/event-stream; charset=utf-8", ct)
		}
		if xab := resp.Header.Get("X-Accel-Buffering"); xab != "no" {
			t.Fatalf("X-Accel-Buffering = %q, want no", xab)
		}

		chunks := startSSEChunkReader(t, resp)
		waitForSSEChunk(t, chunks, "retry: 2000\n", 2*time.Second)
		readyChunk := waitForSSEChunk(t, chunks, "event: ready\n", 2*time.Second)
		if !strings.Contains(readyChunk, `"watermark":"wm-1"`) {
			t.Fatalf("ready chunk = %q, want watermark wm-1", readyChunk)
		}

		keepaliveChunk := waitForSSEChunk(t, chunks, ": keepalive\n", 2*time.Second)
		if keepaliveChunk != ": keepalive\n\n" {
			t.Fatalf("keepalive chunk = %q, want ': keepalive\\n\\n'", keepaliveChunk)
		}

		svc.SetWatermark("wm-2")
		changedChunk := waitForSSEChunk(t, chunks, "event: board_changed\n", 2*time.Second)
		if !strings.Contains(changedChunk, `"watermark":"wm-2"`) {
			t.Fatalf("board_changed chunk = %q, want watermark wm-2", changedChunk)
		}

		cancel()
		select {
		case <-handlerExited:
		case <-time.After(2 * time.Second):
			t.Fatal("SSE handler did not exit promptly after client disconnect")
		}
	})

	t.Run("falls back to semanticBoardETag when BoardWatermarkProvider is unimplemented", func(t *testing.T) {
		svc := &sseFallbackBoardService{board: boardResultFixture()}
		server := httptest.NewServer(newBoardHTTPHandlerWithOptions(svc, boardHTTPOptions{
			pollInterval:      10 * time.Millisecond,
			keepaliveInterval: time.Second,
		}))
		defer server.Close()

		reqCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, server.URL+"/api/events", nil)
		if err != nil {
			t.Fatalf("NewRequestWithContext: %v", err)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("Do GET /api/events: %v", err)
		}
		defer resp.Body.Close()

		chunks := startSSEChunkReader(t, resp)
		readyChunk := waitForSSEChunk(t, chunks, "event: ready\n", 2*time.Second)
		wantReadyWM := strings.Trim(semanticBoardETag(boardResultFixture()), `"`)
		if !strings.Contains(readyChunk, wantReadyWM) {
			t.Fatalf("fallback ready chunk = %q, want semantic ETag %q", readyChunk, wantReadyWM)
		}

		updatedBoard := boardResultFixture()
		updatedBoard.StatusCounts[0].Count = 4
		svc.SetBoard(updatedBoard)

		changedChunk := waitForSSEChunk(t, chunks, "event: board_changed\n", 2*time.Second)
		wantUpdatedWM := strings.Trim(semanticBoardETag(updatedBoard), `"`)
		if !strings.Contains(changedChunk, wantUpdatedWM) {
			t.Fatalf("fallback board_changed chunk = %q, want semantic ETag %q", changedChunk, wantUpdatedWM)
		}
	})
}

func TestBoardHTTPHandlerEventsSurvivesServeHTTPServerWriteTimeout(t *testing.T) {
	svc := &sseWatermarkBoardService{watermark: "wt-1"}
	sseHandler := newBoardHTTPHandlerWithOptions(svc, boardHTTPOptions{
		pollInterval:      10 * time.Millisecond,
		keepaliveInterval: 25 * time.Millisecond,
	})

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()
	addrCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		errCh <- projectruntime.ServeHTTPServer(serverCtx, projectruntime.HTTPServerOptions{
			Address:         "127.0.0.1:0",
			Logger:          slog.Default(),
			Handler:         sseHandler,
			WriteTimeout:    60 * time.Millisecond,
			ShutdownTimeout: time.Second,
			OnListener: func(l net.Listener) {
				addrCh <- l.Addr().String()
			},
		})
	}()

	addr := <-addrCh
	reqCtx, reqCancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "http://"+addr+"/api/events", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}
	defer resp.Body.Close()

	chunks := startSSEChunkReader(t, resp)
	waitForSSEChunk(t, chunks, "event: ready\n", 2*time.Second)

	// Wait longer than WriteTimeout (60ms) so an uncleared write deadline would kill the stream.
	time.Sleep(150 * time.Millisecond)

	svc.SetWatermark("wt-2")
	changedChunk := waitForSSEChunk(t, chunks, "event: board_changed\n", 2*time.Second)
	if !strings.Contains(changedChunk, `"watermark":"wt-2"`) {
		t.Fatalf("post-WriteTimeout board_changed chunk = %q, want watermark wt-2", changedChunk)
	}

	reqCancel()
	serverCancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("ServeHTTPServer() error = %v, want context.Canceled", err)
	}
}

type boardSSESQLiteFixture struct {
	source         *clock.FakeClock
	issueService   *application.IssueService
	attemptService *application.AttemptService
	boardService   *application.BoardService
}

func openBoardSSESQLiteFixture(t *testing.T) boardSSESQLiteFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	source := clock.NewFakeClock(now)
	dbPath := filepath.Join(t.TempDir(), "board-http-sse-sqlite.db")
	db, err := sqlite.Open(ctx, dbPath, sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close(ctx) })
	if _, err := migrations.Migrate(ctx, db, source); err != nil {
		t.Fatalf("migrations.Migrate: %v", err)
	}
	if err := db.Write(ctx, func(ctx context.Context, tx sqlite.Executor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO projects(id, next_issue_number, created_at, updated_at) VALUES (?, 1, ?, ?)`,
			"01ARZ3NDEKTSV4RRFFQ69G5FAV", sqlite.FormatStorageTime(now), sqlite.FormatStorageTime(now))
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	generator, err := ids.NewGenerator(source, rand.Reader)
	if err != nil {
		t.Fatalf("ids.NewGenerator: %v", err)
	}
	issueRepo, err := sqlite.NewIssueRepository(db)
	if err != nil {
		t.Fatalf("NewIssueRepository: %v", err)
	}
	attemptRepo, err := sqlite.NewAttemptRepository(db)
	if err != nil {
		t.Fatalf("NewAttemptRepository: %v", err)
	}
	reservationRepo, err := sqlite.NewReservationRepository(db)
	if err != nil {
		t.Fatalf("NewReservationRepository: %v", err)
	}
	reviewRepo, err := sqlite.NewReviewRepository(db)
	if err != nil {
		t.Fatalf("NewReviewRepository: %v", err)
	}
	graphRepo, err := sqlite.NewGraphRepository(db)
	if err != nil {
		t.Fatalf("NewGraphRepository: %v", err)
	}
	workflowPolicyRepo, err := sqlite.NewWorkflowPolicyRepository(db)
	if err != nil {
		t.Fatalf("NewWorkflowPolicyRepository: %v", err)
	}
	snapshotRepo, err := sqlite.NewBoardSnapshotRepository(db)
	if err != nil {
		t.Fatalf("NewBoardSnapshotRepository: %v", err)
	}

	issueService, err := application.NewIssueService(issueRepo, source, generator)
	if err != nil {
		t.Fatalf("NewIssueService: %v", err)
	}
	attemptService, err := application.NewAttemptService(attemptRepo, source, generator)
	if err != nil {
		t.Fatalf("NewAttemptService: %v", err)
	}
	reservationService, err := application.NewReservationService(reservationRepo)
	if err != nil {
		t.Fatalf("NewReservationService: %v", err)
	}
	reviewService, err := application.NewReviewService(reviewRepo, issueRepo, source, generator)
	if err != nil {
		t.Fatalf("NewReviewService: %v", err)
	}
	graphService, err := application.NewGraphService(graphRepo, source)
	if err != nil {
		t.Fatalf("NewGraphService: %v", err)
	}
	workflowPolicyService, err := application.NewWorkflowPolicyService(workflowPolicyRepo, source, generator)
	if err != nil {
		t.Fatalf("NewWorkflowPolicyService: %v", err)
	}
	boardService, err := application.NewBoardService(issueService, attemptService, reservationService, reviewService, graphService, workflowPolicyService, source)
	if err != nil {
		t.Fatalf("NewBoardService: %v", err)
	}
	boardService.WithSnapshotLoader(snapshotRepo)
	return boardSSESQLiteFixture{
		source:         source,
		issueService:   issueService,
		attemptService: attemptService,
		boardService:   boardService,
	}
}

func TestBoardHTTPHandlerEventsWithSQLiteMutation(t *testing.T) {
	ctx := context.Background()
	fx := openBoardSSESQLiteFixture(t)

	server := httptest.NewServer(newBoardHTTPHandlerWithOptions(
		sqliteBoardSSEHTTPService{boardService: fx.boardService},
		boardHTTPOptions{pollInterval: 15 * time.Millisecond, keepaliveInterval: 5 * time.Second},
	))
	defer server.Close()

	reqCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, server.URL+"/api/events", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Do GET /api/events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/events status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	chunks := startSSEChunkReader(t, resp)
	waitForSSEChunk(t, chunks, "event: ready\n", 2*time.Second)

	if _, err := fx.issueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "SSE Triggered Issue", Status: domain.StatusReady, Priority: domain.PriorityHigh,
	}); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	waitForSSEChunk(t, chunks, "event: board_changed\n", 2*time.Second)
}

func TestBoardHTTPHandlerEventsDetectsClockOnlyLeaseExpiry(t *testing.T) {
	ctx := context.Background()
	fx := openBoardSSESQLiteFixture(t)

	issue, err := fx.issueService.CreateIssue(ctx, domain.CreateIssueInput{
		Type: domain.TypeTask, Title: "SSE Expiry Issue", Status: domain.StatusReady, Priority: domain.PriorityHigh,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	claim, err := fx.attemptService.ClaimIssue(ctx, domain.ClaimIssueInput{
		IssueID:   issue.ID,
		Resources: []domain.Resource{{Kind: domain.ResourceKindFile, Path: "sse-expiry.go"}},
	})
	if err != nil {
		t.Fatalf("ClaimIssue: %v", err)
	}

	server := httptest.NewServer(newBoardHTTPHandlerWithOptions(
		sqliteBoardSSEHTTPService{boardService: fx.boardService},
		boardHTTPOptions{pollInterval: 15 * time.Millisecond, keepaliveInterval: 5 * time.Second},
	))
	defer server.Close()

	reqCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, server.URL+"/api/events", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Do GET /api/events: %v", err)
	}
	defer resp.Body.Close()

	chunks := startSSEChunkReader(t, resp)
	waitForSSEChunk(t, chunks, "event: ready\n", 2*time.Second)

	// Advance fake clock past lease expiry without any database write.
	fx.source.Advance(claim.Attempt.LeaseExpiresAt.Sub(fx.source.Now()) + time.Second)
	waitForSSEChunk(t, chunks, "event: board_changed\n", 2*time.Second)
}

func TestBoardHTTPHandlerEventsShutsDownPromptlyWithOpenTab(t *testing.T) {
	svc := &sseWatermarkBoardService{watermark: "wt-open-tab"}
	sseHandler := newBoardHTTPHandlerWithOptions(svc, boardHTTPOptions{
		pollInterval:      20 * time.Millisecond,
		keepaliveInterval: 5 * time.Second,
	})

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()
	addrCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		errCh <- projectruntime.ServeHTTPServer(serverCtx, projectruntime.HTTPServerOptions{
			Address:         "127.0.0.1:0",
			Logger:          slog.Default(),
			Handler:         sseHandler,
			ShutdownTimeout: 5 * time.Second,
			OnListener: func(l net.Listener) {
				addrCh <- l.Addr().String()
			},
		})
	}()

	addr := <-addrCh
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+"/api/events", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}
	defer resp.Body.Close()

	chunks := startSSEChunkReader(t, resp)
	waitForSSEChunk(t, chunks, "event: ready\n", 2*time.Second)

	// Keep the browser SSE request open and shut down the server.
	started := time.Now()
	serverCancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ServeHTTPServer() error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(started); elapsed >= time.Second {
			t.Fatalf("ServeHTTPServer shutdown took %s with open SSE client, want < 1s", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTPServer hung waiting for open SSE client during shutdown")
	}
}
