package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPReadHeaderTimeout = 10 * time.Second
	defaultHTTPReadTimeout       = 30 * time.Second
	defaultHTTPWriteTimeout      = 30 * time.Second
	defaultHTTPIdleTimeout       = 60 * time.Second
	defaultHTTPMaxHeaderBytes    = 8 << 10
	defaultHTTPMaxBodyBytes      = 1 << 20
	defaultHTTPShutdownTimeout   = 5 * time.Second
)

// HTTPServerOptions configures the loopback-only HTTP lifecycle.
type HTTPServerOptions struct {
	Address             string
	Logger              *slog.Logger
	Handler             http.Handler
	ShutdownTimeout     time.Duration
	ReadHeaderTimeout   time.Duration
	ReadTimeout         time.Duration
	WriteTimeout        time.Duration
	IdleTimeout         time.Duration
	MaxHeaderBytes      int
	MaxRequestBodyBytes int64
}

// ServeHTTPServer starts a loopback-only HTTP listener and blocks until the
// context is canceled or the server stops.
func ServeHTTPServer(ctx context.Context, options HTTPServerOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.Handler == nil {
		options.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		})
	}
	if options.ShutdownTimeout <= 0 {
		options.ShutdownTimeout = defaultHTTPShutdownTimeout
	}
	if options.ReadHeaderTimeout <= 0 {
		options.ReadHeaderTimeout = defaultHTTPReadHeaderTimeout
	}
	if options.ReadTimeout <= 0 {
		options.ReadTimeout = defaultHTTPReadTimeout
	}
	if options.WriteTimeout <= 0 {
		options.WriteTimeout = defaultHTTPWriteTimeout
	}
	if options.IdleTimeout <= 0 {
		options.IdleTimeout = defaultHTTPIdleTimeout
	}
	if options.MaxHeaderBytes <= 0 {
		options.MaxHeaderBytes = defaultHTTPMaxHeaderBytes
	}
	if options.MaxRequestBodyBytes <= 0 {
		options.MaxRequestBodyBytes = defaultHTTPMaxBodyBytes
	}

	address, err := ValidateLoopbackAddress(options.Address)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	options.Logger.Info("http server listening", "endpoint", listener.Addr().String())

	server := &http.Server{
		Handler:           http.MaxBytesHandler(options.Handler, options.MaxRequestBodyBytes),
		ReadHeaderTimeout: options.ReadHeaderTimeout,
		ReadTimeout:       options.ReadTimeout,
		WriteTimeout:      options.WriteTimeout,
		IdleTimeout:       options.IdleTimeout,
		MaxHeaderBytes:    options.MaxHeaderBytes,
	}
	serveErrs := make(chan error, 1)
	go func() {
		serveErrs <- server.Serve(listener)
	}()

	select {
	case err := <-serveErrs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), options.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		if err := <-serveErrs; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return ctx.Err()
	}
}

// ValidateLoopbackAddress parses and validates a literal loopback bind address.
// IPv4 127/8 and IPv6 ::1 are accepted; hostnames and other addresses are rejected.
func ValidateLoopbackAddress(address string) (string, error) {
	if strings.TrimSpace(address) == "" {
		return "", errors.New("http address is required")
	}
	if !strings.Contains(address, ":") {
		return "", fmt.Errorf("invalid http address %q", address)
	}

	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("invalid http address %q: %w", address, err)
	}
	if host == "" {
		return "", fmt.Errorf("invalid http address %q: empty host", address)
	}
	if port == "" {
		return "", fmt.Errorf("invalid http address %q: empty port", address)
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum < 0 || portNum > 65535 {
		return "", fmt.Errorf("invalid http address %q: invalid port %q", address, port)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", fmt.Errorf("invalid http address %q: hostnames are not supported", address)
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 0x7f {
			return net.JoinHostPort(ip.String(), port), nil
		}
		return "", fmt.Errorf("invalid http address %q: only 127/8 is allowed", address)
	}
	if ip.Equal(net.ParseIP("::1")) {
		return net.JoinHostPort("::1", port), nil
	}
	return "", fmt.Errorf("invalid http address %q: only ::1 is allowed", address)
}
