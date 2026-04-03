// Package main provides the capability-gateway binary.
//
// The capability-gateway is a unified gateway for all capability types that need
// an IO proxy between the agent and a capability backend. It supports two modes:
//
//   - cli: REST endpoint (/exec) that validates and executes shell commands.
//     Used for Container-type capabilities (kubectl, gh, glab sidecars).
//     Enforces shell metachar blocking, command prefix, rate limiting, audit.
//
//   - mcp: SSE endpoint (/sse) that bridges MCP stdio servers to HTTP/SSE.
//     Used for server-mode MCP capabilities (operator-managed MCP server pods).
//     Spawns the MCP server subprocess and bridges JSON-RPC over SSE.
//
// Both modes share the same security middleware (rate limiting, audit logging,
// health checks) and are configured via environment variables.
//
// Environment variables:
//
//	GATEWAY_MODE     - "cli" or "mcp" (default: "cli")
//	GATEWAY_PORT     - HTTP port (default: 8080, also reads TOOL_PORT for compat)
//	GATEWAY_COMMAND  - MCP server command to spawn (mcp mode only)
//	TOOL_NAME        - Capability name for logging
//	TOOL_PORT        - Port alias (alternative to GATEWAY_PORT)
//	WORKSPACE_PATH   - Working directory for CLI commands (cli mode only)
//	RATE_LIMIT_RPM   - Requests per minute limit (both modes)
//	AUDIT_ENABLED    - Enable audit logging (both modes)
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
	"github.com/samyn92/agent-operator-core/pkg/gateway/cli"
	"github.com/samyn92/agent-operator-core/pkg/gateway/mcp"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	config := gateway.LoadConfig()

	logger.Info("starting capability-gateway",
		"mode", config.Mode,
		"tool", config.ToolName,
		"port", config.Port,
		"rate_limit_rpm", config.RateLimitRPM,
		"audit_enabled", config.AuditEnabled,
	)

	mux := http.NewServeMux()

	// Register shared health endpoints
	mux.HandleFunc("GET /healthz", gateway.HealthHandler())
	mux.HandleFunc("GET /readyz", gateway.HealthHandler())

	// Register mode-specific handlers
	switch config.Mode {
	case "cli":
		// Set up git HTTPS authentication if tokens are available
		gateway.ConfigureGitAuth(logger)

		handler := cli.NewHandler(config, logger)

		// Start ConfigWatcher for hot-reloading ConfigMap changes (e.g., command-prefix).
		// This allows permission and config updates to take effect without pod restarts.
		// The watcher monitors the ConfigMap mount directory for Kubernetes symlink swaps.
		if config.ConfigPath != "" {
			cw, err := gateway.NewConfigWatcher(config.ConfigPath, logger)
			if err != nil {
				logger.Warn("failed to start config watcher, falling back to static config",
					"path", config.ConfigPath,
					"error", err,
				)
			} else {
				cw.Start()
				handler.SetConfigWatcher(cw)
				defer cw.Stop()
				logger.Info("config watcher started", "path", config.ConfigPath)
			}
		}

		handler.Register(mux)

		logger.Info("CLI mode active",
			"command_prefix", config.CommandPrefix,
			"workspace_path", config.WorkspacePath,
		)

	case "mcp":
		handler := mcp.NewHandler(config, logger)
		handler.Register(mux)

		logger.Info("MCP mode active",
			"command", config.Command,
		)

	default:
		logger.Error("unknown gateway mode", "mode", config.Mode)
		fmt.Fprintf(os.Stderr, "unknown GATEWAY_MODE: %q (must be 'cli' or 'mcp')\n", config.Mode)
		os.Exit(1)
	}

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
