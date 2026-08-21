package runtime

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.Buffer.Write(data)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.Buffer.String()
}

func (buffer *lockedBuffer) Len() int {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.Buffer.Len()
}

func TestValidateLoopbackAddress(t *testing.T) {
	accepted := []string{"127.0.0.1:0", "127.255.255.255:8080", "[::1]:0", "[::1]:8080"}
	for _, address := range accepted {
		got, err := ValidateLoopbackAddress(address)
		if err != nil {
			t.Fatalf("ValidateLoopbackAddress(%q) returned error: %v", address, err)
		}
		if got == "" {
			t.Fatalf("ValidateLoopbackAddress(%q) returned empty address", address)
		}
	}

	rejected := []string{"0.0.0.0:8080", ":8080", "localhost:8080", "8.8.8.8:8080", "[::]:8080", "[::2]:8080", "[2001:db8::1]:8080", "example.com:8080", "127.0.0.1"}
	for _, address := range rejected {
		if _, err := ValidateLoopbackAddress(address); err == nil {
			t.Fatalf("ValidateLoopbackAddress(%q) accepted invalid address", address)
		}
	}
}

func TestServeHTTPServerInvokesListenerCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	notified := make(chan net.Listener, 1)
	done := make(chan error, 1)
	go func() {
		done <- ServeHTTPServer(ctx, HTTPServerOptions{Address: "127.0.0.1:0", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), OnListener: func(listener net.Listener) { notified <- listener }})
	}()

	select {
	case listener := <-notified:
		defer listener.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for listener callback")
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown returned %v, want context.Canceled", err)
	}
}

func TestServeHTTPServerPropagatesListenFailure(t *testing.T) {
	err := ServeHTTPServer(context.Background(), HTTPServerOptions{
		Address: "127.0.0.1:0",
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
		Listen: func(network, address string) (net.Listener, error) {
			return nil, errors.New("listen failed")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "listen failed") {
		t.Fatalf("error = %v, want listen failure", err)
	}
}

func TestServeHTTPServerRejectsOccupiedPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on ephemeral port: %v", err)
	}
	defer listener.Close()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}
	if err := ServeHTTPServer(context.Background(), HTTPServerOptions{Address: "127.0.0.1:" + port, Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}); err == nil {
		t.Fatal("expected occupied-port error")
	}
}

func TestServeHTTPServerUsesEphemeralListenerAndShutdowns(t *testing.T) {
	logs := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- ServeHTTPServer(ctx, HTTPServerOptions{Address: "127.0.0.1:0", Logger: logger})
	}()

	var endpoint string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if logs.Len() > 0 {
			endpoint = extractEndpoint(logs.String())
			if endpoint != "" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if endpoint == "" {
		t.Fatal("expected endpoint to be logged")
	}

	resp, err := http.Get("http://" + endpoint + "/")
	if err != nil {
		t.Fatalf("GET placeholder endpoint: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown returned %v, want context.Canceled", err)
	}
}

// TestServeHTTPServerCancelsInFlightRequestContextOnShutdown is the
// ISSUE-204 regression for the HTTP half of the bug: previously Shutdown
// only stopped accepting new connections and waited for existing ones to go
// idle, but never canceled an in-flight handler's request context, so a
// still-running MCP tool call (and the router lease its context is bound
// to) stayed live past ServeHTTPServer's own return. The handler here
// blocks on its request context exactly like a long-running tool call
// would; it must be canceled once Shutdown's grace period elapses, and
// ServeHTTPServer must still return within that same grace period rather
// than hanging on the still-open connection.
func TestServeHTTPServerCancelsInFlightRequestContextOnShutdown(t *testing.T) {
	logs := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const shutdownGrace = 200 * time.Millisecond
	requestStarted := make(chan struct{})
	requestContextCanceled := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
		close(requestContextCanceled)
	})

	done := make(chan error, 1)
	go func() {
		done <- ServeHTTPServer(ctx, HTTPServerOptions{
			Address: "127.0.0.1:0", Logger: logger, Handler: handler, ShutdownTimeout: shutdownGrace,
		})
	}()

	var endpoint string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if logs.Len() > 0 {
			endpoint = extractEndpoint(logs.String())
			if endpoint != "" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if endpoint == "" {
		t.Fatal("expected endpoint to be logged")
	}

	clientErrs := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + endpoint + "/")
		if err == nil {
			resp.Body.Close()
		}
		clientErrs <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("request never reached the handler")
	}

	cancel()

	select {
	case <-requestContextCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("handler's request context was never canceled by shutdown")
	}

	// The handler never returns on its own within shutdownGrace (it only
	// exits once its context is canceled, which — per the fix under test —
	// happens after Shutdown's own wait gives up), so Shutdown times out
	// waiting for that connection to go idle and ServeHTTPServer surfaces
	// that timeout rather than the outer ctx's cancellation. What matters
	// here is that it returns promptly instead of hanging on the
	// still-open connection, and that the handler was in fact canceled.
	select {
	case err := <-done:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("ServeHTTPServer() = %v, want a non-nil shutdown-timeout error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTPServer did not return within its shutdown grace period")
	}
	<-clientErrs
}

func TestWrapHTTPHandlerAcceptsLoopbackHostAndOrigin(t *testing.T) {
	for _, tc := range []struct {
		name     string
		address  string
		host     string
		origin   string
		wantCode int
	}{
		{name: "ipv4", address: "127.0.0.1:8080", host: "127.0.0.1:8080", wantCode: http.StatusNoContent},
		{name: "ipv6", address: "[::1]:8080", host: "[::1]:8080", wantCode: http.StatusNoContent},
		{name: "ipv4-with-origin", address: "127.0.0.1:8080", host: "127.0.0.1:8080", origin: "http://127.0.0.1:8080", wantCode: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := WrapHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}), tc.address, slog.New(slog.NewTextHandler(io.Discard, nil)))
			request := httptest.NewRequest(http.MethodGet, "http://"+tc.host+"/mcp", nil)
			request.Host = tc.host
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantCode)
			}
		})
	}
}

func TestWrapHTTPHandlerRejectsHostAndOrigin(t *testing.T) {
	handler := WrapHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), "127.0.0.1:8080", slog.New(slog.NewTextHandler(io.Discard, nil)))

	t.Run("host mismatch", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/mcp", nil)
		request.Host = "example.com:8080"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusMisdirectedRequest {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMisdirectedRequest)
		}
	})

	t.Run("origin mismatch", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/mcp", nil)
		request.Host = "127.0.0.1:8080"
		request.Header.Set("Origin", "http://127.0.0.1:9090")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
		}
	})

	t.Run("forwards ignored", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/mcp", nil)
		request.Host = "127.0.0.1:8080"
		request.Header.Set("X-Forwarded-Host", "evil.example")
		request.Header.Set("Forwarded", "for=8.8.8.8")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
		}
	})
}

func TestWrapHTTPHandlerLogsSafeMCPIdentity(t *testing.T) {
	var logs bytes.Buffer
	handler := WrapHTTPHandler(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}), "127.0.0.1:8080", slog.New(slog.NewTextHandler(&logs, nil)))
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/mcp", strings.NewReader(`{"agent_session_handle":"secret-handle","lease_token":"secret-token"}`))
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	request.Header.Set("Mcp-Method", "tools/call")
	request.Header.Set("Mcp-Name", "create_issue")
	request.Header.Set("Mcp-Session-Id", "legacy-session")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	output := logs.String()
	for _, expected := range []string{"mcp_protocol_version=2026-07-28", "mcp_method=tools/call", "mcp_tool=create_issue"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("log %q does not contain %q", output, expected)
		}
	}
	for _, secret := range []string{"legacy-session", "secret-handle", "secret-token"} {
		if strings.Contains(output, secret) {
			t.Fatalf("log leaked %q: %q", secret, output)
		}
	}
}

func TestWrapHTTPHandlerRecoversPanics(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	handler := WrapHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		panic("leaked payload")
	}), "127.0.0.1:8080", logger)

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/mcp", nil)
	request.Host = "127.0.0.1:8080"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if body := recorder.Body.String(); strings.Contains(body, "leaked") || strings.Contains(body, "payload") {
		t.Fatalf("unexpected panic payload in response body: %q", body)
	}
	if !strings.Contains(logs.String(), "http handler panic") {
		t.Fatalf("expected panic to be logged, got %q", logs.String())
	}
}

func TestServeHTTPServerRejectsOversizedBody(t *testing.T) {
	logs := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- ServeHTTPServer(ctx, HTTPServerOptions{Address: "127.0.0.1:0", Logger: logger, Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			_, _ = io.ReadAll(request.Body)
			w.WriteHeader(http.StatusNoContent)
		}), MaxRequestBodyBytes: 8})
	}()

	var endpoint string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if logs.Len() > 0 {
			endpoint = extractEndpoint(logs.String())
			if endpoint != "" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if endpoint == "" {
		t.Fatal("expected endpoint to be logged")
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post("http://"+endpoint+"/mcp", "application/octet-stream", strings.NewReader(strings.Repeat("x", 16)))
	if err != nil {
		t.Fatalf("POST oversized body: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown returned %v, want context.Canceled", err)
	}
}

func TestServeHTTPServerRejectsMalformedRequest(t *testing.T) {
	logs := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- ServeHTTPServer(ctx, HTTPServerOptions{Address: "127.0.0.1:0", Logger: logger, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})})
	}()

	var endpoint string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if logs.Len() > 0 {
			endpoint = extractEndpoint(logs.String())
			if endpoint != "" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if endpoint == "" {
		t.Fatal("expected endpoint to be logged")
	}

	conn, err := net.Dial("tcp", endpoint)
	if err != nil {
		t.Fatalf("dial endpoint: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET /mcp HTTP/1.1\r\nHost: 127.0.0.1:" + strings.Split(endpoint, ":")[1] + "\r\nBad header\r\n\r\n")); err != nil {
		t.Fatalf("write malformed request: %v", err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read malformed response: %v", err)
	}
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown returned %v, want context.Canceled", err)
	}
}

func extractEndpoint(logs string) string {
	prefix := "endpoint="
	start := strings.Index(logs, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := strings.IndexAny(logs[start:], " \t\n")
	if end < 0 {
		return logs[start:]
	}
	return logs[start : start+end]
}
