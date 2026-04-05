// Package main provides the capability-gateway binary.
//
// The capability-gateway bridges MCP stdio servers to HTTP/SSE, providing a
// network-accessible endpoint for operator-managed MCP server pods.
//
// It exposes SSE endpoints (/sse, /message) that spawn the MCP server subprocess
// and bridge JSON-RPC over SSE. Shared security middleware (rate limiting, audit
// logging, health checks) is applied to all requests.
//
// Environment variables:
//
//	GATEWAY_PORT     - HTTP port (default: 8080, also reads TOOL_PORT for compat)
//	GATEWAY_COMMAND  - MCP server command to spawn
//	TOOL_NAME        - Capability name for logging
//	TOOL_PORT        - Port alias (alternative to GATEWAY_PORT)
//	RATE_LIMIT_RPM   - Requests per minute limit
//	AUDIT_ENABLED    - Enable audit logging
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/samyn92/agent-operator-core/pkg/gateway"
	"github.com/samyn92/agent-operator-core/pkg/gateway/mcp"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	config := gateway.LoadConfig()

	logger.Info("starting capability-gateway",
		"tool", config.ToolName,
		"port", config.Port,
		"rate_limit_rpm", config.RateLimitRPM,
		"audit_enabled", config.AuditEnabled,
	)

	mux := http.NewServeMux()

	// Register shared health endpoints
	mux.HandleFunc("GET /healthz", gateway.HealthHandler())
	mux.HandleFunc("GET /readyz", gateway.HealthHandler())

	// Set up MCP handler — bridges MCP stdio servers to HTTP/SSE
	handler := mcp.NewHandler(config, logger)

	// Start ConfigWatcher for hot-reloading MCP deny rules.
	// The watcher monitors the ConfigMap mount directory for Kubernetes symlink swaps
	// and reloads "mcp-deny-rules" when the ConfigMap is updated.
	if config.ConfigPath != "" {
		cw, err := gateway.NewConfigWatcher(config.ConfigPath, logger)
		if err != nil {
			logger.Warn("failed to start config watcher, MCP deny rules disabled",
				"path", config.ConfigPath,
				"error", err,
			)
		} else {
			cw.Start()
			handler.SetConfigWatcher(cw)
			defer cw.Stop()
			logger.Info("config watcher started for MCP deny rules", "path", config.ConfigPath)
		}
	}

	handler.Register(mux)

	logger.Info("MCP gateway active",
		"command", config.Command,
	)

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", config.Port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 0, // No write timeout for SSE streams
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh

		logger.Info("shutting down server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		httpServer.Shutdown(shutdownCtx)
	}()

	logger.Info("server listening", "addr", httpServer.Addr)
	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}
