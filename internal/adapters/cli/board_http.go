package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"rhizome-mcp/internal/domain"
)

const boardHTTPContentSecurityPolicy = "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; script-src 'unsafe-inline'; font-src 'self' data:; connect-src 'none'; base-uri 'none'; form-action 'none'"

// BoardHTTPService exposes the board and issue-detail reads used by the served board HTTP adapter.
type BoardHTTPService interface {
	GetBoard(context.Context) (domain.BoardResult, error)
	GetIssueDetail(context.Context, string) (domain.IssueDetail, error)
}

// NewBoardHTTPHandler serves the board as an interactive loopback-only page and JSON API.
func NewBoardHTTPHandler(boardService BoardHTTPService) http.Handler {
	if boardService == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeBoardHTTPResponse(w, http.StatusServiceUnavailable, "text/plain; charset=utf-8", []byte("service unavailable"), true)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		setBoardHTTPHeaders(w)
		if request == nil {
			writeBoardHTTPResponse(w, http.StatusBadRequest, "text/plain; charset=utf-8", []byte("bad request"), true)
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeBoardHTTPResponse(w, http.StatusMethodNotAllowed, "text/plain; charset=utf-8", []byte(http.StatusText(http.StatusMethodNotAllowed)), true)
			return
		}
		path := "/"
		if request.URL != nil && request.URL.Path != "" {
			path = request.URL.Path
		}
		switch {
		case path == "/":
			serveBoardPage(w, request.Method, boardService, request.Context())
		case path == "/api/board":
			serveBoardAPI(w, request.Method, boardService, request.Context())
		case strings.HasPrefix(path, "/issues/"):
			serveIssueDetailPage(w, request.Method, boardService, request.Context(), path)
		default:
			writeBoardHTTPResponse(w, http.StatusNotFound, "text/plain; charset=utf-8", []byte(http.StatusText(http.StatusNotFound)), true)
		}
	})
}

func serveBoardPage(w http.ResponseWriter, method string, boardService BoardHTTPService, ctx context.Context) {
	result, err := boardService.GetBoard(ctx)
	if err != nil {
		writeBoardHTTPResponse(w, http.StatusInternalServerError, "text/plain; charset=utf-8", []byte(http.StatusText(http.StatusInternalServerError)), true)
		return
	}
	body := []byte(renderServedBoardHTML(result))
	if method == http.MethodHead {
		writeBoardHTTPResponse(w, http.StatusOK, "text/html; charset=utf-8", nil, true)
		return
	}
	writeBoardHTTPResponse(w, http.StatusOK, "text/html; charset=utf-8", body, true)
}

func serveIssueDetailPage(w http.ResponseWriter, method string, boardService BoardHTTPService, ctx context.Context, path string) {
	identifier, statusCode, err := parseIssueDetailRoute(path)
	if err != nil {
		writeBoardHTTPResponse(w, statusCode, "text/plain; charset=utf-8", []byte(err.Error()), true)
		return
	}
	result, err := boardService.GetIssueDetail(ctx, identifier)
	if err != nil {
		var domainErr *domain.Error
		if errors.As(err, &domainErr) {
			switch domainErr.Code {
			case domain.CodeIssueNotFound:
				writeBoardHTTPResponse(w, http.StatusNotFound, "text/plain; charset=utf-8", []byte(http.StatusText(http.StatusNotFound)), true)
				return
			case domain.CodeInvalidArgument:
				writeBoardHTTPResponse(w, http.StatusBadRequest, "text/plain; charset=utf-8", []byte(http.StatusText(http.StatusBadRequest)), true)
				return
			}
		}
		writeBoardHTTPResponse(w, http.StatusInternalServerError, "text/plain; charset=utf-8", []byte(http.StatusText(http.StatusInternalServerError)), true)
		return
	}
	body := []byte(renderIssueDetailHTML(result))
	if method == http.MethodHead {
		writeBoardHTTPResponse(w, http.StatusOK, "text/html; charset=utf-8", nil, true)
		return
	}
	writeBoardHTTPResponse(w, http.StatusOK, "text/html; charset=utf-8", body, true)
}

func parseIssueDetailRoute(path string) (string, int, error) {
	if !strings.HasPrefix(path, "/issues/") {
		return "", http.StatusNotFound, errors.New(http.StatusText(http.StatusNotFound))
	}
	rest := strings.TrimPrefix(path, "/issues/")
	if rest == "" || strings.Contains(rest, "/") {
		return "", http.StatusBadRequest, errors.New(http.StatusText(http.StatusBadRequest))
	}
	identifier, err := url.PathUnescape(rest)
	if err != nil || identifier == "" || strings.Contains(identifier, "/") {
		return "", http.StatusBadRequest, errors.New(http.StatusText(http.StatusBadRequest))
	}
	if strings.Contains(identifier, "%2F") || strings.Contains(identifier, "%5C") {
		return "", http.StatusBadRequest, errors.New(http.StatusText(http.StatusBadRequest))
	}
	return identifier, http.StatusOK, nil
}

func serveBoardAPI(w http.ResponseWriter, method string, boardService BoardHTTPService, ctx context.Context) {
	result, err := boardService.GetBoard(ctx)
	if err != nil {
		writeBoardHTTPResponse(w, http.StatusInternalServerError, "application/json; charset=utf-8", []byte(`{"error":"internal server error"}`), true)
		return
	}
	payload, err := json.MarshalIndent(boardResponseFromDomain(result), "", "  ")
	if err != nil {
		writeBoardHTTPResponse(w, http.StatusInternalServerError, "application/json; charset=utf-8", []byte(`{"error":"internal server error"}`), true)
		return
	}
	if method == http.MethodHead {
		writeBoardHTTPResponse(w, http.StatusOK, "application/json; charset=utf-8", nil, false)
		return
	}
	writeBoardHTTPResponse(w, http.StatusOK, "application/json; charset=utf-8", payload, true)
}

func setBoardHTTPHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", boardHTTPContentSecurityPolicy)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
}

func writeBoardHTTPResponse(w http.ResponseWriter, statusCode int, contentType string, body []byte, includeBody bool) {
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if body != nil && len(body) > 0 {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}
	w.WriteHeader(statusCode)
	if includeBody && len(body) > 0 {
		_, _ = w.Write(body)
	}
}
