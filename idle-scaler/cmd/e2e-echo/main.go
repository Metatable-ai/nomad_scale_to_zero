package main

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
)

func envOrDefault(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	return value
}

func listenAddr() string {
	if addr := os.Getenv("E2E_ECHO_LISTEN_ADDR"); addr != "" {
		return addr
	}

	port := envOrDefault("NOMAD_PORT_http", "8080")
	return ":" + port
}

func main() {
	addr := listenAddr()
	responseText := envOrDefault("E2E_ECHO_TEXT", "Hello from e2e echo")
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseEchoLogLevel(os.Getenv("LOG_LEVEL"))})).With("service", "e2e-echo")

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("ok\n"))
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(responseText))
	})

	server := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	logger.Info("starting e2e echo server", "addr", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("failed to start e2e echo server", "error", err)
		os.Exit(1)
	}
}

func parseEchoLogLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
