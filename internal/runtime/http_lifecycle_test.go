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
	"testing"
	"time"
)

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
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
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
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
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
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
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
