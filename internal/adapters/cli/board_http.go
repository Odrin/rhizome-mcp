package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
)

const boardHTTPContentSecurityPolicy = "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; script-src 'unsafe-inline'; font-src 'self' data:; connect-src 'none'; base-uri 'none'; form-action 'none'"

// NewBoardHTTPHandler serves the board as an interactive loopback-only page and JSON API.
func NewBoardHTTPHandler(boardService BoardService) http.Handler {
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
		switch path {
		case "/":
			serveBoardPage(w, request.Method, boardService, request.Context())
		case "/api/board":
			serveBoardAPI(w, request.Method, boardService, request.Context())
		default:
			writeBoardHTTPResponse(w, http.StatusNotFound, "text/plain; charset=utf-8", []byte(http.StatusText(http.StatusNotFound)), true)
		}
	})
}

func serveBoardPage(w http.ResponseWriter, method string, boardService BoardService, ctx context.Context) {
	result, err := boardService.GetBoard(ctx)
	if err != nil {
		writeBoardHTTPResponse(w, http.StatusInternalServerError, "text/plain; charset=utf-8", []byte(http.StatusText(http.StatusInternalServerError)), true)
		return
	}
	body := []byte(renderBoardHTML(result))
	if method == http.MethodHead {
		writeBoardHTTPResponse(w, http.StatusOK, "text/html; charset=utf-8", nil, true)
		return
	}
	writeBoardHTTPResponse(w, http.StatusOK, "text/html; charset=utf-8", body, true)
}

func serveBoardAPI(w http.ResponseWriter, method string, boardService BoardService, ctx context.Context) {
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
