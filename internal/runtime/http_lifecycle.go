package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var (
	errInvalidRequestHost  = errors.New("invalid request host")
	errMisdirectedRequest  = errors.New("misdirected request")
	errOriginMismatch      = errors.New("origin mismatch")
	errRequestBodyTooLarge = errors.New("request body too large")
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

type statusRecorder struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

type requestBodyReader struct {
	io.ReadCloser
	limit     int64
	readSoFar int64
	exceeded  bool
}

func (r *requestBodyReader) Read(data []byte) (int, error) {
	if r.exceeded {
		return 0, errRequestBodyTooLarge
	}
	n, err := r.ReadCloser.Read(data)
	if n > 0 {
		r.readSoFar += int64(n)
	}
	if r.readSoFar > r.limit {
		r.exceeded = true
		if err == nil {
			err = errRequestBodyTooLarge
		}
		return n, err
	}
	return n, err
}

func (r *statusRecorder) WriteHeader(statusCode int) {
	if r.wroteHeader {
		return
	}
	r.statusCode = statusCode
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(data)
}

// WrapHTTPHandler hardens a local HTTP handler by validating the request host
// and origin against the configured loopback endpoint before the next handler
// runs, and by recovering panics to return a 500 without leaking payloads.
func WrapHTTPHandler(handler http.Handler, authority string, logger *slog.Logger) http.Handler {
	return wrapHTTPHandler(handler, authority, logger, 0)
}

// WrapHTTPHandlerWithBodyLimit hardens a local HTTP handler and applies a
// request-body size limit before the next handler runs.
func WrapHTTPHandlerWithBodyLimit(handler http.Handler, authority string, logger *slog.Logger, maxRequestBodyBytes int64) http.Handler {
	return wrapHTTPHandler(handler, authority, logger, maxRequestBodyBytes)
}

func wrapHTTPHandler(handler http.Handler, authority string, logger *slog.Logger, maxRequestBodyBytes int64) http.Handler {
	if handler == nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		})
	}
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		startedAt := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		var bodyReader *requestBodyReader
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("http handler panic", "error", recovered)
				if !recorder.wroteHeader {
					recorder.WriteHeader(http.StatusInternalServerError)
				}
				if recorder.statusCode == http.StatusOK {
					recorder.statusCode = http.StatusInternalServerError
				}
			}
			if bodyReader != nil && bodyReader.exceeded && !recorder.wroteHeader {
				recorder.WriteHeader(http.StatusRequestEntityTooLarge)
			}
			method := ""
			var sessionID string
			if request != nil {
				method = request.Method
				sessionID = strings.TrimSpace(request.Header.Get("Mcp-Session-Id"))
			}
			attrs := []any{"method", method, "path", requestPath(request), "status", recorder.statusCode, "duration", time.Since(startedAt)}
			if sessionID != "" {
				attrs = append(attrs, "mcp_session_id", sessionID)
			}
			logger.Info("http request completed", attrs...)
		}()

		if err := validateRequest(request, authority); err != nil {
			switch {
			case errors.Is(err, errInvalidRequestHost):
				http.Error(recorder, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			case errors.Is(err, errMisdirectedRequest):
				http.Error(recorder, http.StatusText(http.StatusMisdirectedRequest), http.StatusMisdirectedRequest)
			case errors.Is(err, errOriginMismatch):
				http.Error(recorder, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			default:
				http.Error(recorder, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
			return
		}
		if maxRequestBodyBytes > 0 {
			if request != nil && request.ContentLength > maxRequestBodyBytes {
				http.Error(recorder, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
				return
			}
			if request != nil && request.Body != nil {
				bodyReader = &requestBodyReader{ReadCloser: request.Body, limit: maxRequestBodyBytes}
				request.Body = bodyReader
			}
		}
		handler.ServeHTTP(recorder, request)
	})
}

func requestPath(request *http.Request) string {
	if request == nil {
		return ""
	}
	if request.URL != nil && request.URL.Path != "" {
		return request.URL.Path
	}
	return "/"
}

func validateRequest(request *http.Request, authority string) error {
	if request == nil {
		return errInvalidRequestHost
	}
	if authority == "" {
		return errInvalidRequestHost
	}
	canonicalAuthority, err := normalizeLoopbackAuthority(authority)
	if err != nil {
		return errInvalidRequestHost
	}
	requestHost := strings.TrimSpace(request.Host)
	if requestHost == "" && request.URL != nil {
		requestHost = strings.TrimSpace(request.URL.Host)
	}
	if requestHost == "" {
		return errInvalidRequestHost
	}
	requestAuthority, err := parseAuthority(requestHost)
	if err != nil {
		return errInvalidRequestHost
	}
	allowedAuthority, err := parseAuthority(canonicalAuthority)
	if err != nil {
		return errInvalidRequestHost
	}
	if requestAuthority.host != allowedAuthority.host || requestAuthority.port != allowedAuthority.port {
		return errMisdirectedRequest
	}
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return nil
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	expectedOrigin := scheme + "://" + canonicalAuthority
	if origin != expectedOrigin {
		return errOriginMismatch
	}
	return nil
}

type parsedAuthority struct {
	host string
	port string
}

func parseAuthority(value string) (parsedAuthority, error) {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return parsedAuthority{}, err
	}
	if host == "" || port == "" {
		return parsedAuthority{}, errInvalidRequestHost
	}
	ip := net.ParseIP(host)
	if ip != nil {
		return parsedAuthority{host: ip.String(), port: port}, nil
	}
	return parsedAuthority{host: strings.ToLower(host), port: port}, nil
}

func normalizeLoopbackAuthority(value string) (string, error) {
	canonical, err := ValidateLoopbackAddress(value)
	if err != nil {
		return "", err
	}
	return canonical, nil
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

	hardeningHandler := WrapHTTPHandlerWithBodyLimit(options.Handler, listener.Addr().String(), options.Logger, options.MaxRequestBodyBytes)
	server := &http.Server{
		Handler:           hardeningHandler,
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
