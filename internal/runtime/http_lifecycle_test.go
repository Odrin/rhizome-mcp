package runtime

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
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
