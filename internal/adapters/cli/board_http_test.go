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

func TestBoardHTTPHandlerPassesRequestContextToBoardService(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "html", path: "/"},
		{name: "json", path: "/api/board"},
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
